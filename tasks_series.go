package main

import (
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
)

// ==================== 我的任务时序曲线（user 大盘） ====================
//
// 普通用户"我的统计任务报告"除了任务表格，还需要一条趋势曲线。
// 本接口把当前登录用户**自己创建**的定时拨测任务（source=sched）在窗口内的样本
// 按时间分桶，每桶统计 samples/up/down/availability/avgMs，返回前端画可用率曲线。
// 归属口径与 /tasks 列表一致：role=user 只算 owner_id==uid；admin/静态 token 算全量。

// registerTaskMineSeriesRoutes 挂到 /admin 组（admin.go 中调用）。
func registerTaskMineSeriesRoutes(g *gin.RouterGroup) {
	g.GET("/tasks/mine/series", func(c *gin.Context) {
		tr := parseTimeRange(c, 24)
		uid, role, _ := currentUserFromCtx(c)

		ctx, cancel := dbCtx()
		defer cancel()

		// 1) 收集当前用户可看的所有任务
		var tasks []ProbeTask
		q := db.WithContext(ctx).Model(&ProbeTask{})
		if role == RoleUser {
			q = q.Where("owner_id = ?", uid)
		}
		if err := q.Find(&tasks).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}
		taskByID := make(map[uint]*ProbeTask, len(tasks))
		ids := make([]uint, 0, len(tasks))
		for i := range tasks {
			taskByID[tasks[i].ID] = &tasks[i]
			ids = append(ids, tasks[i].ID)
		}

		// 无任务 → 空序列
		if len(ids) == 0 {
			c.JSON(http.StatusOK, gin.H{"stepMinutes": 1, "series": []gin.H{}})
			return
		}

		// 2) 拉取这些任务窗口内的全部定时样本
		var rows []ProbeResult
		if err := db.WithContext(ctx).
			Where("task_id IN ? AND source = ? AND created_at >= ? AND created_at <= ?",
				ids, sourceSched, tr.From, tr.To).
			Order("created_at asc, id asc").Find(&rows).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}

		// 3) 分桶粒度：窗口越大桶越粗，控制曲线点数（约 ≤240 点）
		stepMin := stepForWindow(tr.Hours)
		series := rowsToSeries(taskByID, rows, stepMin)
		c.JSON(http.StatusOK, gin.H{"stepMinutes": stepMin, "series": series})
	})
}

// rowsToSeries 将已按任务/窗口过滤的定时样本分桶为曲线序列（跨任务聚合成一条）。
// 每桶含 samples/up/down/availability(%)/avgMs；SLA 卡片与 user 大盘共用此判定与分桶口径。
func rowsToSeries(taskByID map[uint]*ProbeTask, rows []ProbeResult, stepMin int) []gin.H {
	step := time.Duration(stepMin) * time.Minute
	type bucket struct {
		samples int64
		up      int64
		down    int64
		valid   int64 // 有判定结果的样本（进可用率分母）
		sumMs   int64
		nLat    int64
	}
	agg := map[int64]*bucket{}
	var startIdx []int64 // 保序
	for _, r := range rows {
		t := taskByID[r.TaskID]
		if t == nil {
			continue // 理论上不会发生（已按 task_id 过滤）
		}
		ev := evalSample(t, r.Status, r.Body, r.LatencyMs)
		bk := r.CreatedAt.Truncate(step).Unix()
		b := agg[bk]
		if b == nil {
			b = &bucket{}
			agg[bk] = b
			startIdx = append(startIdx, bk)
		}
		b.samples++
		if !ev.invalid {
			b.valid++ // 可用率分母：有明确 up/down 判定的样本
		}
		if ev.up {
			b.up++
			// 延迟只认可达(up)样本：不可达(down)延迟无意义(落库为 0)，不计入曲线平均
			b.sumMs += r.LatencyMs
			b.nLat++
		} else if !ev.invalid {
			b.down++
		}
	}
	sort.Slice(startIdx, func(i, j int) bool { return startIdx[i] < startIdx[j] })

	series := make([]gin.H, 0, len(startIdx))
	for _, bk := range startIdx {
		b := agg[bk]
		avail := 0.0
		if b.valid > 0 {
			avail = math.Round(float64(b.up)/float64(b.valid)*10000) / 100
		}
		avgMs := 0.0
		if b.nLat > 0 {
			avgMs = math.Round(float64(b.sumMs)/float64(b.nLat)*10) / 10
		}
		series = append(series, gin.H{
			"time":         time.Unix(bk, 0).UTC().Format(time.RFC3339),
			"minute":       bk / 60,
			"samples":      b.samples,
			"up":           b.up,
			"down":         b.down,
			"availability": avail, // %
			"avgMs":        avgMs,
		})
	}
	return series
}

// stepForWindow 根据窗口小时数选分桶粒度（分钟），控制曲线点数上限。
func stepForWindow(hours float64) int {
	switch {
	case hours <= 1:
		return 1
	case hours <= 6:
		return 5
	case hours <= 24:
		return 30
	case hours <= 72:
		return 120 // 2h
	default:
		return 360 // 6h
	}
}

// rowsToRoundSeries 把单任务的定时样本按"采样轮次"聚成曲线点（SLA 卡延迟曲线用）。
//
// 与 rowsToSeries（跨时间桶聚合、把多轮样本混进一个点）不同：定时调度器每轮对目标节点池
// 并发拨测并落库时，同一轮所有节点共用同一个 CreatedAt（见 probe_task.go runTaskSamples 的
// now := time.Now().UTC()）。因此按 CreatedAt 精确分组即还原"每一轮采样"——
// 每个点 = 一轮，多节点延迟在该点内取平均（不跨轮平均、也不按节点拆线），让曲线贴近逐次真实走势。
//
// 判定沿用 evalSample：invalid 不计可用率分母也不进 avg 分母；samples=该轮样本数（含 invalid）。
// 返回每轮元素 { time, minute, samples, up, down, availability(%), avgMs }；
// 点数超过 maxRoundPoints 时相邻若干轮会合并成一个点（该点额外带 rounds 字段）。
func rowsToRoundSeries(t *ProbeTask, rows []ProbeResult) []gin.H {
	agg := map[int64]*roundAgg{}
	var keys []int64 // 保序
	for _, r := range rows {
		at := r.CreatedAt.Round(time.Second).UTC()
		k := at.Unix()
		g := agg[k]
		if g == nil {
			g = &roundAgg{at: at}
			agg[k] = g
			keys = append(keys, k)
		}
		ev := evalSample(t, r.Status, r.Body, r.LatencyMs)
		g.samples++
		if !ev.invalid {
			g.valid++
		}
		if ev.up {
			g.up++
			// 延迟只认可达(up)样本：不可达(down)延迟无意义(落库为 0)，不计入曲线平均
			g.sumMs += r.LatencyMs
			g.nLat++
		} else if !ev.invalid {
			g.down++
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	list := make([]*roundAgg, 0, len(keys))
	for _, k := range keys {
		list = append(list, agg[k])
	}

	// 点数超限 → 相邻轮次等份合并（每个点 = merge 轮）：各计数直接累加，延迟按可达样本
	// 加权求和后再平均。**含失败的轮不会被单独丢掉** —— down 照旧累加，红标仍落在合并点上，
	// 只是从"每点 = 一轮"变成"每点 = 一组轮"。
	merge := 1
	if len(list) > maxRoundPoints {
		merge = (len(list) + maxRoundPoints - 1) / maxRoundPoints
	}
	series := make([]gin.H, 0, (len(list)+merge-1)/merge)
	for i := 0; i < len(list); i += merge {
		end := i + merge
		if end > len(list) {
			end = len(list)
		}
		acc := &roundAgg{at: list[i].at}
		for _, x := range list[i:end] {
			acc.samples += x.samples
			acc.valid += x.valid
			acc.up += x.up
			acc.down += x.down
			acc.sumMs += x.sumMs
			acc.nLat += x.nLat
		}
		series = append(series, roundToJSON(acc, end-i))
	}
	return series
}

// maxRoundPoints 单条曲线返回的最大点数。窗口放宽到 7/30/90 天后，节点每 10s 一轮会产生
// 几万个点（JSON 数 MB、前端画不动），超出即按"多轮合并成一个点"降采样。
const maxRoundPoints = 1500

// roundAgg 一轮采样的聚合：多节点在该轮内取平均（不跨轮平均、也不按节点拆线）。
type roundAgg struct {
	at      time.Time
	samples int64 // 该轮全部样本（含 invalid）
	valid   int64 // 有明确 up/down 判定（可用率分母）
	up      int64
	down    int64
	sumMs   int64 // 可达样本延迟总和
	nLat    int64
}

// roundToJSON 单个（或合并后的）轮次组 → 曲线点。rounds > 1 表示该点由多轮合并而来。
func roundToJSON(g *roundAgg, rounds int) gin.H {
	avail := 0.0
	if g.valid > 0 {
		avail = math.Round(float64(g.up)/float64(g.valid)*10000) / 100
	}
	avgMs := 0.0
	if g.nLat > 0 {
		avgMs = math.Round(float64(g.sumMs)/float64(g.nLat)*10) / 10
	}
	h := gin.H{
		"time":         g.at.Format(time.RFC3339),
		"minute":       g.at.Unix() / 60,
		"samples":      g.samples,
		"up":           g.up,
		"down":         g.down,
		"availability": avail, // %
		"avgMs":        avgMs,
	}
	if rounds > 1 {
		h["rounds"] = rounds
	}
	return h
}

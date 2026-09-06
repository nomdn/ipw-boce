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
		hours := clampFloat(c.Query("hours"), 24, 1, 24*90)
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

		to := time.Now().UTC()
		from := to.Add(-time.Duration(hours*float64(time.Hour)))

		// 2) 拉取这些任务窗口内的全部定时样本
		var rows []ProbeResult
		if err := db.WithContext(ctx).
			Where("task_id IN ? AND source = ? AND created_at >= ? AND created_at <= ?",
				ids, sourceSched, from, to).
			Order("created_at asc, id asc").Find(&rows).Error; err != nil {
			apiError(c, http.StatusInternalServerError, err.Error())
			return
		}

		// 3) 分桶粒度：窗口越大桶越粗，控制曲线点数（约 ≤240 点）
		stepMin := stepForWindow(hours)
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
// 返回每轮元素 { time, minute, samples, up, down, availability(%), avgMs }。
func rowsToRoundSeries(t *ProbeTask, rows []ProbeResult) []gin.H {
	type round struct {
		at     time.Time // 该轮 CreatedAt（按秒取整）
		samples int64
		valid   int64
		up      int64
		down    int64
		sumMs   int64
		nLat    int64
	}
	agg := map[int64]*round{}
	var keys []int64 // 保序
	for _, r := range rows {
		at := r.CreatedAt.Round(time.Second).UTC()
		k := at.Unix()
		g := agg[k]
		if g == nil {
			g = &round{at: at}
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

	series := make([]gin.H, 0, len(keys))
	for _, k := range keys {
		g := agg[k]
		avail := 0.0
		if g.valid > 0 {
			avail = math.Round(float64(g.up)/float64(g.valid)*10000) / 100
		}
		avgMs := 0.0
		if g.nLat > 0 {
			avgMs = math.Round(float64(g.sumMs)/float64(g.nLat)*10) / 10
		}
		series = append(series, gin.H{
			"time":         g.at.Format(time.RFC3339),
			"minute":       k / 60,
			"samples":      g.samples,
			"up":           g.up,
			"down":         g.down,
			"availability": avail, // %
			"avgMs":        avgMs,
		})
	}
	return series
}

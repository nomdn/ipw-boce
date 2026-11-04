package main

// ==================== 弹窗公告（后台「系统设置」下发，boce 与 1IPW 两边显示） ====================
//
// 配置存在 siteSettings.Popup（保存即生效）。内容（标题/正文/图片/链接/频率/显示位置）一变，rev 就换成当前时间戳，
// 前端据此判断"用户是否已读过这一版"。
//
//   POST /admin/popup/image        管理员：上传图片（multipart 字段 file，≤3MB，png/jpg/gif/webp），返回 {url}
//   GET  /popup-img/:name          公开：读取已上传图片（文件名是内容哈希，可长期缓存）
//   GET  /admin/public/site        里的 popup 字段：boce 自己的页面用（勾选了「在 boce 显示」才返回）
//   GET  /api/sync/popup           sync-token：1IPW 主站后端拉取（勾选了「在 1IPW 显示」才返回，图片为绝对地址）

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	popupFreqOnce    = "once"
	popupFreqDaily   = "daily"
	popupFreqSession = "session"

	maxPopupImage = 3 << 20
	maxPopupTitle = 80
	maxPopupBody  = 2000
)

type popupSettings struct {
	Enabled   bool   `json:"enabled"`
	ShowBoce  bool   `json:"showBoce"`
	ShowIPW   bool   `json:"showIpw"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	ImageURL  string `json:"imageUrl"` // 相对（/popup-img/…）或绝对 http(s) 地址
	Link      string `json:"link"`
	Frequency string `json:"frequency"` // once | daily | session
	Rev       int64  `json:"rev"`
}

func defaultPopup() popupSettings {
	return popupSettings{ShowBoce: true, ShowIPW: true, Frequency: popupFreqOnce}
}

// safeHTTPURL 只接受 http(s) 绝对地址或本站 /popup-img/ 相对地址，其余（javascript: 等）一律清空
func safeHTTPURL(s string, allowLocalImg bool) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	l := strings.ToLower(s)
	if strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "http://") {
		return s
	}
	if allowLocalImg && strings.HasPrefix(s, "/popup-img/") {
		return s
	}
	return ""
}

func runeTrunc(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// sanitize 规整输入；返回错误文案（空 = 通过）
func (p *popupSettings) sanitize() string {
	p.Title = runeTrunc(strings.TrimSpace(p.Title), maxPopupTitle)
	p.Body = runeTrunc(strings.TrimSpace(p.Body), maxPopupBody)
	if raw := strings.TrimSpace(p.ImageURL); raw != "" && safeHTTPURL(raw, true) == "" {
		return "图片地址必须是 http(s) 链接或上传的图片"
	}
	if raw := strings.TrimSpace(p.Link); raw != "" && safeHTTPURL(raw, false) == "" {
		return "跳转链接必须是 http(s) 链接"
	}
	p.ImageURL, p.Link = safeHTTPURL(p.ImageURL, true), safeHTTPURL(p.Link, false)
	switch p.Frequency {
	case popupFreqOnce, popupFreqDaily, popupFreqSession:
	case "":
		p.Frequency = popupFreqOnce
	default:
		return "弹出频率只能是 once / daily / session"
	}
	if p.Enabled && p.Title == "" && p.Body == "" && p.ImageURL == "" {
		return "开启弹窗前请至少填写标题、正文或图片之一"
	}
	return ""
}

// sameContent 内容是否一致（不含 rev）；用来决定是否换 rev
func (p popupSettings) sameContent(o popupSettings) bool {
	p.Rev, o.Rev = 0, 0
	return p == o
}

// popupView 对外的精简视图；absBase 非空时把相对图片地址补成绝对地址
func (p popupSettings) view(absBase string) gin.H {
	img := p.ImageURL
	if absBase != "" && strings.HasPrefix(img, "/") {
		img = absBase + img
	}
	return gin.H{"rev": p.Rev, "title": p.Title, "body": p.Body, "imageUrl": img, "link": p.Link, "frequency": p.Frequency}
}

// ---- 图片存储 ----

func uploadDir() string {
	if strings.EqualFold(DB_CONF.Driver, "sqlite") && DB_CONF.DSN != "" {
		return filepath.Join(filepath.Dir(DB_CONF.DSN), "uploads")
	}
	return "uploads"
}

var popupImgRe = regexp.MustCompile(`^[a-f0-9]{32}\.(png|jpg|gif|webp)$`)

// detectImage 按文件内容识别（不信任扩展名与 Content-Type）；SVG 不允许（可内嵌脚本）
func detectImage(head []byte) (ext string, ok bool) {
	switch http.DetectContentType(head) {
	case "image/png":
		return "png", true
	case "image/jpeg":
		return "jpg", true
	case "image/gif":
		return "gif", true
	case "image/webp":
		return "webp", true
	}
	return "", false
}

func registerPopupRoutes(r *gin.Engine, restricted *gin.RouterGroup) {
	restricted.POST("/popup/image", func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxPopupImage+64*1024)
		fh, err := c.FormFile("file")
		if err != nil {
			apiError(c, http.StatusBadRequest, "请选择要上传的图片（大小不超过 3MB）")
			return
		}
		if fh.Size > maxPopupImage {
			apiError(c, http.StatusRequestEntityTooLarge, "图片不能超过 3MB")
			return
		}
		f, err := fh.Open()
		if err != nil {
			apiError(c, http.StatusBadRequest, "读取图片失败")
			return
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, maxPopupImage+1))
		if err != nil || len(data) > maxPopupImage {
			apiError(c, http.StatusRequestEntityTooLarge, "图片不能超过 3MB")
			return
		}
		ext, ok := detectImage(data)
		if !ok {
			apiError(c, http.StatusBadRequest, "只支持 png / jpg / gif / webp 图片")
			return
		}
		sum := sha256.Sum256(data)
		name := hex.EncodeToString(sum[:16]) + "." + ext
		dir := uploadDir()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			apiError(c, http.StatusInternalServerError, "无法创建上传目录："+err.Error())
			return
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			apiError(c, http.StatusInternalServerError, "保存失败："+err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"url": "/popup-img/" + name, "size": len(data)})
	})

	r.GET("/popup-img/:name", func(c *gin.Context) {
		name := c.Param("name")
		if !popupImgRe.MatchString(name) {
			apiError(c, http.StatusNotFound, "Not Found")
			return
		}
		p := filepath.Join(uploadDir(), name)
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			apiError(c, http.StatusNotFound, "Not Found")
			return
		}
		// 文件名即内容哈希：可以放心长期缓存；nosniff 防止被当成别的类型解析
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Access-Control-Allow-Origin", "*")
		c.File(p)
	})

	r.GET("/api/sync/popup", func(c *gin.Context) {
		if SYNC_TOKEN == "" {
			restError(c, http.StatusNotFound, "not_found", "sync disabled")
			return
		}
		if bearerToken(c) != SYNC_TOKEN {
			restError(c, http.StatusUnauthorized, "unauthorized", "invalid sync token")
			return
		}
		p := getSiteSettings().Popup
		c.Header("Cache-Control", "no-store")
		if !p.Enabled || !p.ShowIPW {
			c.JSON(http.StatusOK, gin.H{"popup": nil, "generatedAt": time.Now().UTC()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"popup": p.view(publicBaseURL(c)), "generatedAt": time.Now().UTC()})
	})
}

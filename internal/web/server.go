package web

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stm/video-transcoder/internal/cleaner"
	"github.com/stm/video-transcoder/internal/config"
	"github.com/stm/video-transcoder/internal/database"
	"github.com/stm/video-transcoder/internal/metrics"
	"github.com/stm/video-transcoder/internal/scanner"
	"github.com/stm/video-transcoder/internal/worker"
)

// Server Web服务器
type Server struct {
	config  *config.Config
	db      *database.DB
	scanner *scanner.Scanner
	worker  *worker.Worker
	cleaner *cleaner.Cleaner
	router  *gin.Engine
}

// New 创建Web服务器实例
func New(cfg *config.Config, db *database.DB, scan *scanner.Scanner, work *worker.Worker, clean *cleaner.Cleaner) *Server {
	// 设置Gin模式
	gin.SetMode(gin.ReleaseMode)

	router := gin.Default()

	// 加载HTML模板
	router.LoadHTMLGlob("/app/templates/*.html")

	s := &Server{
		config:  cfg,
		db:      db,
		scanner: scan,
		worker:  work,
		cleaner: clean,
		router:  router,
	}

	s.setupRoutes()
	return s
}

// setupRoutes 设置路由
func (s *Server) setupRoutes() {
	// API路由
	api := s.router.Group("/api")
	{
		api.GET("/stats", s.handleGetStats)
		api.GET("/tasks", s.handleGetTasks)
		api.POST("/tasks/retry-failed", s.handleRetryFailedTasks)
		api.POST("/tasks/retry-processing", s.handleRetryProcessingTasks)
		api.POST("/scan", s.handleTriggerScan)
		api.POST("/tasks/:id/retry", s.handleRetryTask)
		api.DELETE("/tasks/:id", s.handleDeleteTask)
		api.GET("/worker/status", s.handleWorkerStatus)
		api.POST("/worker/force-start", s.handleForceStart)
		api.POST("/worker/force-stop", s.handleForceStop)
		api.POST("/worker/set-max", s.handleSetMaxWorkers)
		api.GET("/directories", s.handleGetDirectories)         // 获取监控目录列表
		api.POST("/directories", s.handleAddDirectory)          // 添加监控目录
		api.DELETE("/directories", s.handleRemoveDirectory)     // 删除监控目录
		api.GET("/directories/browse", s.handleBrowseDirectory) // 新增：浏览目录
		api.GET("/trash", s.handleGetTrash)
		api.DELETE("/trash/:filename", s.handleDeleteTrash)
		api.GET("/health", s.handleHealth)
	}

	// Prometheus metrics 端点
	s.router.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// 前端路由
	s.router.GET("/", s.handleIndex)
	s.router.GET("/tasks", s.handleTasksPage)
	s.router.GET("/trash", s.handleTrashPage)
}

// handleGetStats 获取统计信息
func (s *Server) handleGetStats(c *gin.Context) {
	stats, err := s.db.GetStats()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 更新 Prometheus metrics
	metrics.UpdateTaskStats(
		stats.PendingCount,
		stats.ProcessingCount,
		stats.CompletedCount,
		stats.FailedCount,
	)

	c.JSON(http.StatusOK, gin.H{
		"pending":    stats.PendingCount,
		"processing": stats.ProcessingCount,
		"completed":  stats.CompletedCount,
		"failed":     stats.FailedCount,
		"saved_gb":   float64(stats.TotalSaved) / 1024 / 1024 / 1024,
	})
}

// handleGetTasks 获取任务列表
func (s *Server) handleGetTasks(c *gin.Context) {
	status := c.Query("status")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))

	if page < 1 {
		page = 1
	}
	offset := (page - 1) * limit

	var (
		tasks []*database.Task
		err   error
	)
	if status == "scan_error" {
		tasks, err = s.db.GetScanErrorTasks(limit, offset)
	} else {
		tasks, err = s.db.GetAllTasks(status, limit, offset)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, tasks)
}

// handleTriggerScan 手动触发扫描
func (s *Server) handleTriggerScan(c *gin.Context) {
	log.Printf("[API] 收到手动扫描请求，来自: %s", c.ClientIP())

	go func() {
		// 使用独立的 context，不绑定到 HTTP 请求生命周期
		ctx := context.Background()
		log.Printf("[API] 启动扫描 goroutine，context 类型: %T, 已取消: %v", ctx, ctx.Err() != nil)

		if err := s.scanner.Scan(ctx); err != nil {
			log.Printf("手动扫描失败: %v", err)
		}
	}()

	c.JSON(http.StatusOK, gin.H{"message": "扫描已启动"})
}

// handleRetryTask 重试失败任务
func (s *Server) handleRetryTask(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的任务ID"})
		return
	}

	if err := s.db.UpdateTaskStatus(id, database.StatusPending, "手动重试"); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "任务已重置为待处理"})
}

// handleRetryFailedTasks 一键重试所有失败任务
func (s *Server) handleRetryFailedTasks(c *gin.Context) {
	count, err := s.db.ResetFailedTasksToPending()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "失败任务已重置为待处理",
		"count":   count,
	})
}

// handleRetryProcessingTasks 恢复未完成任务
func (s *Server) handleRetryProcessingTasks(c *gin.Context) {
	count, err := s.db.ResetProcessingTasksToPending()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "未完成任务已重置为待处理",
		"count":   count,
	})
}

// handleDeleteTask 删除任务记录
func (s *Server) handleDeleteTask(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的任务ID"})
		return
	}

	if err := s.db.DeleteTask(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "任务已删除"})
}

// handleWorkerStatus 获取Worker运行状态
func (s *Server) handleWorkerStatus(c *gin.Context) {
	isWorking := s.worker.IsWorkingHours()
	forceRun := s.worker.GetForceRun()
	workerCount := s.worker.GetWorkerCount()
	maxWorkers := s.worker.GetMaxWorkers()

	c.JSON(http.StatusOK, gin.H{
		"is_working_hours": isWorking,
		"force_run":        forceRun,
		"worker_count":     workerCount,
		"max_workers":      maxWorkers,
		"active":           forceRun || isWorking,
		"mode":             s.getWorkerMode(),
	})
}

// handleForceStart 强制启动Worker
func (s *Server) handleForceStart(c *gin.Context) {
	s.worker.SetForceRun(true)
	c.JSON(http.StatusOK, gin.H{"message": "强制运行模式已启用"})
}

// handleForceStop 停止强制运行
func (s *Server) handleForceStop(c *gin.Context) {
	s.worker.SetForceRun(false)
	c.JSON(http.StatusOK, gin.H{"message": "强制运行模式已关闭"})
}

// handleSetMaxWorkers 设置最大Worker数量
func (s *Server) handleSetMaxWorkers(c *gin.Context) {
	var req struct {
		MaxWorkers int `json:"max_workers" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	if req.MaxWorkers < 1 || req.MaxWorkers > 10 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Worker数量必须在1-10之间"})
		return
	}

	s.worker.SetMaxWorkers(req.MaxWorkers)
	c.JSON(http.StatusOK, gin.H{
		"message":     "最大Worker数量已更新",
		"max_workers": req.MaxWorkers,
	})
}

// handleGetDirectories 获取监控目录列表
func (s *Server) handleGetDirectories(c *gin.Context) {
	pairs := s.config.GetPairs()
	c.JSON(http.StatusOK, gin.H{
		"pairs": pairs,
	})
}

// handleAddDirectory 添加监控目录配对
func (s *Server) handleAddDirectory(c *gin.Context) {
	var req struct {
		InputDir  string `json:"input_dir" binding:"required"`
		OutputDir string `json:"output_dir" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	if err := s.config.AddInputOutputPair(req.InputDir, req.OutputDir); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var warning string
	if err := s.config.Save(); err != nil {
		warning = "目录已生效，但配置文件保存失败，重启容器后可能丢失: " + err.Error()
		log.Printf("[API] 保存配置失败: %v", err)
	}

	go func() {
		if err := s.scanner.Scan(context.Background()); err != nil {
			log.Printf("[API] 新增目录后立即扫描失败: %v", err)
		}
	}()

	resp := gin.H{
		"message":    "目录配对已添加",
		"input_dir":  req.InputDir,
		"output_dir": req.OutputDir,
		"pairs":      s.config.GetPairs(),
	}
	if warning != "" {
		resp["warning"] = warning
	}
	c.JSON(http.StatusOK, resp)
}

// handleRemoveDirectory 删除监控目录配对
func (s *Server) handleRemoveDirectory(c *gin.Context) {
	var req struct {
		InputDir string `json:"input_dir" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	if err := s.config.RemoveInputOutputPair(req.InputDir); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var warning string
	if err := s.config.Save(); err != nil {
		warning = "目录已移除，但配置文件保存失败，重启容器后可能恢复: " + err.Error()
		log.Printf("[API] 保存配置失败: %v", err)
	}

	resp := gin.H{
		"message": "目录配对已删除",
		"pairs":   s.config.GetPairs(),
	}
	if warning != "" {
		resp["warning"] = warning
	}
	c.JSON(http.StatusOK, resp)
}

// handleBrowseDirectory 浏览目录（用于选择监控目录）
func (s *Server) handleBrowseDirectory(c *gin.Context) {
	path := c.Query("path")
	if path == "" {
		path = s.getDefaultBrowsePath()
	}

	path = filepath.Clean(path)
	if path == "." {
		path = "/"
	}
	if !filepath.IsAbs(path) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "路径必须是绝对路径"})
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "路径不可访问: " + err.Error()})
		return
	}
	if !info.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "路径不是目录"})
		return
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "读取目录失败: " + err.Error()})
		return
	}

	type DirInfo struct {
		Name  string `json:"name"`
		Path  string `json:"path"`
		IsDir bool   `json:"is_dir"`
	}

	var dirs []DirInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue // 只返回目录
		}

		// 跳过隐藏目录
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		fullPath := filepath.Join(path, entry.Name())
		if info, err := entry.Info(); err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				continue
			}
		}
		dirs = append(dirs, DirInfo{
			Name:  entry.Name(),
			Path:  fullPath,
			IsDir: true,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"current_path": path,
		"parent_path":  filepath.Dir(path),
		"directories":  dirs,
	})
}

// handleGetTrash 获取垃圾桶文件列表
func (s *Server) handleGetTrash(c *gin.Context) {
	files, err := s.cleaner.ListTrashFiles()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 计算总大小
	var totalSize int64
	for _, f := range files {
		totalSize += f.Size
	}

	c.JSON(http.StatusOK, gin.H{
		"files":      files,
		"total_size": totalSize,
	})
}

// handleDeleteTrash 删除垃圾桶文件
func (s *Server) handleDeleteTrash(c *gin.Context) {
	filename := c.Param("filename")

	if err := s.cleaner.DeleteTrashFile(filename); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "文件已删除"})
}

// handleHealth 健康检查
func (s *Server) handleHealth(c *gin.Context) {
	// 检查数据库连接
	dbOk := true
	if _, err := s.db.GetStats(); err != nil {
		dbOk = false
	}

	// 获取 Worker 状态
	workerOk := s.worker.GetWorkerCount() >= 0

	// 总体健康状态
	healthy := dbOk && workerOk

	status := "healthy"
	statusCode := http.StatusOK
	if !healthy {
		status = "unhealthy"
		statusCode = http.StatusServiceUnavailable
	}

	c.JSON(statusCode, gin.H{
		"status":        status,
		"database":      dbOk,
		"worker":        workerOk,
		"worker_count":  s.worker.GetWorkerCount(),
		"force_run":     s.worker.GetForceRun(),
		"working_hours": s.worker.IsWorkingHours(),
	})
}

// getWorkerMode 获取Worker模式描述
func (s *Server) getWorkerMode() string {
	if s.worker.GetForceRun() {
		return "强制运行"
	} else if s.worker.IsWorkingHours() {
		return "自动运行（工作时间）"
	} else {
		return "休眠中"
	}
}

// Start 启动Web服务器
func (s *Server) Start(addr string) error {
	log.Printf("[Web] 启动Web服务器: http://%s", addr)
	return s.router.Run(addr)
}

func (s *Server) getDefaultBrowsePath() string {
	for _, pair := range s.config.GetPairs() {
		if info, err := os.Stat(pair.Input); err == nil && info.IsDir() {
			return pair.Input
		}
	}

	candidates := []string{"/mnt", "/input", "/media", "/"}
	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return path
		}
	}
	return "/"
}

// handleIndex 首页（仪表盘）
func (s *Server) handleIndex(c *gin.Context) {
	c.HTML(http.StatusOK, "index.html", nil)
}

// handleTasksPage 任务列表页
func (s *Server) handleTasksPage(c *gin.Context) {
	c.HTML(http.StatusOK, "tasks.html", nil)
}

// handleTrashPage 垃圾桶页
func (s *Server) handleTrashPage(c *gin.Context) {
	c.HTML(http.StatusOK, "trash.html", nil)
}

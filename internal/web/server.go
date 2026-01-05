package web

import (
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

	tasks, err := s.db.GetAllTasks(status, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, tasks)
}

// handleTriggerScan 手动触发扫描
func (s *Server) handleTriggerScan(c *gin.Context) {
	go func() {
		if err := s.scanner.Scan(c.Request.Context()); err != nil {
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
	dirs := s.config.GetInputDirs()
	c.JSON(http.StatusOK, gin.H{
		"input_dirs": dirs,
		"output_dir": s.config.Path.Output,
	})
}

// handleAddDirectory 添加监控目录
func (s *Server) handleAddDirectory(c *gin.Context) {
	var req struct {
		Directory string `json:"directory" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	if err := s.config.AddInputDir(req.Directory); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":    "目录已添加",
		"directory":  req.Directory,
		"input_dirs": s.config.GetInputDirs(),
	})
}

// handleRemoveDirectory 删除监控目录
func (s *Server) handleRemoveDirectory(c *gin.Context) {
	var req struct {
		Directory string `json:"directory" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	if err := s.config.RemoveInputDir(req.Directory); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":    "目录已删除",
		"input_dirs": s.config.GetInputDirs(),
	})
}

// handleBrowseDirectory 浏览目录（用于选择监控目录）
func (s *Server) handleBrowseDirectory(c *gin.Context) {
	path := c.Query("path")
	if path == "" {
		path = "/mnt" // 默认从 /mnt 开始浏览
	}

	// 安全检查：只允许浏览 /mnt 下的目录
	if !strings.HasPrefix(path, "/mnt") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "只能浏览 /mnt 目录"})
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

package web

import (
	"context"
	"crypto/subtle"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stm/video-transcoder/internal/cleaner"
	"github.com/stm/video-transcoder/internal/config"
	"github.com/stm/video-transcoder/internal/database"
	"github.com/stm/video-transcoder/internal/metrics"
	"github.com/stm/video-transcoder/internal/scanner"
	"github.com/stm/video-transcoder/internal/worker"
)

// ScanRunner 定义扫描器接口
type ScanRunner interface {
	Scan(ctx context.Context) error
	StartScanAsync(ctx context.Context) error
}

// Server Web服务器
type Server struct {
	config        *config.Config
	db            *database.DB
	scanner       ScanRunner // 使用接口替代具体实现
	worker        *worker.Worker
	cleaner       *cleaner.Cleaner
	router        *gin.Engine
	srv           *http.Server
	ctx           context.Context // 应用程序生命周期 context
	metricsCancel context.CancelFunc
}

// New 创建Web服务器实例
func New(cfg *config.Config, db *database.DB, scan *scanner.Scanner, work *worker.Worker, clean *cleaner.Cleaner) *Server {
	// 设置Gin模式
	gin.SetMode(gin.ReleaseMode)

	router := gin.Default()

	// 加载HTML模板
	templatePath := cfg.Path.Templates
	if templatePath == "" {
		templatePath = "/app/templates"
	}
	// 确保路径以 / 结尾以便拼接 glob
	if !strings.HasSuffix(templatePath, "/") {
		templatePath += "/"
	}

	// 检查目录是否存在，如果不存在则尝试默认本地路径（用于开发环境）
	if _, err := os.Stat(templatePath); os.IsNotExist(err) {
		// 尝试多个本地回退路径
		candidates := []string{
			"internal/web/templates/", // 源代码结构
			"templates/",              // 工作目录
		}
		for _, path := range candidates {
			if _, err := os.Stat(path); err == nil {
				templatePath = path
				break
			}
		}
	}

	router.LoadHTMLGlob(templatePath + "*.html")

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

// SetContext 设置应用程序生命周期 context
func (s *Server) SetContext(ctx context.Context) {
	s.ctx = ctx
}

// apiKeyAuth API 密钥认证中间件
func (s *Server) apiKeyAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 如果未配置 API Key，则跳过认证
		apiKey := s.config.Web.APIKey
		if apiKey == "" {
			c.Next()
			return
		}

		// 从请求头获取 API Key
		requestKey := c.GetHeader("X-API-Key")
		if requestKey == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "缺少 X-API-Key 请求头"})
			c.Abort()
			return
		}

		// 验证 API Key
		if subtle.ConstantTimeCompare([]byte(requestKey), []byte(apiKey)) != 1 {

			log.Printf("[API Security] 非法访问尝试，来自: %s", c.ClientIP())
			c.JSON(http.StatusUnauthorized, gin.H{"error": "无效的 API Key"})
			c.Abort()
			return
		}

		c.Next()
	}
}

// setupRoutes 设置路由
func (s *Server) setupRoutes() {
	// API路由（需要认证）
	api := s.router.Group("/api")
	api.Use(s.apiKeyAuth()) // 为所有 API 添加认证中间件
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
		api.POST("/trash/restore", s.handleRestoreTrash)
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
		log.Printf("[API] Error getting stats: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get stats"})
		return
	}

	s.updateTaskMetrics(stats)

	c.JSON(http.StatusOK, gin.H{
		"pending":       stats.PendingCount,
		"processing":    stats.ProcessingCount,
		"completed":     stats.CompletedCount,
		"failed":        stats.FailedCount,
		"soft_deleted":  stats.SoftDeletedCount,
		"hard_deleted":  stats.HardDeletedCount,
		"cleanup_error": stats.CleanupErrorCount,
		"saved_gb":      float64(stats.TotalSaved) / 1024 / 1024 / 1024,
	})
}

func (s *Server) updateTaskMetrics(stats *database.Stats) {
	metrics.UpdateTaskStats(
		stats.PendingCount,
		stats.ProcessingCount,
		stats.CompletedCount,
		stats.FailedCount,
		stats.SoftDeletedCount,
		stats.HardDeletedCount,
		stats.CleanupErrorCount,
	)
}

func (s *Server) startMetricsSync(ctx context.Context) {
	// 启动时先同步一次，避免冷启动阶段指标全 0
	if stats, err := s.db.GetStats(); err == nil {
		s.updateTaskMetrics(stats)
	} else {
		log.Printf("[Web] 初始指标同步失败: %v", err)
	}

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stats, err := s.db.GetStats()
			if err != nil {
				log.Printf("[Web] 指标同步失败: %v", err)
				continue
			}
			s.updateTaskMetrics(stats)
		}
	}
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
		log.Printf("[API] Error getting tasks: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get tasks"})
		return
	}

	c.JSON(http.StatusOK, tasks)
}

// handleTriggerScan 手动触发扫描
func (s *Server) handleTriggerScan(c *gin.Context) {
	log.Printf("[API] 收到手动扫描请求，来自: %s", c.ClientIP())

	// 使用应用程序生命周期 context，如果未设置则使用 Background
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
		log.Println("[API] 警告：Server context 未设置，使用 Background context")
	}

	// 尝试异步启动扫描
	if err := s.scanner.StartScanAsync(ctx); err != nil {
		if errors.Is(err, scanner.ErrScanInProgress) {
			c.JSON(http.StatusConflict, gin.H{"error": "扫描正在进行中"})
			return
		}
		// 其他启动错误（如果有）
		log.Printf("[API] 启动扫描失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "启动扫描失败"})
		return
	}

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
		log.Printf("[API] Error retrying task %d: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retry task"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "任务已重置为待处理"})
}

// handleRetryFailedTasks 一键重试所有失败任务
func (s *Server) handleRetryFailedTasks(c *gin.Context) {
	count, err := s.db.ResetFailedTasksToPending()
	if err != nil {
		log.Printf("[API] Error retrying failed tasks: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retry failed tasks"})
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
		log.Printf("[API] Error retrying processing tasks: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retry processing tasks"})
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
		log.Printf("[API] Error deleting task %d: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete task"})
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
	activeTasks := s.worker.GetActiveTaskCount()
	draining := s.worker.IsDraining()

	c.JSON(http.StatusOK, gin.H{
		"is_working_hours": isWorking,
		"force_run":        forceRun,
		"worker_count":     workerCount,
		"max_workers":      maxWorkers,
		"active_tasks":     activeTasks,
		"draining":         draining,
		"active":           workerCount > 0 || activeTasks > 0,
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
	s.config.SetMaxWorkers(req.MaxWorkers)
	warning := s.persistConfig()

	resp := gin.H{
		"message":     "最大Worker数量已更新",
		"max_workers": req.MaxWorkers,
	}
	if warning != "" {
		resp["warning"] = warning
	}
	c.JSON(http.StatusOK, resp)
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
		log.Printf("[API] Error adding directory pair: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to add directory pair"})
		return
	}

	warning := s.persistConfig()

	// 新增目录后异步触发扫描（使用独立的 context，不受 API 请求生命周期影响）
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
		log.Printf("[API] Error removing directory pair: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to remove directory pair"})
		return
	}

	warning := s.persistConfig()

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

	// 安全检查：防止路径遍历攻击
	// 只允许访问已配置的输入/输出目录及其父目录
	if !s.isPathSafeForBrowsing(path) {
		log.Printf("[API Security] 拒绝目录浏览请求，非法路径: %s, 来自: %s", path, c.ClientIP())
		c.JSON(http.StatusForbidden, gin.H{"error": "无权访问该路径"})
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		log.Printf("[API] Error stating path %s: %v", path, err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Path not accessible"})
		return
	}
	if !info.IsDir() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "路径不是目录"})
		return
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		log.Printf("[API] Error reading directory %s: %v", path, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read directory"})
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

// isPathSafeForBrowsing 检查路径是否允许浏览（防止路径遍历攻击）
// 使用 filepath.Rel 做严格的路径边界判断，避免前缀匹配导致的绕过
func (s *Server) isPathSafeForBrowsing(path string) bool {
	// 清理路径
	cleanPath := filepath.Clean(path)

	// 获取所有已配置的输入/输出目录
	pairs := s.config.GetPairs()

	// 收集所有合法目录
	allowedDirs := make([]string, 0, len(pairs)*2)
	for _, pair := range pairs {
		allowedDirs = append(allowedDirs, filepath.Clean(pair.Input), filepath.Clean(pair.Output))
	}

	for _, dir := range allowedDirs {
		// 精确匹配目录本身
		if cleanPath == dir {
			return true
		}
		// cleanPath 是 dir 的子路径
		if rel, err := filepath.Rel(dir, cleanPath); err == nil && !strings.HasPrefix(rel, "..") {
			return true
		}
		// dir 是 cleanPath 的子路径（允许浏览 dir 的父目录）
		if rel, err := filepath.Rel(cleanPath, dir); err == nil && !strings.HasPrefix(rel, "..") {
			return true
		}
	}

	return false
}

// handleGetTrash 获取垃圾桶文件列表
func (s *Server) handleGetTrash(c *gin.Context) {
	files, err := s.cleaner.ListTrashFiles()
	if err != nil {
		log.Printf("[API] Error getting trash files: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get trash files"})
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
		log.Printf("[API] Error deleting trash file %s: %v", filename, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete trash file"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "文件已删除"})
}

// handleRestoreTrash 恢复垃圾桶文件到原路径
func (s *Server) handleRestoreTrash(c *gin.Context) {
	var req struct {
		Path string `json:"path" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}

	if err := s.cleaner.RestoreTrashFile(req.Path); err != nil {
		log.Printf("[API] Error restoring trash file %s: %v", req.Path, err)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to restore trash file"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "文件已恢复到原路径"})
}

// handleHealth 健康检查
func (s *Server) handleHealth(c *gin.Context) {
	// 检查数据库连接
	dbOk := true
	if _, err := s.db.GetStats(); err != nil {
		dbOk = false
	}

	// 获取 Worker 状态：
	// - 工作时间或强制运行：至少有 1 个 worker 才算健康
	// - 非工作时间：worker 为 0 是预期行为，视为健康
	workerOk := true
	if s.worker.IsWorkingHours() || s.worker.GetForceRun() {
		workerOk = s.worker.GetWorkerCount() > 0
	}

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
	}
	if s.worker.IsWorkingHours() {
		return "自动运行（工作时间）"
	}
	if s.worker.IsDraining() || s.worker.GetActiveTaskCount() > 0 {
		return "非工作时间排空中"
	}
	return "休眠中"
}

func saveConfigHint(err error) string {
	if errors.Is(err, syscall.EROFS) || errors.Is(err, syscall.EPERM) {
		return err.Error() + "（当前配置文件可能是只读挂载，请将 docker-compose 的 config.yaml 挂载改为可写）"
	}
	if errors.Is(err, syscall.EBUSY) || strings.Contains(err.Error(), "device or resource busy") {
		return err.Error() + "（当前配置文件是单文件挂载点，建议改为目录挂载或可写文件挂载）"
	}
	return err.Error()
}

func (s *Server) persistConfig() string {
	// 修复：数据库为主存储且优先级最高，必须先保证DB写成功
	// 否则重启后会从DB加载旧值，导致配置回退
	dbErr := s.db.UpsertAppConfig(s.config.RuntimeKV())
	if dbErr != nil {
		log.Printf("[API] 保存数据库配置失败: %v", dbErr)
		return "配置已在内存生效，但数据库保存失败（重启后会回退到数据库旧值）: " + dbErr.Error()
	}

	// DB 写入成功后才写 YAML（仅作备用）
	fileErr := s.config.Save()
	if fileErr != nil {
		log.Printf("[API] 保存配置文件失败: %v", fileErr)
		return "配置已写入数据库并生效，但回写YAML失败: " + saveConfigHint(fileErr)
	}

	return ""
}

// Start 启动Web服务器
func (s *Server) Start(addr string) error {
	log.Printf("[Web] 启动Web服务器: http://%s", addr)

	metricsCtx, cancel := context.WithCancel(context.Background())
	s.metricsCancel = cancel
	go s.startMetricsSync(metricsCtx)

	s.srv = &http.Server{
		Addr:    addr,
		Handler: s.router,
	}

	if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		// 启动失败时取消 metricsSync goroutine，避免泄漏
		cancel()
		return err
	}
	return nil
}

// Shutdown 优雅关闭Web服务器
func (s *Server) Shutdown(ctx context.Context) error {
	if s.metricsCancel != nil {
		s.metricsCancel()
	}
	if s.srv != nil {
		return s.srv.Shutdown(ctx)
	}
	return nil
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

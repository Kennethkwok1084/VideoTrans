package scanner

import (
	"context"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stm/video-transcoder/internal/config"
	"github.com/stm/video-transcoder/internal/database"
)

// Scanner 目录扫描器
type Scanner struct {
	config *config.Config
	db     *database.DB
}

// New 创建扫描器实例
func New(cfg *config.Config, db *database.DB) *Scanner {
	return &Scanner{
		config: cfg,
		db:     db,
	}
}

// Scan 扫描输入目录并更新数据库
func (s *Scanner) Scan(ctx context.Context) error {
	// 调试日志：检查 context 状态
	if ctx.Err() != nil {
		log.Printf("[Scanner] ⚠️  WARNING: Scan 启动时 context 已被取消: %v", ctx.Err())
	} else {
		log.Printf("[Scanner] ✓ context 状态正常，类型: %T", ctx)
	}

	pairs := s.config.GetPairs()
	log.Printf("[Scanner] 开始扫描 %d 个目录配对", len(pairs))

	totalNew := 0
	totalUpdate := 0
	totalSkip := 0
	startTime := time.Now()

	for _, pair := range pairs {
		log.Printf("[Scanner] 扫描目录: %s -> %s", pair.Input, pair.Output)
		newCount, updateCount, skipCount, err := s.scanDirectory(ctx, pair.Input, pair.Output)
		if err != nil {
			log.Printf("[Scanner] 扫描目录失败 %s: %v", pair.Input, err)
			continue
		}
		totalNew += newCount
		totalUpdate += updateCount
		totalSkip += skipCount
	}

	elapsed := time.Since(startTime)
	log.Printf("[Scanner] 扫描完成，耗时: %v，新增: %d, 更新: %d, 跳过: %d",
		elapsed, totalNew, totalUpdate, totalSkip)

	if err := s.verifyCompletedOutputs(ctx); err != nil {
		log.Printf("[Scanner] 输出校验失败: %v", err)
	}

	return nil
}

// scanDirectory 扫描单个目录
func (s *Scanner) scanDirectory(ctx context.Context, inputDir string, outputDir string) (newCount, updateCount, skipCount int, err error) {
	// 调试日志：扫描开始前检查 context
	if ctx.Err() != nil {
		log.Printf("[Scanner] ⚠️  scanDirectory 启动时 context 已取消: %v", ctx.Err())
		return 0, 0, 0, ctx.Err()
	}

	err = filepath.WalkDir(inputDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			log.Printf("[Scanner] 访问路径失败 %s: %v", path, walkErr)
			return nil // 继续扫描其他文件
		}

		// 检查上下文取消
		select {
		case <-ctx.Done():
			log.Printf("[Scanner] ⚠️  ctx.Done() 被触发，原因: %v", ctx.Err())
			return ctx.Err()
		default:
		}

		// 跳过目录
		if d.IsDir() {
			// 检查是否需要跳过此目录
			if shouldSkipDir(d.Name()) {
				log.Printf("[Scanner] 跳过系统目录: %s", path)
				return filepath.SkipDir
			}
			return nil
		}

		// 文件过滤
		if shouldSkipFile(d.Name()) {
			return nil
		}

		// 检查是否为视频文件
		if !s.config.IsVideoFile(path) {
			return nil
		}

		// 获取文件信息
		info, err := d.Info()
		if err != nil {
			log.Printf("[Scanner] 获取文件信息失败 %s: %v", path, err)
			return nil
		}

		// 计算相对于输入目录的路径
		relPath, err := filepath.Rel(inputDir, path)
		if err != nil {
			log.Printf("[Scanner] 计算相对路径失败 %s: %v", path, err)
			return nil
		}

		// 处理文件（传入对应的输出目录）
		action := s.processFile(path, relPath, outputDir, info.ModTime(), info.Size())
		switch action {
		case "new":
			newCount++
		case "update":
			updateCount++
		case "skip":
			skipCount++
		}

		return nil
	})

	return
}

// processFile 处理单个文件
func (s *Scanner) processFile(fullPath, relPath, outputDir string, mtime time.Time, size int64) string {
	// 查询数据库中是否存在该文件（先尝试完整路径）
	task, err := s.db.GetTaskByPath(fullPath)
	if err != nil {
		log.Printf("[Scanner] 查询任务失败 %s: %v", fullPath, err)
		return "error"
	}

	// 向后兼容：如果没有找到，尝试查询相对路径（旧版本数据）
	if task == nil {
		task, err = s.db.GetTaskByPath(relPath)
		if err != nil {
			log.Printf("[Scanner] 查询任务失败 %s: %v", relPath, err)
			return "error"
		}
		// 如果找到了旧记录，更新为完整路径
		if task != nil {
			log.Printf("[Scanner] 发现旧版本记录，更新路径: %s -> %s", relPath, fullPath)
			if err := s.db.UpdateTaskPath(task.ID, fullPath); err != nil {
				log.Printf("[Scanner] 更新任务路径失败: %v", err)
				return "error"
			}
			// 重新查询获取更新后的任务
			task, _ = s.db.GetTaskByPath(fullPath)
		}
	}

	// 情况1: 新文件
	if task == nil {
		newTask := &database.Task{
			SourcePath:  fullPath,
			SourceMtime: mtime,
			SourceSize:  size,
		}
		if err := s.db.CreateTask(newTask); err != nil {
			log.Printf("[Scanner] 创建任务失败 %s: %v", fullPath, err)
			return "error"
		}
		log.Printf("[Scanner] 新文件入库: %s (%.2f MB)",
			fullPath, float64(size)/1024/1024)
		return "new"
	}

	// 情况2: 文件已更新（mtime或size变化）
	if !task.SourceMtime.Equal(mtime) || task.SourceSize != size {
		if err := s.db.ResetTaskToPending(fullPath, mtime, size); err != nil {
			log.Printf("[Scanner] 重置任务失败 %s: %v", fullPath, err)
			return "error"
		}
		log.Printf("[Scanner] 文件已更新，重置任务: %s", relPath)
		return "update"
	}

	// 情况3: 已完成且目标文件存在
	if task.Status == database.StatusCompleted {
		// 使用配对的输出目录而非全局配置
		basePath := filepath.Join(outputDir, relPath)
		targetPath := s.config.ApplyOutputExtension(basePath)
		if _, err := os.Stat(targetPath); err == nil {
			return "skip"
		}
		if targetPath != basePath {
			if _, err := os.Stat(basePath); err == nil {
				return "skip"
			}
		}
		// 目标文件不存在，重置任务
		log.Printf("[Scanner] 目标文件丢失，重置任务: %s", relPath)
		if err := s.db.ResetTaskToPending(fullPath, mtime, size); err != nil {
			log.Printf("[Scanner] 重置任务失败 %s: %v", fullPath, err)
			return "error"
		}
		return "update"
	}

	return "skip"
}

// shouldSkipDir 检查是否应跳过该目录
func shouldSkipDir(name string) bool {
	skipDirs := []string{
		".stm_trash", // 垃圾桶
		"@eaDir",     // 群晖索引
		"#recycle",   // 群晖回收站
		".DS_Store",  // macOS
	}

	for _, dir := range skipDirs {
		if name == dir {
			return true
		}
	}

	return false
}

// shouldSkipFile 检查是否应跳过该文件（支持通配符）
func shouldSkipFile(name string) bool {
	// 群晖缩略图/视频
	if strings.HasPrefix(name, "SYNOPHOTO_") {
		return true
	}

	// 隐藏文件
	if strings.HasPrefix(name, ".") {
		return true
	}

	// 临时文件
	if strings.HasSuffix(name, ".tmp") || strings.HasSuffix(name, ".part") {
		return true
	}

	// 锁文件
	if strings.HasSuffix(name, ".lock") {
		return true
	}

	return false
}

// RunPeriodically 周期性运行扫描器
func (s *Scanner) RunPeriodically(ctx context.Context) {
	interval := time.Duration(s.config.System.ScanInterval) * time.Minute
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.Printf("[Scanner] 启动周期性扫描，间隔: %v", interval)

	// 立即执行一次
	if err := s.Scan(ctx); err != nil && err != context.Canceled {
		log.Printf("[Scanner] 扫描失败: %v", err)
	}

	// 周期性执行
	for {
		select {
		case <-ctx.Done():
			log.Println("[Scanner] 收到停止信号，退出扫描")
			return
		case <-ticker.C:
			if err := s.Scan(ctx); err != nil && err != context.Canceled {
				log.Printf("[Scanner] 扫描失败: %v", err)
			}
		}
	}
}

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
	log.Printf("[Scanner] 开始扫描目录: %s", s.config.Path.Input)
	startTime := time.Now()
	newCount := 0
	updateCount := 0
	skipCount := 0

	err := filepath.WalkDir(s.config.Path.Input, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			log.Printf("[Scanner] 访问路径失败 %s: %v", path, err)
			return nil // 继续扫描其他文件
		}

		// 检查上下文取消
		select {
		case <-ctx.Done():
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

		// 计算相对路径
		relPath, err := filepath.Rel(s.config.Path.Input, path)
		if err != nil {
			log.Printf("[Scanner] 计算相对路径失败 %s: %v", path, err)
			return nil
		}

		// 处理文件
		action := s.processFile(relPath, info.ModTime(), info.Size())
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

	duration := time.Since(startTime)
	log.Printf("[Scanner] 扫描完成: 新增=%d, 更新=%d, 跳过=%d, 耗时=%v",
		newCount, updateCount, skipCount, duration)

	return err
}

// processFile 处理单个文件
func (s *Scanner) processFile(relPath string, mtime time.Time, size int64) string {
	// 查询数据库中是否存在该文件
	task, err := s.db.GetTaskByPath(relPath)
	if err != nil {
		log.Printf("[Scanner] 查询任务失败 %s: %v", relPath, err)
		return "error"
	}

	// 情况1: 新文件
	if task == nil {
		newTask := &database.Task{
			SourcePath:  relPath,
			SourceMtime: mtime,
			SourceSize:  size,
		}
		if err := s.db.CreateTask(newTask); err != nil {
			log.Printf("[Scanner] 创建任务失败 %s: %v", relPath, err)
			return "error"
		}
		log.Printf("[Scanner] 新文件入库: %s (%.2f MB)",
			relPath, float64(size)/1024/1024)
		return "new"
	}

	// 情况2: 文件已更新（mtime或size变化）
	if !task.SourceMtime.Equal(mtime) || task.SourceSize != size {
		if err := s.db.ResetTaskToPending(relPath, mtime, size); err != nil {
			log.Printf("[Scanner] 重置任务失败 %s: %v", relPath, err)
			return "error"
		}
		log.Printf("[Scanner] 文件已更新，重置任务: %s", relPath)
		return "update"
	}

	// 情况3: 已完成且目标文件存在
	if task.Status == database.StatusCompleted {
		targetPath := filepath.Join(s.config.Path.Output, relPath)
		if _, err := os.Stat(targetPath); err == nil {
			return "skip"
		}
		// 目标文件不存在，重置任务
		log.Printf("[Scanner] 目标文件丢失，重置任务: %s", relPath)
		if err := s.db.ResetTaskToPending(relPath, mtime, size); err != nil {
			log.Printf("[Scanner] 重置任务失败 %s: %v", relPath, err)
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

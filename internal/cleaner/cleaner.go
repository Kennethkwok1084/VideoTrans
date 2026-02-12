package cleaner

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/stm/video-transcoder/internal/config"
	"github.com/stm/video-transcoder/internal/database"
	"github.com/stm/video-transcoder/internal/metrics"
)

// Cleaner 清理模块
type Cleaner struct {
	config *config.Config
	db     *database.DB
}

// New 创建Cleaner实例
func New(cfg *config.Config, db *database.DB) *Cleaner {
	return &Cleaner{
		config: cfg,
		db:     db,
	}
}

// Run 运行清理任务（每天上午10点执行）
func (c *Cleaner) Run(ctx context.Context) {
	log.Println("[Cleaner] 清理模块启动")

	// 创建 cron 调度器
	cronScheduler := cron.New()

	// 添加定时任务：每天上午 10:00 执行
	cronSpec := strings.TrimSpace(c.config.Cleaning.Cron)
	if cronSpec == "" {
		cronSpec = "0 10 * * *"
	}
	_, err := cronScheduler.AddFunc(cronSpec, func() {
		c.runCleaning()
	})

	if err != nil {
		log.Printf("[Cleaner] 添加定时任务失败: %v", err)
		return
	}

	// 启动调度器
	cronScheduler.Start()
	log.Printf("[Cleaner] Cron 调度器已启动（%s）", cronSpec)

	// 可选：立即执行一次清理
	go c.runCleaning()

	// 等待停止信号
	<-ctx.Done()
	log.Println("[Cleaner] 收到停止信号，停止清理模块")
	cronScheduler.Stop()
}

// runCleaning 执行清理任务
func (c *Cleaner) runCleaning() {
	log.Println("[Cleaner] 开始执行清理任务")

	// Phase 5: 先尝试自愈 cleanup_error 任务，避免长期堆积
	if err := c.retryCleanupErrors(); err != nil {
		log.Printf("[Cleaner] cleanup_error 自动重试失败: %v", err)
	}

	// 一级清理：移入垃圾桶
	if err := c.moveToTrash(); err != nil {
		log.Printf("[Cleaner] 移入垃圾桶失败: %v", err)
	}

	// 二级清理：清空垃圾桶
	if err := c.emptyTrash(); err != nil {
		log.Printf("[Cleaner] 清空垃圾桶失败: %v", err)
	}

	log.Println("[Cleaner] 清理任务完成")
}

// retryCleanupErrors 自动重试清理失败任务
// 恢复策略：
// 1) 有 trash_path 的任务恢复到 soft_deleted（通常为硬删除阶段失败）
// 2) 无 trash_path 的任务恢复到 completed（通常为软删除阶段失败）
func (c *Cleaner) retryCleanupErrors() error {
	limit := c.config.Cleaning.CleanupRetryBatch
	if limit <= 0 {
		limit = 200
	}

	tasks, err := c.db.GetCleanupErrorTasks(limit)
	if err != nil {
		return fmt.Errorf("查询 cleanup_error 任务失败: %w", err)
	}

	if len(tasks) == 0 {
		return nil
	}

	recovered := 0
	failed := 0

	for _, task := range tasks {
		targetStatus := database.StatusCompleted
		if task.GetTrashPath() != "" {
			targetStatus = database.StatusSoftDeleted
		}

		note := fmt.Sprintf("自动重试恢复到 %s（原始错误: %s）", targetStatus, task.GetCleanupLog())
		if err := c.db.ResetCleanupErrorStatus(task.ID, targetStatus, note); err != nil {
			log.Printf("[Cleaner] cleanup_error 任务恢复失败 #%d: %v", task.ID, err)
			failed++
			continue
		}
		recovered++
	}

	log.Printf("[Cleaner] cleanup_error 自动重试: 恢复 %d, 失败 %d", recovered, failed)
	return nil
}

// moveToTrash 将完成N天的源文件移入垃圾桶
func (c *Cleaner) moveToTrash() error {
	// 计算截止时间
	cutoffTime := time.Now().AddDate(0, 0, -c.config.Cleaning.SoftDeleteDays)

	// 查询符合条件的任务（状态为 completed）
	tasks, err := c.db.GetCompletedOldTasks(cutoffTime)
	if err != nil {
		return fmt.Errorf("查询旧任务失败: %w", err)
	}

	if len(tasks) == 0 {
		log.Println("[Cleaner] 没有需要移入垃圾桶的文件")
		return nil
	}

	log.Printf("[Cleaner] 找到 %d 个需要移入垃圾桶的文件", len(tasks))
	movedCount := 0
	errorCount := 0

	for _, task := range tasks {
		srcPath := c.resolveSourcePath(task.SourcePath)
		if srcPath == "" {
			log.Printf("[Cleaner] 无法解析路径 %s，标记为清理错误", task.SourcePath)
			c.db.MarkCleanupError(task.ID, "无法解析源文件路径")
			errorCount++
			continue
		}

		// 检查源文件是否存在
		if _, err := os.Stat(srcPath); os.IsNotExist(err) {
			log.Printf("[Cleaner] 源文件不存在 %s，标记为清理错误", srcPath)
			c.db.MarkCleanupError(task.ID, "源文件不存在")
			errorCount++
			continue
		}

		// 移动到垃圾桶，返回实际的垃圾桶路径
		trashPath, err := c.safeMoveToTrash(srcPath)
		if err != nil {
			log.Printf("[Cleaner] 移动文件失败 %s: %v", task.SourcePath, err)
			c.db.MarkCleanupError(task.ID, fmt.Sprintf("移动文件失败: %v", err))
			errorCount++
			continue
		}

		// 更新数据库状态为 soft_deleted
		if err := c.db.MarkSoftDeleted(task.ID, trashPath); err != nil {
			log.Printf("[Cleaner] 更新数据库失败 %s: %v，尝试回滚文件", task.SourcePath, err)
			// 尝试回滚：把文件移回去
			if rollbackErr := os.Rename(trashPath, srcPath); rollbackErr != nil {
				log.Printf("[Cleaner] 回滚失败，文件可能处于不一致状态: 源=%s 垃圾桶=%s", srcPath, trashPath)
			}
			c.db.MarkCleanupError(task.ID, fmt.Sprintf("更新数据库失败: %v", err))
			errorCount++
			continue
		}

		movedCount++
		log.Printf("[Cleaner] 已移入垃圾桶: %s -> %s", task.SourcePath, trashPath)

		// 更新 Prometheus metrics
		metrics.FilesSoftDeleted.Inc()
	}

	log.Printf("[Cleaner] 软删除完成: 成功 %d, 失败 %d", movedCount, errorCount)
	return nil
}

// safeMoveToTrash 安全地移动文件到垃圾桶，返回垃圾桶路径
func (c *Cleaner) safeMoveToTrash(srcPath string) (string, error) {
	// 查找文件所属的输入根目录，以统一垃圾桶位置
	root := c.config.GetInputRoot(srcPath)
	var trashDir string

	if root != "" {
		trashDir = filepath.Join(root, c.config.Path.Trash)
	} else {
		// 如果找不到输入根目录（罕见），回退到同级目录
		log.Printf("[Cleaner] 警告: 无法确定文件 %s 的输入根目录，将在同级目录创建垃圾桶", srcPath)
		trashDir = filepath.Join(filepath.Dir(srcPath), c.config.Path.Trash)
	}

	// 确保垃圾桶目录存在
	if err := os.MkdirAll(trashDir, 0755); err != nil {
		return "", fmt.Errorf("创建垃圾桶目录失败: %w", err)
	}

	// 生成带时间戳的目标文件名
	filename := filepath.Base(srcPath)
	timestamp := time.Now().Format("20060102_150405")
	trashPath := filepath.Join(trashDir, filename+"_del_"+timestamp)

	// 尝试直接移动（同分区快速操作）
	err := os.Rename(srcPath, trashPath)
	if err == nil {
		log.Printf("[Cleaner] 文件已移入垃圾桶（os.Rename）: %s", trashPath)
		return trashPath, nil
	}

	// 检查是否为跨分区错误
	if !isLinkError(err) {
		return "", err
	}

	// 跨分区：使用复制+删除
	log.Printf("[Cleaner] 检测到跨分区，使用复制+删除模式: %s", srcPath)
	if err := c.copyAndDelete(srcPath, trashPath); err != nil {
		return "", err
	}
	return trashPath, nil
}

// isLinkError 检查是否为跨设备链接错误
func isLinkError(err error) bool {
	return strings.Contains(err.Error(), "invalid cross-device link")
}

// copyAndDelete 复制文件然后删除源文件
func (c *Cleaner) copyAndDelete(src, dst string) error {
	// 打开源文件
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("打开源文件失败: %w", err)
	}
	defer srcFile.Close()

	// 创建目标文件
	dstFile, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("创建目标文件失败: %w", err)
	}
	defer dstFile.Close()

	// 复制数据
	written, err := io.Copy(dstFile, srcFile)
	if err != nil {
		os.Remove(dst) // 清理失败的复制
		return fmt.Errorf("复制数据失败: %w", err)
	}

	// 验证文件大小
	srcInfo, _ := srcFile.Stat()
	if written != srcInfo.Size() {
		os.Remove(dst)
		return fmt.Errorf("复制数据不完整: 预期 %d 字节, 实际 %d 字节",
			srcInfo.Size(), written)
	}

	// 同步到磁盘
	if err := dstFile.Sync(); err != nil {
		os.Remove(dst)
		return fmt.Errorf("同步数据失败: %w", err)
	}

	// 删除源文件
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("删除源文件失败: %w", err)
	}

	log.Printf("[Cleaner] 跨分区移动成功: %s -> %s", src, dst)
	return nil
}

// emptyTrash 清空超过N天的垃圾桶文件
func (c *Cleaner) emptyTrash() error {
	cutoffTime := time.Now().AddDate(0, 0, -c.config.Cleaning.HardDeleteDays)

	// 从数据库查询超时的软删除任务
	tasks, err := c.db.GetSoftDeletedOldTasks(cutoffTime)
	if err != nil {
		return fmt.Errorf("查询软删除任务失败: %w", err)
	}

	if len(tasks) == 0 {
		log.Println("[Cleaner] 垃圾桶中没有过期文件")
		return nil
	}

	log.Printf("[Cleaner] 找到 %d 个需要硬删除的文件", len(tasks))
	deletedCount := 0
	errorCount := 0

	for _, task := range tasks {
		trashPath := task.GetTrashPath()
		if trashPath == "" {
			log.Printf("[Cleaner] 任务 %d 缺少垃圾桶路径，标记为清理错误", task.ID)
			c.db.MarkCleanupError(task.ID, "缺少垃圾桶路径")
			errorCount++
			continue
		}

		// 检查垃圾桶文件是否存在
		if _, err := os.Stat(trashPath); os.IsNotExist(err) {
			// 文件已经不存在，直接标记为硬删除
			log.Printf("[Cleaner] 垃圾桶文件不存在 %s，直接标记为硬删除", trashPath)
			if err := c.db.MarkHardDeleted(task.ID); err != nil {
				log.Printf("[Cleaner] 更新数据库失败 %s: %v", trashPath, err)
				c.db.MarkCleanupError(task.ID, fmt.Sprintf("更新数据库失败: %v", err))
				errorCount++
			} else {
				deletedCount++
			}
			continue
		}

		// 删除垃圾桶文件
		if err := os.Remove(trashPath); err != nil {
			log.Printf("[Cleaner] 删除文件失败 %s: %v", trashPath, err)
			c.db.MarkCleanupError(task.ID, fmt.Sprintf("删除文件失败: %v", err))
			errorCount++
			continue
		}

		// 更新数据库状态为 hard_deleted
		if err := c.db.MarkHardDeleted(task.ID); err != nil {
			log.Printf("[Cleaner] 更新数据库失败 %s: %v（文件已删除但状态未更新）", trashPath, err)
			c.db.MarkCleanupError(task.ID, fmt.Sprintf("更新数据库失败: %v", err))
			errorCount++
			continue
		}

		deletedCount++
		log.Printf("[Cleaner] 彻底删除过期文件: %s", trashPath)

		// 更新 Prometheus metrics
		metrics.FilesHardDeleted.Inc()
	}

	log.Printf("[Cleaner] 硬删除完成: 成功 %d, 失败 %d", deletedCount, errorCount)

	// 兜底清理：删除没有数据库记录的历史trash文件
	orphanDeleted, orphanErrors := c.cleanOrphanTrashFiles(cutoffTime)
	if orphanDeleted > 0 || orphanErrors > 0 {
		log.Printf("[Cleaner] 孤立垃圾文件清理完成: 成功 %d, 失败 %d", orphanDeleted, orphanErrors)
	}

	return nil
}

// cleanOrphanTrashFiles 清理没有数据库记录的历史垃圾文件（兜底方案）
func (c *Cleaner) cleanOrphanTrashFiles(cutoffTime time.Time) (deletedCount int, errorCount int) {
	// 获取数据库中所有 soft_deleted 任务的 trash_path
	dbPaths := make(map[string]bool)
	tasks, err := c.db.GetAllSoftDeleted()
	if err == nil {
		for _, task := range tasks {
			if path := task.GetTrashPath(); path != "" {
				dbPaths[path] = true
			}
		}
	}

	// 扫描垃圾桶目录
	trashRoots := c.getTrashRoots()
	for _, trashRoot := range trashRoots {
		if _, err := os.Stat(trashRoot); os.IsNotExist(err) {
			continue
		}

		filepath.WalkDir(trashRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}

			// 跳过数据库中已有记录的文件
			if dbPaths[path] {
				return nil
			}

			// 解析删除时间
			filename := d.Name()
			parts := strings.Split(filename, "_del_")
			var deleteTime time.Time

			if len(parts) >= 2 {
				timestamp := parts[len(parts)-1]
				parsedTime, err := time.Parse("20060102_150405", timestamp)
				if err == nil {
					deleteTime = parsedTime
				} else {
					// 解析失败，使用文件修改时间
					if info, err := d.Info(); err == nil {
						deleteTime = info.ModTime()
					}
				}
			} else {
				// 没有时间戳，使用文件修改时间
				if info, err := d.Info(); err == nil {
					deleteTime = info.ModTime()
				}
			}

			// 检查是否超过保留期限
			if deleteTime.Before(cutoffTime) {
				if err := os.Remove(path); err != nil {
					log.Printf("[Cleaner] 删除孤立垃圾文件失败 %s: %v", path, err)
					errorCount++
				} else {
					log.Printf("[Cleaner] 删除孤立垃圾文件: %s (删除时间: %s)", path, deleteTime.Format("2006-01-02 15:04:05"))
					deletedCount++
				}
			}

			return nil
		})
	}

	return deletedCount, errorCount
}

// ListTrashFiles 列出垃圾桶中的文件（优先从数据库，兼容文件系统扫描）
func (c *Cleaner) ListTrashFiles() ([]TrashFile, error) {
	var files []TrashFile
	pathsSeen := make(map[string]bool)

	// 方法1：从数据库查询所有 soft_deleted 状态的任务
	tasks, err := c.db.GetAllSoftDeleted()
	if err == nil {
		for _, task := range tasks {
			trashPath := task.GetTrashPath()
			if trashPath == "" {
				continue
			}

			pathsSeen[trashPath] = true

			// 检查文件是否存在并获取大小
			var fileSize int64
			if info, err := os.Stat(trashPath); err == nil {
				fileSize = info.Size()
			} else {
				fileSize = task.SourceSize // 文件丢失时用源文件大小
			}

			// 计算删除时间和剩余天数
			var deleteTime time.Time
			if task.SourceDeletedAt.Valid {
				deleteTime = task.SourceDeletedAt.Time
			} else {
				deleteTime = time.Now() // 降级处理
			}

			hardDeleteTime := deleteTime.AddDate(0, 0, c.config.Cleaning.HardDeleteDays)
			daysLeft := int(time.Until(hardDeleteTime).Hours() / 24)
			if daysLeft < 0 {
				daysLeft = 0
			}

			files = append(files, TrashFile{
				Name:       filepath.Base(trashPath),
				Path:       trashPath,
				Size:       fileSize,
				DeleteTime: deleteTime,
				DaysLeft:   daysLeft,
			})
		}
	}

	// 方法2：兜底扫描文件系统（检测未记录在数据库的文件）
	trashRoots := c.getTrashRoots()
	for _, trashRoot := range trashRoots {
		if _, err := os.Stat(trashRoot); os.IsNotExist(err) {
			continue
		}

		filepath.WalkDir(trashRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}

			// 跳过已从数据库获取的文件
			if pathsSeen[path] {
				return nil
			}

			info, err := d.Info()
			if err != nil {
				return nil
			}

			// 解析删除时间
			filename := d.Name()
			parts := strings.Split(filename, "_del_")
			var deleteTime time.Time

			if len(parts) >= 2 {
				timestamp := parts[len(parts)-1]
				deleteTime, err = time.Parse("20060102_150405", timestamp)
				if err != nil {
					deleteTime = info.ModTime()
				}
			} else {
				deleteTime = info.ModTime()
			}

			// 计算剩余天数
			hardDeleteTime := deleteTime.AddDate(0, 0, c.config.Cleaning.HardDeleteDays)
			daysLeft := int(time.Until(hardDeleteTime).Hours() / 24)
			if daysLeft < 0 {
				daysLeft = 0
			}

			files = append(files, TrashFile{
				Name:       filename,
				Path:       path,
				Size:       info.Size(),
				DeleteTime: deleteTime,
				DaysLeft:   daysLeft,
			})

			return nil
		})
	}

	return files, nil
}

// TrashFile 垃圾桶文件信息
type TrashFile struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Size       int64     `json:"size"`
	DeleteTime time.Time `json:"delete_time"`
	DaysLeft   int       `json:"days_left"`
}

// DeleteTrashFile 立即删除垃圾桶中的指定文件
func (c *Cleaner) DeleteTrashFile(filename string) error {
	if strings.Contains(filename, "/") || strings.Contains(filename, "\\") {
		return fmt.Errorf("非法路径")
	}

	for _, trashRoot := range c.getTrashRoots() {
		filePath := filepath.Join(trashRoot, filename)
		filePath = filepath.Clean(filePath)

		// 验证路径安全性（防止路径穿越）
		prefix := filepath.Clean(trashRoot) + string(filepath.Separator)
		if !strings.HasPrefix(filePath, prefix) {
			continue
		}

		// 检查文件是否存在
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			continue
		}

		// 删除文件
		if err := os.Remove(filePath); err != nil {
			return fmt.Errorf("删除文件失败: %w", err)
		}

		log.Printf("[Cleaner] 手动删除垃圾桶文件: %s", filePath)

		// 查询数据库中匹配的 soft_deleted 任务并更新状态
		tasks, err := c.db.GetAllSoftDeleted()
		if err == nil {
			for _, task := range tasks {
				if task.GetTrashPath() == filePath {
					if err := c.db.MarkHardDeleted(task.ID); err != nil {
						log.Printf("[Cleaner] 警告: 手动删除后更新数据库失败 (任务 %d): %v", task.ID, err)
					} else {
						log.Printf("[Cleaner] 已更新任务 %d 状态为 hard_deleted", task.ID)
					}
					break
				}
			}
		}

		return nil
	}

	return fmt.Errorf("文件不存在")
}

func (c *Cleaner) getTrashRoots() []string {
	roots := c.config.GetTrashRoots()
	if len(roots) == 0 {
		return []string{c.config.GetTrashPath()}
	}
	return roots
}

func (c *Cleaner) resolveSourcePath(taskPath string) string {
	if taskPath == "" {
		return ""
	}

	// 新版本记录直接存完整路径
	if filepath.IsAbs(taskPath) {
		return taskPath
	}

	// 兼容旧版本记录（相对路径）
	var candidates []string
	if input := c.config.GetPrimaryInputDir(); input != "" {
		candidates = append(candidates, filepath.Join(input, taskPath))
	}
	for _, pair := range c.config.GetPairs() {
		candidates = append(candidates, filepath.Join(pair.Input, taskPath))
	}

	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	if len(candidates) > 0 {
		return candidates[0]
	}
	return taskPath
}

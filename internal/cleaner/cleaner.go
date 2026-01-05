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
	_, err := cronScheduler.AddFunc("0 10 * * *", func() {
		c.runCleaning()
	})

	if err != nil {
		log.Printf("[Cleaner] 添加定时任务失败: %v", err)
		return
	}

	// 启动调度器
	cronScheduler.Start()
	log.Println("[Cleaner] Cron 调度器已启动（每天 10:00 执行清理）")

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

// moveToTrash 将完成N天的源文件移入垃圾桶
func (c *Cleaner) moveToTrash() error {
	// 计算截止时间
	cutoffTime := time.Now().AddDate(0, 0, -c.config.Cleaning.SoftDeleteDays)

	// 查询符合条件的任务
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

	for _, task := range tasks {
		srcPath := filepath.Join(c.config.Path.Input, task.SourcePath)

		// 检查源文件是否存在
		if _, err := os.Stat(srcPath); os.IsNotExist(err) {
			continue
		}

		// 移动到垃圾桶
		if err := c.safeMoveToTrash(srcPath); err != nil {
			log.Printf("[Cleaner] 移动文件失败 %s: %v", task.SourcePath, err)
			continue
		}

		movedCount++
		log.Printf("[Cleaner] 已移入垃圾桶: %s", task.SourcePath)
		
		// 更新 Prometheus metrics
		metrics.FilesSoftDeleted.Inc()
	}

	log.Printf("[Cleaner] 共移动 %d 个文件到垃圾桶", movedCount)
	return nil
}

// safeMoveToTrash 安全地移动文件到垃圾桶
func (c *Cleaner) safeMoveToTrash(srcPath string) error {
	// 构建垃圾桶路径（同级目录，避免跨分区）
	srcDir := filepath.Dir(srcPath)
	trashDir := filepath.Join(srcDir, c.config.Path.Trash)

	// 确保垃圾桶目录存在
	if err := os.MkdirAll(trashDir, 0755); err != nil {
		return fmt.Errorf("创建垃圾桶目录失败: %w", err)
	}

	// 生成带时间戳的目标文件名
	filename := filepath.Base(srcPath)
	timestamp := time.Now().Format("20060102_150405")
	trashPath := filepath.Join(trashDir, filename+"_del_"+timestamp)

	// 尝试直接移动（同分区快速操作）
	err := os.Rename(srcPath, trashPath)
	if err == nil {
		log.Printf("[Cleaner] 文件已移入垃圾桶（os.Rename）: %s", trashPath)
		return nil
	}

	// 检查是否为跨分区错误
	if !isLinkError(err) {
		return err
	}

	// 跨分区：使用复制+删除
	log.Printf("[Cleaner] 检测到跨分区，使用复制+删除模式: %s", srcPath)
	return c.copyAndDelete(srcPath, trashPath)
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
	trashRoot := c.config.GetTrashPath()

	// 检查垃圾桶目录是否存在
	if _, err := os.Stat(trashRoot); os.IsNotExist(err) {
		return nil
	}

	cutoffTime := time.Now().AddDate(0, 0, -c.config.Cleaning.HardDeleteDays)
	deletedCount := 0

	err := filepath.WalkDir(trashRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		// 跳过目录
		if d.IsDir() {
			return nil
		}

		// 解析文件名时间戳 "video.mp4_del_20260105_120000"
		filename := d.Name()
		parts := strings.Split(filename, "_del_")
		var fileTime time.Time

		if len(parts) >= 2 {
			// 尝试解析时间戳
			timestamp := parts[len(parts)-1]
			fileTime, err = time.Parse("20060102_150405", timestamp)
			if err != nil {
				// 降级到使用文件修改时间
				info, _ := d.Info()
				fileTime = info.ModTime()
			}
		} else {
			// 无时间戳，使用文件修改时间
			info, _ := d.Info()
			fileTime = info.ModTime()
		}

		// 检查是否超过清理时间
		if fileTime.Before(cutoffTime) {
			if err := os.Remove(path); err != nil {
				log.Printf("[Cleaner] 删除文件失败 %s: %v", path, err)
				return nil
			}
			deletedCount++
			log.Printf("[Cleaner] 彻底删除过期文件: %s", filename)
			
			// 更新 Prometheus metrics
			metrics.FilesHardDeleted.Inc()
		}

		return nil
	})

	if deletedCount > 0 {
		log.Printf("[Cleaner] 共彻底删除 %d 个过期文件", deletedCount)
	} else {
		log.Println("[Cleaner] 垃圾桶中没有过期文件")
	}

	return err
}

// ListTrashFiles 列出垃圾桶中的文件
func (c *Cleaner) ListTrashFiles() ([]TrashFile, error) {
	trashRoot := c.config.GetTrashPath()

	// 检查垃圾桶目录是否存在
	if _, err := os.Stat(trashRoot); os.IsNotExist(err) {
		return []TrashFile{}, nil
	}

	var files []TrashFile

	err := filepath.WalkDir(trashRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
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

	return files, err
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
	trashRoot := c.config.GetTrashPath()
	filePath := filepath.Join(trashRoot, filename)

	// 验证路径安全性（防止路径穿越）
	if !strings.HasPrefix(filePath, trashRoot) {
		return fmt.Errorf("非法路径")
	}

	// 检查文件是否存在
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return fmt.Errorf("文件不存在")
	}

	// 删除文件
	if err := os.Remove(filePath); err != nil {
		return fmt.Errorf("删除文件失败: %w", err)
	}

	log.Printf("[Cleaner] 手动删除垃圾桶文件: %s", filename)
	return nil
}

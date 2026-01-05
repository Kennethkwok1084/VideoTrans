package worker

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/stm/video-transcoder/internal/config"
	"github.com/stm/video-transcoder/internal/database"
	"github.com/stm/video-transcoder/internal/metrics"
)

// Worker 转码工作器
type Worker struct {
	config         *config.Config
	db             *database.DB
	forceRun       bool // 强制运行标志
	maxWorkers     int  // 动态最大Worker数（可在运行时调整）
	taskQueue      chan *database.Task
	workerCount    int
	wg             sync.WaitGroup
	mu             sync.RWMutex // 保护 forceRun, maxWorkers 和 workerCount
	workerCtx      context.Context
	cancelWorkers  context.CancelFunc
	workersStopped bool
}

// New 创建Worker实例
func New(cfg *config.Config, db *database.DB) *Worker {
	return &Worker{
		config:         cfg,
		db:             db,
		maxWorkers:     cfg.System.MaxWorkers, // 从配置初始化
		taskQueue:      make(chan *database.Task, cfg.System.TaskQueueSize),
		workerCount:    0,
		workersStopped: true,
	}
}

// Run 运行Worker守护进程
func (w *Worker) Run(ctx context.Context) {
	log.Println("[Worker] Worker守护进程启动")

	// 启动任务调度器
	go w.scheduler(ctx)

	// 启动Worker Pool
	go w.manageWorkerPool(ctx)

	<-ctx.Done()
	log.Println("[Worker] 收到停止信号，等待Worker完成...")

	// 关闭任务队列
	close(w.taskQueue)

	// 等待所有Worker完成
	w.wg.Wait()
	log.Println("[Worker] Worker守护进程已退出")
}

// isWorkingHours 检查是否在工作时间窗口内
func (w *Worker) IsWorkingHours() bool {
	now := time.Now()
	hour := now.Hour()

	start := w.config.System.CronStart
	end := w.config.System.CronEnd

	// 处理跨天情况（如 22:00 - 06:00）
	if start < end {
		return hour >= start && hour < end
	} else {
		return hour >= start || hour < end
	}
}

// GetForceRun 获取强制运行状态
func (w *Worker) GetForceRun() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.forceRun
}

// SetForceRun 设置强制运行标志
func (w *Worker) SetForceRun(force bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.forceRun = force
	if force {
		log.Println("[Worker] 强制运行模式已启用")
	} else {
		log.Println("[Worker] 强制运行模式已关闭")
	}
}

// GetWorkerCount 获取当前Worker数量
func (w *Worker) GetWorkerCount() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.workerCount
}

// GetMaxWorkers 获取最大Worker数量
func (w *Worker) GetMaxWorkers() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.maxWorkers
}

// SetMaxWorkers 设置最大Worker数量（运行时动态调整）
func (w *Worker) SetMaxWorkers(count int) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if count < 1 {
		count = 1
	}
	if count > 10 {
		count = 10 // 安全上限
	}

	w.maxWorkers = count
	log.Printf("[Worker] 最大Worker数量已调整为: %d", count)
}

// scheduler 任务调度器，定期从数据库获取任务
func (w *Worker) scheduler(ctx context.Context) {
	interval := time.Duration(w.config.System.SchedulerInterval) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.Printf("[Scheduler] 调度器启动，检查间隔: %v", interval)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// 检查是否在工作时间或强制运行
			if !w.IsWorkingHours() && !w.GetForceRun() {
				continue
			}

			// 检查队列容量
			if len(w.taskQueue) >= cap(w.taskQueue) {
				continue // 队列已满，跳过本次调度
			}

			// 获取待处理任务
			limit := cap(w.taskQueue) - len(w.taskQueue)
			tasks, err := w.db.GetPendingTasks(limit)
			if err != nil {
				log.Printf("[Scheduler] 获取待处理任务失败: %v", err)
				continue
			}

			if len(tasks) == 0 {
				continue
			}

			log.Printf("[Scheduler] 发现 %d 个待处理任务，加入队列", len(tasks))

			// 将任务加入队列
			for _, task := range tasks {
				select {
				case w.taskQueue <- task:
					log.Printf("[Scheduler] 任务 #%d 已加入队列: %s", task.ID, task.SourcePath)
				case <-ctx.Done():
					return
				default:
					log.Printf("[Scheduler] 队列已满，跳过任务 #%d", task.ID)
				}
			}
		}
	}
}

// manageWorkerPool 动态管理Worker Pool大小
func (w *Worker) manageWorkerPool(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			targetWorkers := w.getTargetWorkerCount()
			currentWorkers := w.GetWorkerCount()

			if targetWorkers != currentWorkers {
				log.Printf("[WorkerPool] 调整Worker数量: %d -> %d", currentWorkers, targetWorkers)
				w.adjustWorkerPool(ctx, targetWorkers)
			}
		}
	}
}

// getTargetWorkerCount 根据时间窗口和强制模式确定目标Worker数量
func (w *Worker) getTargetWorkerCount() int {
	maxWorkers := w.GetMaxWorkers() // 使用动态的maxWorkers

	if w.GetForceRun() {
		// 强制运行：使用当前设置的最大并发数
		return maxWorkers
	}

	if w.IsWorkingHours() {
		// 工作时间：使用当前设置的最大并发数
		return maxWorkers
	}

	// 非工作时间：停止所有Worker
	return 0
}

// adjustWorkerPool 调整Worker Pool大小
func (w *Worker) adjustWorkerPool(ctx context.Context, targetCount int) {
	w.mu.Lock()
	defer w.mu.Unlock()

	currentCount := w.workerCount

	if currentCount == 0 && targetCount > 0 {
		// 启动Worker Pool
		w.workerCount = targetCount
		w.workersStopped = false

		// 创建新的Context用于控制Workers
		w.workerCtx, w.cancelWorkers = context.WithCancel(ctx)

		for i := 0; i < targetCount; i++ {
			w.wg.Add(1)
			go w.processWorker(w.workerCtx, i+1)
		}
		log.Printf("[WorkerPool] 已启动 %d 个Worker", targetCount)

		// 更新 Prometheus metrics
		metrics.WorkersActive.Set(float64(targetCount))

	} else if currentCount > 0 && targetCount == 0 {
		// 停止所有Worker
		log.Println("[WorkerPool] 停止所有Worker...")

		if w.cancelWorkers != nil {
			w.cancelWorkers() // 取消Worker Context
		}

		// 等待所有Worker退出（不持有锁）
		w.mu.Unlock()
		w.wg.Wait()
		w.mu.Lock()

		w.workerCount = 0
		w.workersStopped = true
		log.Println("[WorkerPool] 所有Worker已停止")

		// 更新 Prometheus metrics
		metrics.WorkersActive.Set(0)

	} else if currentCount > 0 && targetCount > 0 && currentCount != targetCount {
		// 动态调整Worker数量（暂不支持，需重启）
		log.Printf("[WorkerPool] Worker数量调整 %d->%d 需重启Pool", currentCount, targetCount)
	}
}

// processWorker Worker goroutine，从队列中获取任务并处理
func (w *Worker) processWorker(ctx context.Context, workerID int) {
	defer w.wg.Done()
	log.Printf("[Worker-%d] 启动", workerID)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[Worker-%d] 收到停止信号，退出", workerID)
			return
		case task, ok := <-w.taskQueue:
			if !ok {
				log.Printf("[Worker-%d] 任务队列已关闭，退出", workerID)
				return
			}

			log.Printf("[Worker-%d] 开始处理任务 #%d: %s", workerID, task.ID, task.SourcePath)

			// 记录开始时间
			startTime := time.Now()

			// 更新状态为处理中
			if err := w.db.UpdateTaskStatus(task.ID, database.StatusProcessing, ""); err != nil {
				log.Printf("[Worker-%d] 更新任务状态失败: %v", workerID, err)
				continue
			}

			// 执行转码
			if err := w.transcode(ctx, task, workerID); err != nil {
				log.Printf("[Worker-%d] 转码失败 #%d: %v", workerID, task.ID, err)

				// 增加重试次数
				w.db.IncrementRetryCount(task.ID)

				// 更新状态为失败
				w.db.UpdateTaskStatus(task.ID, database.StatusFailed, err.Error())

				// 更新 Prometheus metrics
				metrics.TranscodeFailed.Inc()
			} else {
				log.Printf("[Worker-%d] 转码成功 #%d: %s", workerID, task.ID, task.SourcePath)

				// 更新输出文件大小
				outputPath := filepath.Join(w.config.Path.Output, task.SourcePath)
				if info, err := os.Stat(outputPath); err == nil {
					w.db.UpdateTaskOutputSize(task.ID, info.Size())

					// 计算节省的空间
					if task.SourceSize > 0 {
						savedBytes := task.SourceSize - info.Size()
						metrics.SpaceSaved.Add(float64(savedBytes))
					}
				}

				// 更新状态为完成
				w.db.UpdateTaskStatus(task.ID, database.StatusCompleted, "转码成功")

				// 更新 Prometheus metrics
				metrics.TranscodeSuccess.Inc()

				// 记录转码耗时
				duration := time.Since(startTime).Seconds()
				metrics.TranscodeDuration.Observe(duration)
			}
		}
	}
}

// transcode 执行FFmpeg转码
func (w *Worker) transcode(ctx context.Context, task *database.Task, workerID int) error {
	// 构建输入和输出路径
	inputPath := filepath.Join(w.config.Path.Input, task.SourcePath)
	outputPath := filepath.Join(w.config.Path.Output, task.SourcePath)

	// 确保输出目录存在
	outputDir := filepath.Dir(outputPath)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("创建输出目录失败: %w", err)
	}

	// 检查磁盘空间
	if err := w.checkDiskSpace(outputDir); err != nil {
		return fmt.Errorf("磁盘空间检查失败: %w", err)
	}

	// 使用ffprobe检查文件完整性
	if err := w.probeFile(inputPath); err != nil {
		return fmt.Errorf("文件检查失败: %w", err)
	}

	// 获取视频总时长
	duration, err := w.getDuration(inputPath)
	if err != nil {
		log.Printf("[Worker-%d] 获取视频时长失败: %v", workerID, err)
		duration = 0
	}

	// 构建FFmpeg命令
	args := []string{
		"-y",                  // 覆盖输出文件
		"-progress", "pipe:1", // 输出进度到stdout
		"-i", inputPath, // 输入文件
		"-c:v", w.config.FFmpeg.Codec, // 视频编码器
		"-preset", w.config.FFmpeg.Preset, // 预设
		"-crf", strconv.Itoa(w.config.FFmpeg.CRF), // CRF质量
		"-c:a", w.config.FFmpeg.Audio, // 音频编码器
		"-b:a", w.config.FFmpeg.AudioBitrate, // 音频比特率
		"-movflags", "+faststart", // 优化流式播放
		outputPath, // 输出文件
	}

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)

	// 获取stdout和stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("创建stdout管道失败: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("创建stderr管道失败: %w", err)
	}

	// 启动命令
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动FFmpeg失败: %w", err)
	}

	// 收集stderr日志
	var stderrBuf strings.Builder
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			stderrBuf.WriteString(scanner.Text() + "\n")
		}
	}()

	// 解析进度
	go w.parseProgress(bufio.NewReader(stdout), task.ID, duration, workerID)

	// 等待命令完成
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("FFmpeg执行失败: %w\n日志:\n%s", err, stderrBuf.String())
	}

	return nil
}

// probeFile 使用ffprobe检查文件
func (w *Worker) probeFile(path string) error {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffprobe失败: %w, output: %s", err, string(output))
	}

	return nil
}

// getDuration 获取视频时长（秒）
func (w *Worker) getDuration(path string) (float64, error) {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, err
	}

	durationStr := strings.TrimSpace(string(output))
	duration, err := strconv.ParseFloat(durationStr, 64)
	if err != nil {
		return 0, err
	}

	return duration, nil
}

// parseProgress 解析FFmpeg进度输出 (优化：每5%或5秒更新一次)
func (w *Worker) parseProgress(reader *bufio.Reader, taskID int64, totalDuration float64, workerID int) {
	scanner := bufio.NewScanner(reader)
	lastUpdate := time.Now()
	lastProgress := 0.0

	for scanner.Scan() {
		line := scanner.Text()

		// 解析 out_time_ms
		if strings.HasPrefix(line, "out_time_ms=") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) != 2 {
				continue
			}

			outTimeMs, err := strconv.ParseInt(parts[1], 10, 64)
			if err != nil {
				continue
			}

			if totalDuration > 0 {
				// 计算百分比
				outTimeSeconds := float64(outTimeMs) / 1000000.0
				progress := (outTimeSeconds / totalDuration) * 100.0

				// 限制在0-100之间
				if progress < 0 {
					progress = 0
				} else if progress > 100 {
					progress = 100
				}

				// 优化：每5%或每5秒更新一次数据库
				progressDelta := progress - lastProgress
				timeSinceLastUpdate := time.Since(lastUpdate)

				if progressDelta >= 5.0 || timeSinceLastUpdate >= 5*time.Second {
					w.db.UpdateTaskProgress(taskID, progress)
					lastUpdate = time.Now()
					lastProgress = progress
					log.Printf("[Worker-%d] 任务 #%d 进度: %.1f%%", workerID, taskID, progress)
				}
			}
		}
	}

	// 最后确保进度设为100%
	if totalDuration > 0 {
		w.db.UpdateTaskProgress(taskID, 100.0)
		log.Printf("[Worker-%d] 任务 #%d 已完成", workerID, taskID)
	}
}

// checkDiskSpace 检查磁盘空间
func (w *Worker) checkDiskSpace(path string) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return fmt.Errorf("获取磁盘信息失败: %w", err)
	}

	// 计算可用空间（GB）
	availableGB := float64(stat.Bavail*uint64(stat.Bsize)) / 1024 / 1024 / 1024
	minRequiredGB := float64(w.config.System.MinDiskSpaceGB)

	if availableGB < minRequiredGB {
		return fmt.Errorf("磁盘空间不足: 可用 %.2fGB, 需要至少 %.0fGB", availableGB, minRequiredGB)
	}

	log.Printf("[Worker] 磁盘可用空间: %.2fGB", availableGB)
	return nil
}

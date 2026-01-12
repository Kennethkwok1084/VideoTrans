package worker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/stm/video-transcoder/internal/config"
	"github.com/stm/video-transcoder/internal/database"
	"github.com/stm/video-transcoder/internal/media"
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
	mainCtx        context.Context // 主 context，用于启动 Worker
	activeTasks    int64
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

	// 保存主 context
	w.mainCtx = ctx

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
	w.forceRun = force
	w.mu.Unlock()

	if force {
		log.Println("[Worker] 强制运行模式已启用")
		// 立即触发 Worker Pool 调整
		go func() {
			targetWorkers := w.getTargetWorkerCount()
			currentWorkers := w.GetWorkerCount()

			if targetWorkers != currentWorkers {
				log.Printf("[WorkerPool] 强制模式触发：调整Worker数量 %d -> %d", currentWorkers, targetWorkers)
				// 使用主 context
				if w.mainCtx != nil {
					w.adjustWorkerPool(w.mainCtx, targetWorkers)
				}
			}
		}()
	} else {
		log.Println("[Worker] 强制运行模式已关闭")
		// 立即检查是否需要停止 Worker
		go func() {
			targetWorkers := w.getTargetWorkerCount()
			currentWorkers := w.GetWorkerCount()

			if targetWorkers != currentWorkers {
				log.Printf("[WorkerPool] 取消强制模式：调整Worker数量 %d -> %d", currentWorkers, targetWorkers)
				if w.mainCtx != nil {
					w.adjustWorkerPool(w.mainCtx, targetWorkers)
				}
			}
		}()
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

func (w *Worker) getActiveTasks() int64 {
	return atomic.LoadInt64(&w.activeTasks)
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

			// 检查是否正在优雅关闭
			w.mu.RLock()
			stopped := w.workersStopped
			w.mu.RUnlock()
			if stopped {
				continue // 优雅关闭中，不再添加新任务
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
		// 优雅停止所有Worker：不再接受新任务，等待当前任务完成
		log.Println("[WorkerPool] 进入优雅关闭模式，等待当前任务完成...")

		// 设置标志：不再接受新任务（调度器会检查这个）
		w.workersStopped = true

		activeTasks := w.getActiveTasks()
		queuedTasks := len(w.taskQueue)
		if activeTasks > 0 || queuedTasks > 0 {
			log.Printf("[WorkerPool] 非工作时间，等待任务完成后停止 (active=%d, queued=%d)", activeTasks, queuedTasks)
			return
		}

		// 关闭任务队列，通知workers不再有新任务
		// 但不取消context，让正在执行的任务继续完成
		close(w.taskQueue)

		// 释放锁，等待所有Worker完成当前任务
		w.mu.Unlock()
		log.Println("[WorkerPool] 等待所有正在处理的任务完成...")
		w.wg.Wait()
		log.Println("[WorkerPool] 所有任务已完成")
		w.mu.Lock()

		// 现在可以安全地清理资源
		if w.cancelWorkers != nil {
			w.cancelWorkers()
		}

		// 重新创建任务队列供下次启动使用
		w.taskQueue = make(chan *database.Task, w.config.System.TaskQueueSize)

		w.workerCount = 0
		log.Println("[WorkerPool] 所有Worker已优雅停止")

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
				// 队列已关闭，说明进入优雅关闭模式，完成当前任务后退出
				log.Printf("[Worker-%d] 任务队列已关闭，退出", workerID)
				return
			}

			log.Printf("[Worker-%d] 开始处理任务 #%d: %s", workerID, task.ID, task.SourcePath)

			atomic.AddInt64(&w.activeTasks, 1)
			func() {
				defer atomic.AddInt64(&w.activeTasks, -1)

				// 记录开始时间
				startTime := time.Now()

				// 更新状态为处理中
				if err := w.db.UpdateTaskStatus(task.ID, database.StatusProcessing, ""); err != nil {
					log.Printf("[Worker-%d] 更新任务状态失败: %v", workerID, err)
					return
				}

				// 执行转码（使用独立的 context，不受 ctx.Done() 影响）
				taskCtx := context.Background()
				if err := w.transcode(taskCtx, task, workerID); err != nil {
				// 详细的错误日志
				errMsg := err.Error()
				log.Printf("[Worker-%d] ❌ 转码失败 #%d: %s", workerID, task.ID, task.SourcePath)

				category, transient := classifyError(errMsg)
				if category != "" {
					log.Printf("[Worker-%d] 🧭 失败原因: %s", workerID, category)
				}

				// 截取关键错误信息（避免日志过长）
				if len(errMsg) > 1000 {
					log.Printf("[Worker-%d] 📋 错误详情 (前500字符): %s", workerID, errMsg[:500])
				} else {
					log.Printf("[Worker-%d] 📋 错误详情: %s", workerID, errMsg)
				}

				nextRetry := task.RetryCount + 1
				w.db.IncrementRetryCount(task.ID)

				if transient && nextRetry < 3 {
					logMsg := errMsg
					if category != "" {
						logMsg = fmt.Sprintf("自动重试: %s\n%s", category, errMsg)
					}
					w.db.UpdateTaskProgress(task.ID, 0)
					w.db.UpdateTaskStatus(task.ID, database.StatusPending, logMsg)
				} else {
					// 更新状态为失败（存储完整错误信息到数据库）
					w.db.UpdateTaskStatus(task.ID, database.StatusFailed, errMsg)
				}

				// 更新 Prometheus metrics
				metrics.TranscodeFailed.Inc()
			} else {
					log.Printf("[Worker-%d] ✅ 转码成功 #%d: %s", workerID, task.ID, task.SourcePath)

					// 更新输出文件大小 - 单次遍历获取输出路径
					var outputDir, relPath string
					pairs := w.config.GetPairs()
					for _, pair := range pairs {
						if rel, err := filepath.Rel(pair.Input, task.SourcePath); err == nil && !strings.HasPrefix(rel, "..") {
							outputDir = pair.Output
							relPath = rel
							break
						}
					}

					if outputDir != "" && relPath != "" {
						outputPath := w.config.ApplyOutputExtension(filepath.Join(outputDir, relPath))
						if info, err := os.Stat(outputPath); err == nil {
							w.db.UpdateTaskOutputSize(task.ID, info.Size())

							// 计算节省的空间
							if task.SourceSize > 0 {
								savedBytes := task.SourceSize - info.Size()
								metrics.SpaceSaved.Add(float64(savedBytes))
							}
						}
					}

					w.db.UpdateTaskProgress(task.ID, 100.0)

					// 更新状态为完成
					w.db.UpdateTaskStatus(task.ID, database.StatusCompleted, "转码成功")

					// 更新 Prometheus metrics
					metrics.TranscodeSuccess.Inc()

					// 记录转码耗时
					duration := time.Since(startTime).Seconds()
					metrics.TranscodeDuration.Observe(duration)
				}
			}()
		}
	}
}

// transcode 执行FFmpeg转码
func (w *Worker) transcode(ctx context.Context, task *database.Task, workerID int) error {
	// 源文件的完整路径就是task.SourcePath
	inputPath := task.SourcePath

	// 单次遍历找到匹配的输入目录，同时获取输出目录和相对路径
	var (
		outputDir string
		relPath   string
	)
	pairs := w.config.GetPairs()
	for _, pair := range pairs {
		if rel, err := filepath.Rel(pair.Input, inputPath); err == nil && !strings.HasPrefix(rel, "..") {
			outputDir = pair.Output
			relPath = rel
			break
		}
	}

	if outputDir == "" || relPath == "" {
		return fmt.Errorf("无法找到源文件对应的输入输出配对: %s", inputPath)
	}

	// 构建输出路径（保持目录结构，必要时统一扩展名）
	outputPath := w.config.ApplyOutputExtension(filepath.Join(outputDir, relPath))

	// 确保输出目录存在
	outputPathDir := filepath.Dir(outputPath)
	if err := os.MkdirAll(outputPathDir, 0755); err != nil {
		return fmt.Errorf("创建输出目录失败: %w", err)
	}

	// 检查磁盘空间
	if err := w.checkDiskSpace(outputPathDir); err != nil {
		return fmt.Errorf("磁盘空间检查失败: %w", err)
	}

	// 使用ffprobe检查文件完整性
	probeTimeout := time.Duration(w.config.FFmpeg.ProbeTimeoutSeconds) * time.Second
	if err := media.ProbeFile(inputPath, probeTimeout, 2); err != nil {
		return fmt.Errorf("文件检查失败: %w", err)
	}

	// 获取视频总时长
	duration, err := w.getDuration(inputPath)
	if err != nil {
		log.Printf("[Worker-%d] 获取视频时长失败: %v", workerID, err)
		duration = 0
	}

	repairMode := w.selectCorruptStrategy(inputPath, workerID)
	discardCorrupt := w.config.FFmpeg.DiscardCorrupt
	if repairMode == "discard" || repairMode == "cfr" {
		discardCorrupt = true
	}

	outputTempPath := outputPath + ".stm_tmp"
	success := false
	defer func() {
		if !success {
			_ = os.Remove(outputTempPath)
		}
	}()

	// 构建FFmpeg命令
	args := []string{
		"-y",                  // 覆盖输出文件
		"-progress", "pipe:1", // 输出进度到stdout
	}
	if discardCorrupt {
		args = append(args, "-fflags", "+discardcorrupt")
		args = append(args, "-err_detect", "ignore_err")
	}
	args = append(args,
		"-i", inputPath, // 输入文件
		"-c:v", w.config.FFmpeg.Codec, // 视频编码器
		"-preset", w.config.FFmpeg.Preset, // 预设
		"-crf", strconv.Itoa(w.config.FFmpeg.CRF), // CRF质量
		"-pix_fmt", "yuv420p", // 提高兼容性
		"-c:a", w.config.FFmpeg.Audio, // 音频编码器
		"-b:a", w.config.FFmpeg.AudioBitrate, // 音频比特率
	)
	if repairMode == "cfr" {
		fps := w.config.FFmpeg.OutputFPS
		if fps <= 0 {
			fps = 30
		}
		args = append(args, "-fps_mode", "cfr", "-r", strconv.Itoa(fps))
	}
	args = append(args,
		"-movflags", "+faststart", // 优化流式播放
		outputTempPath, // 输出文件（临时）
	)

	maxDuration := computeFfmpegTimeout(duration, w.config)
	ffCtx, cancel := context.WithTimeout(ctx, maxDuration)
	defer cancel()

	cmd := exec.CommandContext(ffCtx, "ffmpeg", args...)

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

	progressStall := time.Duration(w.config.FFmpeg.ProgressStallMinutes) * time.Minute
	lastProgressUnix := time.Now().UnixNano()
	progressDone := make(chan struct{})
	go func() {
		w.parseProgress(bufio.NewReader(stdout), task.ID, duration, workerID, &lastProgressUnix)
		close(progressDone)
	}()

	stallReasonCh := make(chan string, 1)
	stallTicker := time.NewTicker(30 * time.Second)
	defer stallTicker.Stop()
	go func() {
		for {
			select {
			case <-progressDone:
				return
			case <-ffCtx.Done():
				return
			case <-stallTicker.C:
				last := time.Unix(0, atomic.LoadInt64(&lastProgressUnix))
				if time.Since(last) > progressStall {
					if cmd.Process != nil {
						w.logStallDiagnostics(workerID, task, inputPath, outputTempPath, cmd.Process.Pid, last)
					}
					stallReasonCh <- fmt.Sprintf("FFmpeg进度超过%v未更新，疑似IO卡住", progressStall)
					cancel()
					return
				}
			}
		}
	}()

	// 等待命令完成
	if err := cmd.Wait(); err != nil {
		stallReason := ""
		select {
		case stallReason = <-stallReasonCh:
		default:
		}

		if stallReason != "" {
			return fmt.Errorf("%s: %w\n日志:\n%s", stallReason, err, stderrBuf.String())
		}
		if errors.Is(ffCtx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("FFmpeg超时(%s): %w\n日志:\n%s", maxDuration, err, stderrBuf.String())
		}
		return fmt.Errorf("FFmpeg执行失败: %w\n日志:\n%s", err, stderrBuf.String())
	}

	if w.config.FFmpeg.StrictCheck {
		if err := media.ProbeFile(outputTempPath, probeTimeout, 0); err != nil {
			return fmt.Errorf("输出文件验证失败: %w", err)
		}

		decodeSeconds := w.config.FFmpeg.VerifyDecodeSeconds
		if decodeSeconds > 0 {
			if err := media.DecodeSegmentStrict(outputTempPath, probeTimeout, 0, decodeSeconds); err != nil {
				return fmt.Errorf("输出文件验证失败: %w", err)
			}
			if w.config.FFmpeg.VerifyTailSeekSeconds > 0 {
				if err := media.DecodeSegmentStrict(outputTempPath, probeTimeout, w.config.FFmpeg.VerifyTailSeekSeconds, decodeSeconds); err != nil {
					return fmt.Errorf("输出文件验证失败: %w", err)
				}
			}
		}
	}

	if err := os.Rename(outputTempPath, outputPath); err != nil {
		_ = os.Remove(outputPath)
		if renameErr := os.Rename(outputTempPath, outputPath); renameErr != nil {
			return fmt.Errorf("移动输出文件失败: %w", renameErr)
		}
	}

	success = true
	return nil
}

// getDuration 获取视频时长（秒）
func (w *Worker) getDuration(path string) (float64, error) {
	probeTimeout := time.Duration(w.config.FFmpeg.ProbeTimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	)

	output, err := cmd.CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return 0, fmt.Errorf("ffprobe超时(%s): %w", probeTimeout, ctx.Err())
	}
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

func computeFfmpegTimeout(duration float64, cfg *config.Config) time.Duration {
	timeout := time.Duration(cfg.FFmpeg.MaxDurationHours) * time.Hour
	if duration > 0 && cfg.FFmpeg.DurationFactor > 0 {
		candidate := time.Duration(duration*cfg.FFmpeg.DurationFactor*float64(time.Second)) +
			time.Duration(cfg.FFmpeg.DurationExtraMinutes)*time.Minute
		if candidate > timeout {
			timeout = candidate
		}
	}
	return timeout
}

func (w *Worker) selectCorruptStrategy(path string, workerID int) string {
	strategy := strings.ToLower(strings.TrimSpace(w.config.FFmpeg.CorruptStrategy))
	if strategy == "" {
		strategy = "auto"
	}

	switch strategy {
	case "discard", "cfr":
		return strategy
	case "auto":
	default:
		return "cfr"
	}

	probeSeconds := w.config.FFmpeg.CorruptProbeSeconds
	if probeSeconds <= 0 {
		log.Printf("[Worker-%d] 抽样检测关闭，使用补帧策略", workerID)
		return "cfr"
	}

	probeTimeout := time.Duration(w.config.FFmpeg.ProbeTimeoutSeconds) * time.Second
	if need := time.Duration(probeSeconds+5) * time.Second; need > probeTimeout {
		probeTimeout = need
	}

	errCount, err := media.CountDecodeErrors(path, probeTimeout, probeSeconds)
	if err != nil {
		log.Printf("[Worker-%d] 抽样检测失败，降级为补帧: %v", workerID, err)
		return "cfr"
	}

	threshold := w.config.FFmpeg.CorruptErrorThreshold
	if threshold <= 0 {
		threshold = 1
	}

	if errCount >= threshold {
		log.Printf("[Worker-%d] 抽样错误=%d >= %d，使用补帧策略", workerID, errCount, threshold)
		return "cfr"
	}

	log.Printf("[Worker-%d] 抽样错误=%d < %d，使用丢坏帧策略", workerID, errCount, threshold)
	return "discard"
}

// parseProgress 解析FFmpeg进度输出 (优化：每5%或5秒更新一次)
func (w *Worker) parseProgress(reader *bufio.Reader, taskID int64, totalDuration float64, workerID int, lastProgressUnix *int64) {
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

			atomic.StoreInt64(lastProgressUnix, time.Now().UnixNano())

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
}

func classifyError(errMsg string) (string, bool) {
	lower := strings.ToLower(errMsg)

	if strings.Contains(errMsg, "进度超过") || strings.Contains(errMsg, "FFmpeg超时") || strings.Contains(errMsg, "ffprobe超时") {
		return "疑似IO卡住或进程超时", true
	}
	if strings.Contains(errMsg, "输出文件验证失败") {
		return "输出文件损坏，自动重试", true
	}
	if strings.Contains(lower, "input/output error") ||
		strings.Contains(lower, "i/o error") ||
		strings.Contains(lower, "stale file handle") ||
		strings.Contains(lower, "operation timed out") ||
		strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "connection timed out") ||
		strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "no such file") ||
		strings.Contains(lower, "broken pipe") {
		return "疑似IO/挂载盘问题", true
	}
	if strings.Contains(errMsg, "磁盘空间") {
		return "磁盘空间不足", false
	}
	if strings.Contains(errMsg, "文件检查失败") ||
		strings.Contains(errMsg, "文件损坏") ||
		strings.Contains(errMsg, "解码测试失败") ||
		strings.Contains(errMsg, "Invalid NAL") ||
		strings.Contains(errMsg, "Error splitting") ||
		strings.Contains(errMsg, "Invalid data found") ||
		strings.Contains(errMsg, "moov atom not found") {
		return "文件损坏或格式不支持", false
	}

	return "未知原因", false
}

type mountInfo struct {
	Source  string
	Target  string
	FSType  string
	Options string
}

func (w *Worker) logStallDiagnostics(workerID int, task *database.Task, inputPath, outputPath string, pid int, lastProgress time.Time) {
	log.Printf("[Worker-%d] 🧾 卡住诊断: task=%d pid=%d last_progress=%s input=%s output=%s",
		workerID, task.ID, pid, lastProgress.Format(time.RFC3339), inputPath, outputPath)

	if info, err := findMountInfo(inputPath); err == nil && info != nil {
		log.Printf("[Worker-%d] 📁 输入挂载: source=%s target=%s type=%s options=%s",
			workerID, info.Source, info.Target, info.FSType, info.Options)
	}
	if info, err := findMountInfo(outputPath); err == nil && info != nil {
		log.Printf("[Worker-%d] 📁 输出挂载: source=%s target=%s type=%s options=%s",
			workerID, info.Source, info.Target, info.FSType, info.Options)
	}

	if stat, err := os.Stat(inputPath); err == nil {
		log.Printf("[Worker-%d] 📄 输入文件: size=%d mtime=%s mode=%s",
			workerID, stat.Size(), stat.ModTime().Format(time.RFC3339), stat.Mode().String())
	} else {
		log.Printf("[Worker-%d] 📄 输入文件: stat失败: %v", workerID, err)
	}

	procRoot := fmt.Sprintf("/proc/%d", pid)
	if snippet := readProcSnippet(filepath.Join(procRoot, "wchan"), 200); snippet != "" {
		log.Printf("[Worker-%d] 🔎 ffmpeg wchan: %s", workerID, snippet)
	}
	if snippet := readProcSnippet(filepath.Join(procRoot, "status"), 600); snippet != "" {
		log.Printf("[Worker-%d] 🔎 ffmpeg status: %s", workerID, snippet)
	}
	if snippet := readProcSnippet(filepath.Join(procRoot, "io"), 400); snippet != "" {
		log.Printf("[Worker-%d] 🔎 ffmpeg io: %s", workerID, snippet)
	}
}

func readProcSnippet(path string, maxLen int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(data))
	if maxLen > 0 && len(text) > maxLen {
		return text[:maxLen] + "..."
	}
	return text
}

func findMountInfo(path string) (*mountInfo, error) {
	data, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return nil, err
	}

	bestLen := -1
	var best *mountInfo
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		source := unescapeMountField(fields[0])
		target := unescapeMountField(fields[1])
		fstype := fields[2]
		options := fields[3]

		if target == "/" || target == "" {
			if bestLen < 1 && strings.HasPrefix(path, "/") {
				bestLen = 1
				best = &mountInfo{Source: source, Target: target, FSType: fstype, Options: options}
			}
			continue
		}

		if path == target || strings.HasPrefix(path, target+"/") {
			if len(target) > bestLen {
				bestLen = len(target)
				best = &mountInfo{Source: source, Target: target, FSType: fstype, Options: options}
			}
		}
	}

	return best, nil
}

func unescapeMountField(value string) string {
	replacer := strings.NewReplacer(
		`\\040`, " ",
		`\\011`, "\t",
		`\\012`, "\n",
		`\\134`, "\\",
	)
	return replacer.Replace(value)
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

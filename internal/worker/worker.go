package worker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
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
	queuedTaskIDs  map[int64]struct{}
	workerCount    int
	wg             sync.WaitGroup
	poolMu         sync.Mutex
	mu             sync.RWMutex // 保护 forceRun, maxWorkers 和 workerCount
	workerCtx      context.Context
	cancelWorkers  context.CancelFunc
	workersStopped bool
	mainCtx        context.Context // 主 context，用于启动 Worker
	activeTasks    int64
}

// New 创建Worker实例
func New(cfg *config.Config, db *database.DB) *Worker {
	maxWorkers := 1
	queueSize := 10
	if cfg != nil {
		if cfg.System.MaxWorkers > 0 {
			maxWorkers = cfg.System.MaxWorkers
		}
		if cfg.System.TaskQueueSize > 0 {
			queueSize = cfg.System.TaskQueueSize
		}
	}

	return &Worker{
		config:         cfg,
		db:             db,
		maxWorkers:     maxWorkers, // 从配置初始化
		taskQueue:      make(chan *database.Task, queueSize),
		queuedTaskIDs:  make(map[int64]struct{}),
		workerCount:    0,
		workersStopped: true,
	}
}

// Run 运行Worker守护进程
func (w *Worker) Run(ctx context.Context) {
	log.Println("[Worker] Worker守护进程启动")

	// 保存主 context
	w.mainCtx = ctx

	// 启动时先按当前策略立即调整一次，避免首次需要等待ticker
	w.adjustWorkerPool(ctx, w.getTargetWorkerCount())

	// 启动任务调度器
	go w.scheduler(ctx)

	// 启动Worker Pool
	go w.manageWorkerPool(ctx)

	<-ctx.Done()
	log.Println("[Worker] 收到停止信号，等待Worker完成...")

	// 停止接收和执行任务
	w.mu.Lock()
	w.workersStopped = true
	w.queuedTaskIDs = make(map[int64]struct{})
	w.workerCount = 0
	cancelWorkers := w.cancelWorkers
	w.cancelWorkers = nil
	w.workerCtx = nil
	w.mu.Unlock()

	if cancelWorkers != nil {
		cancelWorkers()
	}

	// 等待所有Worker完成
	w.wg.Wait()
	metrics.WorkersActive.Set(0)
	log.Println("[Worker] Worker守护进程已退出")
}

// IsWorkingHours checks if the current time is within the configured working hours.
func (w *Worker) IsWorkingHours() bool {
	if w.config == nil {
		return false
	}

	now := time.Now()
	hour := now.Hour()

	start := w.config.System.CronStart
	end := w.config.System.CronEnd

	if start == end {
		return true
	}

	// 处理跨天情况（如 22:00 - 06:00）
	if start < end {
		return hour >= start && hour < end
	}
	return hour >= start || hour < end
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
	mainCtx := w.mainCtx
	w.mu.Unlock()

	if force {
		log.Println("[Worker] 强制运行模式已启用")
	} else {
		log.Println("[Worker] 强制运行模式已关闭")
	}

	// 立即触发 Worker Pool 调整
	if mainCtx != nil {
		go func() {
			targetWorkers := w.getTargetWorkerCount()
			currentWorkers := w.GetWorkerCount()

			if targetWorkers != currentWorkers {
				log.Printf("[WorkerPool] 强制模式变更：调整Worker数量 %d -> %d", currentWorkers, targetWorkers)
				w.adjustWorkerPool(mainCtx, targetWorkers)
			}
		}()
	} else {
		log.Println("[Worker] 警告：mainCtx 未设置，无法立即调整 Worker Pool")
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

// GetActiveTaskCount 获取当前正在处理的任务数
func (w *Worker) GetActiveTaskCount() int64 {
	return w.getActiveTasks()
}

// IsDraining 返回是否处于非工作时间排空阶段
func (w *Worker) IsDraining() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.workersStopped && w.workerCount > 0
}

func (w *Worker) getActiveTasks() int64 {
	return atomic.LoadInt64(&w.activeTasks)
}

// SetMaxWorkers 设置最大Worker数量（运行时动态调整）
func (w *Worker) SetMaxWorkers(count int) {
	w.mu.Lock()

	if count < 1 {
		count = 1
	}
	if count > 10 {
		count = 10 // 安全上限
	}

	w.maxWorkers = count
	mainCtx := w.mainCtx
	w.mu.Unlock()

	log.Printf("[Worker] 最大Worker数量已调整为: %d", count)

	if mainCtx != nil {
		go w.adjustWorkerPool(mainCtx, count)
	} else {
		log.Println("[Worker] 警告：mainCtx 未设置，无法立即调整 Worker Pool")
	}
}

func (w *Worker) markTaskEnqueued(taskID int64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.workersStopped {
		return false
	}
	if _, exists := w.queuedTaskIDs[taskID]; exists {
		return false
	}
	w.queuedTaskIDs[taskID] = struct{}{}
	return true
}

func (w *Worker) unmarkTaskEnqueued(taskID int64) {
	w.mu.Lock()
	delete(w.queuedTaskIDs, taskID)
	w.mu.Unlock()
}

// scheduler 任务调度器，定期从数据库获取任务
func (w *Worker) scheduler(ctx context.Context) {
	interval := 10 * time.Second
	if w.config != nil && w.config.System.SchedulerInterval > 0 {
		interval = time.Duration(w.config.System.SchedulerInterval) * time.Second
	}
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

			if w.db == nil {
				continue
			}

			// 按可用并发槽位调度，避免一次性 claim 过多任务（例如 3 worker + 10 queue = 13 processing）
			activeTasks := int(w.getActiveTasks())
			queuedTasks := len(w.taskQueue)
			targetWorkers := w.getTargetWorkerCount()
			availableSlots := targetWorkers - activeTasks - queuedTasks
			if availableSlots <= 0 {
				continue
			}

			queueCapacityLeft := cap(w.taskQueue) - queuedTasks
			limit := availableSlots
			if limit > queueCapacityLeft {
				limit = queueCapacityLeft
			}
			if limit <= 0 {
				continue
			}

			var tasks []*database.Task
			var err error

			// Phase 4: 支持原子 Claim 调度
			if w.config != nil && w.config.Scheduler.AtomicClaim {
				// 使用原子 Claim 方式（pending -> processing 在 DB 层完成）
				tasks, err = w.db.ClaimPendingTasks(limit, w.config.System.MaxRetry)
			} else {
				// 传统方式（pending -> processing 在 Worker 处理时完成）
				tasks, err = w.db.GetPendingTasks(limit, w.config.System.MaxRetry)
			}

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
				if !w.markTaskEnqueued(task.ID) {
					continue
				}

				select {
				case w.taskQueue <- task:
					log.Printf("[Scheduler] 任务 #%d 已加入队列: %s", task.ID, task.SourcePath)
				case <-ctx.Done():
					w.unmarkTaskEnqueued(task.ID)
					return
				default:
					w.unmarkTaskEnqueued(task.ID)
					log.Printf("[Scheduler] 队列已满，跳过任务 #%d", task.ID)
				}
			}
		}
	}
}

// manageWorkerPool 动态管理Worker Pool大小
func (w *Worker) manageWorkerPool(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
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
	w.poolMu.Lock()
	defer w.poolMu.Unlock()

	if targetCount < 0 {
		targetCount = 0
	}

	w.mu.Lock()

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
		w.mu.Unlock()
		log.Printf("[WorkerPool] 已启动 %d 个Worker", targetCount)

		// 更新 Prometheus metrics
		metrics.WorkersActive.Set(float64(targetCount))
		return

	} else if currentCount > 0 && targetCount == 0 {
		// 优雅停止所有Worker：不再接受新任务，等待当前任务完成
		log.Println("[WorkerPool] 进入优雅关闭模式，等待当前任务完成...")

		// 设置标志：不再接受新任务（调度器会检查这个）
		w.workersStopped = true

		activeTasks := w.getActiveTasks()
		queuedTasks := len(w.taskQueue)
		if activeTasks > 0 || queuedTasks > 0 {
			w.mu.Unlock()
			log.Printf("[WorkerPool] 非工作时间，等待任务完成后停止 (active=%d, queued=%d)", activeTasks, queuedTasks)
			return
		}

		cancelWorkers := w.cancelWorkers
		w.cancelWorkers = nil
		w.workerCtx = nil
		w.workerCount = 0
		w.queuedTaskIDs = make(map[int64]struct{})
		w.mu.Unlock()

		if cancelWorkers != nil {
			cancelWorkers()
		}

		log.Println("[WorkerPool] 等待所有正在处理的任务完成...")
		w.wg.Wait()
		log.Println("[WorkerPool] 所有Worker已优雅停止")

		// 更新 Prometheus metrics
		metrics.WorkersActive.Set(0)
		return

	} else if currentCount > 0 && targetCount > 0 {
		// 修复：即使 currentCount == targetCount，如果处于排空状态也需要恢复调度
		if w.workersStopped && currentCount == targetCount {
			w.workersStopped = false
			w.mu.Unlock()
			log.Printf("[WorkerPool] 从排空状态恢复调度（Worker数量保持=%d）", targetCount)
			return
		}

		if currentCount == targetCount {
			// 数量相同且未排空，无需调整
			w.mu.Unlock()
			return
		}

		if targetCount > currentCount {
			// 扩容：直接新增 Worker
			startIndex := currentCount + 1
			for i := currentCount; i < targetCount; i++ {
				w.wg.Add(1)
				go w.processWorker(w.workerCtx, i+1)
			}
			w.workerCount = targetCount
			w.workersStopped = false
			w.mu.Unlock()
			log.Printf("[WorkerPool] Worker扩容: %d -> %d", startIndex-1, targetCount)
			metrics.WorkersActive.Set(float64(targetCount))
			return
		}

		// 缩容：等待任务清空后重建Pool
		activeTasks := w.getActiveTasks()
		queuedTasks := len(w.taskQueue)
		if activeTasks > 0 || queuedTasks > 0 {
			w.mu.Unlock()
			log.Printf("[WorkerPool] 缩容等待任务完成 (active=%d, queued=%d)", activeTasks, queuedTasks)
			return
		}

		cancelWorkers := w.cancelWorkers
		w.cancelWorkers = nil
		w.workerCtx = nil
		w.workerCount = 0
		w.workersStopped = true
		w.queuedTaskIDs = make(map[int64]struct{})
		w.mu.Unlock()

		if cancelWorkers != nil {
			cancelWorkers()
		}
		w.wg.Wait()

		// 重新按目标数量启动
		w.mu.Lock()
		w.workerCtx, w.cancelWorkers = context.WithCancel(ctx)
		for i := 0; i < targetCount; i++ {
			w.wg.Add(1)
			go w.processWorker(w.workerCtx, i+1)
		}
		w.workerCount = targetCount
		w.workersStopped = false
		w.mu.Unlock()

		log.Printf("[WorkerPool] Worker缩容重建完成: %d -> %d", currentCount, targetCount)
		metrics.WorkersActive.Set(float64(targetCount))
		return
	}

	w.mu.Unlock()
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
			w.unmarkTaskEnqueued(task.ID)

			log.Printf("[Worker-%d] 开始处理任务 #%d: %s", workerID, task.ID, task.SourcePath)

			atomic.AddInt64(&w.activeTasks, 1)
			func() {
				defer atomic.AddInt64(&w.activeTasks, -1)

				// 记录开始时间
				startTime := time.Now()

				// Phase 4: 原子 Claim 模式下任务已经是 processing 状态
				// 传统模式下需要在这里更新状态
				if w.config == nil || !w.config.Scheduler.AtomicClaim {
					// 更新状态为处理中
					if err := w.db.UpdateTaskStatus(task.ID, database.StatusProcessing, ""); err != nil {
						log.Printf("[Worker-%d] 更新任务状态失败: %v", workerID, err)
						return
					}
				}

				// 执行转码（使用独立的 context，不受 ctx.Done() 影响）
				taskCtx := context.Background()
				if err := w.transcode(taskCtx, task, workerID); err != nil {
					// 详细的错误日志
					errMsg := err.Error()
					log.Printf("[Worker-%d] ❌ 转码失败 #%d: %s", workerID, task.ID, task.SourcePath)

					category, transient, corrupt := classifyError(errMsg)
					if category != "" {
						log.Printf("[Worker-%d] 🧭 失败原因: %s", workerID, getCategoryDescription(category))
					}

					// 截取关键错误信息（避免日志过长）
					if len(errMsg) > 1000 {
						log.Printf("[Worker-%d] 📋 错误详情 (前500字符): %s", workerID, errMsg[:500])
					} else {
						log.Printf("[Worker-%d] 📋 错误详情: %s", workerID, errMsg)
					}

					nextRetry := task.RetryCount + 1
					w.db.IncrementRetryCount(task.ID)

					if corrupt {
						nextMode := getNextRepairMode(task.RepairMode)
						if nextMode != "" {
							msg := fmt.Sprintf("检测到损坏，尝试%s修复", repairModeLabel(nextMode))
							_ = w.db.UpdateTaskRepairMode(task.ID, nextMode)
							_ = w.db.UpdateTaskProgress(task.ID, 0)
							_ = w.db.UpdateTaskStatus(task.ID, database.StatusPending, msg)
						} else {
							_ = w.db.UpdateTaskRepairMode(task.ID, "")
							_ = w.db.UpdateTaskStatus(task.ID, database.StatusIrrecoverable, "文件损坏不可恢复")
						}
					} else if w.isRetryable(category, transient) && nextRetry < w.config.System.MaxRetry {

						// Phase 4: 支持指数退避重试
						if w.config != nil && w.config.Retry.BackoffEnabled {
							// 使用指数退避 + 抖动
							baseDelay := 60  // 默认 60 秒
							maxDelay := 3600 // 默认 1 小时
							if w.config.Retry.BaseDelaySeconds > 0 {
								baseDelay = w.config.Retry.BaseDelaySeconds
							}
							if w.config.Retry.MaxDelaySeconds > 0 {
								maxDelay = w.config.Retry.MaxDelaySeconds
							}

							// 指数退避: base * 2^(retry-1)
							delay := baseDelay
							for i := 1; i < nextRetry; i++ {
								delay *= 2
								if delay > maxDelay {
									delay = maxDelay
									break
								}
							}

							// 添加随机抖动 (±20%)
							jitter := float64(delay) * 0.2 * (2.0*rand.Float64() - 1.0)
							finalDelay := delay + int(jitter)
							if finalDelay < 1 {
								finalDelay = 1
							}

							nextRetryAt := time.Now().Add(time.Duration(finalDelay) * time.Second)
							logMsg := fmt.Sprintf("自动重试(%d/%d): %s\n将在 %s 后重试", nextRetry, w.config.System.MaxRetry, getCategoryDescription(category), time.Duration(finalDelay)*time.Second)
							if category != "" {
								logMsg += fmt.Sprintf("\n%s", errMsg)
							}

							_ = w.db.UpdateTaskStatus(task.ID, database.StatusFailed, logMsg) // 先设置日志
							_ = w.db.ScheduleRetry(task.ID, nextRetryAt, category)

							log.Printf("[Worker-%d] 📅 任务 #%d 将在 %d 秒后重试", workerID, task.ID, finalDelay)
						} else {
							// 传统模式：立即重试
							logMsg := errMsg
							if category != "" {
								logMsg = fmt.Sprintf("自动重试: %s\n%s", getCategoryDescription(category), errMsg)
							}
							w.db.UpdateTaskProgress(task.ID, 0)
							w.db.UpdateTaskStatus(task.ID, database.StatusPending, logMsg)
						}
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

	repairMode := w.selectCorruptStrategy(task, inputPath, workerID)
	attemptingRepair := task != nil && strings.TrimSpace(task.RepairMode) != ""

	// 使用ffprobe检查文件完整性（修复模式下放宽）
	probeTimeout := time.Duration(w.config.FFmpeg.ProbeTimeoutSeconds) * time.Second
	if !attemptingRepair {
		if err := media.ProbeFile(inputPath, probeTimeout, 2); err != nil {
			return fmt.Errorf("文件检查失败: %w", err)
		}
	}

	// 获取视频总时长
	duration, err := w.getDuration(inputPath)
	if err != nil {
		log.Printf("[Worker-%d] 获取视频时长失败: %v", workerID, err)
		duration = 0
	}
	discardCorrupt := w.config.FFmpeg.DiscardCorrupt
	if repairMode == "discard" {
		discardCorrupt = true
	}
	if repairMode == "cfr" {
		discardCorrupt = false
	}

	// 临时文件名: 保持扩展名,在基础名后加 .stm_tmp
	// 例如: /path/file.mp4 -> /path/file.stm_tmp.mp4
	ext := filepath.Ext(outputPath)
	base := strings.TrimSuffix(outputPath, ext)
	outputTempPath := base + ".stm_tmp" + ext

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

	cmd := exec.CommandContext(ffCtx, w.config.FFmpeg.FFmpegPath, args...)

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
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
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
	// 先等待 stdout/stderr 读取完成（Go 文档要求在所有管道读取完成后才能调用 Wait）
	<-progressDone
	<-stderrDone
	waitErr := cmd.Wait()

	if waitErr != nil {
		stallReason := ""
		select {
		case stallReason = <-stallReasonCh:
		default:
		}

		if stallReason != "" {
			return fmt.Errorf("%s: %w\n日志:\n%s", stallReason, waitErr, stderrBuf.String())
		}
		if errors.Is(ffCtx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("FFmpeg超时(%s): %w\n日志:\n%s", maxDuration, waitErr, stderrBuf.String())
		}
		return fmt.Errorf("FFmpeg执行失败: %w\n日志:\n%s", waitErr, stderrBuf.String())
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

	cmd := exec.CommandContext(ctx, w.config.FFmpeg.FFprobePath,
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

func (w *Worker) selectCorruptStrategy(task *database.Task, path string, workerID int) string {
	if task != nil {
		if mode := strings.ToLower(strings.TrimSpace(task.RepairMode)); mode != "" {
			return mode
		}
	}

	strategy := strings.ToLower(strings.TrimSpace(w.config.FFmpeg.CorruptStrategy))
	if strategy == "" {
		strategy = "auto"
	}

	switch strategy {
	case "discard", "cfr":
		return strategy
	case "auto":
		// 优先补帧策略
		return "cfr"
	default:
		return "cfr"
	}
}

func getNextRepairMode(current string) string {
	switch strings.ToLower(strings.TrimSpace(current)) {
	case "":
		return "cfr"
	case "cfr":
		return "discard"
	default:
		return ""
	}
}

func repairModeLabel(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "cfr":
		return "补帧"
	case "discard":
		return "丢帧"
	default:
		return "修复"
	}
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

// isRetryable 检查错误类别是否允许重试
func (w *Worker) isRetryable(category string, transient bool) bool {
	// 如果没有配置白名单，使用 classifyError 的 transient 判断
	if w.config == nil || len(w.config.Retry.RetryableErrors) == 0 {
		return transient
	}

	// 配置了白名单，检查 category 是否在白名单中
	for _, allowedCategory := range w.config.Retry.RetryableErrors {
		if category == allowedCategory {
			return true
		}
	}

	return false
}

func classifyError(errMsg string) (string, bool, bool) {
	lower := strings.ToLower(errMsg)

	// timeout - 超时类错误（可重试）
	if strings.Contains(errMsg, "进度超过") || strings.Contains(errMsg, "FFmpeg超时") || strings.Contains(errMsg, "ffprobe超时") {
		return "timeout", true, false
	}

	// output_error - 输出文件验证失败（可重试，可能是文件损坏）
	if strings.Contains(errMsg, "输出文件验证失败") {
		return "output_error", true, true
	}

	// io_error - IO/挂载盘问题（可重试）
	if strings.Contains(lower, "input/output error") ||
		strings.Contains(lower, "i/o error") ||
		strings.Contains(lower, "stale file handle") ||
		strings.Contains(lower, "operation timed out") ||
		strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "connection timed out") ||
		strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "no such file") ||
		strings.Contains(lower, "broken pipe") {
		return "io_error", true, false
	}

	// disk_space - 磁盘空间不足（不可重试）
	if strings.Contains(errMsg, "磁盘空间") {
		return "disk_space", false, false
	}

	// invalid_data - 无效数据/文件损坏（不可重试，但可能触发修复模式）
	if strings.Contains(errMsg, "文件检查失败") ||
		strings.Contains(errMsg, "文件损坏") ||
		strings.Contains(errMsg, "解码测试失败") ||
		strings.Contains(errMsg, "Invalid NAL") ||
		strings.Contains(errMsg, "Error splitting") ||
		strings.Contains(errMsg, "Invalid data found") ||
		strings.Contains(errMsg, "moov atom not found") {
		return "invalid_data", false, true
	}

	return "unknown", false, false
}

// getCategoryDescription 获取错误类别的中文描述
func getCategoryDescription(category string) string {
	switch category {
	case "timeout":
		return "疑似IO卡住或进程超时"
	case "output_error":
		return "输出文件损坏"
	case "io_error":
		return "疑似IO/挂载盘问题"
	case "disk_space":
		return "磁盘空间不足"
	case "invalid_data":
		return "文件损坏或格式不支持"
	default:
		return "未知原因"
	}
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

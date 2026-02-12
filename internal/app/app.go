package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/stm/video-transcoder/internal/cleaner"
	"github.com/stm/video-transcoder/internal/config"
	"github.com/stm/video-transcoder/internal/database"
	"github.com/stm/video-transcoder/internal/scanner"
	"github.com/stm/video-transcoder/internal/web"
	"github.com/stm/video-transcoder/internal/worker"
)

// ErrorLevel 定义错误级别
type ErrorLevel int

const (
	// ErrorLevelFatal 致命错误，触发整个系统退出
	ErrorLevelFatal ErrorLevel = iota
	// ErrorLevelRecoverable 可恢复错误，记录并重试/降级
	ErrorLevelRecoverable
)

// ComponentError 组件错误，包含错误级别信息
type ComponentError struct {
	Component string
	Err       error
	Level     ErrorLevel
}

func (e *ComponentError) Error() string {
	return fmt.Sprintf("[%s] %v", e.Component, e.Err)
}

// Component 定义统一的组件接口
type Component interface {
	// Run 运行组件，阻塞直到 ctx 取消或发生错误
	Run(ctx context.Context) error
	// Name 返回组件名称（用于日志）
	Name() string
}

// App 应用程序 Supervisor，统一管理所有组件生命周期
type App struct {
	config     *config.Config
	db         *database.DB
	components []Component
	webServer  *web.Server // Web 需要特殊处理 Shutdown

	// 停机配置
	drainTimeout time.Duration // drain 模式的超时时间

	// Web 降级重试配置
	maxQuickRetries          int           // 快速重试的最大次数
	webDegradedRetryInterval time.Duration // Web 降级模式的慢速重试间隔
}

// New 创建应用程序 Supervisor
func New(cfg *config.Config, db *database.DB) *App {
	scan := scanner.New(cfg, db)
	work := worker.New(cfg, db)
	clean := cleaner.New(cfg, db)
	webServer := web.New(cfg, db, scan, work, clean)

	// 创建组件适配器
	scannerAdapter := NewScannerAdapter(scan)
	workerAdapter := NewWorkerAdapter(work)
	cleanerAdapter := NewCleanerAdapter(clean)
	webAdapter := NewWebAdapter(webServer)

	return &App{
		config:                   cfg,
		db:                       db,
		components:               []Component{scannerAdapter, workerAdapter, cleanerAdapter, webAdapter},
		webServer:                webServer,
		drainTimeout:             30 * time.Second, // 默认 30 秒 drain 超时
		maxQuickRetries:          10,               // 默认 10 次快速重试
		webDegradedRetryInterval: 5 * time.Minute,  // 默认 5 分钟慢速重试
	}
}

// Run 启动应用程序，阻塞直到所有组件退出或收到信号
// 返回的错误类型为 *ComponentError，包含错误级别信息
func (a *App) Run(ctx context.Context) error {
	log.Println("[App] 应用程序启动")

	g, ctx := errgroup.WithContext(ctx)

	// 为每个组件启动一个 goroutine
	for _, comp := range a.components {
		comp := comp // 闭包捕获
		g.Go(func() error {
			return a.runComponentWithRecovery(ctx, comp)
		})
	}

	// 等待所有组件完成或第一个 fatal 错误
	if err := g.Wait(); err != nil {
		// 检查是否是 context 取消（正常关闭）
		if errors.Is(err, context.Canceled) {
			log.Println("[App] 应用程序正常关闭")
			return nil
		}

		// 检查错误级别
		var compErr *ComponentError
		if errors.As(err, &compErr) {
			if compErr.Level == ErrorLevelFatal {
				return compErr
			}
			// Recoverable 错误不应该到达这里（已在组件内处理）
			log.Printf("[App] 可恢复错误意外到达顶层: %v", compErr)
			return nil
		}

		return err
	}

	log.Println("[App] 应用程序正常退出")
	return nil
}

// runComponentWithRecovery 运行组件并捕获 panic，带指数退避重试
func (a *App) runComponentWithRecovery(ctx context.Context, comp Component) (err error) {
	// 指数退避参数
	const (
		initialDelay = 1 * time.Second
		maxDelay     = 64 * time.Second
	)

	retryCount := 0
	delay := initialDelay

	for {
		innerErr := a.runComponentOnce(ctx, comp)

		// 正常退出（ctx 取消）
		if innerErr == nil || errors.Is(innerErr, context.Canceled) {
			// 在上层打印日志，避免 runComponentOnce 重复打印
			if errors.Is(innerErr, context.Canceled) {
				log.Printf("[App] 组件 [%s] 正常停止", comp.Name())
			}
			return nil
		}

		// 检查错误级别
		var compErr *ComponentError
		if errors.As(innerErr, &compErr) {
			// Fatal 错误直接返回
			if compErr.Level == ErrorLevelFatal {
				return compErr
			}

			// Recoverable 错误：重试
			retryCount++
			if retryCount > a.maxQuickRetries {
				// Web 组件特殊处理：切换到慢速周期性重试（自动恢复）
				if comp.Name() == "Web" {
					log.Printf("[App] 组件 [Web] 快速重试次数超过上限 (%d)，切换到慢速重试模式（每 %v）", a.maxQuickRetries, a.webDegradedRetryInterval)
					log.Println("[App] 系统将在无 Web 界面模式下继续运行，并尝试自动恢复")

					// 慢速重试：使用可配置的间隔
					ticker := time.NewTicker(a.webDegradedRetryInterval)
					defer ticker.Stop()

					for {
						select {
						case <-ctx.Done():
							log.Println("[App] Web 服务正常停止")
							return nil
						case <-ticker.C:
							log.Println("[App] Web 慢速重试中...")
							innerErr := a.runComponentOnce(ctx, comp)
							if errors.Is(innerErr, context.Canceled) {
								// 正常停机
								log.Println("[App] Web 服务正常停止")
								return nil
							}
							if innerErr != nil {
								// 慢速重试失败，继续等待下一个周期
								log.Printf("[App] Web 慢速重试失败: %v，将在 %v 后再次尝试", innerErr, a.webDegradedRetryInterval)
								continue
							}
							// Web 组件异常返回 nil（非预期），退出慢速重试循环避免空转
							log.Println("[App] Web 服务运行结束（无错误返回）")
							return nil
						}
					}
				}

				// 其他组件：转换为 fatal 错误
				log.Printf("[App] 组件 [%s] 重试次数超过上限 (%d)，放弃重试", comp.Name(), a.maxQuickRetries)
				return &ComponentError{
					Component: comp.Name(),
					Err:       fmt.Errorf("重试失败: %w", compErr.Err),
					Level:     ErrorLevelFatal,
				}
			}

			log.Printf("[App] 组件 [%s] 将在 %v 后重试 (第 %d 次)...", comp.Name(), delay, retryCount)

			// 指数退避等待
			select {
			case <-ctx.Done():
				log.Printf("[App] 组件 [%s] 正常停止", comp.Name())
				return nil
			case <-time.After(delay):
				// 增加延迟（指数退避）
				delay *= 2
				if delay > maxDelay {
					delay = maxDelay
				}
			}

			continue
		}

		// 其他错误视为 fatal
		return innerErr
	}
}

// runComponentOnce 运行组件一次，带 panic 恢复
func (a *App) runComponentOnce(ctx context.Context, comp Component) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[App] ⚠️  组件 [%s] 发生 panic: %v", comp.Name(), r)

			// 将 panic 转换为 ComponentError
			panicErr := fmt.Errorf("panic: %v", r)

			// Web 组件的 panic 视为 recoverable，其他视为 fatal
			level := ErrorLevelFatal
			if comp.Name() == "Web" {
				level = ErrorLevelRecoverable
			}

			err = &ComponentError{
				Component: comp.Name(),
				Err:       panicErr,
				Level:     level,
			}
		}
	}()

	// 运行组件
	err = comp.Run(ctx)

	// 如果是正常的 ctx.Done()，保留 context.Canceled 以便上层判断
	// 注意：不在这里打印日志，由上层调用者决定是否打印
	if errors.Is(err, context.Canceled) {
		return err // 保留 context.Canceled，不转 nil
	}

	if err != nil {
		log.Printf("[App] 组件 [%s] 返回错误: %v", comp.Name(), err)
	}

	return err
}

// Shutdown 优雅关闭应用程序
// 使用 drain 策略：等待当前任务完成（带超时）
func (a *App) Shutdown(ctx context.Context) error {
	log.Println("[App] 开始优雅关闭...")

	// 创建带超时的 context
	shutdownCtx, cancel := context.WithTimeout(ctx, a.drainTimeout)
	defer cancel()

	var wg sync.WaitGroup
	errChan := make(chan error, 1)

	// 1. 先关闭 Web 服务器（停止接收新请求）
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Println("[App] 关闭 Web 服务器...")
		if err := a.webServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("[App] Web 服务器关闭出错: %v", err)
			select {
			case errChan <- err:
			default:
			}
		} else {
			log.Println("[App] Web 服务器已关闭")
		}
	}()

	// 2. 其他组件通过 context 取消自然停止（由 Run 的 ctx.Done() 处理）
	// 注意：Worker 会在 context 取消时等待当前任务完成（drain 模式）

	// 等待关闭完成或超时
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Println("[App] 优雅关闭完成")
		return nil
	case err := <-errChan:
		return fmt.Errorf("关闭过程中出错: %w", err)
	case <-shutdownCtx.Done():
		log.Println("[App] ⚠️  关闭超时，部分组件可能未完全停止")
		return fmt.Errorf("关闭超时")
	}
}

// ===== 组件适配器：将现有组件适配到统一接口 =====

// ScannerAdapter 扫描器适配器
type ScannerAdapter struct {
	scanner *scanner.Scanner
}

func NewScannerAdapter(s *scanner.Scanner) *ScannerAdapter {
	return &ScannerAdapter{scanner: s}
}

func (a *ScannerAdapter) Name() string {
	return "Scanner"
}

func (a *ScannerAdapter) Run(ctx context.Context) error {
	// Scanner.RunPeriodically 会阻塞直到 ctx 取消
	a.scanner.RunPeriodically(ctx)
	return nil
}

// WorkerAdapter Worker 适配器
type WorkerAdapter struct {
	worker *worker.Worker
}

func NewWorkerAdapter(w *worker.Worker) *WorkerAdapter {
	return &WorkerAdapter{worker: w}
}

func (a *WorkerAdapter) Name() string {
	return "Worker"
}

func (a *WorkerAdapter) Run(ctx context.Context) error {
	// Worker.Run 会阻塞直到 ctx 取消
	a.worker.Run(ctx)
	return nil
}

// CleanerAdapter 清理器适配器
type CleanerAdapter struct {
	cleaner *cleaner.Cleaner
}

func NewCleanerAdapter(c *cleaner.Cleaner) *CleanerAdapter {
	return &CleanerAdapter{cleaner: c}
}

func (a *CleanerAdapter) Name() string {
	return "Cleaner"
}

func (a *CleanerAdapter) Run(ctx context.Context) error {
	// Cleaner.Run 会阻塞直到 ctx 取消
	a.cleaner.Run(ctx)
	return nil
}

// WebAdapter Web 服务器适配器
type WebAdapter struct {
	server *web.Server
}

func NewWebAdapter(s *web.Server) *WebAdapter {
	return &WebAdapter{server: s}
}

func (a *WebAdapter) Name() string {
	return "Web"
}

func (a *WebAdapter) Run(ctx context.Context) error {
	// 设置应用程序生命周期 context
	a.server.SetContext(ctx)

	// Web 启动失败属于 recoverable 错误
	errChan := make(chan error, 1)

	go func() {
		if err := a.server.Start(":8080"); err != nil && err != http.ErrServerClosed {
			errChan <- &ComponentError{
				Component: "Web",
				Err:       err,
				Level:     ErrorLevelRecoverable, // 用户选择：Web 失败可降级
			}
		}
	}()

	// 等待 context 取消或启动错误
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errChan:
		// Web 启动失败，返回 ComponentError 触发指数退避重试
		return err
	}
}

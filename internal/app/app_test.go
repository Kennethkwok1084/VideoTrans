package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// mockComponent 模拟组件用于测试
type mockComponent struct {
	name        string
	runFunc     func(ctx context.Context) error
	runDuration time.Duration
}

func (m *mockComponent) Name() string {
	return m.name
}

func (m *mockComponent) Run(ctx context.Context) error {
	if m.runFunc != nil {
		return m.runFunc(ctx)
	}

	// 默认行为：等待 context 取消
	if m.runDuration > 0 {
		select {
		case <-time.After(m.runDuration):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	<-ctx.Done()
	return ctx.Err()
}

// TestAppNormalShutdown 测试正常关闭流程
func TestAppNormalShutdown(t *testing.T) {
	// 创建一个简单的 mock 组件
	comp := &mockComponent{
		name: "TestComponent",
	}

	app := &App{
		components:      []Component{comp},
		drainTimeout:    5 * time.Second,
		maxQuickRetries: 10, // 默认 10 次快速重试
	}

	ctx, cancel := context.WithCancel(context.Background())

	errChan := make(chan error, 1)
	go func() {
		errChan <- app.Run(ctx)
	}()

	// 等待一小段时间以确保组件启动
	time.Sleep(100 * time.Millisecond)

	// 取消 context
	cancel()

	// 等待 app 退出
	select {
	case err := <-errChan:
		if err != nil {
			t.Errorf("正常关闭不应返回错误，但得到: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Error("App 关闭超时")
	}
}

// TestAppComponentPanic 测试组件 panic 恢复
func TestAppComponentPanic(t *testing.T) {
	// 创建一个会 panic 的组件
	panicComp := &mockComponent{
		name: "PanicComponent",
		runFunc: func(ctx context.Context) error {
			panic("test panic")
		},
	}

	app := &App{
		components:      []Component{panicComp},
		drainTimeout:    5 * time.Second,
		maxQuickRetries: 10, // 默认 10 次快速重试
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := app.Run(ctx)

	// 应该捕获 panic 并转换为错误
	if err == nil {
		t.Error("期望返回 panic 错误，但得到 nil")
	}

	var compErr *ComponentError
	if !errors.As(err, &compErr) {
		t.Errorf("期望返回 ComponentError，但得到: %T", err)
	}
}

// TestAppRecoverableError 测试可恢复错误重试
func TestAppRecoverableError(t *testing.T) {
	var (
		retryCount int
		mu         sync.Mutex
	)

	// 创建一个会失败几次然后成功的组件
	retryComp := &mockComponent{
		name: "RetryComponent",
		runFunc: func(ctx context.Context) error {
			mu.Lock()
			retryCount++
			count := retryCount
			mu.Unlock()

			if count < 3 {
				// 前两次返回 recoverable 错误
				return &ComponentError{
					Component: "RetryComponent",
					Err:       errors.New("temporary error"),
					Level:     ErrorLevelRecoverable,
				}
			}
			// 第三次成功，等待 context 取消
			<-ctx.Done()
			return ctx.Err()
		},
	}

	app := &App{
		components:      []Component{retryComp},
		drainTimeout:    5 * time.Second,
		maxQuickRetries: 10, // 默认 10 次快速重试
	}

	ctx, cancel := context.WithCancel(context.Background())

	errChan := make(chan error, 1)
	go func() {
		errChan <- app.Run(ctx)
	}()

	// 等待重试完成（至少 1s + 2s = 3s）
	time.Sleep(4 * time.Second)

	// 检查重试次数
	mu.Lock()
	count := retryCount
	mu.Unlock()

	if count < 3 {
		t.Errorf("期望至少重试 3 次，但只有 %d 次", count)
	}

	// 取消 context
	cancel()

	// 等待 app 退出
	select {
	case err := <-errChan:
		if err != nil {
			t.Errorf("重试成功后不应返回错误，但得到: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("App 关闭超时")
	}
}

// TestAppFatalError 测试致命错误立即退出
func TestAppFatalError(t *testing.T) {
	// 创建一个返回 fatal 错误的组件
	fatalComp := &mockComponent{
		name: "FatalComponent",
		runFunc: func(ctx context.Context) error {
			return &ComponentError{
				Component: "FatalComponent",
				Err:       errors.New("fatal error"),
				Level:     ErrorLevelFatal,
			}
		},
	}

	normalComp := &mockComponent{
		name: "NormalComponent",
	}

	app := &App{
		components:      []Component{fatalComp, normalComp},
		drainTimeout:    5 * time.Second,
		maxQuickRetries: 10, // 默认 10 次快速重试
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	err := app.Run(ctx)
	elapsed := time.Since(start)

	// 应该立即返回 fatal 错误（不等待其他组件）
	if elapsed > 1*time.Second {
		t.Errorf("Fatal 错误应该立即退出，但花费了 %v", elapsed)
	}

	var compErr *ComponentError
	if !errors.As(err, &compErr) {
		t.Fatalf("期望返回 ComponentError，但得到: %T", err)
	}

	if compErr.Level != ErrorLevelFatal {
		t.Errorf("期望 ErrorLevelFatal，但得到: %v", compErr.Level)
	}
}

// TestAppMultipleComponents 测试多组件协同
func TestAppMultipleComponents(t *testing.T) {
	var (
		comp1Started bool
		comp2Started bool
		mu           sync.Mutex
	)

	comp1 := &mockComponent{
		name: "Component1",
		runFunc: func(ctx context.Context) error {
			mu.Lock()
			comp1Started = true
			mu.Unlock()
			<-ctx.Done()
			return ctx.Err()
		},
	}

	comp2 := &mockComponent{
		name: "Component2",
		runFunc: func(ctx context.Context) error {
			mu.Lock()
			comp2Started = true
			mu.Unlock()
			<-ctx.Done()
			return ctx.Err()
		},
	}

	app := &App{
		components:      []Component{comp1, comp2},
		drainTimeout:    5 * time.Second,
		maxQuickRetries: 10, // 默认 10 次快速重试
	}

	ctx, cancel := context.WithCancel(context.Background())

	errChan := make(chan error, 1)
	go func() {
		errChan <- app.Run(ctx)
	}()

	// 等待组件启动
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	started1 := comp1Started
	started2 := comp2Started
	mu.Unlock()

	if !started1 {
		t.Error("Component1 未启动")
	}
	if !started2 {
		t.Error("Component2 未启动")
	}

	// 取消 context
	cancel()

	// 等待 app 退出
	select {
	case err := <-errChan:
		if err != nil {
			t.Errorf("多组件正常关闭不应返回错误，但得到: %v", err)
		}
	case <-time.After(1 * time.Second):
		t.Error("App 关闭超时")
	}
}

// TestAppExponentialBackoff 测试指数退避重试
func TestAppExponentialBackoff(t *testing.T) {
	var (
		retryTimes []time.Time
		mu         sync.Mutex
	)

	retryComp := &mockComponent{
		name: "BackoffComponent",
		runFunc: func(ctx context.Context) error {
			mu.Lock()
			retryTimes = append(retryTimes, time.Now())
			count := len(retryTimes)
			mu.Unlock()

			// 前 5 次返回 recoverable 错误（足够验证指数退避）
			if count <= 5 {
				return &ComponentError{
					Component: "BackoffComponent",
					Err:       errors.New("temporary error"),
					Level:     ErrorLevelRecoverable,
				}
			}

			// 第 6 次成功，等待 context 取消
			<-ctx.Done()
			return ctx.Err()
		},
	}

	app := &App{
		components:      []Component{retryComp},
		drainTimeout:    5 * time.Second,
		maxQuickRetries: 10, // 默认 10 次快速重试
	}

	ctx, cancel := context.WithCancel(context.Background())

	errChan := make(chan error, 1)
	go func() {
		errChan <- app.Run(ctx)
	}()

	// 等待至少 4 次重试完成（1s + 2s + 4s + 8s = 15s）
	time.Sleep(17 * time.Second)

	// 取消 context，让测试退出
	cancel()

	// 等待 app 退出
	select {
	case <-errChan:
	case <-time.After(2 * time.Second):
	}

	// 检查重试次数
	mu.Lock()
	times := make([]time.Time, len(retryTimes))
	copy(times, retryTimes)
	mu.Unlock()

	if len(times) < 5 {
		t.Errorf("期望至少 5 次执行（包含初始），但只有 %d 次", len(times))
	}

	// 检查指数退避（每次延迟应该大致翻倍）
	// 前几次：~1s, ~2s, ~4s
	if len(times) >= 4 {
		delay1 := times[1].Sub(times[0])
		delay2 := times[2].Sub(times[1])
		delay3 := times[3].Sub(times[2])

		t.Logf("重试延迟: %v, %v, %v", delay1, delay2, delay3)

		// 允许 ±30% 的误差（考虑调度延迟）
		if delay1 < 700*time.Millisecond || delay1 > 1300*time.Millisecond {
			t.Errorf("第一次重试延迟应该约为 1s，但得到 %v", delay1)
		}
		if delay2 < 1400*time.Millisecond || delay2 > 2600*time.Millisecond {
			t.Errorf("第二次重试延迟应该约为 2s，但得到 %v", delay2)
		}
		if delay3 < 2800*time.Millisecond || delay3 > 5200*time.Millisecond {
			t.Errorf("第三次重试延迟应该约为 4s，但得到 %v", delay3)
		}
	}
}

// TestAppWebQuickRetryRecovery 测试 Web 组件快速重试恢复
func TestAppWebQuickRetryRecovery(t *testing.T) {
	var (
		failCount int
		mu        sync.Mutex
	)

	// 创建一个前 3 次失败、第 4 次成功的 Web 组件
	webComp := &mockComponent{
		name: "Web",
		runFunc: func(ctx context.Context) error {
			mu.Lock()
			failCount++
			count := failCount
			mu.Unlock()

			// 前 3 次失败（测试快速重试），第 4 次成功
			if count <= 3 {
				return &ComponentError{
					Component: "Web",
					Err:       errors.New("port in use"),
					Level:     ErrorLevelRecoverable,
				}
			}

			// 第 4 次成功，等待 context 取消
			t.Logf("Web 组件在第 %d 次尝试时成功恢复", count)
			<-ctx.Done()
			return ctx.Err()
		},
	}

	// 使用更短的慢速重试间隔以加快测试
	app := &App{
		components:               []Component{webComp},
		drainTimeout:             5 * time.Second,
		maxQuickRetries:          10,              // 使用默认 10 次快速重试
		webDegradedRetryInterval: 2 * time.Second, // 测试使用 2 秒间隔
	}

	ctx, cancel := context.WithCancel(context.Background())

	errChan := make(chan error, 1)
	go func() {
		errChan <- app.Run(ctx)
	}()

	// 等待快速重试完成：1s+2s+4s = 7 秒 + 余量
	time.Sleep(10 * time.Second)

	// 检查快速重试完成（应该有 4 次：初始 + 3 次重试）
	mu.Lock()
	count := failCount
	mu.Unlock()

	if count != 4 {
		t.Errorf("期望 4 次尝试（初始 + 3 次快速重试），但有 %d 次", count)
	}

	// 取消 context
	cancel()

	// 等待 app 退出
	select {
	case err := <-errChan:
		if err != nil {
			t.Errorf("Web 组件降级运行不应返回错误，但得到: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("App 关闭超时")
	}

	t.Logf("Web 组件在快速重试阶段恢复（共 %d 次尝试）", count)
}

// TestAppWebComponentSlowRetryRecovery 测试 Web 组件慢速重试自动恢复
func TestAppWebComponentSlowRetryRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过长时间运行的测试")
	}

	var (
		failCount  int
		mu         sync.Mutex
		recoveryAt int
		recovered  = make(chan struct{}) // 恢复信号
	)

	// 设置在第 5 次尝试时恢复（3次快速 + 1次慢速 + 1次成功）
	// 这样可以在合理时间内完成测试：1+2+4 = 7 秒快速重试 + 0.5 秒慢速重试
	recoveryAt = 5

	webComp := &mockComponent{
		name: "Web",
		runFunc: func(ctx context.Context) error {
			mu.Lock()
			failCount++
			count := failCount
			mu.Unlock()

			if count < recoveryAt {
				return &ComponentError{
					Component: "Web",
					Err:       fmt.Errorf("port in use (attempt %d)", count),
					Level:     ErrorLevelRecoverable,
				}
			}

			// 恢复成功，发送信号
			t.Logf("Web 组件在第 %d 次尝试时成功恢复（慢速重试阶段）", count)
			close(recovered)
			<-ctx.Done()
			return ctx.Err()
		},
	}

	app := &App{
		components:               []Component{webComp},
		drainTimeout:             5 * time.Second,
		maxQuickRetries:          3,                      // 使用 3 次快速重试以加快测试
		webDegradedRetryInterval: 500 * time.Millisecond, // 使用 500ms 间隔以加速测试
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- app.Run(ctx)
	}()

	// 等待恢复信号或超时
	select {
	case <-recovered:
		// 恢复成功，验证次数
		mu.Lock()
		count := failCount
		mu.Unlock()

		if count != recoveryAt {
			t.Errorf("期望 %d 次尝试（包含慢速重试），但有 %d 次", recoveryAt, count)
		}

		t.Logf("Web 组件成功在慢速重试阶段恢复（共 %d 次尝试）", count)

		// 取消 context 让 app 退出
		cancel()

		// 等待 app 退出
		select {
		case err := <-errChan:
			if err != nil {
				t.Errorf("Web 恢复后不应返回错误，但得到: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("App 关闭超时")
		}

	case <-time.After(20 * time.Second):
		cancel()
		t.Error("Web 慢速重试未能在预期时间内恢复")
	}
}

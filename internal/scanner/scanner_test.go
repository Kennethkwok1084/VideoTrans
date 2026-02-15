package scanner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stm/video-transcoder/internal/config"
	"github.com/stm/video-transcoder/internal/database"
)

func TestScanner_Concurrency_Real(t *testing.T) {
	scanner, db, _ := setupTestScanner(t)
	defer db.Close()

	// 1. 启动一个主扫描，并让它在 beforeScan 中阻塞，直到我们允许它继续
	// 这确保了 Scanner 处于锁定状态
	scanLocked := make(chan struct{})
	releaseScan := make(chan struct{})

	// 使用 sync.Once 确保 close 操作只执行一次，防止 panic
	var onceRelease sync.Once

	scanner.beforeScan = func() {
		close(scanLocked) // 通知主 goroutine：我已经拿到了锁
		<-releaseScan     // 等待测试允许释放锁
	}

	// 异步启动主扫描
	var mainScanWg sync.WaitGroup
	var mainScanErr error
	mainScanWg.Add(1)
	go func() {
		defer mainScanWg.Done()
		mainScanErr = scanner.Scan(context.Background())
	}()

	// 等待主扫描进入锁定状态
	<-scanLocked

	// 2. 现在 Scanner 已锁定，发起并发扫描请求
	// 这些请求应该立即失败并返回 ErrScanInProgress
	concurrency := 5
	var wg sync.WaitGroup
	wg.Add(concurrency)

	errChan := make(chan error, concurrency)

	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			err := scanner.Scan(context.Background())
			if err != nil {
				errChan <- err
			}
		}()
	}

	wg.Wait()
	close(errChan)

	// 3. 释放主扫描
	onceRelease.Do(func() {
		close(releaseScan)
	})

	// 4. 验证结果：所有并发请求都应该返回 ErrScanInProgress
	conflictCount := 0
	for err := range errChan {
		if errors.Is(err, ErrScanInProgress) {
			conflictCount++
		} else {
			t.Errorf("Unexpected error: %v", err)
		}
	}

	// 必须全部冲突，因为主扫描一直持有锁直到我们释放
	if conflictCount != concurrency {
		t.Errorf("Expected %d conflicts, but got %d", concurrency, conflictCount)
	}

	// 等待主扫描结束，防止 goroutine 泄漏干扰后续测试
	mainScanWg.Wait()

	// 验证主扫描成功（它应该正常完成，不应该被并发请求影响）
	if mainScanErr != nil {
		t.Errorf("Main scan failed: %v", mainScanErr)
	}
}

func TestScanner_Verify_PartialFailure(t *testing.T) {
	// 验证当校验过程中出现系统错误时，水位不推进
	scanner, db, inputDir := setupTestScanner(t)
	defer db.Close()

	// 启用严格检查
	scanner.config.FFmpeg.StrictCheck = true

	// 1. 创建一个已完成的任务
	testFile := filepath.Join(inputDir, "test.mp4")
	if err := os.WriteFile(testFile, []byte("content"), 0644); err != nil {
		t.Fatalf("创建测试文件失败: %v", err)
	}

	task := &database.Task{
		SourcePath:  testFile,
		SourceMtime: time.Now(),
		SourceSize:  1024,
	}
	if err := db.CreateTask(task); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if err := db.UpdateTaskStatus(task.ID, database.StatusCompleted, ""); err != nil {
		t.Fatalf("更新任务状态失败: %v", err)
	}

	// 设置初始水位为 1 小时前
	initialWatermark := time.Now().Add(-1 * time.Hour)
	scanner.lastVerifyTime = initialWatermark

	// 更新任务完成时间为现在
	if err := db.UpdateTaskStatus(task.ID, database.StatusCompleted, ""); err != nil { // update completed_at to now
		t.Fatalf("再次更新任务状态失败: %v", err)
	}

	// 2. 模拟系统错误：删除源文件目录，导致 resolveOutputPath 或 os.Stat 失败
	// 但我们已经在 setupTestScanner 中创建了目录。
	// 让我们通过修改 config 使得 resolveOutputPath 失败
	// 或者直接删除 inputDir
	// 注意：Delete inputDir might cause Scan itself to fail before Verify.
	// We want Verify to fail. Verify calls resolveOutputPath which uses config.

	// 让我们尝试构造一种情况：os.Stat 失败（权限拒绝）。
	// 但在测试环境中 chmod 可能不生效（取决于用户）。
	// 另一种方法：让 resolveOutputPath 失败。
	// 我们可以清空 Config.Pairs，这样 resolveOutputPath 将返回 false。
	scanner.config.Path.Pairs = []config.InputOutputPair{}

	// 3. 执行 Scan
	if err := scanner.Scan(context.Background()); err != nil {
		// Scan 可能会因为 config 为空而报错，也可能只记录日志
		// 但我们不期望它 panic
		// 如果 Scan 返回错误，也是预期的，但我们要检查水位
		t.Logf("Scan returned error (expected): %v", err)
	}

	// 4. 检查水位是否推进
	// 因为 resolveOutputPath 失败，verifyCompletedOutputs 应该返回 error 或不更新 cursorTime
	// 我们的逻辑是：如果 resolveOutputPath 失败，log error 并 return cursorTime (初始值)。
	// 所以 scanner.lastVerifyTime 应该保持不变（或者不应该推进到 task.CompletedAt）。

	if scanner.lastVerifyTime.After(initialWatermark) {
		t.Errorf("Watermark advanced despite failure! Initial: %v, Current: %v", initialWatermark, scanner.lastVerifyTime)
	}
}

func setupTestScanner(t *testing.T) (*Scanner, *database.DB, string) {
	tmpDir := t.TempDir()

	// 创建测试目录
	inputDir := filepath.Join(tmpDir, "input")
	outputDir := filepath.Join(tmpDir, "output")
	dbPath := filepath.Join(tmpDir, "test.db")

	if err := os.MkdirAll(inputDir, 0755); err != nil {
		t.Fatalf("创建输入目录失败: %v", err)
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		t.Fatalf("创建输出目录失败: %v", err)
	}

	cfg := &config.Config{
		System: config.SystemConfig{ScanInterval: 10},
		Path: config.PathConfig{
			Pairs: []config.InputOutputPair{{
				Input:  inputDir,
				Output: outputDir,
			}},
			Database: dbPath,
		},
		FFmpeg: config.FFmpegConfig{
			Extensions: []string{".mp4", ".mkv", ".avi"},
		},
	}

	db, err := database.Init(dbPath)
	if err != nil {
		t.Fatalf("初始化数据库失败: %v", err)
	}
	scanner := New(cfg, db)

	return scanner, db, inputDir
}

func TestShouldSkipDir(t *testing.T) {
	scanner, db, _ := setupTestScanner(t)
	defer db.Close()

	// 设置默认跳过目录
	scanner.config.System.SkipDirs = []string{".stm_trash", "@eaDir", "#recycle", ".DS_Store"}

	tests := []struct {
		name string
		want bool
	}{
		{".stm_trash", true},
		{"@eaDir", true},
		{"#recycle", true},
		{".DS_Store", true},
		{"normal_dir", false},
		{"videos", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scanner.shouldSkipDir(tt.name); got != tt.want {
				t.Errorf("shouldSkipDir(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestShouldSkipFile(t *testing.T) {
	scanner, db, _ := setupTestScanner(t)
	defer db.Close()

	// 设置默认跳过规则
	scanner.config.System.SkipFilePrefixes = []string{"SYNOPHOTO_", "."}
	scanner.config.System.SkipFileSuffixes = []string{".tmp", ".part", ".lock"}

	tests := []struct {
		name string
		want bool
	}{
		{"SYNOPHOTO_FILM_M.mp4", true},
		{"SYNOPHOTO_THUMB.jpg", true},
		{".hidden", true},
		{"video.tmp", true},
		{"video.part", true},
		{"video.lock", true},
		{"normal_video.mp4", false},
		{"movie.mkv", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scanner.shouldSkipFile(tt.name); got != tt.want {
				t.Errorf("shouldSkipFile(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestScanNewFile(t *testing.T) {
	scanner, db, inputDir := setupTestScanner(t)
	defer db.Close()

	// 创建测试视频文件
	testFile := filepath.Join(inputDir, "test.mp4")
	content := []byte("fake video content")
	if err := os.WriteFile(testFile, content, 0644); err != nil {
		t.Fatalf("创建测试文件失败: %v", err)
	}

	// 执行扫描
	ctx := context.Background()
	err := scanner.Scan(ctx)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}

	// 验证任务已创建（使用完整路径）
	task, err := db.GetTaskByPath(testFile)
	if err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}

	if task == nil {
		t.Fatal("任务未创建")
	}

	if task.Status != database.StatusPending {
		t.Errorf("任务状态错误: %s", task.Status)
	}
}

func TestScanSkipsSystemFiles(t *testing.T) {
	scanner, db, inputDir := setupTestScanner(t)
	defer db.Close()

	// 创建系统文件（应被跳过）
	systemFiles := []string{
		"SYNOPHOTO_FILM_M.mp4",
		".hidden.mp4",
		"video.tmp",
	}

	for _, filename := range systemFiles {
		testFile := filepath.Join(inputDir, filename)
		os.WriteFile(testFile, []byte("content"), 0644)
	}

	// 执行扫描
	ctx := context.Background()
	scanner.Scan(ctx)

	// 验证系统文件未被添加
	for _, filename := range systemFiles {
		task, _ := db.GetTaskByPath(filename)
		if task != nil {
			t.Errorf("系统文件不应被添加: %s", filename)
		}
	}
}

func TestScanSkipsSystemDirectories(t *testing.T) {
	scanner, db, inputDir := setupTestScanner(t)
	defer db.Close()

	// 创建系统目录
	systemDirs := []string{
		"@eaDir",
		"#recycle",
		".stm_trash",
	}

	for _, dirname := range systemDirs {
		dir := filepath.Join(inputDir, dirname)
		os.MkdirAll(dir, 0755)
		// 在系统目录中创建文件
		testFile := filepath.Join(dir, "video.mp4")
		os.WriteFile(testFile, []byte("content"), 0644)
	}

	// 执行扫描
	ctx := context.Background()
	scanner.Scan(ctx)

	// 验证系统目录中的文件未被添加
	for _, dirname := range systemDirs {
		task, _ := db.GetTaskByPath(filepath.Join(dirname, "video.mp4"))
		if task != nil {
			t.Errorf("系统目录中的文件不应被添加: %s", dirname)
		}
	}
}

func TestScanDetectsFileUpdate(t *testing.T) {
	scanner, db, inputDir := setupTestScanner(t)
	defer db.Close()

	// 创建初始文件
	testFile := filepath.Join(inputDir, "test.mp4")
	os.WriteFile(testFile, []byte("original"), 0644)

	// 第一次扫描
	ctx := context.Background()
	scanner.Scan(ctx)

	// 获取初始任务（使用完整路径）
	task1, _ := db.GetTaskByPath(testFile)
	if task1 == nil {
		t.Fatal("初始任务未创建")
	}

	// 更新任务状态为完成
	db.UpdateTaskStatus(task1.ID, database.StatusCompleted, "完成")

	// 修改文件
	time.Sleep(10 * time.Millisecond) // 确保修改时间不同
	os.WriteFile(testFile, []byte("updated content"), 0644)

	// 第二次扫描
	scanner.Scan(ctx)

	// 验证任务被重置（使用完整路径）
	task2, _ := db.GetTaskByPath(testFile)
	if task2.Status != database.StatusPending {
		t.Errorf("文件更新后任务应被重置为pending，实际: %s", task2.Status)
	}
}

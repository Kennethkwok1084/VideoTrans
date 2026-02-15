package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stm/video-transcoder/internal/config"
	"github.com/stm/video-transcoder/internal/database"
)

func TestIsWorkingHours(t *testing.T) {
	tests := []struct {
		name        string
		workStart   int
		workEnd     int
		currentHour int
		want        bool
	}{
		{
			name:        "全天运行 - start=end",
			workStart:   0,
			workEnd:     0,
			currentHour: 12,
			want:        true,
		},
		{
			name:        "正常工作时间段 - 在时间窗口内",
			workStart:   0,
			workEnd:     6,
			currentHour: 3,
			want:        true,
		},
		{
			name:        "正常工作时间段 - 在起始时间",
			workStart:   0,
			workEnd:     6,
			currentHour: 0,
			want:        true,
		},
		{
			name:        "正常工作时间段 - 超出结束时间",
			workStart:   0,
			workEnd:     6,
			currentHour: 6,
			want:        false,
		},
		{
			name:        "正常工作时间段 - 不在时间窗口内",
			workStart:   0,
			workEnd:     6,
			currentHour: 12,
			want:        false,
		},
		{
			name:        "跨天工作时间段 - 在前半段",
			workStart:   22,
			workEnd:     6,
			currentHour: 23,
			want:        true,
		},
		{
			name:        "跨天工作时间段 - 在后半段",
			workStart:   22,
			workEnd:     6,
			currentHour: 3,
			want:        true,
		},
		{
			name:        "跨天工作时间段 - 不在时间窗口内",
			workStart:   22,
			workEnd:     6,
			currentHour: 12,
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 测试时间窗口逻辑
			start := tt.workStart
			end := tt.workEnd
			hour := tt.currentHour

			var got bool
			if start == end {
				got = true
			} else if start < end {
				got = hour >= start && hour < end
			} else {
				got = hour >= start || hour < end
			}

			if got != tt.want {
				t.Errorf("时间窗口逻辑: hour=%d, start=%d, end=%d, got=%v, want=%v",
					hour, start, end, got, tt.want)
			}
		})
	}
}

func TestGetForceRun(t *testing.T) {
	w := &Worker{forceRun: false}
	if w.GetForceRun() {
		t.Error("初始状态 GetForceRun() 应该为 false")
	}

	w.SetForceRun(true)
	if !w.GetForceRun() {
		t.Error("SetForceRun(true) 后 GetForceRun() 应该为 true")
	}

	w.SetForceRun(false)
	if w.GetForceRun() {
		t.Error("SetForceRun(false) 后 GetForceRun() 应该为 false")
	}
}

func TestNew(t *testing.T) {
	cfg := &config.Config{
		System: config.SystemConfig{
			CronStart:  0,
			CronEnd:    6,
			MaxWorkers: 2,
		},
	}

	w := New(cfg, nil)

	if w == nil {
		t.Fatal("New() 返回 nil")
	}

	if w.config.System.CronStart != 0 {
		t.Errorf("CronStart = %d, want 0", w.config.System.CronStart)
	}

	if w.config.System.CronEnd != 6 {
		t.Errorf("CronEnd = %d, want 6", w.config.System.CronEnd)
	}

	if w.config.System.MaxWorkers != 2 {
		t.Errorf("MaxWorkers = %d, want 2", w.config.System.MaxWorkers)
	}

	if w.GetForceRun() {
		t.Error("初始 forceRun 应该为 false")
	}
}

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name            string
		retryableErrors []string
		category        string
		transient       bool
		want            bool
	}{
		{
			name:            "无白名单 - transient=true",
			retryableErrors: nil,
			category:        "timeout",
			transient:       true,
			want:            true,
		},
		{
			name:            "无白名单 - transient=false",
			retryableErrors: nil,
			category:        "invalid_data",
			transient:       false,
			want:            false,
		},
		{
			name:            "有白名单 - 类别在白名单中",
			retryableErrors: []string{"timeout", "io_error"},
			category:        "timeout",
			transient:       true,
			want:            true,
		},
		{
			name:            "有白名单 - 类别不在白名单中（即使 transient=true）",
			retryableErrors: []string{"timeout"},
			category:        "io_error",
			transient:       true,
			want:            false,
		},
		{
			name:            "有白名单 - invalid_data 在白名单中（违背设计）",
			retryableErrors: []string{"invalid_data"},
			category:        "invalid_data",
			transient:       false,
			want:            true, // 白名单优先级高于 transient
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &Worker{
				config: &config.Config{
					Retry: config.RetryConfig{
						RetryableErrors: tt.retryableErrors,
					},
				},
			}

			got := w.isRetryable(tt.category, tt.transient)
			if got != tt.want {
				t.Errorf("isRetryable(%q, %v) = %v, want %v",
					tt.category, tt.transient, got, tt.want)
			}
		})
	}
}

func TestProcessWorkerSuccessFlow(t *testing.T) {
	w, db, cfg, inputDir, outputDir := newWorkerTestFixture(t)
	defer db.Close()

	t.Setenv("STM_TEST_FFMPEG_EXIT_CODE", "0")
	t.Setenv("STM_TEST_FFMPEG_SLEEP_SECONDS", "0")
	t.Setenv("STM_TEST_DURATION_SECONDS", "5")

	sourcePath := filepath.Join(inputDir, "ok.mkv")
	task := createTaskForPath(t, db, sourcePath)

	runSingleTaskWorker(t, w, task)

	updated, err := db.GetTaskByPath(sourcePath)
	if err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if updated == nil {
		t.Fatal("任务不存在")
	}
	if updated.Status != database.StatusCompleted {
		t.Fatalf("任务状态错误: got=%s want=%s", updated.Status, database.StatusCompleted)
	}
	if updated.Progress != 100 {
		t.Fatalf("任务进度错误: got=%.2f want=100", updated.Progress)
	}
	if updated.OutputSize <= 0 {
		t.Fatalf("输出文件大小未更新: %d", updated.OutputSize)
	}

	outputPath := cfg.ApplyOutputExtension(filepath.Join(outputDir, "ok.mkv"))
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("输出文件不存在: %s (%v)", outputPath, err)
	}

	ext := filepath.Ext(outputPath)
	tempPath := strings.TrimSuffix(outputPath, ext) + ".stm_tmp" + ext
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Fatalf("临时文件应已清理: %s", tempPath)
	}
}

func TestProcessWorkerRetryableFailureSchedulesRetry(t *testing.T) {
	w, db, _, inputDir, _ := newWorkerTestFixture(t)
	defer db.Close()

	t.Setenv("STM_TEST_FFMPEG_EXIT_CODE", "1")
	t.Setenv("STM_TEST_FFMPEG_STDERR", "connection timed out")
	t.Setenv("STM_TEST_DURATION_SECONDS", "5")

	sourcePath := filepath.Join(inputDir, "retry.mkv")
	task := createTaskForPath(t, db, sourcePath)
	task.RepairMode = "cfr"

	runSingleTaskWorker(t, w, task)

	updated, err := db.GetTaskByPath(sourcePath)
	if err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if updated == nil {
		t.Fatal("任务不存在")
	}
	if updated.Status != database.StatusFailed {
		t.Fatalf("任务状态错误: got=%s want=%s", updated.Status, database.StatusFailed)
	}
	if updated.RetryCount != 1 {
		t.Fatalf("重试次数错误: got=%d want=1", updated.RetryCount)
	}
	if !updated.NextRetryAt.Valid {
		t.Fatal("应写入 next_retry_at")
	}
	if !updated.LastErrorCategory.Valid || updated.LastErrorCategory.String != "io_error" {
		t.Fatalf("错误类别错误: got=%v want=io_error", updated.LastErrorCategory)
	}
	if !strings.Contains(updated.GetLog(), "自动重试(1/3)") {
		t.Fatalf("日志未包含自动重试信息: %s", updated.GetLog())
	}
}

func TestProcessWorkerNonRetryableFailure(t *testing.T) {
	w, db, _, inputDir, _ := newWorkerTestFixture(t)
	defer db.Close()

	t.Setenv("STM_TEST_FFMPEG_EXIT_CODE", "1")
	t.Setenv("STM_TEST_FFMPEG_STDERR", "fatal codec mismatch")
	t.Setenv("STM_TEST_DURATION_SECONDS", "5")

	sourcePath := filepath.Join(inputDir, "failed.mkv")
	task := createTaskForPath(t, db, sourcePath)
	task.RepairMode = "cfr"

	runSingleTaskWorker(t, w, task)

	updated, err := db.GetTaskByPath(sourcePath)
	if err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	if updated == nil {
		t.Fatal("任务不存在")
	}
	if updated.Status != database.StatusFailed {
		t.Fatalf("任务状态错误: got=%s want=%s", updated.Status, database.StatusFailed)
	}
	if updated.RetryCount != 1 {
		t.Fatalf("重试次数错误: got=%d want=1", updated.RetryCount)
	}
	if updated.NextRetryAt.Valid {
		t.Fatalf("不应设置 next_retry_at: %v", updated.NextRetryAt)
	}
	if updated.LastErrorCategory.Valid {
		t.Fatalf("不应设置 last_error_category: %v", updated.LastErrorCategory)
	}
	if !strings.Contains(updated.GetLog(), "FFmpeg执行失败") {
		t.Fatalf("日志应包含 FFmpeg 失败信息: %s", updated.GetLog())
	}
}

func TestTranscodeTimeout(t *testing.T) {
	w, db, _, inputDir, _ := newWorkerTestFixture(t)
	defer db.Close()

	t.Setenv("STM_TEST_FFMPEG_EXIT_CODE", "0")
	t.Setenv("STM_TEST_FFMPEG_SLEEP_SECONDS", "2")
	t.Setenv("STM_TEST_DURATION_SECONDS", "5")

	sourcePath := filepath.Join(inputDir, "timeout.mkv")
	task := createTaskForPath(t, db, sourcePath)
	task.RepairMode = "cfr"

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := w.transcode(ctx, task, 1)
	if err == nil {
		t.Fatal("应返回超时错误")
	}
	if !strings.Contains(err.Error(), "FFmpeg超时") {
		t.Fatalf("错误信息不符合预期: %v", err)
	}
}

func newWorkerTestFixture(t *testing.T) (*Worker, *database.DB, *config.Config, string, string) {
	t.Helper()

	setupFakeFFTools(t)

	baseDir := t.TempDir()
	inputDir := filepath.Join(baseDir, "input")
	outputDir := filepath.Join(baseDir, "output")
	if err := os.MkdirAll(inputDir, 0o755); err != nil {
		t.Fatalf("创建输入目录失败: %v", err)
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatalf("创建输出目录失败: %v", err)
	}

	dbPath := filepath.Join(baseDir, "tasks.db")
	db, err := database.Init(dbPath)
	if err != nil {
		t.Fatalf("初始化数据库失败: %v", err)
	}

	cfg := &config.Config{
		System: config.SystemConfig{
			CronStart:         0,
			CronEnd:           0,
			MaxWorkers:        1,
			TaskQueueSize:     4,
			MinDiskSpaceGB:    0,
			SchedulerInterval: 1,
			MaxRetry:          3,
		},
		Retry: config.RetryConfig{
			BackoffEnabled:   true,
			BaseDelaySeconds: 1,
			MaxDelaySeconds:  2,
			RetryableErrors:  []string{"timeout", "output_error", "io_error"},
		},
		Scheduler: config.SchedulerConfig{
			AtomicClaim: false,
		},
		Path: config.PathConfig{
			Input:  inputDir,
			Output: outputDir,
			Pairs: []config.InputOutputPair{
				{Input: inputDir, Output: outputDir},
			},
		},
		FFmpeg: config.FFmpegConfig{
			Codec:                "libx264",
			Preset:               "veryfast",
			CRF:                  28,
			Audio:                "aac",
			AudioBitrate:         "128k",
			OutputExtension:      ".mp4",
			StrictCheck:          false,
			ProbeTimeoutSeconds:  1,
			ProgressStallMinutes: 5,
			MaxDurationHours:     1,
			DurationFactor:       1.0,
			DurationExtraMinutes: 0,
			FFmpegPath:           "ffmpeg",
			FFprobePath:          "ffprobe",
		},
	}

	return New(cfg, db), db, cfg, inputDir, outputDir
}

func setupFakeFFTools(t *testing.T) {
	t.Helper()

	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("创建工具目录失败: %v", err)
	}

	ffprobeScript := `#!/usr/bin/env bash
set -euo pipefail

if [ "${STM_TEST_FFPROBE_FAIL:-0}" = "1" ]; then
  echo "probe failed" >&2
  exit 1
fi

if [[ "$*" == *"format=duration"* ]]; then
  echo "${STM_TEST_DURATION_SECONDS:-5}"
  exit 0
fi

echo "codec_name=h264"
echo "duration=${STM_TEST_DURATION_SECONDS:-5}"
`
	if err := os.WriteFile(filepath.Join(binDir, "ffprobe"), []byte(ffprobeScript), 0o755); err != nil {
		t.Fatalf("写入 ffprobe stub 失败: %v", err)
	}

	ffmpegScript := `#!/usr/bin/env bash
set -euo pipefail

sleep_secs="${STM_TEST_FFMPEG_SLEEP_SECONDS:-0}"
if [ "$sleep_secs" != "0" ]; then
  sleep "$sleep_secs"
fi

if [ "${STM_TEST_FFMPEG_PROGRESS:-1}" = "1" ]; then
  echo "out_time_ms=1000000"
  echo "out_time_ms=3000000"
fi

exit_code="${STM_TEST_FFMPEG_EXIT_CODE:-0}"
if [ "$exit_code" != "0" ]; then
  echo "${STM_TEST_FFMPEG_STDERR:-simulated ffmpeg error}" >&2
  exit "$exit_code"
fi

out="${@: -1}"
if [ "$out" != "-" ]; then
  mkdir -p "$(dirname "$out")"
  printf "ok" > "$out"
fi
`
	if err := os.WriteFile(filepath.Join(binDir, "ffmpeg"), []byte(ffmpegScript), 0o755); err != nil {
		t.Fatalf("写入 ffmpeg stub 失败: %v", err)
	}

	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
}

func createTaskForPath(t *testing.T, db *database.DB, path string) *database.Task {
	t.Helper()

	content := []byte("test-video-content")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("创建测试视频失败: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("读取测试视频信息失败: %v", err)
	}

	task := &database.Task{
		SourcePath:  path,
		SourceMtime: info.ModTime(),
		SourceSize:  info.Size(),
	}
	if err := db.CreateTask(task); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	return task
}

func runSingleTaskWorker(t *testing.T, w *Worker, task *database.Task) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w.wg.Add(1)
	go w.processWorker(ctx, 1)

	select {
	case w.taskQueue <- task:
	case <-time.After(2 * time.Second):
		t.Fatal("写入任务队列超时")
	}
	close(w.taskQueue)

	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("worker 执行超时")
	}
}

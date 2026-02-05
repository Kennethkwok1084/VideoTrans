package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	// 创建临时配置文件
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	configContent := `
system:
  cron_start: 2
  cron_end: 8
  max_workers: 3
  scan_interval: 10

path:
  input: "/input"
  output: "/output"
  trash: ".stm_trash"
  database: "/data/tasks.db"

ffmpeg:
  codec: "libx264"
  preset: "veryslow"
  crf: 28
  audio: "aac"
  audio_bitrate: "128k"
  extensions: [".mp4", ".mkv"]
  exclude_patterns:
    - "SYNOPHOTO_*"

cleaning:
  soft_delete_days: 7
  hard_delete_days: 30

log:
  level: "info"
  file: "/data/stm.log"
`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("创建配置文件失败: %v", err)
	}

	// 加载配置
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}

	// 验证配置
	if cfg.System.CronStart != 2 {
		t.Errorf("CronStart 错误: 期望 2, 实际 %d", cfg.System.CronStart)
	}

	if cfg.System.MaxWorkers != 3 {
		t.Errorf("MaxWorkers 错误: 期望 3, 实际 %d", cfg.System.MaxWorkers)
	}

	if cfg.Path.Input != "/input" {
		t.Errorf("Input 路径错误: %s", cfg.Path.Input)
	}

	if cfg.FFmpeg.CRF != 28 {
		t.Errorf("CRF 错误: 期望 28, 实际 %d", cfg.FFmpeg.CRF)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{
			name: "有效配置",
			config: Config{
				System: SystemConfig{
					CronStart:    0,
					CronEnd:      0,
					MaxWorkers:   3,
					ScanInterval: 10,
				},
				Path: PathConfig{
					Input:    "/input",
					Output:   "/output",
					Database: "/data/db",
				},
				FFmpeg: FFmpegConfig{
					CRF: 28,
				},
				Cleaning: CleaningConfig{
					SoftDeleteDays: 7,
					HardDeleteDays: 30,
				},
			},
			wantErr: false,
		},
		{
			name: "无效时间窗口",
			config: Config{
				System: SystemConfig{
					CronStart:  25, // 无效
					CronEnd:    8,
					MaxWorkers: 3,
				},
				Path: PathConfig{
					Input:    "/input",
					Output:   "/output",
					Database: "/data/db",
				},
				FFmpeg: FFmpegConfig{CRF: 28},
				Cleaning: CleaningConfig{
					SoftDeleteDays: 7,
					HardDeleteDays: 30,
				},
			},
			wantErr: true,
		},
		{
			name: "无效并发数",
			config: Config{
				System: SystemConfig{
					CronStart:  2,
					CronEnd:    8,
					MaxWorkers: 0, // 无效
				},
				Path: PathConfig{
					Input:    "/input",
					Output:   "/output",
					Database: "/data/db",
				},
				FFmpeg: FFmpegConfig{CRF: 28},
				Cleaning: CleaningConfig{
					SoftDeleteDays: 7,
					HardDeleteDays: 30,
				},
			},
			wantErr: true,
		},
		{
			name: "无效清理天数",
			config: Config{
				System: SystemConfig{
					CronStart:  2,
					CronEnd:    8,
					MaxWorkers: 3,
				},
				Path: PathConfig{
					Input:    "/input",
					Output:   "/output",
					Database: "/data/db",
				},
				FFmpeg: FFmpegConfig{CRF: 28},
				Cleaning: CleaningConfig{
					SoftDeleteDays: 30,
					HardDeleteDays: 7, // 小于软删除天数
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestIsVideoFile(t *testing.T) {
	cfg := &Config{
		FFmpeg: FFmpegConfig{
			Extensions: []string{".mp4", ".mkv", ".avi"},
		},
	}

	tests := []struct {
		filename string
		want     bool
	}{
		{"video.mp4", true},
		{"video.mkv", true},
		{"video.avi", true},
		{"video.txt", false},
		{"video.jpg", false},
		{"video", false},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			if got := cfg.IsVideoFile(tt.filename); got != tt.want {
				t.Errorf("IsVideoFile(%s) = %v, want %v", tt.filename, got, tt.want)
			}
		})
	}
}

func TestGetTrashPath(t *testing.T) {
	cfg := &Config{
		Path: PathConfig{
			Pairs: []InputOutputPair{{
				Input:  "/input",
				Output: "/output",
			}},
			Trash: ".stm_trash",
		},
	}

	expected := filepath.Join("/input", ".stm_trash")
	if got := cfg.GetTrashPath(); got != expected {
		t.Errorf("GetTrashPath() = %s, want %s", got, expected)
	}
}

func TestEnvOverrides(t *testing.T) {
	// 设置环境变量
	os.Setenv("STM_MAX_WORKERS", "5")
	os.Setenv("STM_INPUT_PATH", "/custom/input")
	defer os.Unsetenv("STM_MAX_WORKERS")
	defer os.Unsetenv("STM_INPUT_PATH")

	cfg := &Config{
		System: SystemConfig{MaxWorkers: 3},
		Path:   PathConfig{Input: "/default/input", Pairs: []InputOutputPair{{Input: "/default/input", Output: "/default/output"}}},
	}

	cfg.applyEnvOverrides()

	if cfg.System.MaxWorkers != 5 {
		t.Errorf("环境变量覆盖失败: MaxWorkers = %d, want 5", cfg.System.MaxWorkers)
	}

	if cfg.Path.Input != "/custom/input" {
		t.Errorf("环境变量覆盖失败: Input = %s, want /custom/input", cfg.Path.Input)
	}

	if cfg.Path.Pairs[0].Input != "/default/input" {
		t.Errorf("Pairs 不应被环境变量覆盖: %s", cfg.Path.Pairs[0].Input)
	}
}

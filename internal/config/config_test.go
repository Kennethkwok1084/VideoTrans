package config

import (
	"fmt"
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

	for i := range tests {
		tt := &tests[i]
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

// TestEncoderProfiles 测试硬件编码配置
func TestEncoderProfiles(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "Docker模式自动降级硬件profile",
			config: Config{
				RunMode: "docker",
				System: SystemConfig{
					CronStart:  0,
					CronEnd:    0,
					MaxWorkers: 2,
				},
				Path: PathConfig{
					Input:    "/input",
					Output:   "/output",
					Database: "/data/db",
				},
				FFmpeg: FFmpegConfig{
					Codec:  "libx264",
					Preset: "medium",
					CRF:    23,
				},
				EncoderProfiles: []EncoderProfile{
					{
						Name:  "nvidia_profile",
						Type:  EncoderTypeNVIDIA,
						Codec: "h264_nvenc",
						Params: map[string]string{
							"preset": "p7",
							"cq":     "23",
						},
					},
				},
				WorkerMapping: []string{"nvidia_profile"},
				Cleaning: CleaningConfig{
					SoftDeleteDays: 7,
					HardDeleteDays: 30,
				},
			},
			wantErr: false,
		},
		{
			name: "Systemd模式允许硬件profile",
			config: Config{
				RunMode: "systemd",
				System: SystemConfig{
					CronStart:  0,
					CronEnd:    0,
					MaxWorkers: 2,
				},
				Path: PathConfig{
					Input:    "/input",
					Output:   "/output",
					Database: "/data/db",
				},
				FFmpeg: FFmpegConfig{
					Codec:  "libx264",
					Preset: "medium",
					CRF:    23,
				},
				EncoderProfiles: []EncoderProfile{
					{
						Name:  "nvidia_profile",
						Type:  EncoderTypeNVIDIA,
						Codec: "h264_nvenc",
						Params: map[string]string{
							"preset": "p7",
							"cq":     "23",
						},
					},
				},
				WorkerMapping: []string{"nvidia_profile"},
				Cleaning: CleaningConfig{
					SoftDeleteDays: 7,
					HardDeleteDays: 30,
				},
			},
			wantErr: false,
		},
		{
			name: "Worker mapping引用不存在的profile",
			config: Config{
				RunMode: "docker",
				System: SystemConfig{
					CronStart:  0,
					CronEnd:    0,
					MaxWorkers: 2,
				},
				Path: PathConfig{
					Input:    "/input",
					Output:   "/output",
					Database: "/data/db",
				},
				FFmpeg: FFmpegConfig{
					Codec:  "libx264",
					Preset: "medium",
					CRF:    23,
				},
				EncoderProfiles: []EncoderProfile{
					{
						Name:  "cpu_profile",
						Type:  EncoderTypeCPU,
						Codec: "libx264",
						Params: map[string]string{
							"preset": "medium",
							"crf":    "23",
						},
					},
				},
				WorkerMapping: []string{"nonexistent_profile"},
				Cleaning: CleaningConfig{
					SoftDeleteDays: 7,
					HardDeleteDays: 30,
				},
			},
			wantErr: true,
			errMsg:  "不存在",
		},
		{
			name: "空codec应该报错",
			config: Config{
				RunMode: "docker",
				System: SystemConfig{
					CronStart:  0,
					CronEnd:    0,
					MaxWorkers: 2,
				},
				Path: PathConfig{
					Input:    "/input",
					Output:   "/output",
					Database: "/data/db",
				},
				FFmpeg: FFmpegConfig{
					Codec:  "libx264",
					Preset: "medium",
					CRF:    23,
				},
				EncoderProfiles: []EncoderProfile{
					{
						Name:   "bad_profile",
						Type:   EncoderTypeCPU,
						Codec:  "", // 空codec
						Params: map[string]string{},
					},
				},
				WorkerMapping: []string{"bad_profile"},
				Cleaning: CleaningConfig{
					SoftDeleteDays: 7,
					HardDeleteDays: 30,
				},
			},
			wantErr: true,
			errMsg:  "codec 不能为空",
		},
		{
			name: "Docker降级时替换不兼容的NVENC preset",
			config: Config{
				RunMode: "docker",
				System: SystemConfig{
					CronStart:  0,
					CronEnd:    0,
					MaxWorkers: 2,
				},
				Path: PathConfig{
					Input:    "/input",
					Output:   "/output",
					Database: "/data/db",
				},
				FFmpeg: FFmpegConfig{
					Codec:  "libx264",
					Preset: "medium",
					CRF:    23,
				},
				EncoderProfiles: []EncoderProfile{
					{
						Name:  "nvidia_p7",
						Type:  EncoderTypeNVIDIA,
						Codec: "h264_nvenc",
						Params: map[string]string{
							"preset": "p7", // NVENC 特定的 preset，应该被替换
							"cq":     "23",
						},
					},
				},
				WorkerMapping: []string{"nvidia_p7"},
				Cleaning: CleaningConfig{
					SoftDeleteDays: 7,
					HardDeleteDays: 30,
				},
			},
			wantErr: false,
		},
		{
			name: "Docker模式应降级type=cpu但codec为硬件编码器的profile",
			config: Config{
				RunMode: "docker",
				System: SystemConfig{
					CronStart:  0,
					CronEnd:    0,
					MaxWorkers: 2,
				},
				Path: PathConfig{
					Input:    "/input",
					Output:   "/output",
					Database: "/data/db",
				},
				FFmpeg: FFmpegConfig{
					Codec:  "libx264",
					Preset: "medium",
					CRF:    23,
				},
				EncoderProfiles: []EncoderProfile{
					{
						Name:  "cpu_type_but_nvenc_codec",
						Type:  EncoderTypeCPU,
						Codec: "h264_nvenc",
						Params: map[string]string{
							"cq": "21",
						},
					},
				},
				WorkerMapping: []string{"cpu_type_but_nvenc_codec"},
				Cleaning: CleaningConfig{
					SoftDeleteDays: 7,
					HardDeleteDays: 30,
				},
			},
			wantErr: false,
		},
		{
			name: "Systemd模式下type与codec不匹配应报错",
			config: Config{
				RunMode: "systemd",
				System: SystemConfig{
					CronStart:  0,
					CronEnd:    0,
					MaxWorkers: 2,
				},
				Path: PathConfig{
					Input:    "/input",
					Output:   "/output",
					Database: "/data/db",
				},
				FFmpeg: FFmpegConfig{
					Codec:  "libx264",
					Preset: "medium",
					CRF:    23,
				},
				EncoderProfiles: []EncoderProfile{
					{
						Name:  "bad_mismatch",
						Type:  EncoderTypeNVIDIA,
						Codec: "libx264",
					},
				},
				WorkerMapping: []string{"bad_mismatch"},
				Cleaning: CleaningConfig{
					SoftDeleteDays: 7,
					HardDeleteDays: 30,
				},
			},
			wantErr: true,
			errMsg:  "不匹配",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errMsg != "" {
				if err == nil || !contains(err.Error(), tt.errMsg) {
					t.Errorf("期望错误信息包含 '%s'，实际错误: %v", tt.errMsg, err)
				}
			}

			// 验证 Docker 模式降级逻辑
			if !tt.wantErr && tt.config.RunMode == "docker" {
				for _, profile := range tt.config.EncoderProfiles {
					if profile.Type != EncoderTypeCPU {
						t.Errorf("Docker模式下所有profile应该被降级为CPU，但 %s 仍是 %s", profile.Name, profile.Type)
					}
					if profile.Codec != "libx264" && profile.Codec != "libx265" {
						t.Errorf("Docker模式下应使用CPU编码器，但 %s 使用 %s", profile.Name, profile.Codec)
					}
					// 验证硬件特定参数被移除
					if _, hasCQ := profile.Params["cq"]; hasCQ {
						t.Errorf("Docker模式下不应有硬件参数 'cq'，但 %s 仍有该参数", profile.Name)
					}
					if _, hasGQ := profile.Params["global_quality"]; hasGQ {
						t.Errorf("Docker模式下不应有硬件参数 'global_quality'，但 %s 仍有该参数", profile.Name)
					}
					// 验证 preset 被替换为有效的 x264 preset（不能是 NVENC 的 p1-p7）
					if preset, hasPreset := profile.Params["preset"]; hasPreset {
						// NVENC presets: p1-p7
						if len(preset) == 2 && preset[0] == 'p' && preset[1] >= '1' && preset[1] <= '7' {
							t.Errorf("Docker模式下 preset 应该被替换为有效的x264值，但 %s 仍使用 NVENC preset: %s", profile.Name, preset)
						}
					}
				}
			}
		})
	}
}

// TestGetEncoderProfile 测试获取编码器profile
func TestGetEncoderProfile(t *testing.T) {
	cfg := &Config{
		RunMode: "systemd",
		System: SystemConfig{
			CronStart:  0,
			CronEnd:    0,
			MaxWorkers: 3,
		},
		Path: PathConfig{
			Input:    "/input",
			Output:   "/output",
			Database: "/data/db",
		},
		FFmpeg: FFmpegConfig{
			Codec:  "libx264",
			Preset: "medium",
			CRF:    23,
		},
		EncoderProfiles: []EncoderProfile{
			{Name: "nvidia_high", Type: EncoderTypeNVIDIA, Codec: "h264_nvenc"},
			{Name: "intel_mid", Type: EncoderTypeIntel, Codec: "h264_qsv"},
			{Name: "cpu_low", Type: EncoderTypeCPU, Codec: "libx264"},
		},
		WorkerMapping: []string{"nvidia_high", "intel_mid", "cpu_low"},
		Cleaning: CleaningConfig{
			SoftDeleteDays: 7,
			HardDeleteDays: 30,
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("配置验证失败: %v", err)
	}

	tests := []struct {
		workerID     int
		wantName     string
		wantType     EncoderType
		wantNotFound bool
	}{
		{workerID: 1, wantName: "nvidia_high", wantType: EncoderTypeNVIDIA}, // worker-1 -> 索引0
		{workerID: 2, wantName: "intel_mid", wantType: EncoderTypeIntel},    // worker-2 -> 索引1
		{workerID: 3, wantName: "cpu_low", wantType: EncoderTypeCPU},        // worker-3 -> 索引2
		{workerID: 4, wantName: "nvidia_high", wantType: EncoderTypeNVIDIA}, // worker-4 -> 索引0（循环）
		{workerID: 5, wantName: "intel_mid", wantType: EncoderTypeIntel},    // worker-5 -> 索引1（循环）
		{workerID: 6, wantName: "cpu_low", wantType: EncoderTypeCPU},        // worker-6 -> 索引2（循环）
		{workerID: 0, wantNotFound: true},                                   // 非法workerID: 0
		{workerID: -1, wantNotFound: true},                                  // 非法workerID: 负数
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("worker-%d", tt.workerID), func(t *testing.T) {
			profile, err := cfg.GetEncoderProfile(tt.workerID)
			if tt.wantNotFound {
				if err == nil {
					t.Errorf("期望返回错误，但成功返回了 profile: %s", profile.Name)
				}
				return
			}

			if err != nil {
				t.Fatalf("GetEncoderProfile(%d) 返回错误: %v", tt.workerID, err)
			}

			if profile.Name != tt.wantName {
				t.Errorf("worker-%d 期望使用 %s，实际使用 %s", tt.workerID, tt.wantName, profile.Name)
			}

			if profile.Type != tt.wantType {
				t.Errorf("profile %s 期望类型 %s，实际类型 %s", profile.Name, tt.wantType, profile.Type)
			}
		})
	}
}

// TestGetCPUFallbackProfile 测试获取CPU回退profile
func TestGetCPUFallbackProfile(t *testing.T) {
	tests := []struct {
		name       string
		config     Config
		wantCPU    bool
		wantPreset string
	}{
		{
			name: "有CPU profile时返回它",
			config: Config{
				EncoderProfiles: []EncoderProfile{
					{Name: "nvidia_high", Type: EncoderTypeNVIDIA, Codec: "h264_nvenc"},
					{Name: "cpu_baseline", Type: EncoderTypeCPU, Codec: "libx264"},
				},
			},
			wantCPU:    true,
			wantPreset: "",
		},
		{
			name: "没有CPU profile时创建默认的",
			config: Config{
				FFmpeg: FFmpegConfig{
					Codec:  "libx264",
					Preset: "medium",
					CRF:    23,
				},
				EncoderProfiles: []EncoderProfile{
					{Name: "nvidia_high", Type: EncoderTypeNVIDIA, Codec: "h264_nvenc"},
				},
			},
			wantCPU:    true,
			wantPreset: "medium",
		},
		{
			name: "硬件preset应回退为medium",
			config: Config{
				FFmpeg: FFmpegConfig{
					Codec:  "libx264",
					Preset: "p7",
					CRF:    23,
				},
				EncoderProfiles: []EncoderProfile{
					{Name: "intel_high", Type: EncoderTypeIntel, Codec: "h264_qsv"},
				},
			},
			wantCPU:    true,
			wantPreset: "medium",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile := tt.config.GetCPUFallbackProfile()
			if profile == nil {
				t.Fatal("期望返回CPU profile，但返回了 nil")
			}

			if tt.wantCPU && profile.Type != EncoderTypeCPU {
				t.Errorf("期望CPU类型，实际: %s", profile.Type)
			}
			if tt.wantPreset != "" {
				gotPreset := profile.Params["preset"]
				if gotPreset != tt.wantPreset {
					t.Errorf("期望 preset=%s，实际=%s", tt.wantPreset, gotPreset)
				}
			}
		})
	}
}

// TestMinDiskSpaceDefault 测试 min_disk_space_gb 的默认值行为
func TestMinDiskSpaceDefault(t *testing.T) {
	tests := []struct {
		name           string
		inputValue     int
		wantAfterValid int
	}{
		{
			name:           "未设置时应默认为5GB",
			inputValue:     0,
			wantAfterValid: 5,
		},
		{
			name:           "显式设置为10GB应保持",
			inputValue:     10,
			wantAfterValid: 10,
		},
		{
			name:           "负数应返回错误",
			inputValue:     -1,
			wantAfterValid: -1, // 不会运行到这里，验证会失败
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				System: SystemConfig{
					CronStart:      0,
					CronEnd:        0,
					MaxWorkers:     2,
					MinDiskSpaceGB: tt.inputValue,
				},
				Path: PathConfig{
					Input:    "/input",
					Output:   "/output",
					Database: "/data/db",
				},
				FFmpeg: FFmpegConfig{
					Codec:  "libx264",
					Preset: "medium",
					CRF:    23,
				},
				Cleaning: CleaningConfig{
					SoftDeleteDays: 7,
					HardDeleteDays: 30,
				},
			}

			err := cfg.Validate()

			if tt.inputValue < 0 {
				// 负数应该返回错误
				if err == nil {
					t.Errorf("期望返回错误，但验证成功")
				}
				if err != nil && !contains(err.Error(), "不能为负数") {
					t.Errorf("期望错误信息包含'不能为负数'，实际: %v", err)
				}
				return
			}

			if err != nil {
				t.Fatalf("验证失败: %v", err)
			}

			if cfg.System.MinDiskSpaceGB != tt.wantAfterValid {
				t.Errorf("验证后 MinDiskSpaceGB = %d, 期望 %d", cfg.System.MinDiskSpaceGB, tt.wantAfterValid)
			}
		})
	}
}

func TestMinDiskSpaceFromYAML(t *testing.T) {
	tests := []struct {
		name       string
		minDiskYML string
		wantGB     int
	}{
		{
			name:       "缺失时使用默认5GB",
			minDiskYML: "",
			wantGB:     5,
		},
		{
			name:       "显式设置0时保持0",
			minDiskYML: "  min_disk_space_gb: 0\n",
			wantGB:     0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "config.yaml")

			content := "system:\n" +
				"  cron_start: 0\n" +
				"  cron_end: 0\n" +
				"  max_workers: 2\n" +
				tt.minDiskYML +
				"path:\n" +
				"  input: \"/input\"\n" +
				"  output: \"/output\"\n" +
				"  database: \"/data/db\"\n" +
				"ffmpeg:\n" +
				"  codec: \"libx264\"\n" +
				"  preset: \"medium\"\n" +
				"  crf: 23\n" +
				"cleaning:\n" +
				"  soft_delete_days: 7\n" +
				"  hard_delete_days: 30\n"

			if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
				t.Fatalf("创建配置文件失败: %v", err)
			}

			cfg, err := Load(configPath)
			if err != nil {
				t.Fatalf("加载配置失败: %v", err)
			}

			if cfg.System.MinDiskSpaceGB != tt.wantGB {
				t.Fatalf("MinDiskSpaceGB=%d, want=%d", cfg.System.MinDiskSpaceGB, tt.wantGB)
			}
		})
	}
}

func TestSavePreservesWebConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	cfg := &Config{
		ConfigPath: configPath,
		System: SystemConfig{
			CronStart:      0,
			CronEnd:        0,
			MaxWorkers:     2,
			MinDiskSpaceGB: 5,
		},
		Web: WebConfig{
			APIKey: "secret-key",
		},
		Path: PathConfig{
			Input:    "/input",
			Output:   "/output",
			Database: "/data/db",
		},
		FFmpeg: FFmpegConfig{
			Codec:  "libx264",
			Preset: "medium",
			CRF:    23,
		},
		Cleaning: CleaningConfig{
			SoftDeleteDays: 7,
			HardDeleteDays: 30,
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate 失败: %v", err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}

	loaded, err := Load(configPath)
	if err != nil {
		t.Fatalf("重新加载失败: %v", err)
	}
	if loaded.Web.APIKey != "secret-key" {
		t.Fatalf("web.api_key 未保留: got=%q", loaded.Web.APIKey)
	}
}

// 辅助函数
func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) >= len(substr) && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

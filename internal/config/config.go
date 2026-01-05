package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config 全局配置结构
type Config struct {
	System   SystemConfig   `yaml:"system"`
	Path     PathConfig     `yaml:"path"`
	FFmpeg   FFmpegConfig   `yaml:"ffmpeg"`
	Cleaning CleaningConfig `yaml:"cleaning"`
	Log      LogConfig      `yaml:"log"`
}

// SystemConfig 系统配置
type SystemConfig struct {
	CronStart         int `yaml:"cron_start"`         // 工作开始时间（小时）
	CronEnd           int `yaml:"cron_end"`           // 工作结束时间（小时）
	MaxWorkers        int `yaml:"max_workers"`        // 最大并发数
	ScanInterval      int `yaml:"scan_interval"`      // 扫描间隔（分钟）
	SchedulerInterval int `yaml:"scheduler_interval"` // 调度器检查间隔（秒）
	TaskQueueSize     int `yaml:"task_queue_size"`    // 任务队列容量
	MinDiskSpaceGB    int `yaml:"min_disk_space_gb"`  // 最小磁盘空间要求（GB）
}

// PathConfig 路径配置
type PathConfig struct {
	Input    string   `yaml:"input"`    // 默认输入目录（保持兼容性）
	Inputs   []string `yaml:"inputs"`   // 多个输入目录（新增）
	Output   string   `yaml:"output"`   // 输出目录
	Trash    string   `yaml:"trash"`    // 垃圾桶目录名
	Database string   `yaml:"database"` // 数据库文件路径
}

// FFmpegConfig FFmpeg配置
type FFmpegConfig struct {
	Codec           string   `yaml:"codec"`
	Preset          string   `yaml:"preset"`
	CRF             int      `yaml:"crf"`
	Audio           string   `yaml:"audio"`
	AudioBitrate    string   `yaml:"audio_bitrate"`
	Extensions      []string `yaml:"extensions"`
	ExcludePatterns []string `yaml:"exclude_patterns"`
}

// CleaningConfig 清理配置
type CleaningConfig struct {
	SoftDeleteDays int `yaml:"soft_delete_days"` // 移入垃圾桶天数
	HardDeleteDays int `yaml:"hard_delete_days"` // 彻底删除天数
}

// LogConfig 日志配置
type LogConfig struct {
	Level string `yaml:"level"`
	File  string `yaml:"file"`
}

// Load 从文件加载配置
func Load(configPath string) (*Config, error) {
	// 如果没有指定配置文件，使用默认路径
	if configPath == "" {
		configPath = "configs/config.yaml"
	}

	// 读取配置文件
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	// 解析YAML
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	// 验证配置
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("配置验证失败: %w", err)
	}

	// 处理环境变量覆盖
	cfg.applyEnvOverrides()

	return &cfg, nil
}

// Validate 验证配置的合法性
func (c *Config) Validate() error {
	// 验证时间窗口
	if c.System.CronStart < 0 || c.System.CronStart > 23 {
		return fmt.Errorf("cron_start 必须在 0-23 之间")
	}
	if c.System.CronEnd < 0 || c.System.CronEnd > 23 {
		return fmt.Errorf("cron_end 必须在 0-23 之间")
	}

	// 验证并发数
	if c.System.MaxWorkers < 1 || c.System.MaxWorkers > 10 {
		return fmt.Errorf("max_workers 必须在 1-10 之间")
	}

	// 设置默认值
	if c.System.SchedulerInterval == 0 {
		c.System.SchedulerInterval = 10 // 默认10秒
	}
	if c.System.TaskQueueSize == 0 {
		c.System.TaskQueueSize = 10 // 默认队列容量10
	}
	if c.System.MinDiskSpaceGB == 0 {
		c.System.MinDiskSpaceGB = 5 // 默认至少5GB空闲
	}

	// 验证路径
	if c.Path.Input == "" && len(c.Path.Inputs) == 0 {
		return fmt.Errorf("至少需要配置一个input路径")
	}

	// 兼容性处理：如果只配置了Input，将其添加到Inputs
	if c.Path.Input != "" && len(c.Path.Inputs) == 0 {
		c.Path.Inputs = []string{c.Path.Input}
	}

	if c.Path.Output == "" {
		return fmt.Errorf("output 路径不能为空")
	}
	if c.Path.Database == "" {
		return fmt.Errorf("database 路径不能为空")
	}

	// 验证FFmpeg参数
	if c.FFmpeg.CRF < 0 || c.FFmpeg.CRF > 51 {
		return fmt.Errorf("crf 必须在 0-51 之间")
	}

	// 验证清理天数
	if c.Cleaning.SoftDeleteDays < 0 {
		return fmt.Errorf("soft_delete_days 不能为负数")
	}
	if c.Cleaning.HardDeleteDays < c.Cleaning.SoftDeleteDays {
		return fmt.Errorf("hard_delete_days 必须大于等于 soft_delete_days")
	}

	return nil
}

// applyEnvOverrides 应用环境变量覆盖
func (c *Config) applyEnvOverrides() {
	if val := os.Getenv("STM_MAX_WORKERS"); val != "" {
		fmt.Sscanf(val, "%d", &c.System.MaxWorkers)
	}
	if val := os.Getenv("STM_INPUT_PATH"); val != "" {
		c.Path.Input = val
	}
	if val := os.Getenv("STM_OUTPUT_PATH"); val != "" {
		c.Path.Output = val
	}
}

// GetTrashPath 获取完整的垃圾桶路径
func (c *Config) GetTrashPath() string {
	return filepath.Join(c.Path.Input, c.Path.Trash)
}

// GetInputDirs 获取所有输入目录
func (c *Config) GetInputDirs() []string {
	if len(c.Path.Inputs) > 0 {
		return c.Path.Inputs
	}
	if c.Path.Input != "" {
		return []string{c.Path.Input}
	}
	return []string{}
}

// AddInputDir 添加输入目录
func (c *Config) AddInputDir(dir string) error {
	// 检查目录是否已存在
	for _, existing := range c.Path.Inputs {
		if existing == dir {
			return fmt.Errorf("目录已存在")
		}
	}

	// 检查目录是否存在
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return fmt.Errorf("目录不存在: %s", dir)
	}

	c.Path.Inputs = append(c.Path.Inputs, dir)
	return nil
}

// RemoveInputDir 删除输入目录
func (c *Config) RemoveInputDir(dir string) error {
	newInputs := []string{}
	found := false

	for _, existing := range c.Path.Inputs {
		if existing == dir {
			found = true
			continue
		}
		newInputs = append(newInputs, existing)
	}

	if !found {
		return fmt.Errorf("目录不存在")
	}

	if len(newInputs) == 0 {
		return fmt.Errorf("至少需要保留一个监控目录")
	}

	c.Path.Inputs = newInputs
	return nil
}

// IsVideoFile 检查文件是否为支持的视频格式
func (c *Config) IsVideoFile(filename string) bool {
	ext := filepath.Ext(filename)
	for _, validExt := range c.FFmpeg.Extensions {
		if ext == validExt {
			return true
		}
	}
	return false
}

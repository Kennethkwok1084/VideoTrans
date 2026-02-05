package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Config 全局配置结构
type Config struct {
	mu         sync.RWMutex   `yaml:"-"`
	ConfigPath string         `yaml:"-"`
	System     SystemConfig   `yaml:"system"`
	Path       PathConfig     `yaml:"path"`
	FFmpeg     FFmpegConfig   `yaml:"ffmpeg"`
	Cleaning   CleaningConfig `yaml:"cleaning"`
	Log        LogConfig      `yaml:"log"`
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
	Input    string            `yaml:"input"`    // 默认输入目录（保持兼容性）
	Inputs   []string          `yaml:"inputs"`   // 多个输入目录（已废弃，使用Pairs）
	Output   string            `yaml:"output"`   // 默认输出目录（保持兼容性）
	Pairs    []InputOutputPair `yaml:"pairs"`    // 输入输出目录配对
	Trash    string            `yaml:"trash"`    // 垃圾桶目录名
	Database string            `yaml:"database"` // 数据库文件路径
}

// InputOutputPair 输入输出目录配对
type InputOutputPair struct {
	Input  string `yaml:"input" json:"input"`
	Output string `yaml:"output" json:"output"`
}

// FFmpegConfig FFmpeg配置
type FFmpegConfig struct {
	Codec                 string   `yaml:"codec"`
	Preset                string   `yaml:"preset"`
	CRF                   int      `yaml:"crf"`
	Audio                 string   `yaml:"audio"`
	AudioBitrate          string   `yaml:"audio_bitrate"`
	OutputExtension       string   `yaml:"output_extension"`
	VerifyDecodeSeconds   int      `yaml:"verify_decode_seconds"`
	VerifyTailSeekSeconds int      `yaml:"verify_tail_seek_seconds"`
	DiscardCorrupt        bool     `yaml:"discard_corrupt"`
	CorruptStrategy       string   `yaml:"corrupt_strategy"`
	CorruptProbeSeconds   int      `yaml:"corrupt_probe_seconds"`
	CorruptErrorThreshold int      `yaml:"corrupt_error_threshold"`
	OutputFPS             int      `yaml:"output_fps"`
	Extensions            []string `yaml:"extensions"`
	ExcludePatterns       []string `yaml:"exclude_patterns"`
	StrictCheck           bool     `yaml:"strict_check"` // 是否启用严格文件检查（检测损坏文件）
	ProbeTimeoutSeconds   int      `yaml:"probe_timeout_seconds"`
	ProgressStallMinutes  int      `yaml:"progress_stall_minutes"`
	MaxDurationHours      int      `yaml:"max_duration_hours"`
	DurationFactor        float64  `yaml:"duration_factor"`
	DurationExtraMinutes  int      `yaml:"duration_extra_minutes"`
}

// CleaningConfig 清理配置
type CleaningConfig struct {
	SoftDeleteDays int    `yaml:"soft_delete_days"` // 移入垃圾桶天数
	HardDeleteDays int    `yaml:"hard_delete_days"` // 彻底删除天数
	Cron           string `yaml:"cron"`             // 清理任务 Cron 表达式
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
	cfg.ConfigPath = configPath

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
	c.mu.Lock()
	defer c.mu.Unlock()

	// 验证时间窗口
	if c.System.CronStart < 0 || c.System.CronStart > 23 {
		return fmt.Errorf("cron_start 必须在 0-23 之间")
	}
	if c.System.CronEnd < 0 || c.System.CronEnd > 23 {
		return fmt.Errorf("cron_end 必须在 0-23 之间")
	}

	if c.System.CronStart == c.System.CronEnd {
		// 0-0 表示全天运行
		c.System.CronStart = 0
		c.System.CronEnd = 0
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
	if c.Path.Input == "" && len(c.Path.Inputs) == 0 && len(c.Path.Pairs) == 0 {
		return fmt.Errorf("至少需要配置一个input路径")
	}

	// 兼容性处理：如果只配置了Input/Output，将其添加到Pairs
	if c.Path.Input != "" && c.Path.Output != "" && len(c.Path.Pairs) == 0 {
		c.Path.Pairs = []InputOutputPair{{
			Input:  c.Path.Input,
			Output: c.Path.Output,
		}}
	}

	// 兼容旧的Inputs配置
	if len(c.Path.Inputs) > 0 && c.Path.Output != "" && len(c.Path.Pairs) == 0 {
		for _, input := range c.Path.Inputs {
			c.Path.Pairs = append(c.Path.Pairs, InputOutputPair{
				Input:  input,
				Output: c.Path.Output,
			})
		}
	}

	// 兼容：如果只配置了Pairs，则填充Input/Output为首对
	if len(c.Path.Pairs) > 0 {
		if c.Path.Input == "" {
			c.Path.Input = c.Path.Pairs[0].Input
		}
		if c.Path.Output == "" {
			c.Path.Output = c.Path.Pairs[0].Output
		}
	}

	// 验证转换后的Pairs
	for i, pair := range c.Path.Pairs {
		if pair.Input == "" {
			return fmt.Errorf("第%d个配对的输入路径不能为空", i+1)
		}
		if pair.Output == "" {
			return fmt.Errorf("第%d个配对的输出路径不能为空", i+1)
		}
		if pair.Input == pair.Output {
			return fmt.Errorf("第%d个配对的输入和输出目录不能相同: %s", i+1, pair.Input)
		}
	}

	// 验证清理天数
	if c.Cleaning.SoftDeleteDays < 0 {
		return fmt.Errorf("soft_delete_days 不能为负数")
	}
	if c.Cleaning.HardDeleteDays < c.Cleaning.SoftDeleteDays {
		return fmt.Errorf("hard_delete_days 必须大于等于 soft_delete_days")
	}
	if strings.TrimSpace(c.Cleaning.Cron) == "" {
		c.Cleaning.Cron = "0 10 * * *" // 默认每天 10:00
	}

	// 设置 FFmpeg 默认值
	if !c.FFmpeg.StrictCheck {
		// 默认不启用（已废弃，现在默认启用）
		// 保持向后兼容，如果配置文件中未指定，默认为 true
	}

	if c.FFmpeg.ProbeTimeoutSeconds < 0 {
		return fmt.Errorf("probe_timeout_seconds 不能为负数")
	}
	if c.FFmpeg.VerifyDecodeSeconds < 0 {
		return fmt.Errorf("verify_decode_seconds 不能为负数")
	}
	if c.FFmpeg.VerifyTailSeekSeconds < 0 {
		return fmt.Errorf("verify_tail_seek_seconds 不能为负数")
	}
	if c.FFmpeg.CorruptProbeSeconds < 0 {
		return fmt.Errorf("corrupt_probe_seconds 不能为负数")
	}
	if c.FFmpeg.CorruptErrorThreshold < 0 {
		return fmt.Errorf("corrupt_error_threshold 不能为负数")
	}
	if c.FFmpeg.OutputFPS < 0 {
		return fmt.Errorf("output_fps 不能为负数")
	}
	if c.FFmpeg.ProgressStallMinutes < 0 {
		return fmt.Errorf("progress_stall_minutes 不能为负数")
	}
	if c.FFmpeg.MaxDurationHours < 0 {
		return fmt.Errorf("max_duration_hours 不能为负数")
	}
	if c.FFmpeg.DurationFactor < 0 {
		return fmt.Errorf("duration_factor 不能为负数")
	}
	if c.FFmpeg.DurationExtraMinutes < 0 {
		return fmt.Errorf("duration_extra_minutes 不能为负数")
	}

	if c.FFmpeg.ProbeTimeoutSeconds == 0 {
		c.FFmpeg.ProbeTimeoutSeconds = 30
	}
	if c.FFmpeg.VerifyDecodeSeconds == 0 {
		c.FFmpeg.VerifyDecodeSeconds = 2
	}
	if c.FFmpeg.CorruptProbeSeconds == 0 {
		c.FFmpeg.CorruptProbeSeconds = 30
	}
	if c.FFmpeg.CorruptErrorThreshold == 0 {
		c.FFmpeg.CorruptErrorThreshold = 5
	}
	if c.FFmpeg.OutputFPS == 0 {
		c.FFmpeg.OutputFPS = 30
	}
	if c.FFmpeg.CorruptStrategy == "" {
		c.FFmpeg.CorruptStrategy = "auto"
	}
	switch strings.ToLower(c.FFmpeg.CorruptStrategy) {
	case "auto", "discard", "cfr":
	default:
		return fmt.Errorf("corrupt_strategy 必须是 auto/discard/cfr")
	}
	if c.FFmpeg.ProgressStallMinutes == 0 {
		c.FFmpeg.ProgressStallMinutes = 10
	}
	if c.FFmpeg.MaxDurationHours == 0 {
		c.FFmpeg.MaxDurationHours = 2
	}
	if c.FFmpeg.DurationFactor == 0 {
		c.FFmpeg.DurationFactor = 2.0
	}
	if c.FFmpeg.DurationExtraMinutes == 0 {
		c.FFmpeg.DurationExtraMinutes = 15
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

// Save 将当前配置持久化到文件。
func (c *Config) Save() error {
	c.mu.RLock()
	configPath := c.ConfigPath
	snapshot := Config{
		System:   c.System,
		FFmpeg:   c.FFmpeg,
		Cleaning: c.Cleaning,
		Log:      c.Log,
		Path: PathConfig{
			Input:    c.Path.Input,
			Inputs:   append([]string(nil), c.Path.Inputs...),
			Output:   c.Path.Output,
			Pairs:    append([]InputOutputPair(nil), c.Path.Pairs...),
			Trash:    c.Path.Trash,
			Database: c.Path.Database,
		},
	}
	c.mu.RUnlock()

	if configPath == "" {
		return fmt.Errorf("配置文件路径为空，无法保存")
	}

	data, err := yaml.Marshal(&snapshot)
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}

	tmpPath := configPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("写入临时配置失败: %w", err)
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("替换配置文件失败: %w", err)
	}

	return nil
}

// GetTrashPath 获取完整的垃圾桶路径
func (c *Config) GetTrashPath() string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	base := c.Path.Input
	if base == "" && len(c.Path.Pairs) > 0 {
		base = c.Path.Pairs[0].Input
	}
	if base == "" {
		base = "."
	}
	return filepath.Join(base, c.Path.Trash)
}

// GetTrashRoots 获取所有输入目录的垃圾桶路径
func (c *Config) GetTrashRoots() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	seen := make(map[string]struct{})
	var roots []string

	addRoot := func(input string) {
		if input == "" {
			return
		}
		root := filepath.Join(input, c.Path.Trash)
		if _, ok := seen[root]; ok {
			return
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}

	if c.Path.Input != "" {
		addRoot(c.Path.Input)
	}
	for _, pair := range c.Path.Pairs {
		addRoot(pair.Input)
	}

	return roots
}

// GetInputDirs 获取所有输入目录
func (c *Config) GetInputDirs() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	seen := make(map[string]struct{})
	var dirs []string
	if c.Path.Input != "" {
		seen[c.Path.Input] = struct{}{}
		dirs = append(dirs, c.Path.Input)
	}
	for _, pair := range c.Path.Pairs {
		if _, ok := seen[pair.Input]; ok {
			continue
		}
		seen[pair.Input] = struct{}{}
		dirs = append(dirs, pair.Input)
	}
	return dirs
}

// GetPrimaryInputDir 获取首选输入目录。
func (c *Config) GetPrimaryInputDir() string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.Path.Input != "" {
		return c.Path.Input
	}
	if len(c.Path.Pairs) > 0 {
		return c.Path.Pairs[0].Input
	}
	return ""
}

// GetOutputDir 根据输入目录或文件路径获取对应的输出目录
// 参数可以是目录路径或文件路径，会自动查找匹配的输入目录配对
func (c *Config) GetOutputDir(path string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// 遍历所有配对，检查路径是否在某个输入目录下
	for _, pair := range c.Path.Pairs {
		// 精确匹配目录
		if pair.Input == path {
			return pair.Output
		}
		// 检查路径是否在该输入目录下
		if rel, err := filepath.Rel(pair.Input, path); err == nil && !strings.HasPrefix(rel, "..") {
			return pair.Output
		}
	}
	// 默认返回第一个输出目录
	if len(c.Path.Pairs) > 0 {
		return c.Path.Pairs[0].Output
	}
	return c.Path.Output
}

// GetPairs 获取所有输入输出配对
func (c *Config) GetPairs() []InputOutputPair {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return append([]InputOutputPair(nil), c.Path.Pairs...)
}

// AddInputOutputPair 添加输入输出目录配对
func (c *Config) AddInputOutputPair(inputDir, outputDir string) error {
	inputDir = filepath.Clean(strings.TrimSpace(inputDir))
	outputDir = filepath.Clean(strings.TrimSpace(outputDir))

	// 检查输入输出目录不能相同
	if inputDir == outputDir {
		return fmt.Errorf("输入目录和输出目录不能相同: %s", inputDir)
	}

	if inputDir == "" || outputDir == "" {
		return fmt.Errorf("输入输出目录不能为空")
	}

	// 检查输入目录是否已存在
	c.mu.RLock()
	for _, pair := range c.Path.Pairs {
		if pair.Input == inputDir {
			c.mu.RUnlock()
			return fmt.Errorf("输入目录已存在")
		}
	}
	c.mu.RUnlock()

	// 检查目录是否存在
	if _, err := os.Stat(inputDir); os.IsNotExist(err) {
		return fmt.Errorf("输入目录不存在: %s", inputDir)
	}
	if _, err := os.Stat(outputDir); os.IsNotExist(err) {
		return fmt.Errorf("输出目录不存在: %s", outputDir)
	}

	c.mu.Lock()
	for _, pair := range c.Path.Pairs {
		if pair.Input == inputDir {
			c.mu.Unlock()
			return fmt.Errorf("输入目录已存在")
		}
	}
	c.Path.Pairs = append(c.Path.Pairs, InputOutputPair{
		Input:  inputDir,
		Output: outputDir,
	})
	c.mu.Unlock()
	return nil
}

// RemoveInputOutputPair 删除输入输出目录配对
func (c *Config) RemoveInputOutputPair(inputDir string) error {
	inputDir = filepath.Clean(strings.TrimSpace(inputDir))

	c.mu.Lock()
	defer c.mu.Unlock()

	newPairs := []InputOutputPair{}
	found := false

	for _, pair := range c.Path.Pairs {
		if pair.Input == inputDir {
			found = true
			continue
		}
		newPairs = append(newPairs, pair)
	}

	if !found {
		return fmt.Errorf("目录配对不存在")
	}

	if len(newPairs) == 0 {
		return fmt.Errorf("至少需要保留一个监控目录")
	}

	c.Path.Pairs = newPairs
	return nil
}

// IsVideoFile 检查文件是否为支持的视频格式
func (c *Config) IsVideoFile(filename string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	ext := strings.ToLower(filepath.Ext(filename))
	for _, validExt := range c.FFmpeg.Extensions {
		if ext == strings.ToLower(validExt) {
			return true
		}
	}
	return false
}

// ApplyOutputExtension 将输出文件扩展名统一为配置值（为空则保持原样）。
func (c *Config) ApplyOutputExtension(path string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	ext := strings.TrimSpace(c.FFmpeg.OutputExtension)
	if ext == "" {
		return path
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return strings.TrimSuffix(path, filepath.Ext(path)) + ext
}

package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// TasksTotal 任务总数（按状态分类）
	TasksTotal = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "stm_tasks_total",
			Help: "Total number of tasks by status",
		},
		[]string{"status"},
	)

	// TasksProcessing 当前处理中的任务数
	TasksProcessing = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "stm_tasks_processing",
		Help: "Number of tasks currently being processed",
	})

	// WorkersActive 当前活跃的 Worker 数量
	WorkersActive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "stm_workers_active",
		Help: "Number of active worker goroutines",
	})

	// TranscodeDuration 转码耗时（秒）
	TranscodeDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "stm_transcode_duration_seconds",
		Help:    "Duration of video transcoding in seconds",
		Buckets: prometheus.ExponentialBuckets(60, 2, 10), // 60s, 120s, 240s, ..., 30720s
	})

	// TranscodeSuccess 转码成功计数
	TranscodeSuccess = promauto.NewCounter(prometheus.CounterOpts{
		Name: "stm_transcode_success_total",
		Help: "Total number of successful transcodes",
	})

	// TranscodeFailed 转码失败计数
	TranscodeFailed = promauto.NewCounter(prometheus.CounterOpts{
		Name: "stm_transcode_failed_total",
		Help: "Total number of failed transcodes",
	})

	// SpaceSaved 节省的存储空间（字节）
	SpaceSaved = promauto.NewCounter(prometheus.CounterOpts{
		Name: "stm_space_saved_bytes",
		Help: "Total storage space saved in bytes",
	})

	// FilesSoftDeleted 移入垃圾桶的文件数
	FilesSoftDeleted = promauto.NewCounter(prometheus.CounterOpts{
		Name: "stm_files_soft_deleted_total",
		Help: "Total number of files moved to trash",
	})

	// FilesHardDeleted 彻底删除的文件数
	FilesHardDeleted = promauto.NewCounter(prometheus.CounterOpts{
		Name: "stm_files_hard_deleted_total",
		Help: "Total number of files permanently deleted",
	})

	// DiskSpaceAvailable 可用磁盘空间（字节）
	DiskSpaceAvailable = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "stm_disk_space_available_bytes",
		Help: "Available disk space in bytes",
	})

	// SystemInfo 系统信息（版本、模式等）
	SystemInfo = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "stm_system_info",
			Help: "System information",
		},
		[]string{"version", "mode"},
	)
)

// UpdateTaskStats 更新任务统计
func UpdateTaskStats(pending, processing, completed, failed int) {
	TasksTotal.WithLabelValues("pending").Set(float64(pending))
	TasksTotal.WithLabelValues("processing").Set(float64(processing))
	TasksTotal.WithLabelValues("completed").Set(float64(completed))
	TasksTotal.WithLabelValues("failed").Set(float64(failed))
	TasksProcessing.Set(float64(processing))
}

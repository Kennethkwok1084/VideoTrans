package database

import (
	"database/sql"
	"time"
)

// TaskStatus 任务状态
type TaskStatus string

const (
	StatusPending       TaskStatus = "pending"
	StatusProcessing    TaskStatus = "processing"
	StatusCompleted     TaskStatus = "completed"
	StatusFailed        TaskStatus = "failed"
	StatusIrrecoverable TaskStatus = "irrecoverable"
)

// Task 转码任务模型
type Task struct {
	ID          int64          `db:"id" json:"id"`
	SourcePath  string         `db:"source_path" json:"source_path"`   // 相对路径
	SourceMtime time.Time      `db:"source_mtime" json:"source_mtime"` // 文件修改时间
	SourceSize  int64          `db:"source_size" json:"source_size"`   // 文件大小
	Status      TaskStatus     `db:"status" json:"status"`             // 任务状态
	RetryCount  int            `db:"retry_count" json:"retry_count"`   // 重试次数
	Progress    float64        `db:"progress" json:"progress"`         // 转码进度（0-100）
	OutputSize  int64          `db:"output_size" json:"output_size"`   // 输出文件大小
	RepairMode  string         `db:"repair_mode" json:"repair_mode"`   // 修复模式（cfr/discard）
	CreatedAt   time.Time      `db:"created_at" json:"created_at"`     // 创建时间
	CompletedAt *time.Time     `db:"completed_at" json:"completed_at"` // 完成时间
	Log         sql.NullString `db:"log" json:"log"`                   // 日志信息（可为NULL）
}

// GetLog 获取日志内容
func (t *Task) GetLog() string {
	if t.Log.Valid {
		return t.Log.String
	}
	return ""
}

// SetLog 设置日志内容
func (t *Task) SetLog(log string) {
	if log == "" {
		t.Log = sql.NullString{Valid: false}
	} else {
		t.Log = sql.NullString{String: log, Valid: true}
	}
}

// Stats 统计信息
type Stats struct {
	PendingCount    int   `db:"pending_count" json:"pending_count"`
	ProcessingCount int   `db:"processing_count" json:"processing_count"`
	CompletedCount  int   `db:"completed_count" json:"completed_count"`
	FailedCount     int   `db:"failed_count" json:"failed_count"`
	TotalSaved      int64 `db:"total_saved" json:"total_saved"` // 节省的空间（字节）
}

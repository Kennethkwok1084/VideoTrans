package database

import (
	"database/sql"
	"time"
)

// TaskStatus 任务状态
type TaskStatus string

// a series of task statuses.
const (
	// StatusPending is the initial status of a task.
	StatusPending TaskStatus = "pending"
	// StatusProcessing means the task is being processed.
	StatusProcessing TaskStatus = "processing"
	// StatusCompleted means the task has been completed successfully.
	StatusCompleted TaskStatus = "completed"
	// StatusFailed means the task has failed.
	StatusFailed TaskStatus = "failed"
	// StatusIrrecoverable means the task has failed and cannot be recovered.
	StatusIrrecoverable TaskStatus = "irrecoverable"
	// StatusSoftDeleted means the source file has been moved to the trash.
	StatusSoftDeleted TaskStatus = "soft_deleted"
	// StatusHardDeleted means the source file has been permanently deleted.
	StatusHardDeleted TaskStatus = "hard_deleted"
	// StatusCleanupError means the cleanup action failed.
	StatusCleanupError TaskStatus = "cleanup_error"
)

// Task represents a transcoding task.
type Task struct {
	ID          int64          `db:"id" json:"id"`
	SourcePath  string         `db:"source_path" json:"source_path"`   // Relative path of the source file
	SourceMtime time.Time      `db:"source_mtime" json:"source_mtime"` // Modification time of the source file
	SourceSize  int64          `db:"source_size" json:"source_size"`   // Size of the source file
	Status      TaskStatus     `db:"status" json:"status"`             // Task status
	RetryCount  int            `db:"retry_count" json:"retry_count"`   // Number of retries
	Progress    float64        `db:"progress" json:"progress"`         // Transcoding progress (0-100)
	OutputSize  int64          `db:"output_size" json:"output_size"`   // Size of the output file
	RepairMode  string         `db:"repair_mode" json:"repair_mode"`   // Repair mode (cfr/discard)
	CreatedAt   time.Time      `db:"created_at" json:"created_at"`     // Creation time
	CompletedAt *time.Time     `db:"completed_at" json:"completed_at"` // Completion time
	Log         sql.NullString `db:"log" json:"log"`                   // Log information (can be NULL)
	// Cleanup lifecycle fields
	SourceDeletedAt sql.NullTime   `db:"source_deleted_at" json:"source_deleted_at"` // Source file deletion time
	TrashPath       sql.NullString `db:"trash_path" json:"trash_path"`               // Trash path
	CleanupLog      sql.NullString `db:"cleanup_log" json:"cleanup_log"`             // Cleanup log
	// Phase 4: Exponential backoff retry fields
	NextRetryAt       sql.NullTime   `db:"next_retry_at" json:"next_retry_at"`             // Next retry time
	LastErrorCategory sql.NullString `db:"last_error_category" json:"last_error_category"` // Last error category
}

// GetLog gets the log content.
func (t *Task) GetLog() string {
	if t.Log.Valid {
		return t.Log.String
	}
	return ""
}

// SetLog sets the log content.
func (t *Task) SetLog(log string) {
	if log == "" {
		t.Log = sql.NullString{Valid: false}
	} else {
		t.Log = sql.NullString{String: log, Valid: true}
	}
}

// GetTrashPath gets the trash path.
func (t *Task) GetTrashPath() string {
	if t.TrashPath.Valid {
		return t.TrashPath.String
	}
	return ""
}

// SetTrashPath sets the trash path.
func (t *Task) SetTrashPath(path string) {
	if path == "" {
		t.TrashPath = sql.NullString{Valid: false}
	} else {
		t.TrashPath = sql.NullString{String: path, Valid: true}
	}
}

// GetCleanupLog gets the cleanup log.
func (t *Task) GetCleanupLog() string {
	if t.CleanupLog.Valid {
		return t.CleanupLog.String
	}
	return ""
}

// SetCleanupLog sets the cleanup log.
func (t *Task) SetCleanupLog(log string) {
	if log == "" {
		t.CleanupLog = sql.NullString{Valid: false}
	} else {
		t.CleanupLog = sql.NullString{String: log, Valid: true}
	}
}

// Stats represents statistics.
type Stats struct {
	PendingCount      int   `db:"pending_count" json:"pending_count"`
	ProcessingCount   int   `db:"processing_count" json:"processing_count"`
	CompletedCount    int   `db:"completed_count" json:"completed_count"`
	FailedCount       int   `db:"failed_count" json:"failed_count"`
	SoftDeletedCount  int   `db:"soft_deleted_count" json:"soft_deleted_count"`
	HardDeletedCount  int   `db:"hard_deleted_count" json:"hard_deleted_count"`
	CleanupErrorCount int   `db:"cleanup_error_count" json:"cleanup_error_count"`
	TotalSaved        int64 `db:"total_saved" json:"total_saved"` // Saved space in bytes
}

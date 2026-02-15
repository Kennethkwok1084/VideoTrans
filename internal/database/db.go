package database

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// DB 数据库连接包装器
type DB struct {
	conn *sql.DB
}

const migrationPhase3HistoricalCompatV1 = "phase3_historical_compat_v1"

// Init 初始化数据库连接
func Init(dbPath string) (*DB, error) {
	// 确保数据库目录存在
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("创建数据库目录失败: %w", err)
	}

	// 打开数据库连接
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}

	// 设置连接池参数
	conn.SetMaxOpenConns(1) // SQLite 单写入模式
	conn.SetMaxIdleConns(1)

	// 启用WAL模式（提高并发性能）
	if _, err := conn.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, fmt.Errorf("启用WAL模式失败: %w", err)
	}

	db := &DB{conn: conn}

	// 初始化表结构
	if err := db.createTables(); err != nil {
		return nil, fmt.Errorf("创建表失败: %w", err)
	}

	// Phase 3: 执行历史数据兼容迁移（仅在首次运行时）
	if err := db.migratePhase3HistoricalData(); err != nil {
		log.Printf("[Database] 警告：历史数据迁移失败（不影响系统运行）: %v", err)
	}

	return db, nil
}

// createTables 创建数据库表
func (db *DB) createTables() error {
	schema := `
	CREATE TABLE IF NOT EXISTS tasks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		source_path TEXT NOT NULL UNIQUE,
		source_mtime DATETIME NOT NULL,
		source_size INTEGER NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending',
		retry_count INTEGER NOT NULL DEFAULT 0,
		progress REAL NOT NULL DEFAULT 0,
		output_size INTEGER DEFAULT 0,
		repair_mode TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		completed_at DATETIME,
		log TEXT,
		source_deleted_at DATETIME,
		trash_path TEXT,
		cleanup_log TEXT,
		next_retry_at DATETIME,
		last_error_category TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_source_path ON tasks(source_path);
	CREATE INDEX IF NOT EXISTS idx_status ON tasks(status);
	CREATE INDEX IF NOT EXISTS idx_completed_at ON tasks(completed_at);

	CREATE TABLE IF NOT EXISTS schema_migrations (
		name TEXT PRIMARY KEY,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS app_config (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	`

	if _, err := db.conn.Exec(schema); err != nil {
		return err
	}
	return db.ensureColumns()
}

// GetAppConfig 读取数据库中的应用配置键值对
func (db *DB) GetAppConfig() (map[string]string, error) {
	rows, err := db.conn.Query(`SELECT key, value FROM app_config`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// UpsertAppConfig 批量写入应用配置键值对
func (db *DB) UpsertAppConfig(values map[string]string) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO app_config(key, value, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET
			value = excluded.value,
			updated_at = CURRENT_TIMESTAMP
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		if _, err := stmt.Exec(k, values[k]); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (db *DB) ensureColumns() error {
	columns := map[string]struct{}{}
	rows, err := db.conn.Query(`PRAGMA table_info(tasks)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var (
		cid       int
		name      string
		colType   string
		notNull   int
		dfltValue interface{}
		pk        int
	)
	for rows.Next() {
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return err
		}
		columns[name] = struct{}{}
	}
	if rows.Err() != nil {
		return rows.Err()
	}

	if _, ok := columns["repair_mode"]; !ok {
		if _, err := db.conn.Exec(`ALTER TABLE tasks ADD COLUMN repair_mode TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}

	// Phase 3: 清理生命周期字段
	if _, ok := columns["source_deleted_at"]; !ok {
		if _, err := db.conn.Exec(`ALTER TABLE tasks ADD COLUMN source_deleted_at DATETIME`); err != nil {
			return err
		}
	}
	if _, ok := columns["trash_path"]; !ok {
		if _, err := db.conn.Exec(`ALTER TABLE tasks ADD COLUMN trash_path TEXT`); err != nil {
			return err
		}
	}
	if _, ok := columns["cleanup_log"]; !ok {
		if _, err := db.conn.Exec(`ALTER TABLE tasks ADD COLUMN cleanup_log TEXT`); err != nil {
			return err
		}
	}

	// Phase 4: 指数退避重试字段
	if _, ok := columns["next_retry_at"]; !ok {
		if _, err := db.conn.Exec(`ALTER TABLE tasks ADD COLUMN next_retry_at DATETIME`); err != nil {
			return err
		}
	}
	if _, ok := columns["last_error_category"]; !ok {
		if _, err := db.conn.Exec(`ALTER TABLE tasks ADD COLUMN last_error_category TEXT`); err != nil {
			return err
		}
	}

	indexDDL := []string{
		`CREATE INDEX IF NOT EXISTS idx_source_path ON tasks(source_path)`,
		`CREATE INDEX IF NOT EXISTS idx_status ON tasks(status)`,
		`CREATE INDEX IF NOT EXISTS idx_completed_at ON tasks(completed_at)`,
		`CREATE INDEX IF NOT EXISTS idx_status_next_retry_created_at ON tasks(status, next_retry_at, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_status_source_deleted_at ON tasks(status, source_deleted_at)`,
		`CREATE INDEX IF NOT EXISTS idx_status_completed_at_id ON tasks(status, completed_at, id)`,
		`CREATE INDEX IF NOT EXISTS idx_status_created_at ON tasks(status, created_at)`,
	}
	for _, ddl := range indexDDL {
		if _, err := db.conn.Exec(ddl); err != nil {
			return err
		}
	}

	return nil
}

// Close 关闭数据库连接
func (db *DB) Close() error {
	return db.conn.Close()
}

// CreateTask 创建新任务
func (db *DB) CreateTask(task *Task) error {
	query := `
		INSERT INTO tasks (source_path, source_mtime, source_size, status, repair_mode)
		VALUES (?, ?, ?, ?, ?)
	`

	result, err := db.conn.Exec(query,
		task.SourcePath,
		task.SourceMtime,
		task.SourceSize,
		StatusPending,
		"",
	)

	if err != nil {
		return err
	}

	id, _ := result.LastInsertId()
	task.ID = id
	task.Status = StatusPending
	task.CreatedAt = time.Now()

	return nil
}

// GetTaskByPath 通过路径查询任务
func (db *DB) GetTaskByPath(path string) (*Task, error) {
	query := `
		SELECT id, source_path, source_mtime, source_size, status, retry_count,
		       progress, output_size, repair_mode, created_at, completed_at, log,
		       source_deleted_at, trash_path, cleanup_log,
		       next_retry_at, last_error_category
		FROM tasks
		WHERE source_path = ?
	`

	task := &Task{}
	err := db.conn.QueryRow(query, path).Scan(
		&task.ID,
		&task.SourcePath,
		&task.SourceMtime,
		&task.SourceSize,
		&task.Status,
		&task.RetryCount,
		&task.Progress,
		&task.OutputSize,
		&task.RepairMode,
		&task.CreatedAt,
		&task.CompletedAt,
		&task.Log,
		&task.SourceDeletedAt,
		&task.TrashPath,
		&task.CleanupLog,
		&task.NextRetryAt,
		&task.LastErrorCategory,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}

	return task, err
}

// UpdateTaskStatus 更新任务状态
func (db *DB) UpdateTaskStatus(id int64, status TaskStatus, log string) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	query := `UPDATE tasks SET status = ?, log = ? WHERE id = ?`
	if _, err := tx.Exec(query, status, log, id); err != nil {
		return err
	}

	// 如果是完成状态，记录完成时间
	if status == StatusCompleted {
		now := time.Now()
		if _, err := tx.Exec(`UPDATE tasks SET completed_at = ? WHERE id = ?`, now, id); err != nil {
			return err
		}
	}

	// 如果是手动重置为 Pending，清空指数退避状态
	if status == StatusPending {
		if _, err := tx.Exec(`UPDATE tasks SET next_retry_at = NULL, last_error_category = NULL WHERE id = ?`, id); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// UpdateTaskRepairMode 更新任务修复模式
func (db *DB) UpdateTaskRepairMode(id int64, mode string) error {
	query := `UPDATE tasks SET repair_mode = ? WHERE id = ?`
	_, err := db.conn.Exec(query, mode, id)
	return err
}

// UpdateTaskProgress 更新任务进度
func (db *DB) UpdateTaskProgress(id int64, progress float64) error {
	query := `UPDATE tasks SET progress = ? WHERE id = ?`
	_, err := db.conn.Exec(query, progress, id)
	return err
}

// UpdateTaskOutputSize 更新输出文件大小
func (db *DB) UpdateTaskOutputSize(id int64, size int64) error {
	query := `UPDATE tasks SET output_size = ? WHERE id = ?`
	_, err := db.conn.Exec(query, size, id)
	return err
}

// UpdateTaskPath 更新任务路径（用于迁移旧版本相对路径到新版本完整路径）
func (db *DB) UpdateTaskPath(id int64, newPath string) error {
	query := `UPDATE tasks SET source_path = ? WHERE id = ?`
	_, err := db.conn.Exec(query, newPath, id)
	return err
}

// GetPendingTasks 获取待处理任务（支持指数退避重试过滤）
// 包括：1) status=pending 的任务；2) status=failed 但 next_retry_at 已到期的任务
func (db *DB) GetPendingTasks(limit int) ([]*Task, error) {
	query := `
		SELECT id, source_path, source_mtime, source_size, status, retry_count,
		       progress, output_size, repair_mode, created_at, completed_at, log,
		       next_retry_at, last_error_category
		FROM tasks
		WHERE retry_count < 3
		  AND (
		      (status = ? AND (next_retry_at IS NULL OR next_retry_at <= datetime('now')))
		      OR (status = ? AND next_retry_at IS NOT NULL AND next_retry_at <= datetime('now'))
		  )
		ORDER BY created_at ASC
		LIMIT ?
	`

	rows, err := db.conn.Query(query, StatusPending, StatusFailed, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*Task
	for rows.Next() {
		task := &Task{}
		err := rows.Scan(
			&task.ID,
			&task.SourcePath,
			&task.SourceMtime,
			&task.SourceSize,
			&task.Status,
			&task.RetryCount,
			&task.Progress,
			&task.OutputSize,
			&task.RepairMode,
			&task.CreatedAt,
			&task.CompletedAt,
			&task.Log,
			&task.NextRetryAt,
			&task.LastErrorCategory,
		)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}

	return tasks, rows.Err()
}

// GetCompletedOldTasks 查询N天前完成的任务
func (db *DB) GetCompletedOldTasks(cutoffTime time.Time) ([]*Task, error) {
	query := `
		SELECT id, source_path, source_mtime, source_size, status, retry_count,
		       progress, output_size, repair_mode, created_at, completed_at, log
		FROM tasks
		WHERE status = ? AND completed_at < ?
	`

	rows, err := db.conn.Query(query, StatusCompleted, cutoffTime)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*Task
	for rows.Next() {
		task := &Task{}
		err := rows.Scan(
			&task.ID,
			&task.SourcePath,
			&task.SourceMtime,
			&task.SourceSize,
			&task.Status,
			&task.RetryCount,
			&task.Progress,
			&task.OutputSize,
			&task.RepairMode,
			&task.CreatedAt,
			&task.CompletedAt,
			&task.Log,
		)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}

	return tasks, rows.Err()
}

// ResetTaskToPending 重置任务为待处理状态（文件更新时使用）
func (db *DB) ResetTaskToPending(path string, mtime time.Time, size int64) error {
	query := `
		UPDATE tasks 
		SET status = ?, source_mtime = ?, source_size = ?, retry_count = 0,
		    progress = 0, completed_at = NULL, log = '', repair_mode = '',
		    next_retry_at = NULL, last_error_category = NULL
		WHERE source_path = ?
	`

	_, err := db.conn.Exec(query, StatusPending, mtime, size, path)
	return err
}

// IncrementRetryCount 增加重试次数
func (db *DB) IncrementRetryCount(id int64) error {
	query := `UPDATE tasks SET retry_count = retry_count + 1 WHERE id = ?`
	_, err := db.conn.Exec(query, id)
	return err
}

// ResetFailedTasksToPending 批量重置失败任务为待处理
func (db *DB) ResetFailedTasksToPending() (int64, error) {
	query := `
		UPDATE tasks
		SET status = ?, retry_count = 0, progress = 0, completed_at = NULL, log = ?,
		    next_retry_at = NULL, last_error_category = NULL
		WHERE status = ?
	`
	result, err := db.conn.Exec(query, StatusPending, "手动一键重试", StatusFailed)
	if err != nil {
		return 0, err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}

	return rows, nil
}

// ResetProcessingTasksToPending 批量重置处理中任务为待处理
func (db *DB) ResetProcessingTasksToPending() (int64, error) {
	query := `
		UPDATE tasks
		SET status = ?, retry_count = 0, progress = 0, completed_at = NULL, log = ?,
		    next_retry_at = NULL, last_error_category = NULL
		WHERE status = ?
	`
	result, err := db.conn.Exec(query, StatusPending, "恢复未完成任务", StatusProcessing)
	if err != nil {
		return 0, err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}

	return rows, nil
}

// GetStats 获取统计信息
func (db *DB) GetStats() (*Stats, error) {
	query := `
		SELECT 
			COALESCE(SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END), 0) as pending_count,
			COALESCE(SUM(CASE WHEN status = 'processing' THEN 1 ELSE 0 END), 0) as processing_count,
			COALESCE(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0) as completed_count,
			COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0) as failed_count,
			COALESCE(SUM(CASE WHEN status = 'soft_deleted' THEN 1 ELSE 0 END), 0) as soft_deleted_count,
			COALESCE(SUM(CASE WHEN status = 'hard_deleted' THEN 1 ELSE 0 END), 0) as hard_deleted_count,
			COALESCE(SUM(CASE WHEN status = 'cleanup_error' THEN 1 ELSE 0 END), 0) as cleanup_error_count,
			COALESCE(SUM(CASE WHEN status = 'completed' THEN (source_size - output_size) ELSE 0 END), 0) as total_saved
		FROM tasks
	`

	stats := &Stats{}
	err := db.conn.QueryRow(query).Scan(
		&stats.PendingCount,
		&stats.ProcessingCount,
		&stats.CompletedCount,
		&stats.FailedCount,
		&stats.SoftDeletedCount,
		&stats.HardDeletedCount,
		&stats.CleanupErrorCount,
		&stats.TotalSaved,
	)

	return stats, err
}

// GetAllTasks 获取所有任务（支持分页和状态筛选）
func (db *DB) GetAllTasks(status string, limit, offset int) ([]*Task, error) {
	var query string
	var args []interface{}

	if status != "" {
		query = `
			SELECT id, source_path, source_mtime, source_size, status, retry_count,
			       progress, output_size, repair_mode, created_at, completed_at, log,
			       cleanup_log
			FROM tasks
			WHERE status = ?
			ORDER BY created_at DESC
			LIMIT ? OFFSET ?
		`
		args = []interface{}{status, limit, offset}
	} else {
		query = `
			SELECT id, source_path, source_mtime, source_size, status, retry_count,
			       progress, output_size, repair_mode, created_at, completed_at, log,
			       cleanup_log
			FROM tasks
			ORDER BY created_at DESC
			LIMIT ? OFFSET ?
		`
		args = []interface{}{limit, offset}
	}

	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*Task
	for rows.Next() {
		task := &Task{}
		err := rows.Scan(
			&task.ID,
			&task.SourcePath,
			&task.SourceMtime,
			&task.SourceSize,
			&task.Status,
			&task.RetryCount,
			&task.Progress,
			&task.OutputSize,
			&task.RepairMode,
			&task.CreatedAt,
			&task.CompletedAt,
			&task.Log,
			&task.CleanupLog,
		)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}

	return tasks, rows.Err()
}

// GetScanErrorTasks 获取输出校验/扫描发现异常的任务
func (db *DB) GetScanErrorTasks(limit, offset int) ([]*Task, error) {
	query := `
		SELECT id, source_path, source_mtime, source_size, status, retry_count,
		       progress, output_size, repair_mode, created_at, completed_at, log
		FROM tasks
		WHERE status != ? AND COALESCE(log, '') LIKE ?
		ORDER BY created_at DESC
		LIMIT ? OFFSET ?
	`

	rows, err := db.conn.Query(query, StatusCompleted, "%输出文件%", limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*Task
	for rows.Next() {
		task := &Task{}
		err := rows.Scan(
			&task.ID,
			&task.SourcePath,
			&task.SourceMtime,
			&task.SourceSize,
			&task.Status,
			&task.RetryCount,
			&task.Progress,
			&task.OutputSize,
			&task.RepairMode,
			&task.CreatedAt,
			&task.CompletedAt,
			&task.Log,
		)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}

	return tasks, rows.Err()
}

// DeleteTask 删除任务记录
func (db *DB) DeleteTask(id int64) error {
	query := `DELETE FROM tasks WHERE id = ?`
	_, err := db.conn.Exec(query, id)
	return err
}

// ==================== Phase 3: 清理生命周期管理 ====================

// MarkSoftDeleted 标记任务为软删除（已移入垃圾桶）
func (db *DB) MarkSoftDeleted(id int64, trashPath string) error {
	now := time.Now()
	query := `
		UPDATE tasks 
		SET status = ?, trash_path = ?, source_deleted_at = ?
		WHERE id = ?
	`
	_, err := db.conn.Exec(query, StatusSoftDeleted, trashPath, now, id)
	return err
}

// MarkHardDeleted 标记任务为硬删除（已彻底删除）
func (db *DB) MarkHardDeleted(id int64) error {
	query := `UPDATE tasks SET status = ? WHERE id = ?`
	_, err := db.conn.Exec(query, StatusHardDeleted, id)
	return err
}

// GetSoftDeletedTaskByTrashPath 按垃圾桶路径查询 soft_deleted 任务
func (db *DB) GetSoftDeletedTaskByTrashPath(trashPath string) (*Task, error) {
	query := `
		SELECT id, source_path, source_mtime, source_size, status, retry_count,
		       progress, output_size, repair_mode, created_at, completed_at, log,
		       source_deleted_at, trash_path, cleanup_log,
		       next_retry_at, last_error_category
		FROM tasks
		WHERE status = ? AND trash_path = ?
		LIMIT 1
	`

	task := &Task{}
	err := db.conn.QueryRow(query, StatusSoftDeleted, trashPath).Scan(
		&task.ID,
		&task.SourcePath,
		&task.SourceMtime,
		&task.SourceSize,
		&task.Status,
		&task.RetryCount,
		&task.Progress,
		&task.OutputSize,
		&task.RepairMode,
		&task.CreatedAt,
		&task.CompletedAt,
		&task.Log,
		&task.SourceDeletedAt,
		&task.TrashPath,
		&task.CleanupLog,
		&task.NextRetryAt,
		&task.LastErrorCategory,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return task, nil
}

// MarkRestored 标记任务为已恢复（文件从垃圾桶恢复到原路径）
func (db *DB) MarkRestored(id int64, note string) error {
	query := `
		UPDATE tasks
		SET status = ?, trash_path = NULL, source_deleted_at = NULL, cleanup_log = ?
		WHERE id = ? AND status = ?
	`
	_, err := db.conn.Exec(query, StatusCompleted, note, id, StatusSoftDeleted)
	return err
}

// MarkCleanupError 标记清理错误
func (db *DB) MarkCleanupError(id int64, errorLog string) error {
	query := `
		UPDATE tasks 
		SET status = ?, cleanup_log = ?
		WHERE id = ?
	`
	_, err := db.conn.Exec(query, StatusCleanupError, errorLog, id)
	return err
}

// GetCleanupErrorTasks 获取清理失败任务（用于自动重试）
func (db *DB) GetCleanupErrorTasks(limit int) ([]*Task, error) {
	if limit <= 0 {
		limit = 100
	}

	query := `
		SELECT id, source_path, source_mtime, source_size, status, retry_count,
		       progress, output_size, repair_mode, created_at, completed_at, log,
		       source_deleted_at, trash_path, cleanup_log,
		       next_retry_at, last_error_category
		FROM tasks
		WHERE status = ?
		ORDER BY created_at ASC
		LIMIT ?
	`

	rows, err := db.conn.Query(query, StatusCleanupError, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*Task
	for rows.Next() {
		task := &Task{}
		err := rows.Scan(
			&task.ID,
			&task.SourcePath,
			&task.SourceMtime,
			&task.SourceSize,
			&task.Status,
			&task.RetryCount,
			&task.Progress,
			&task.OutputSize,
			&task.RepairMode,
			&task.CreatedAt,
			&task.CompletedAt,
			&task.Log,
			&task.SourceDeletedAt,
			&task.TrashPath,
			&task.CleanupLog,
			&task.NextRetryAt,
			&task.LastErrorCategory,
		)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}

	return tasks, rows.Err()
}

// ResetCleanupErrorStatus 将 cleanup_error 任务恢复到目标状态（用于自动自愈）
func (db *DB) ResetCleanupErrorStatus(id int64, targetStatus TaskStatus, note string) error {
	query := `
		UPDATE tasks
		SET status = ?, cleanup_log = ?
		WHERE id = ? AND status = ?
	`
	_, err := db.conn.Exec(query, targetStatus, note, id, StatusCleanupError)
	return err
}

// GetSoftDeletedOldTasks 查询N天前软删除的任务
func (db *DB) GetSoftDeletedOldTasks(cutoffTime time.Time) ([]*Task, error) {
	query := `
		SELECT id, source_path, source_mtime, source_size, status, retry_count,
		       progress, output_size, repair_mode, created_at, completed_at, log,
		       source_deleted_at, trash_path, cleanup_log,
		       next_retry_at, last_error_category
		FROM tasks
		WHERE status = ? AND source_deleted_at < ?
	`

	rows, err := db.conn.Query(query, StatusSoftDeleted, cutoffTime)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*Task
	for rows.Next() {
		task := &Task{}
		err := rows.Scan(
			&task.ID,
			&task.SourcePath,
			&task.SourceMtime,
			&task.SourceSize,
			&task.Status,
			&task.RetryCount,
			&task.Progress,
			&task.OutputSize,
			&task.RepairMode,
			&task.CreatedAt,
			&task.CompletedAt,
			&task.Log,
			&task.SourceDeletedAt,
			&task.TrashPath,
			&task.CleanupLog,
			&task.NextRetryAt,
			&task.LastErrorCategory,
		)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}

	return tasks, rows.Err()
}

// GetBatchTasksCompleted 批量获取完成任务（支持 keyset 游标分页）
func (db *DB) GetBatchTasksCompleted(since time.Time, lastID int64, upperBound time.Time, limit int) ([]*Task, error) {
	query := `
		SELECT id, source_path, source_mtime, source_size, status, retry_count,
		       progress, output_size, repair_mode, created_at, completed_at, log,
		       source_deleted_at, trash_path, cleanup_log,
		       next_retry_at, last_error_category
		FROM tasks
		WHERE status = ? 
		  AND completed_at >= ?
		  AND completed_at <= ?
		  AND (completed_at > ? OR (completed_at = ? AND id > ?))
		ORDER BY completed_at ASC, id ASC
		LIMIT ?
	`

	rows, err := db.conn.Query(query, StatusCompleted, since, upperBound, since, since, lastID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*Task
	for rows.Next() {
		task := &Task{}
		err := rows.Scan(
			&task.ID,
			&task.SourcePath,
			&task.SourceMtime,
			&task.SourceSize,
			&task.Status,
			&task.RetryCount,
			&task.Progress,
			&task.OutputSize,
			&task.RepairMode,
			&task.CreatedAt,
			&task.CompletedAt,
			&task.Log,
			&task.SourceDeletedAt,
			&task.TrashPath,
			&task.CleanupLog,
			&task.NextRetryAt,
			&task.LastErrorCategory,
		)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}

	return tasks, rows.Err()
}

// ==================== Phase 3: 历史数据迁移 ====================

// migratePhase3HistoricalData Phase 3: 历史数据兼容迁移
// 保守策略：仅写入标记表示已迁移，不主动推断历史删除状态
func (db *DB) migratePhase3HistoricalData() error {
	// 检查是否已执行迁移
	var count int
	err := db.conn.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, migrationPhase3HistoricalCompatV1).Scan(&count)
	if err != nil {
		return fmt.Errorf("检查迁移状态失败: %w", err)
	}

	if count > 0 {
		log.Printf("[Database] Phase 3 历史迁移已执行，跳过: %s", migrationPhase3HistoricalCompatV1)
		return nil
	}

	// 统计历史 completed 任务
	var completedCount int
	err = db.conn.QueryRow(`SELECT COUNT(*) FROM tasks WHERE status = ?`, StatusCompleted).Scan(&completedCount)
	if err != nil {
		return fmt.Errorf("统计历史任务失败: %w", err)
	}

	if completedCount > 0 {
		log.Printf("[Database] 开始 Phase 3 历史数据迁移，发现 %d 个 completed 任务", completedCount)
		log.Printf("[Database] 迁移策略：保守处理，不推断历史删除状态")
		log.Printf("[Database] 保持所有 completed 任务不变，后续由 Cleaner 按新逻辑处理")
	} else {
		log.Printf("[Database] 未发现历史 completed 任务，写入迁移标记")
	}

	// 写入迁移标记
	_, err = db.conn.Exec(`INSERT INTO schema_migrations (name) VALUES (?)`, migrationPhase3HistoricalCompatV1)
	if err != nil {
		return fmt.Errorf("写入迁移标记失败: %w", err)
	}

	log.Printf("[Database] Phase 3 历史迁移标记已写入: %s", migrationPhase3HistoricalCompatV1)
	return nil
}

// ==================== Phase 4: 原子 Claim 与指数退避重试 ====================

// ClaimPendingTasks 原子性地 claim 待处理任务（Phase 4: 从 pending 直接变为 processing）
// 在单个事务中完成"选取 + 状态迁移"，避免重复调度
// Phase 4: 支持指数退避，仅 claim next_retry_at <= now 的任务
func (db *DB) ClaimPendingTasks(limit int) ([]*Task, error) {
	tx, err := db.conn.Begin()
	if err != nil {
		return nil, fmt.Errorf("开始事务失败: %w", err)
	}
	defer tx.Rollback()

	// 1. 选取待处理任务（Phase 4: 考虑 next_retry_at）
	// 包括：1) status=pending 的任务；2) status=failed 但 next_retry_at 已到期的任务
	selectQuery := `
		SELECT id, source_path, source_mtime, source_size, status, retry_count,
		       progress, output_size, repair_mode, created_at, completed_at, log,
		       source_deleted_at, trash_path, cleanup_log,
		       next_retry_at, last_error_category
		FROM tasks
		WHERE retry_count < 3
		  AND (
		      (status = ? AND (next_retry_at IS NULL OR next_retry_at <= datetime('now')))
		      OR (status = ? AND next_retry_at IS NOT NULL AND next_retry_at <= datetime('now'))
		  )
		ORDER BY created_at ASC
		LIMIT ?
	`

	rows, err := tx.Query(selectQuery, StatusPending, StatusFailed, limit)
	if err != nil {
		return nil, fmt.Errorf("查询待处理任务失败: %w", err)
	}
	defer rows.Close()

	var tasks []*Task
	var taskIDs []int64

	for rows.Next() {
		task := &Task{}
		err := rows.Scan(
			&task.ID,
			&task.SourcePath,
			&task.SourceMtime,
			&task.SourceSize,
			&task.Status,
			&task.RetryCount,
			&task.Progress,
			&task.OutputSize,
			&task.RepairMode,
			&task.CreatedAt,
			&task.CompletedAt,
			&task.Log,
			&task.SourceDeletedAt,
			&task.TrashPath,
			&task.CleanupLog,
			&task.NextRetryAt,
			&task.LastErrorCategory,
		)
		if err != nil {
			return nil, fmt.Errorf("扫描任务失败: %w", err)
		}
		tasks = append(tasks, task)
		taskIDs = append(taskIDs, task.ID)
	}
	rows.Close()

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历任务失败: %w", err)
	}

	// 2. 如果没有任务，直接返回
	if len(taskIDs) == 0 {
		return tasks, nil
	}

	// 3. 批量更新为 processing 状态
	placeholders := strings.Repeat("?,", len(taskIDs))
	placeholders = placeholders[:len(placeholders)-1]

	updateQuery := fmt.Sprintf(`
		UPDATE tasks 
		SET status = ?, progress = 0, next_retry_at = NULL, last_error_category = NULL
		WHERE id IN (%s) AND (status = ? OR status = ?)
	`, placeholders)

	args := []interface{}{StatusProcessing}
	for _, id := range taskIDs {
		args = append(args, id)
	}
	args = append(args, StatusPending)
	args = append(args, StatusFailed)

	result, err := tx.Exec(updateQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("更新任务状态失败: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("获取受影响行数失败: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("提交事务失败: %w", err)
	}

	for _, task := range tasks {
		task.Status = StatusProcessing
		task.Progress = 0
	}

	log.Printf("[DB] 成功 claim %d/%d 个任务", affected, len(taskIDs))

	return tasks, nil
}

// ScheduleRetry Phase 4: 设置下次重试时间和错误类别（支持指数退避）
// 注意：不改变任务状态，仅更新重试时间和错误类别
func (db *DB) ScheduleRetry(id int64, nextRetryAt time.Time, errorCategory string) error {
	// 转换为 UTC 时间存储，避免时区问题
	nextRetryAtUTC := nextRetryAt.UTC()
	query := `
		UPDATE tasks 
		SET next_retry_at = ?, last_error_category = ?, progress = 0
		WHERE id = ?
	`
	_, err := db.conn.Exec(query, nextRetryAtUTC, errorCategory, id)
	return err
}

// GetAllSoftDeleted 获取所有软删除的任务
func (db *DB) GetAllSoftDeleted() ([]*Task, error) {
	query := `
		SELECT id, source_path, source_mtime, source_size, status, retry_count,
		       progress, output_size, repair_mode, created_at, completed_at, log,
		       source_deleted_at, trash_path, cleanup_log,
		       next_retry_at, last_error_category
		FROM tasks
		WHERE status = ?
		ORDER BY source_deleted_at ASC
	`

	rows, err := db.conn.Query(query, StatusSoftDeleted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []*Task
	for rows.Next() {
		task := &Task{}
		err := rows.Scan(
			&task.ID,
			&task.SourcePath,
			&task.SourceMtime,
			&task.SourceSize,
			&task.Status,
			&task.RetryCount,
			&task.Progress,
			&task.OutputSize,
			&task.RepairMode,
			&task.CreatedAt,
			&task.CompletedAt,
			&task.Log,
			&task.SourceDeletedAt,
			&task.TrashPath,
			&task.CleanupLog,
			&task.NextRetryAt,
			&task.LastErrorCategory,
		)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}

	return tasks, rows.Err()
}

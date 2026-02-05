package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// DB 数据库连接包装器
type DB struct {
	conn *sql.DB
}

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
		log TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_source_path ON tasks(source_path);
	CREATE INDEX IF NOT EXISTS idx_status ON tasks(status);
	CREATE INDEX IF NOT EXISTS idx_completed_at ON tasks(completed_at);
	`

	if _, err := db.conn.Exec(schema); err != nil {
		return err
	}
	return db.ensureColumns()
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
		       progress, output_size, repair_mode, created_at, completed_at, log
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

// GetPendingTasks 获取待处理任务
func (db *DB) GetPendingTasks(limit int) ([]*Task, error) {
	query := `
		SELECT id, source_path, source_mtime, source_size, status, retry_count,
		       progress, output_size, repair_mode, created_at, completed_at, log
		FROM tasks
		WHERE status = ? AND retry_count < 3
		ORDER BY created_at ASC
		LIMIT ?
	`

	rows, err := db.conn.Query(query, StatusPending, limit)
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
		    progress = 0, completed_at = NULL, log = '', repair_mode = ''
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
		SET status = ?, retry_count = 0, progress = 0, completed_at = NULL, log = ?
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
		SET status = ?, retry_count = 0, progress = 0, completed_at = NULL, log = ?
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
			COALESCE(SUM(CASE WHEN status = 'completed' THEN (source_size - output_size) ELSE 0 END), 0) as total_saved
		FROM tasks
	`

	stats := &Stats{}
	err := db.conn.QueryRow(query).Scan(
		&stats.PendingCount,
		&stats.ProcessingCount,
		&stats.CompletedCount,
		&stats.FailedCount,
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
			       progress, output_size, repair_mode, created_at, completed_at, log
			FROM tasks
			WHERE status = ?
			ORDER BY created_at DESC
			LIMIT ? OFFSET ?
		`
		args = []interface{}{status, limit, offset}
	} else {
		query = `
			SELECT id, source_path, source_mtime, source_size, status, retry_count,
			       progress, output_size, repair_mode, created_at, completed_at, log
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

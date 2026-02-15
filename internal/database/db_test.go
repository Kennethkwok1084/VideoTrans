package database

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInit(t *testing.T) {
	// 创建临时数据库路径
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// 初始化数据库
	db, err := Init(dbPath)
	if err != nil {
		t.Fatalf("初始化数据库失败: %v", err)
	}
	defer db.Close()

	// 验证数据库文件存在
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("数据库文件未创建")
	}
}

func TestCreateTask(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, _ := Init(dbPath)
	defer db.Close()

	task := &Task{
		SourcePath:  "test/video.mp4",
		SourceMtime: time.Now(),
		SourceSize:  1024000,
	}

	err := db.CreateTask(task)
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	if task.ID == 0 {
		t.Error("任务ID未设置")
	}

	// 测试重复创建（应该失败）
	err = db.CreateTask(task)
	if err == nil {
		t.Error("重复路径应该失败")
	}
}

func TestGetTaskByPath(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, _ := Init(dbPath)
	defer db.Close()

	// 创建任务
	task := &Task{
		SourcePath:  "test/video.mp4",
		SourceMtime: time.Now(),
		SourceSize:  1024000,
	}
	db.CreateTask(task)

	// 查询任务
	found, err := db.GetTaskByPath("test/video.mp4")
	if err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}

	if found == nil {
		t.Fatal("任务未找到")
	}

	if found.SourcePath != task.SourcePath {
		t.Errorf("路径不匹配: 期望 %s, 实际 %s", task.SourcePath, found.SourcePath)
	}

	// 查询不存在的任务
	notFound, err := db.GetTaskByPath("not/exists.mp4")
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if notFound != nil {
		t.Error("不应找到不存在的任务")
	}
}

func TestUpdateTaskStatus(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, _ := Init(dbPath)
	defer db.Close()

	task := &Task{
		SourcePath:  "test/video.mp4",
		SourceMtime: time.Now(),
		SourceSize:  1024000,
	}
	db.CreateTask(task)

	// 更新为处理中
	err := db.UpdateTaskStatus(task.ID, StatusProcessing, "正在处理")
	if err != nil {
		t.Fatalf("更新状态失败: %v", err)
	}

	// 验证更新
	updated, _ := db.GetTaskByPath(task.SourcePath)
	if updated.Status != StatusProcessing {
		t.Errorf("状态未更新: %s", updated.Status)
	}

	// 更新为完成
	err = db.UpdateTaskStatus(task.ID, StatusCompleted, "完成")
	if err != nil {
		t.Fatalf("更新为完成失败: %v", err)
	}

	// 验证完成时间
	completed, _ := db.GetTaskByPath(task.SourcePath)
	if completed.CompletedAt == nil {
		t.Error("完成时间未设置")
	}
}

func TestGetPendingTasks(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, _ := Init(dbPath)
	defer db.Close()

	// 创建多个任务
	for i := 0; i < 5; i++ {
		task := &Task{
			SourcePath:  filepath.Join("test", string(rune('a'+i))+".mp4"),
			SourceMtime: time.Now(),
			SourceSize:  1024000,
		}
		db.CreateTask(task)
	}

	// 获取待处理任务
	tasks, err := db.GetPendingTasks(3, 3)
	if err != nil {
		t.Fatalf("获取待处理任务失败: %v", err)
	}

	if len(tasks) != 3 {
		t.Errorf("期望获取3个任务，实际 %d", len(tasks))
	}
}

func TestClaimPendingTasks(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, _ := Init(dbPath)
	defer db.Close()

	// 创建多个任务
	for i := 0; i < 5; i++ {
		task := &Task{
			SourcePath:  filepath.Join("test", string(rune('a'+i))+".mp4"),
			SourceMtime: time.Now(),
			SourceSize:  1024000,
		}
		if err := db.CreateTask(task); err != nil {
			t.Fatalf("创建任务失败: %v", err)
		}
	}

	// Claim 3个任务
	tasks, err := db.ClaimPendingTasks(3, 3)
	if err != nil {
		t.Fatalf("Claim任务失败: %v", err)
	}

	if len(tasks) != 3 {
		t.Errorf("期望 claim 3个任务，实际 %d", len(tasks))
	}

	// 验证任务状态已变为 processing
	for _, task := range tasks {
		if task.Status != StatusProcessing {
			t.Errorf("任务 #%d 状态应为 processing，实际为 %s", task.ID, task.Status)
		}
	}

	// 再次查询pending任务，应该只剩2个
	pendingTasks, err := db.GetPendingTasks(10, 3)
	if err != nil {
		t.Fatalf("获取待处理任务失败: %v", err)
	}

	if len(pendingTasks) != 2 {
		t.Errorf("期望剩余2个pending任务，实际 %d", len(pendingTasks))
	}

	// 再次 claim，应该能 claim 剩余的2个
	moreTasks, err := db.ClaimPendingTasks(10, 3)
	if err != nil {
		t.Fatalf("第二次 Claim失败: %v", err)
	}

	if len(moreTasks) != 2 {
		t.Errorf("期望第二次 claim 2个任务，实际 %d", len(moreTasks))
	}

	// 再次 claim 应该返回空列表
	emptyTasks, err := db.ClaimPendingTasks(10, 3)
	if err != nil {
		t.Fatalf("第三次 Claim失败: %v", err)
	}

	if len(emptyTasks) != 0 {
		t.Errorf("期望第三次 claim 0个任务，实际 %d", len(emptyTasks))
	}
}

func TestGetStats(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, _ := Init(dbPath)
	defer db.Close()

	// 创建不同状态的任务
	tasks := []struct {
		path   string
		status TaskStatus
	}{
		{"a.mp4", StatusPending},
		{"b.mp4", StatusPending},
		{"c.mp4", StatusCompleted},
		{"d.mp4", StatusFailed},
	}

	for _, tc := range tasks {
		task := &Task{
			SourcePath:  tc.path,
			SourceMtime: time.Now(),
			SourceSize:  1024000,
		}
		db.CreateTask(task)
		if tc.status != StatusPending {
			db.UpdateTaskStatus(task.ID, tc.status, "")
		}
	}

	stats, err := db.GetStats()
	if err != nil {
		t.Fatalf("获取统计失败: %v", err)
	}

	if stats.PendingCount != 2 {
		t.Errorf("待处理数错误: 期望 2, 实际 %d", stats.PendingCount)
	}

	if stats.CompletedCount != 1 {
		t.Errorf("已完成数错误: 期望 1, 实际 %d", stats.CompletedCount)
	}

	if stats.FailedCount != 1 {
		t.Errorf("失败数错误: 期望 1, 实际 %d", stats.FailedCount)
	}
}

func TestResetTaskToPending(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, _ := Init(dbPath)
	defer db.Close()

	task := &Task{
		SourcePath:  "test/video.mp4",
		SourceMtime: time.Now(),
		SourceSize:  1024000,
	}
	db.CreateTask(task)

	// 更新为完成
	db.UpdateTaskStatus(task.ID, StatusCompleted, "完成")

	// 重置为待处理
	newMtime := time.Now().Add(1 * time.Hour)
	err := db.ResetTaskToPending(task.SourcePath, newMtime, 2048000)
	if err != nil {
		t.Fatalf("重置任务失败: %v", err)
	}

	// 验证重置
	reset, _ := db.GetTaskByPath(task.SourcePath)
	if reset.Status != StatusPending {
		t.Errorf("状态未重置: %s", reset.Status)
	}
	if reset.RetryCount != 0 {
		t.Errorf("重试次数未重置: %d", reset.RetryCount)
	}
	if reset.CompletedAt != nil {
		t.Error("完成时间未清除")
	}
}

// TestCleanupLifecycle 测试清理生命周期状态流转
func TestCleanupLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, _ := Init(dbPath)
	defer db.Close()

	// 创建任务并完成
	task := &Task{
		SourcePath:  "test/video.mp4",
		SourceMtime: time.Now(),
		SourceSize:  1024000,
	}
	db.CreateTask(task)
	db.UpdateTaskStatus(task.ID, StatusCompleted, "完成")

	// 标记为软删除
	trashPath := "/trash/video.mp4"
	err := db.MarkSoftDeleted(task.ID, trashPath)
	if err != nil {
		t.Fatalf("标记软删除失败: %v", err)
	}

	// 验证软删除状态
	updated, _ := db.GetTaskByPath(task.SourcePath)
	if updated.Status != StatusSoftDeleted {
		t.Errorf("状态错误: 期望 %s, 实际 %s", StatusSoftDeleted, updated.Status)
	}
	if updated.GetTrashPath() != trashPath {
		t.Errorf("垃圾桶路径错误: 期望 %s, 实际 %s", trashPath, updated.GetTrashPath())
	}
	if !updated.SourceDeletedAt.Valid {
		t.Error("source_deleted_at 应该被设置")
	}

	// 标记为硬删除
	err = db.MarkHardDeleted(task.ID)
	if err != nil {
		t.Fatalf("标记硬删除失败: %v", err)
	}

	// 验证硬删除状态
	updated, _ = db.GetTaskByPath(task.SourcePath)
	if updated.Status != StatusHardDeleted {
		t.Errorf("状态错误: 期望 %s, 实际 %s", StatusHardDeleted, updated.Status)
	}
}

// TestMarkCleanupError 测试清理失败标记
func TestMarkCleanupError(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, _ := Init(dbPath)
	defer db.Close()

	task := &Task{
		SourcePath:  "test/video.mp4",
		SourceMtime: time.Now(),
		SourceSize:  1024000,
	}
	db.CreateTask(task)
	db.UpdateTaskStatus(task.ID, StatusCompleted, "完成")

	// 标记清理失败
	cleanupLog := "权限不足"
	err := db.MarkCleanupError(task.ID, cleanupLog)
	if err != nil {
		t.Fatalf("标记清理失败: %v", err)
	}

	// 验证
	updated, _ := db.GetTaskByPath(task.SourcePath)
	if updated.Status != StatusCleanupError {
		t.Errorf("状态错误: 期望 %s, 实际 %s", StatusCleanupError, updated.Status)
	}
	if updated.GetCleanupLog() != cleanupLog {
		t.Errorf("清理日志错误: 期望 %s, 实际 %s", cleanupLog, updated.GetCleanupLog())
	}
}

func TestCleanupErrorRecovery(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, _ := Init(dbPath)
	defer db.Close()

	// 任务1：软删阶段失败（无 trash_path）
	task1 := &Task{
		SourcePath:  "test/video1.mp4",
		SourceMtime: time.Now(),
		SourceSize:  1024,
	}
	_ = db.CreateTask(task1)
	_ = db.UpdateTaskStatus(task1.ID, StatusCompleted, "完成")
	_ = db.MarkCleanupError(task1.ID, "权限不足")

	// 任务2：硬删阶段失败（有 trash_path）
	task2 := &Task{
		SourcePath:  "test/video2.mp4",
		SourceMtime: time.Now(),
		SourceSize:  2048,
	}
	_ = db.CreateTask(task2)
	_ = db.UpdateTaskStatus(task2.ID, StatusCompleted, "完成")
	_ = db.MarkSoftDeleted(task2.ID, "/trash/video2.mp4")
	_ = db.MarkCleanupError(task2.ID, "删除失败")

	// 查询 cleanup_error 列表
	cleanupErrors, err := db.GetCleanupErrorTasks(10)
	if err != nil {
		t.Fatalf("GetCleanupErrorTasks 失败: %v", err)
	}
	if len(cleanupErrors) != 2 {
		t.Fatalf("期望 2 个 cleanup_error 任务，实际 %d", len(cleanupErrors))
	}

	// 恢复状态
	if err := db.ResetCleanupErrorStatus(task1.ID, StatusCompleted, "自动重试恢复到 completed"); err != nil {
		t.Fatalf("恢复 task1 失败: %v", err)
	}
	if err := db.ResetCleanupErrorStatus(task2.ID, StatusSoftDeleted, "自动重试恢复到 soft_deleted"); err != nil {
		t.Fatalf("恢复 task2 失败: %v", err)
	}

	updated1, _ := db.GetTaskByPath(task1.SourcePath)
	if updated1.Status != StatusCompleted {
		t.Errorf("task1 状态错误: 期望 %s, 实际 %s", StatusCompleted, updated1.Status)
	}

	updated2, _ := db.GetTaskByPath(task2.SourcePath)
	if updated2.Status != StatusSoftDeleted {
		t.Errorf("task2 状态错误: 期望 %s, 实际 %s", StatusSoftDeleted, updated2.Status)
	}
}

// TestGetSoftDeletedOldTasks 测试获取超时的软删除任务
func TestGetSoftDeletedOldTasks(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, _ := Init(dbPath)
	defer db.Close()

	// 创建两个任务
	task1 := &Task{
		SourcePath:  "test/video1.mp4",
		SourceMtime: time.Now(),
		SourceSize:  1024000,
	}
	task2 := &Task{
		SourcePath:  "test/video2.mp4",
		SourceMtime: time.Now(),
		SourceSize:  2048000,
	}

	db.CreateTask(task1)
	db.CreateTask(task2)
	db.UpdateTaskStatus(task1.ID, StatusCompleted, "完成")
	db.UpdateTaskStatus(task2.ID, StatusCompleted, "完成")

	// task1 软删除（旧）
	db.MarkSoftDeleted(task1.ID, "/trash/video1.mp4")
	// 手动修改时间为2天前
	db.conn.Exec("UPDATE tasks SET source_deleted_at = ? WHERE id = ?",
		time.Now().Add(-48*time.Hour), task1.ID)

	// task2 软删除（新）
	db.MarkSoftDeleted(task2.ID, "/trash/video2.mp4")

	// 查询1天前软删除的任务
	cutoff := time.Now().Add(-24 * time.Hour)
	tasks, err := db.GetSoftDeletedOldTasks(cutoff)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}

	// 应该只有 task1
	if len(tasks) != 1 {
		t.Errorf("任务数错误: 期望 1, 实际 %d", len(tasks))
	}
	if len(tasks) > 0 && tasks[0].ID != task1.ID {
		t.Errorf("任务ID错误: 期望 %d, 实际 %d", task1.ID, tasks[0].ID)
	}
}

// TestStatsWithCleanup 测试包含清理状态的统计
func TestStatsWithCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, _ := Init(dbPath)
	defer db.Close()

	// 创建不同状态的任务
	tasks := []*Task{
		{SourcePath: "pending.mp4", SourceMtime: time.Now(), SourceSize: 1000},
		{SourcePath: "processing.mp4", SourceMtime: time.Now(), SourceSize: 2000},
		{SourcePath: "completed.mp4", SourceMtime: time.Now(), SourceSize: 3000},
		{SourcePath: "failed.mp4", SourceMtime: time.Now(), SourceSize: 4000},
		{SourcePath: "soft_deleted.mp4", SourceMtime: time.Now(), SourceSize: 5000},
		{SourcePath: "hard_deleted.mp4", SourceMtime: time.Now(), SourceSize: 6000},
		{SourcePath: "cleanup_error.mp4", SourceMtime: time.Now(), SourceSize: 7000},
	}

	for i, task := range tasks {
		db.CreateTask(task)
		switch i {
		case 0:
			// pending - 保持默认
		case 1:
			db.UpdateTaskStatus(task.ID, StatusProcessing, "")
		case 2:
			db.UpdateTaskStatus(task.ID, StatusCompleted, "")
		case 3:
			db.UpdateTaskStatus(task.ID, StatusFailed, "")
		case 4:
			db.UpdateTaskStatus(task.ID, StatusCompleted, "")
			db.MarkSoftDeleted(task.ID, "/trash/soft_deleted.mp4")
		case 5:
			db.UpdateTaskStatus(task.ID, StatusCompleted, "")
			db.MarkSoftDeleted(task.ID, "/trash/hard_deleted.mp4")
			db.MarkHardDeleted(task.ID)
		case 6:
			db.UpdateTaskStatus(task.ID, StatusCompleted, "")
			db.MarkCleanupError(task.ID, "error")
		}
	}

	stats, err := db.GetStats()
	if err != nil {
		t.Fatalf("获取统计失败: %v", err)
	}

	if stats.PendingCount != 1 {
		t.Errorf("PendingCount 错误: 期望 1, 实际 %d", stats.PendingCount)
	}
	if stats.ProcessingCount != 1 {
		t.Errorf("ProcessingCount 错误: 期望 1, 实际 %d", stats.ProcessingCount)
	}
	if stats.CompletedCount != 1 {
		t.Errorf("CompletedCount 错误: 期望 1, 实际 %d", stats.CompletedCount)
	}
	if stats.FailedCount != 1 {
		t.Errorf("FailedCount 错误: 期望 1, 实际 %d", stats.FailedCount)
	}
	if stats.SoftDeletedCount != 1 {
		t.Errorf("SoftDeletedCount 错误: 期望 1, 实际 %d", stats.SoftDeletedCount)
	}
	if stats.HardDeletedCount != 1 {
		t.Errorf("HardDeletedCount 错误: 期望 1, 实际 %d", stats.HardDeletedCount)
	}
	if stats.CleanupErrorCount != 1 {
		t.Errorf("CleanupErrorCount 错误: 期望 1, 实际 %d", stats.CleanupErrorCount)
	}
}

// TestEnsureColumnsIdempotent 测试列迁移的幂等性
func TestEnsureColumnsIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// 第一次初始化
	db1, err := Init(dbPath)
	if err != nil {
		t.Fatalf("第一次初始化失败: %v", err)
	}
	db1.Close()

	// 第二次初始化（应该幂等）
	db2, err := Init(dbPath)
	if err != nil {
		t.Fatalf("第二次初始化失败: %v", err)
	}
	defer db2.Close()

	// 创建任务验证所有字段可用
	task := &Task{
		SourcePath:  "test.mp4",
		SourceMtime: time.Now(),
		SourceSize:  1000,
	}
	task.SetTrashPath("/trash/test.mp4")
	task.SetCleanupLog("test log")

	err = db2.CreateTask(task)
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	// 测试新字段的读写
	err = db2.MarkSoftDeleted(task.ID, "/trash/test.mp4")
	if err != nil {
		t.Fatalf("MarkSoftDeleted 失败: %v", err)
	}

	retrieved, _ := db2.GetTaskByPath(task.SourcePath)
	if retrieved.Status != StatusSoftDeleted {
		t.Errorf("状态错误: %v", retrieved.Status)
	}
}

// TestPhase3HistoricalMigrationRunsOnce 测试历史补偿迁移只执行一次
func TestPhase3HistoricalMigrationRunsOnce(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "legacy.db")

	// 构造旧版本数据库（不含 Phase 3 新列和迁移标记表）
	legacyDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("打开旧数据库失败: %v", err)
	}

	legacySchema := `
	CREATE TABLE tasks (
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
	`
	if _, err := legacyDB.Exec(legacySchema); err != nil {
		legacyDB.Close()
		t.Fatalf("创建旧表结构失败: %v", err)
	}

	now := time.Now()
	if _, err := legacyDB.Exec(`
		INSERT INTO tasks (source_path, source_mtime, source_size, status, created_at, completed_at, log)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, "legacy/completed.mp4", now, 1024, StatusCompleted, now, now, "legacy"); err != nil {
		legacyDB.Close()
		t.Fatalf("写入旧数据失败: %v", err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatalf("关闭旧数据库失败: %v", err)
	}

	// 第一次初始化：应执行迁移并写入标记
	db1, err := Init(dbPath)
	if err != nil {
		t.Fatalf("第一次初始化失败: %v", err)
	}
	count1 := getMigrationMarkerCount(t, db1, migrationPhase3HistoricalCompatV1)
	if count1 != 1 {
		db1.Close()
		t.Fatalf("迁移标记数量错误: 期望 1, 实际 %d", count1)
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("关闭第一次初始化数据库失败: %v", err)
	}

	// 第二次初始化：应直接跳过迁移，标记数量保持 1
	db2, err := Init(dbPath)
	if err != nil {
		t.Fatalf("第二次初始化失败: %v", err)
	}
	defer db2.Close()

	count2 := getMigrationMarkerCount(t, db2, migrationPhase3HistoricalCompatV1)
	if count2 != 1 {
		t.Fatalf("迁移标记应保持幂等: 期望 1, 实际 %d", count2)
	}
}

func getMigrationMarkerCount(t *testing.T, db *DB, name string) int {
	t.Helper()

	var count int
	if err := db.conn.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, name).Scan(&count); err != nil {
		t.Fatalf("查询迁移标记失败: %v", err)
	}
	return count
}

// ===== Phase 4 W3 指数退避重试测试 =====

func TestScheduleRetry(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, _ := Init(dbPath)
	defer db.Close()

	// 创建测试任务
	task := &Task{
		SourcePath:  "test/video.mp4",
		SourceMtime: time.Now(),
		SourceSize:  1024000,
	}
	_ = db.CreateTask(task)
	_ = db.UpdateTaskStatus(task.ID, StatusFailed, "初始失败")

	// 设置重试时间
	nextRetryAt := time.Now().Add(5 * time.Minute)
	err := db.ScheduleRetry(task.ID, nextRetryAt, "timeout")
	if err != nil {
		t.Fatalf("ScheduleRetry 失败: %v", err)
	}

	// 验证字段已设置
	retrieved, _ := db.GetTaskByPath("test/video.mp4")
	if !retrieved.NextRetryAt.Valid {
		t.Error("next_retry_at 未设置")
	}
	if !retrieved.LastErrorCategory.Valid || retrieved.LastErrorCategory.String != "timeout" {
		t.Error("last_error_category 未正确设置")
	}

	// 验证时间戳接近（允许1秒误差）
	if retrieved.NextRetryAt.Valid {
		diff := retrieved.NextRetryAt.Time.Sub(nextRetryAt)
		if diff < -time.Second || diff > time.Second {
			t.Errorf("next_retry_at 时间不符: 期望 %v, 实际 %v", nextRetryAt, retrieved.NextRetryAt.Time)
		}
	}
}

func TestGetPendingTasksRespectsNextRetryAt(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, _ := Init(dbPath)
	defer db.Close()

	// 创建三个任务
	task1 := &Task{SourcePath: "test/1.mp4", SourceMtime: time.Now(), SourceSize: 1024}
	task2 := &Task{SourcePath: "test/2.mp4", SourceMtime: time.Now(), SourceSize: 1024}
	task3 := &Task{SourcePath: "test/3.mp4", SourceMtime: time.Now(), SourceSize: 1024}
	_ = db.CreateTask(task1)
	_ = db.CreateTask(task2)
	_ = db.CreateTask(task3)

	// task1: 无 next_retry_at（应返回）
	// task2: status=failed, next_retry_at 在过去（应返回）
	// task3: status=failed, next_retry_at 在未来（不应返回）

	// task1: 保持 pending 状态，无 next_retry_at
	_ = db.UpdateTaskStatus(task1.ID, StatusPending, "")

	// task2 和 task3: 设置为 failed，然后设置 next_retry_at
	_ = db.UpdateTaskStatus(task2.ID, StatusFailed, "失败")
	_ = db.UpdateTaskStatus(task3.ID, StatusFailed, "失败")

	// task2 设置为过去时间，task3 设置为未来时间
	// 注意：ScheduleRetry 会转换为 UTC 存储
	pastTime := time.Now().UTC().Add(-10 * time.Minute)
	futureTime := time.Now().UTC().Add(10 * time.Minute)
	_ = db.ScheduleRetry(task2.ID, pastTime, "io_error")
	_ = db.ScheduleRetry(task3.ID, futureTime, "timeout")

	// 等待一小段时间确保数据库时间戳一致
	time.Sleep(10 * time.Millisecond)

	// 获取待处理任务
	tasks, err := db.GetPendingTasks(10, 3)
	if err != nil {
		t.Fatalf("GetPendingTasks 失败: %v", err)
	}

	// 应该返回task1 和 task2，不应包含 task3
	if len(tasks) != 2 {
		t.Errorf("期望返回 2 个任务，实际返回 %d 个", len(tasks))
	}

	foundTask3 := false
	for _, task := range tasks {
		if task.ID == task3.ID {
			foundTask3 = true
			break
		}
	}
	if foundTask3 {
		t.Error("不应返回 next_retry_at 在未来的任务")
	}
}

func TestClaimPendingTasksRespectsNextRetryAt(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, _ := Init(dbPath)
	defer db.Close()

	// 创建两个任务
	task1 := &Task{SourcePath: "test/1.mp4", SourceMtime: time.Now(), SourceSize: 1024}
	task2 := &Task{SourcePath: "test/2.mp4", SourceMtime: time.Now(), SourceSize: 1024}
	_ = db.CreateTask(task1)
	_ = db.CreateTask(task2)

	// task1: 可立即重试
	// task2: 需要等待（未来）
	_ = db.UpdateTaskStatus(task1.ID, StatusPending, "")
	_ = db.UpdateTaskStatus(task2.ID, StatusPending, "")
	_ = db.ScheduleRetry(task2.ID, time.Now().Add(5*time.Minute), "io_error")

	// Claim 任务
	tasks, err := db.ClaimPendingTasks(10, 3)
	if err != nil {
		t.Fatalf("ClaimPendingTasks 失败: %v", err)
	}

	// 应该只返回 task1
	if len(tasks) != 1 {
		t.Fatalf("期望 claim 1 个任务，实际 claim %d 个", len(tasks))
	}
	if tasks[0].ID != task1.ID {
		t.Error("claim 的任务 ID 不正确")
	}

	// 验证 task1 状态已更新为 processing
	retrieved1, _ := db.GetTaskByPath("test/1.mp4")
	if retrieved1.Status != StatusProcessing {
		t.Errorf("task1 状态应为 processing，实际为 %s", retrieved1.Status)
	}

	// 验证 task2 仍为 pending
	retrieved2, _ := db.GetTaskByPath("test/2.mp4")
	if retrieved2.Status != StatusPending {
		t.Errorf("task2 状态应保持 pending，实际为 %s", retrieved2.Status)
	}
}

func TestManualRetryClearsNextRetryAt(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, _ := Init(dbPath)
	defer db.Close()

	// 测试单个任务重置
	task1 := &Task{SourcePath: "test/1.mp4", SourceMtime: time.Now(), SourceSize: 1024}
	_ = db.CreateTask(task1)
	_ = db.UpdateTaskStatus(task1.ID, StatusFailed, "失败")
	_ = db.ScheduleRetry(task1.ID, time.Now().Add(10*time.Minute), "timeout")

	// 手动重试（通过 UpdateTaskStatus 设置为 Pending）
	_ = db.UpdateTaskStatus(task1.ID, StatusPending, "手动重试")

	retrieved1, _ := db.GetTaskByPath("test/1.mp4")
	if retrieved1.NextRetryAt.Valid {
		t.Error("UpdateTaskStatus 应清空 next_retry_at")
	}
	if retrieved1.LastErrorCategory.Valid {
		t.Error("UpdateTaskStatus 应清空 last_error_category")
	}

	// 测试批量重置失败任务
	task2 := &Task{SourcePath: "test/2.mp4", SourceMtime: time.Now(), SourceSize: 1024}
	task3 := &Task{SourcePath: "test/3.mp4", SourceMtime: time.Now(), SourceSize: 1024}
	_ = db.CreateTask(task2)
	_ = db.CreateTask(task3)
	_ = db.UpdateTaskStatus(task2.ID, StatusFailed, "失败2")
	_ = db.UpdateTaskStatus(task3.ID, StatusFailed, "失败3")
	_ = db.ScheduleRetry(task2.ID, time.Now().Add(5*time.Minute), "io_error")
	_ = db.ScheduleRetry(task3.ID, time.Now().Add(10*time.Minute), "invalid_data")

	count, _ := db.ResetFailedTasksToPending()
	if count != 2 {
		t.Errorf("期望重置 2 个失败任务，实际重置 %d 个", count)
	}

	retrieved2, _ := db.GetTaskByPath("test/2.mp4")
	retrieved3, _ := db.GetTaskByPath("test/3.mp4")
	if retrieved2.NextRetryAt.Valid || retrieved3.NextRetryAt.Valid {
		t.Error("ResetFailedTasksToPending 应清空 next_retry_at")
	}

	// 测试批量重置 processing 任务
	task4 := &Task{SourcePath: "test/4.mp4", SourceMtime: time.Now(), SourceSize: 1024}
	_ = db.CreateTask(task4)
	_ = db.UpdateTaskStatus(task4.ID, StatusProcessing, "处理中")
	_ = db.ScheduleRetry(task4.ID, time.Now().Add(1*time.Minute), "timeout")

	count2, _ := db.ResetProcessingTasksToPending()
	if count2 != 1 {
		t.Errorf("期望重置 1 个处理中任务，实际重置 %d 个", count2)
	}

	retrieved4, _ := db.GetTaskByPath("test/4.mp4")
	if retrieved4.NextRetryAt.Valid {
		t.Error("ResetProcessingTasksToPending 应清空 next_retry_at")
	}
}

func seedPendingTasksForBenchmark(b *testing.B, db *DB, count int) {
	b.Helper()

	tx, err := db.conn.Begin()
	if err != nil {
		b.Fatalf("开始事务失败: %v", err)
	}

	stmt, err := tx.Prepare(`
		INSERT INTO tasks (source_path, source_mtime, source_size, status, repair_mode)
		VALUES (?, ?, ?, ?, '')
	`)
	if err != nil {
		_ = tx.Rollback()
		b.Fatalf("准备语句失败: %v", err)
	}
	defer stmt.Close()

	now := time.Now()
	for i := 0; i < count; i++ {
		path := fmt.Sprintf("bench/video_%05d.mp4", i)
		if _, err := stmt.Exec(path, now, 1024, StatusPending); err != nil {
			_ = tx.Rollback()
			b.Fatalf("插入任务失败: %v", err)
		}
	}

	if err := tx.Commit(); err != nil {
		b.Fatalf("提交事务失败: %v", err)
	}
}

func BenchmarkClaimPendingTasks10k(b *testing.B) {
	const (
		taskCount = 10000
		batchSize = 256
	)

	originalLogWriter := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(originalLogWriter)

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		tmpDir := b.TempDir()
		dbPath := filepath.Join(tmpDir, "bench.db")
		db, err := Init(dbPath)
		if err != nil {
			b.Fatalf("初始化数据库失败: %v", err)
		}

		seedPendingTasksForBenchmark(b, db, taskCount)

		b.StartTimer()
		claimed := 0
		for claimed < taskCount {
			tasks, err := db.ClaimPendingTasks(batchSize, 3)
			if err != nil {
				b.Fatalf("ClaimPendingTasks 失败: %v", err)
			}
			if len(tasks) == 0 {
				break
			}
			claimed += len(tasks)
		}
		b.StopTimer()

		if claimed != taskCount {
			b.Fatalf("claim 数量不完整: 期望 %d, 实际 %d", taskCount, claimed)
		}

		_ = db.Close()
	}
}

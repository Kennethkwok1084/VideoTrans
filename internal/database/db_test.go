package database

import (
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
	tasks, err := db.GetPendingTasks(3)
	if err != nil {
		t.Fatalf("获取待处理任务失败: %v", err)
	}

	if len(tasks) != 3 {
		t.Errorf("期望获取3个任务，实际 %d", len(tasks))
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

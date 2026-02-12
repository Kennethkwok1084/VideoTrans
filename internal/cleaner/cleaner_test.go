package cleaner

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stm/video-transcoder/internal/config"
	"github.com/stm/video-transcoder/internal/database"
)

func TestNew(t *testing.T) {
	cfg := &config.Config{
		Path: config.PathConfig{
			Trash: "/test/trash",
		},
		Cleaning: config.CleaningConfig{
			SoftDeleteDays: 7,
			HardDeleteDays: 30,
		},
	}

	c := New(cfg, nil)

	if c == nil {
		t.Fatal("New() 返回 nil")
	}

	if c.config.Path.Trash != "/test/trash" {
		t.Errorf("Trash path = %s, want /test/trash", c.config.Path.Trash)
	}

	if c.config.Cleaning.SoftDeleteDays != 7 {
		t.Errorf("SoftDeleteDays = %d, want 7", c.config.Cleaning.SoftDeleteDays)
	}

	if c.config.Cleaning.HardDeleteDays != 30 {
		t.Errorf("HardDeleteDays = %d, want 30", c.config.Cleaning.HardDeleteDays)
	}
}

func TestSafeMoveToTrash(t *testing.T) {
	// 此测试验证跨设备移动的逻辑
	tempDir := t.TempDir()
	trashRoot := filepath.Join(tempDir, "trash")

	cfg := &config.Config{
		Path: config.PathConfig{
			Trash: trashRoot,
		},
	}

	c := New(cfg, nil)

	testFile := filepath.Join(tempDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("创建测试文件失败: %v", err)
	}

	// 调用 safeMoveToTrash
	trashPath, err := c.safeMoveToTrash(testFile)
	if err != nil {
		t.Fatalf("safeMoveToTrash() 失败: %v", err)
	}
	if trashPath == "" {
		t.Fatal("应该返回垃圾桶路径")
	}

	// 验证源文件被删除
	if _, err := os.Stat(testFile); !os.IsNotExist(err) {
		t.Error("源文件应该被删除")
	}
}

func TestMoveToTrashIntegration(t *testing.T) {
	// 创建临时测试目录
	tempDir := t.TempDir()
	trashName := ".stm_trash"

	cfg := &config.Config{
		Path: config.PathConfig{
			Input: tempDir,
			Trash: trashName,
		},
		Cleaning: config.CleaningConfig{
			SoftDeleteDays: 1,
			HardDeleteDays: 2,
		},
	}

	// 创建数据库
	dbPath := filepath.Join(tempDir, "test.db")
	db, err := database.Init(dbPath)
	if err != nil {
		t.Fatalf("初始化数据库失败: %v", err)
	}
	defer db.Close()

	c := New(cfg, db)

	// 创建测试文件
	testFile := filepath.Join(tempDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("创建测试文件失败: %v", err)
	}

	// 测试移动到回收站
	trashPath, err := c.safeMoveToTrash(testFile)
	if err != nil {
		t.Fatalf("safeMoveToTrash() 失败: %v", err)
	}
	if trashPath == "" {
		t.Fatal("应该返回垃圾桶路径")
	}

	// 验证原文件被删除
	if _, err := os.Stat(testFile); !os.IsNotExist(err) {
		t.Error("原文件应该被删除")
	}

	// 验证文件在回收站
	trashRoot := filepath.Join(tempDir, trashName)
	entries, err := os.ReadDir(trashRoot)
	if err != nil {
		t.Fatalf("读取垃圾桶失败: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("垃圾桶中未找到文件")
	}
}

func TestRetryCleanupErrors(t *testing.T) {
	tempDir := t.TempDir()
	cfg := &config.Config{
		Path: config.PathConfig{
			Input: tempDir,
			Trash: ".stm_trash",
		},
		Cleaning: config.CleaningConfig{
			SoftDeleteDays: 1,
			HardDeleteDays: 2,
		},
	}

	dbPath := filepath.Join(tempDir, "test.db")
	db, err := database.Init(dbPath)
	if err != nil {
		t.Fatalf("初始化数据库失败: %v", err)
	}
	defer db.Close()

	c := New(cfg, db)

	// 任务1：无 trash_path，cleanup_error 应恢复为 completed
	task1 := &database.Task{
		SourcePath:  filepath.Join(tempDir, "video1.mp4"),
		SourceMtime: time.Now(),
		SourceSize:  1024,
	}
	if err := os.WriteFile(task1.SourcePath, []byte("x"), 0644); err != nil {
		t.Fatalf("创建测试文件失败: %v", err)
	}
	_ = db.CreateTask(task1)
	_ = db.UpdateTaskStatus(task1.ID, database.StatusCompleted, "完成")
	_ = db.MarkCleanupError(task1.ID, "权限不足")

	// 任务2：有 trash_path，cleanup_error 应恢复为 soft_deleted
	task2 := &database.Task{
		SourcePath:  filepath.Join(tempDir, "video2.mp4"),
		SourceMtime: time.Now(),
		SourceSize:  2048,
	}
	_ = db.CreateTask(task2)
	_ = db.UpdateTaskStatus(task2.ID, database.StatusCompleted, "完成")
	_ = db.MarkSoftDeleted(task2.ID, filepath.Join(tempDir, ".stm_trash", "video2.mp4"))
	_ = db.MarkCleanupError(task2.ID, "删除失败")

	if err := c.retryCleanupErrors(); err != nil {
		t.Fatalf("retryCleanupErrors 失败: %v", err)
	}

	updated1, _ := db.GetTaskByPath(task1.SourcePath)
	if updated1.Status != database.StatusCompleted {
		t.Errorf("task1 状态错误: 期望 %s, 实际 %s", database.StatusCompleted, updated1.Status)
	}

	updated2, _ := db.GetTaskByPath(task2.SourcePath)
	if updated2.Status != database.StatusSoftDeleted {
		t.Errorf("task2 状态错误: 期望 %s, 实际 %s", database.StatusSoftDeleted, updated2.Status)
	}
}

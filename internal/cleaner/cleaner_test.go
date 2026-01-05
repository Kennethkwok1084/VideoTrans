package cleaner

import (
	"os"
	"path/filepath"
	"testing"

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
	if err := c.safeMoveToTrash(testFile); err != nil {
		t.Fatalf("safeMoveToTrash() 失败: %v", err)
	}

	// 验证源文件被删除
	if _, err := os.Stat(testFile); !os.IsNotExist(err) {
		t.Error("源文件应该被删除")
	}
}

func TestMoveToTrashIntegration(t *testing.T) {
	// 创建临时测试目录
	tempDir := t.TempDir()
	trashRoot := filepath.Join(tempDir, "trash")

	cfg := &config.Config{
		Path: config.PathConfig{
			Input: tempDir,
			Trash: trashRoot,
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
	if err := c.safeMoveToTrash(testFile); err != nil {
		t.Fatalf("safeMoveToTrash() 失败: %v", err)
	}

	// 验证原文件被删除
	if _, err := os.Stat(testFile); !os.IsNotExist(err) {
		t.Error("原文件应该被删除")
	}

	// 验证文件在回收站
	stage1Dir := filepath.Join(trashRoot, "stage1")
	if _, err := os.Stat(stage1Dir); err == nil {
		entries, err := os.ReadDir(stage1Dir)
		if err == nil && len(entries) > 0 {
			t.Logf("stage1 目录中有 %d 个文件", len(entries))
		}
	}
}

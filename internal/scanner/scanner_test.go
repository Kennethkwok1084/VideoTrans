package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stm/video-transcoder/internal/config"
	"github.com/stm/video-transcoder/internal/database"
)

func setupTestScanner(t *testing.T) (*Scanner, *database.DB, string) {
	tmpDir := t.TempDir()

	// 创建测试目录
	inputDir := filepath.Join(tmpDir, "input")
	outputDir := filepath.Join(tmpDir, "output")
	dbPath := filepath.Join(tmpDir, "test.db")

	os.MkdirAll(inputDir, 0755)
	os.MkdirAll(outputDir, 0755)

	cfg := &config.Config{
		System: config.SystemConfig{ScanInterval: 10},
		Path: config.PathConfig{
			Input:    inputDir,
			Output:   outputDir,
			Database: dbPath,
		},
		FFmpeg: config.FFmpegConfig{
			Extensions: []string{".mp4", ".mkv", ".avi"},
		},
	}

	db, _ := database.Init(dbPath)
	scanner := New(cfg, db)

	return scanner, db, inputDir
}

func TestShouldSkipDir(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{".stm_trash", true},
		{"@eaDir", true},
		{"#recycle", true},
		{".DS_Store", true},
		{"normal_dir", false},
		{"videos", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldSkipDir(tt.name); got != tt.want {
				t.Errorf("shouldSkipDir(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestShouldSkipFile(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"SYNOPHOTO_FILM_M.mp4", true},
		{"SYNOPHOTO_THUMB.jpg", true},
		{".hidden", true},
		{"video.tmp", true},
		{"video.part", true},
		{"video.lock", true},
		{"normal_video.mp4", false},
		{"movie.mkv", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldSkipFile(tt.name); got != tt.want {
				t.Errorf("shouldSkipFile(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestScanNewFile(t *testing.T) {
	scanner, db, inputDir := setupTestScanner(t)
	defer db.Close()

	// 创建测试视频文件
	testFile := filepath.Join(inputDir, "test.mp4")
	content := []byte("fake video content")
	if err := os.WriteFile(testFile, content, 0644); err != nil {
		t.Fatalf("创建测试文件失败: %v", err)
	}

	// 执行扫描
	ctx := context.Background()
	err := scanner.Scan(ctx)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}

	// 验证任务已创建
	task, err := db.GetTaskByPath("test.mp4")
	if err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}

	if task == nil {
		t.Fatal("任务未创建")
	}

	if task.Status != database.StatusPending {
		t.Errorf("任务状态错误: %s", task.Status)
	}
}

func TestScanSkipsSystemFiles(t *testing.T) {
	scanner, db, inputDir := setupTestScanner(t)
	defer db.Close()

	// 创建系统文件（应被跳过）
	systemFiles := []string{
		"SYNOPHOTO_FILM_M.mp4",
		".hidden.mp4",
		"video.tmp",
	}

	for _, filename := range systemFiles {
		testFile := filepath.Join(inputDir, filename)
		os.WriteFile(testFile, []byte("content"), 0644)
	}

	// 执行扫描
	ctx := context.Background()
	scanner.Scan(ctx)

	// 验证系统文件未被添加
	for _, filename := range systemFiles {
		task, _ := db.GetTaskByPath(filename)
		if task != nil {
			t.Errorf("系统文件不应被添加: %s", filename)
		}
	}
}

func TestScanSkipsSystemDirectories(t *testing.T) {
	scanner, db, inputDir := setupTestScanner(t)
	defer db.Close()

	// 创建系统目录
	systemDirs := []string{
		"@eaDir",
		"#recycle",
		".stm_trash",
	}

	for _, dirname := range systemDirs {
		dir := filepath.Join(inputDir, dirname)
		os.MkdirAll(dir, 0755)
		// 在系统目录中创建文件
		testFile := filepath.Join(dir, "video.mp4")
		os.WriteFile(testFile, []byte("content"), 0644)
	}

	// 执行扫描
	ctx := context.Background()
	scanner.Scan(ctx)

	// 验证系统目录中的文件未被添加
	for _, dirname := range systemDirs {
		task, _ := db.GetTaskByPath(filepath.Join(dirname, "video.mp4"))
		if task != nil {
			t.Errorf("系统目录中的文件不应被添加: %s", dirname)
		}
	}
}

func TestScanDetectsFileUpdate(t *testing.T) {
	scanner, db, inputDir := setupTestScanner(t)
	defer db.Close()

	// 创建初始文件
	testFile := filepath.Join(inputDir, "test.mp4")
	os.WriteFile(testFile, []byte("original"), 0644)

	// 第一次扫描
	ctx := context.Background()
	scanner.Scan(ctx)

	// 获取初始任务
	task1, _ := db.GetTaskByPath("test.mp4")
	if task1 == nil {
		t.Fatal("初始任务未创建")
	}

	// 更新任务状态为完成
	db.UpdateTaskStatus(task1.ID, database.StatusCompleted, "完成")

	// 修改文件
	time.Sleep(10 * time.Millisecond) // 确保修改时间不同
	os.WriteFile(testFile, []byte("updated content"), 0644)

	// 第二次扫描
	scanner.Scan(ctx)

	// 验证任务被重置
	task2, _ := db.GetTaskByPath("test.mp4")
	if task2.Status != database.StatusPending {
		t.Errorf("文件更新后任务应被重置为pending，实际: %s", task2.Status)
	}
}

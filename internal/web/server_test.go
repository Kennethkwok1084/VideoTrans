package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stm/video-transcoder/internal/config"
	"github.com/stm/video-transcoder/internal/database"
	"github.com/stm/video-transcoder/internal/scanner"
)

// mockScanRunner 模拟扫描器
type mockScanRunner struct {
	startScanAsyncErr error
}

func (m *mockScanRunner) Scan(ctx context.Context) error {
	return nil
}

func (m *mockScanRunner) StartScanAsync(ctx context.Context) error {
	return m.startScanAsyncErr
}

func TestHandleTriggerScan_Conflict(t *testing.T) {
	// Setup
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := database.Init(dbPath)
	if err != nil {
		t.Fatalf("Failed to init db: %v", err)
	}
	defer db.Close()

	cfg := &config.Config{
		Path: config.PathConfig{
			Input:  tmpDir,
			Output: tmpDir,
			Pairs:  []config.InputOutputPair{{Input: tmpDir, Output: tmpDir}},
		},
	}

	// Case 1: 正常启动扫描 (Mock返回nil)
	t.Run("Normal Start", func(t *testing.T) {
		mockScanner := &mockScanRunner{startScanAsyncErr: nil}
		s := &Server{
			config:  cfg,
			db:      db,
			scanner: mockScanner,
			router:  gin.New(),
		}
		s.router.POST("/api/scan", s.handleTriggerScan)

		req := httptest.NewRequest("POST", "/api/scan", nil)
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected 200 OK, got %d", w.Code)
		}
	})

	// Case 2: 扫描冲突 (Mock返回ErrScanInProgress)
	t.Run("Conflict", func(t *testing.T) {
		mockScanner := &mockScanRunner{startScanAsyncErr: scanner.ErrScanInProgress}
		s := &Server{
			config:  cfg,
			db:      db,
			scanner: mockScanner,
			router:  gin.New(),
		}
		s.router.POST("/api/scan", s.handleTriggerScan)

		req := httptest.NewRequest("POST", "/api/scan", nil)
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)

		if w.Code != http.StatusConflict {
			t.Errorf("Expected 409 Conflict, got %d", w.Code)
		}
	})

	// Case 3: 其他错误
	t.Run("Other Error", func(t *testing.T) {
		expectedErr := errors.New("db error")
		mockScanner := &mockScanRunner{startScanAsyncErr: expectedErr}
		s := &Server{
			config:  cfg,
			db:      db,
			scanner: mockScanner,
			router:  gin.New(),
		}
		s.router.POST("/api/scan", s.handleTriggerScan)

		req := httptest.NewRequest("POST", "/api/scan", nil)
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)

		if w.Code != http.StatusInternalServerError {
			t.Errorf("Expected 500 InternalServerError, got %d", w.Code)
		}
	})
}

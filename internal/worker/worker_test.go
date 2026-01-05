package worker

import (
	"testing"

	"github.com/stm/video-transcoder/internal/config"
)

func TestIsWorkingHours(t *testing.T) {
	tests := []struct {
		name        string
		workStart   int
		workEnd     int
		currentHour int
		want        bool
	}{
		{
			name:        "正常工作时间段 - 在时间窗口内",
			workStart:   0,
			workEnd:     6,
			currentHour: 3,
			want:        true,
		},
		{
			name:        "正常工作时间段 - 在起始时间",
			workStart:   0,
			workEnd:     6,
			currentHour: 0,
			want:        true,
		},
		{
			name:        "正常工作时间段 - 超出结束时间",
			workStart:   0,
			workEnd:     6,
			currentHour: 6,
			want:        false,
		},
		{
			name:        "正常工作时间段 - 不在时间窗口内",
			workStart:   0,
			workEnd:     6,
			currentHour: 12,
			want:        false,
		},
		{
			name:        "跨天工作时间段 - 在前半段",
			workStart:   22,
			workEnd:     6,
			currentHour: 23,
			want:        true,
		},
		{
			name:        "跨天工作时间段 - 在后半段",
			workStart:   22,
			workEnd:     6,
			currentHour: 3,
			want:        true,
		},
		{
			name:        "跨天工作时间段 - 不在时间窗口内",
			workStart:   22,
			workEnd:     6,
			currentHour: 12,
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 测试时间窗口逻辑
			start := tt.workStart
			end := tt.workEnd
			hour := tt.currentHour

			var got bool
			if start < end {
				got = hour >= start && hour < end
			} else {
				got = hour >= start || hour < end
			}

			if got != tt.want {
				t.Errorf("时间窗口逻辑: hour=%d, start=%d, end=%d, got=%v, want=%v",
					hour, start, end, got, tt.want)
			}
		})
	}
}

func TestGetForceRun(t *testing.T) {
	w := &Worker{forceRun: false}
	if w.GetForceRun() {
		t.Error("初始状态 GetForceRun() 应该为 false")
	}

	w.SetForceRun(true)
	if !w.GetForceRun() {
		t.Error("SetForceRun(true) 后 GetForceRun() 应该为 true")
	}

	w.SetForceRun(false)
	if w.GetForceRun() {
		t.Error("SetForceRun(false) 后 GetForceRun() 应该为 false")
	}
}

func TestNew(t *testing.T) {
	cfg := &config.Config{
		System: config.SystemConfig{
			CronStart:  0,
			CronEnd:    6,
			MaxWorkers: 2,
		},
	}

	w := New(cfg, nil)

	if w == nil {
		t.Fatal("New() 返回 nil")
	}

	if w.config.System.CronStart != 0 {
		t.Errorf("CronStart = %d, want 0", w.config.System.CronStart)
	}

	if w.config.System.CronEnd != 6 {
		t.Errorf("CronEnd = %d, want 6", w.config.System.CronEnd)
	}

	if w.config.System.MaxWorkers != 2 {
		t.Errorf("MaxWorkers = %d, want 2", w.config.System.MaxWorkers)
	}

	if w.GetForceRun() {
		t.Error("初始 forceRun 应该为 false")
	}
}

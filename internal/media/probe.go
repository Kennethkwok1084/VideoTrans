package media

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ProbeFile checks for a valid video stream and optionally decodes a short segment.
func ProbeFile(path string, timeout time.Duration, decodeSeconds int) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=codec_name,duration",
		"-of", "default=noprint_wrappers=1",
		path,
	)

	output, err := cmd.CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("ffprobe超时(%s): %w", timeout, ctx.Err())
	}
	if err != nil {
		return fmt.Errorf("视频流检查失败 (文件可能损坏): %w, output: %s", err, string(output))
	}
	if len(output) == 0 {
		return fmt.Errorf("无法检测到有效的视频流")
	}

	if decodeSeconds <= 0 {
		return nil
	}

	return decodeSegment(path, timeout, 0, decodeSeconds, "文件解码测试失败 (文件损坏或格式不支持)", false)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// DecodeSegment decodes a short segment to validate the stream (lenient mode).
func DecodeSegment(path string, timeout time.Duration, seekFromEndSeconds, decodeSeconds int) error {
	if decodeSeconds <= 0 {
		return nil
	}
	return decodeSegment(path, timeout, seekFromEndSeconds, decodeSeconds, "解码测试失败", false)
}

// DecodeSegmentStrict decodes a short segment and fails on any decoder error.
func DecodeSegmentStrict(path string, timeout time.Duration, seekFromEndSeconds, decodeSeconds int) error {
	if decodeSeconds <= 0 {
		return nil
	}
	return decodeSegment(path, timeout, seekFromEndSeconds, decodeSeconds, "解码测试失败", true)
}

func decodeSegment(path string, timeout time.Duration, seekFromEndSeconds, decodeSeconds int, reason string, xerror bool) error {
	decodeCtx, decodeCancel := context.WithTimeout(context.Background(), timeout)
	defer decodeCancel()

	args := []string{"-v", "error"}
	if xerror {
		args = append(args, "-xerror")
	}
	if seekFromEndSeconds > 0 {
		args = append(args, "-sseof", fmt.Sprintf("-%d", seekFromEndSeconds))
	}
	args = append(args,
		"-t", strconv.Itoa(decodeSeconds),
		"-i", path,
		"-f", "null",
		"-",
	)

	decodeCmd := exec.CommandContext(decodeCtx, "ffmpeg", args...)
	decodeOutput, decodeErr := decodeCmd.CombinedOutput()
	if errors.Is(decodeCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("解码测试超时(%s): %w", timeout, decodeCtx.Err())
	}
	if decodeErr != nil {
		errMsg := strings.TrimSpace(string(decodeOutput))
		if errMsg == "" {
			errMsg = decodeErr.Error()
		}
		return fmt.Errorf("%s: %s", reason, errMsg[:min(500, len(errMsg))])
	}

	return nil
}

// CountDecodeErrors decodes a short segment and counts decoder error lines.
func CountDecodeErrors(path string, timeout time.Duration, sampleSeconds int) (int, error) {
	if sampleSeconds <= 0 {
		return 0, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	args := []string{
		"-v", "error",
		"-t", strconv.Itoa(sampleSeconds),
		"-i", path,
		"-f", "null",
		"-",
	}

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	output, err := cmd.CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return 0, fmt.Errorf("解码统计超时(%s): %w", timeout, ctx.Err())
	}

	count := 0
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if isDecodeErrorLine(line) {
			count++
		}
	}

	if err != nil && count == 0 {
		return 0, fmt.Errorf("解码统计失败: %w", err)
	}
	return count, nil
}

func isDecodeErrorLine(line string) bool {
	lower := strings.ToLower(line)
	if strings.Contains(lower, "invalid") || strings.Contains(lower, "error") || strings.Contains(lower, "corrupt") {
		return true
	}
	if strings.Contains(lower, "missing picture") || strings.Contains(lower, "non-existing pps") {
		return true
	}
	if strings.Contains(lower, "no frame") || strings.Contains(lower, "duplicate") {
		return true
	}
	if strings.Contains(lower, "reserved bit") {
		return true
	}
	return false
}

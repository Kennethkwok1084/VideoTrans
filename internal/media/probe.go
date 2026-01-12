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

	decodeCtx, decodeCancel := context.WithTimeout(context.Background(), timeout)
	defer decodeCancel()

	decodeCmd := exec.CommandContext(decodeCtx, "ffmpeg",
		"-v", "error",
		"-t", strconv.Itoa(decodeSeconds),
		"-i", path,
		"-f", "null",
		"-",
	)

	decodeOutput, decodeErr := decodeCmd.CombinedOutput()
	if errors.Is(decodeCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("解码测试超时(%s): %w", timeout, decodeCtx.Err())
	}
	if decodeErr != nil {
		errMsg := string(decodeOutput)
		if strings.Contains(errMsg, "Invalid") || strings.Contains(errMsg, "Error") {
			return fmt.Errorf("文件解码测试失败 (文件损坏或格式不支持): %s", errMsg[:min(500, len(errMsg))])
		}
	}

	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

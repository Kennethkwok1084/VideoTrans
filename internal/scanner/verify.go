package scanner

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stm/video-transcoder/internal/database"
	"github.com/stm/video-transcoder/internal/media"
)

func (s *Scanner) verifyCompletedOutputs(ctx context.Context) error {
	if !s.config.FFmpeg.StrictCheck {
		return nil
	}

	log.Println("[Scanner] 开始校验已完成任务的输出文件")
	batchSize := 200
	offset := 0
	checked := 0
	requeued := 0
	missing := 0

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		tasks, err := s.db.GetAllTasks(string(database.StatusCompleted), batchSize, offset)
		if err != nil {
			return err
		}
		if len(tasks) == 0 {
			break
		}

		for _, task := range tasks {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			basePath, ok := s.resolveOutputPath(task.SourcePath)
			if !ok {
				continue
			}
			primaryPath := s.config.ApplyOutputExtension(basePath)
			checkPath := primaryPath

			if _, err := os.Stat(primaryPath); os.IsNotExist(err) {
				if primaryPath != basePath {
					if _, err := os.Stat(basePath); err == nil {
						checkPath = basePath
					} else if os.IsNotExist(err) {
						if err := s.resetTaskForRecode(task, "输出文件缺失，重新转码"); err != nil {
							log.Printf("[Scanner] 重置任务失败 %s: %v", task.SourcePath, err)
							continue
						}
						missing++
						continue
					} else {
						log.Printf("[Scanner] 输出文件访问失败 %s: %v", basePath, err)
						continue
					}
				} else {
					if err := s.resetTaskForRecode(task, "输出文件缺失，重新转码"); err != nil {
						log.Printf("[Scanner] 重置任务失败 %s: %v", task.SourcePath, err)
						continue
					}
					missing++
					continue
				}
			} else if err != nil {
				log.Printf("[Scanner] 输出文件访问失败 %s: %v", primaryPath, err)
				continue
			}

			probeTimeout := time.Duration(s.config.FFmpeg.ProbeTimeoutSeconds) * time.Second
			if err := media.ProbeFile(checkPath, probeTimeout, 0); err != nil {
				log.Printf("[Scanner] 输出文件损坏: %s, err=%v", checkPath, err)
				if removeErr := os.Remove(checkPath); removeErr != nil {
					log.Printf("[Scanner] 删除损坏输出失败 %s: %v", checkPath, removeErr)
					continue
				}
				if err := s.resetTaskForRecode(task, "输出文件损坏，已删除并重新转码"); err != nil {
					log.Printf("[Scanner] 重置任务失败 %s: %v", task.SourcePath, err)
					continue
				}
				requeued++
				continue
			}

			decodeSeconds := s.config.FFmpeg.VerifyDecodeSeconds
			if decodeSeconds > 0 {
				if err := media.DecodeSegmentStrict(checkPath, probeTimeout, 0, decodeSeconds); err != nil {
					log.Printf("[Scanner] 输出文件损坏: %s, err=%v", checkPath, err)
					if removeErr := os.Remove(checkPath); removeErr != nil {
						log.Printf("[Scanner] 删除损坏输出失败 %s: %v", checkPath, removeErr)
						continue
					}
					if err := s.resetTaskForRecode(task, "输出文件损坏，已删除并重新转码"); err != nil {
						log.Printf("[Scanner] 重置任务失败 %s: %v", task.SourcePath, err)
						continue
					}
					requeued++
					continue
				}
			}

			if decodeSeconds > 0 && s.config.FFmpeg.VerifyTailSeekSeconds > 0 {
				if err := media.DecodeSegmentStrict(checkPath, probeTimeout, s.config.FFmpeg.VerifyTailSeekSeconds, decodeSeconds); err != nil {
					log.Printf("[Scanner] 输出文件损坏: %s, err=%v", checkPath, err)
					if removeErr := os.Remove(checkPath); removeErr != nil {
						log.Printf("[Scanner] 删除损坏输出失败 %s: %v", checkPath, removeErr)
						continue
					}
					if err := s.resetTaskForRecode(task, "输出文件损坏，已删除并重新转码"); err != nil {
						log.Printf("[Scanner] 重置任务失败 %s: %v", task.SourcePath, err)
						continue
					}
					requeued++
					continue
				}
			}

			checked++
		}

		offset += len(tasks)
	}

	log.Printf("[Scanner] 输出文件校验完成: checked=%d, missing=%d, requeued=%d",
		checked, missing, requeued)
	return nil
}

func (s *Scanner) resolveOutputPath(inputPath string) (string, bool) {
	pairs := s.config.GetPairs()
	for _, pair := range pairs {
		if rel, err := filepath.Rel(pair.Input, inputPath); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.Join(pair.Output, rel), true
		}
	}
	return "", false
}

func (s *Scanner) resetTaskForRecode(task *database.Task, reason string) error {
	info, err := os.Stat(task.SourcePath)
	if err != nil {
		return err
	}

	if err := s.db.ResetTaskToPending(task.SourcePath, info.ModTime(), info.Size()); err != nil {
		return err
	}

	if reason != "" {
		return s.db.UpdateTaskStatus(task.ID, database.StatusPending, reason)
	}
	return nil
}

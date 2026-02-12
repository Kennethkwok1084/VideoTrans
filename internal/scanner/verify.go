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

func (s *Scanner) verifyCompletedOutputs(ctx context.Context, since time.Time) (time.Time, error) {
	if !s.config.FFmpeg.StrictCheck {
		// 如果禁用了严格检查，我们仍需返回当前时间作为水位，
		// 否则 lastVerifyTime 不会更新。
		// 但这样做会导致禁用期间完成的任务被永久跳过。
		// 如果希望保留历史记录以便后续开启检查，应返回 since。
		// 但这意味着每次 Scan 都会“空转”并不断尝试校验历史任务（尽管被短路返回），
		// 这取决于我们是否希望“禁用期间的任务在重新启用后被补检”。
		// 用户反馈指出：如果返回 time.Now()，后续开启会跳过。
		// 因此应该返回 since。
		// 但如果仅仅返回 since，下次 Scan 又会传入这个 old since，
		// 然后又立即返回，看似没问题。
		// 实际上，如果 StrictCheck 是动态配置（如热重载），这样是合理的。
		return since, nil
	}

	upperBound := time.Now()
	log.Printf("[Scanner] 开始校验已完成任务的输出文件 (since: %v, upper: %v)", since.Format(time.RFC3339), upperBound.Format(time.RFC3339))

	cursorTime := since
	cursorID := int64(0)
	limit := 100

	checked := 0
	requeued := 0
	missing := 0

	for {
		select {
		case <-ctx.Done():
			return since, ctx.Err()
		default:
		}

		tasks, err := s.db.GetBatchTasksCompleted(cursorTime, cursorID, upperBound, limit)
		if err != nil {
			return since, err
		}

		if len(tasks) == 0 {
			break
		}

		log.Printf("[Scanner] 本批次校验任务数: %d", len(tasks))

		for _, task := range tasks {
			select {
			case <-ctx.Done():
				return since, ctx.Err()
			default:
			}

			// 如果遇到非预期的错误（如系统错误），我们不能简单地跳过，否则会漏检。
			// 策略：记录错误并停止推进，返回当前已安全校验的水位。
			// 对于内容校验失败（损坏/缺失），resetTaskForRecode 算作“已处理”，不阻塞水位。

			basePath, ok := s.resolveOutputPath(task.SourcePath)
			if !ok {
				// 路径解析失败，可能是配置变更。这是一个严重问题。
				// 如果我们跳过，这个任务将永远不再校验。
				// 保守策略：停止校验，等待修复。
				log.Printf("[Scanner] 路径解析失败，停止校验: %s", task.SourcePath)
				// 不更新水位，下次重试
				return cursorTime, ErrVerifyAborted
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
							// DB 操作失败，系统不稳定，停止推进
							return cursorTime, ErrVerifyAborted
						}
						missing++
						// 成功处理（重置），继续
						goto Verified
					} else {
						log.Printf("[Scanner] 输出文件访问失败 %s: %v", basePath, err)
						return cursorTime, ErrVerifyAborted
					}
				} else {
					if err := s.resetTaskForRecode(task, "输出文件缺失，重新转码"); err != nil {
						log.Printf("[Scanner] 重置任务失败 %s: %v", task.SourcePath, err)
						return cursorTime, ErrVerifyAborted
					}
					missing++
					goto Verified
				}
			} else if err != nil {
				log.Printf("[Scanner] 输出文件访问失败 %s: %v", primaryPath, err)
				return cursorTime, ErrVerifyAborted
			}

			// 检查文件内容
			{
				probeTimeout := time.Duration(s.config.FFmpeg.ProbeTimeoutSeconds) * time.Second
				if err := media.ProbeFile(checkPath, probeTimeout, 0); err != nil {
					log.Printf("[Scanner] 输出文件损坏: %s, err=%v", checkPath, err)
					if removeErr := os.Remove(checkPath); removeErr != nil {
						log.Printf("[Scanner] 删除损坏输出失败 %s: %v", checkPath, removeErr)
						// 删不掉文件，可能是权限问题，停止推进
						return cursorTime, ErrVerifyAborted
					}
					if err := s.resetTaskForRecode(task, "输出文件损坏，已删除并重新转码"); err != nil {
						log.Printf("[Scanner] 重置任务失败 %s: %v", task.SourcePath, err)
						return cursorTime, ErrVerifyAborted
					}
					requeued++
					goto Verified
				}

				decodeSeconds := s.config.FFmpeg.VerifyDecodeSeconds
				if decodeSeconds > 0 {
					if err := media.DecodeSegmentStrict(checkPath, probeTimeout, 0, decodeSeconds); err != nil {
						log.Printf("[Scanner] 输出文件损坏: %s, err=%v", checkPath, err)
						if removeErr := os.Remove(checkPath); removeErr != nil {
							log.Printf("[Scanner] 删除损坏输出失败 %s: %v", checkPath, removeErr)
							return cursorTime, ErrVerifyAborted
						}
						if err := s.resetTaskForRecode(task, "输出文件损坏，已删除并重新转码"); err != nil {
							log.Printf("[Scanner] 重置任务失败 %s: %v", task.SourcePath, err)
							return cursorTime, ErrVerifyAborted
						}
						requeued++
						goto Verified
					}
				}

				if decodeSeconds > 0 && s.config.FFmpeg.VerifyTailSeekSeconds > 0 {
					if err := media.DecodeSegmentStrict(checkPath, probeTimeout, s.config.FFmpeg.VerifyTailSeekSeconds, decodeSeconds); err != nil {
						log.Printf("[Scanner] 输出文件损坏: %s, err=%v", checkPath, err)
						if removeErr := os.Remove(checkPath); removeErr != nil {
							log.Printf("[Scanner] 删除损坏输出失败 %s: %v", checkPath, removeErr)
							return cursorTime, ErrVerifyAborted
						}
						if err := s.resetTaskForRecode(task, "输出文件损坏，已删除并重新转码"); err != nil {
							log.Printf("[Scanner] 重置任务失败 %s: %v", task.SourcePath, err)
							return cursorTime, ErrVerifyAborted
						}
						requeued++
						goto Verified
					}
				}
			}

		Verified:
			checked++
			
			// 成功处理完该任务，更新游标
			if task.CompletedAt != nil {
				cursorTime = *task.CompletedAt
			}
			cursorID = task.ID
		}

		lastTask := tasks[len(tasks)-1]
		if lastTask.CompletedAt != nil {
			cursorTime = *lastTask.CompletedAt
		}
		cursorID = lastTask.ID

		if len(tasks) < limit {
			break
		}
	}

	log.Printf("[Scanner] 输出文件校验完成: checked=%d, missing=%d, requeued=%d",
		checked, missing, requeued)
	return upperBound, nil
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

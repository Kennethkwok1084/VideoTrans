# Phase 4 实现进度报告

**日期**: 2026-02-12  
**状态**: W1、W3 已完成 ✅ | Bug 修复完成 ✅

---

## ✅ 已完成工作

### W1: 原子 Claim 调度 (P0)

**核心改动**：
- ✅ 添加 `scheduler.atomic_claim` 配置开关
- ✅ 实现 `ClaimPendingTasks()` 方法：在单个事务中完成"选取 + 状态迁移"
- ✅ 调度器支持原子/传统模式切换
- ✅ Worker 处理逻辑适配：原子模式下任务已是 processing 状态

**关键代码**：
- [configs/config.yaml](configs/config.yaml#L9-L10): 配置开关
- [internal/config/config.go](internal/config/config.go): `SchedulerConfig` 结构
- [internal/database/db.go](internal/database/db.go): `ClaimPendingTasks()` 实现（含锁 + 事务）
- [internal/worker/worker.go](internal/worker/worker.go#L264-L277): 调度器调用逻辑
- [internal/worker/worker.go](internal/worker/worker.go#L500-L509): Worker 适配逻辑

### W3: 指数退避重试 (P1)

**核心改动**：
- ✅ 添加 `retry` 配置段（backoff_enabled, base_delay, max_delay, retryable_errors）
- ✅ 数据模型添加 `next_retry_at` 和 `last_error_category` 字段
- ✅ 实现 `ScheduleRetry()` 方法
- ✅ Claim 逻辑支持 `next_retry_at` 过滤（只 claim 可重试的任务）
- ✅ Worker 失败处理集成指数退避 + 随机抖动

**算法**：
```go
delay = base * 2^(retry-1)
delay = min(delay, max_delay)
finalDelay = delay + jitter(±20%)
nextRetryAt = now + finalDelay
```

**关键代码**：  
- [configs/config.yaml](configs/config.yaml#L12-L22): 重试配置
- [internal/config/config.go](internal/config/config.go): `RetryConfig` 结构
- [internal/database/models.go](internal/database/models.go): 新增字段
- [internal/database/db.go](internal/database/db.go): 
  - `ScheduleRetry()` 方法
  - `ClaimPendingTasks()` 包含 `next_retry_at` 过滤
- [internal/worker/worker.go](internal/worker/worker.go#L527-L590): 指数退避重试逻辑

### Phase 3 补充完成

**注意**: 在实现 Phase 4 时，发现 Phase 3 的 `db.go` 改动未提交，已一并实现：
- ✅ 清理生命周期字段：`source_deleted_at`, `trash_path`, `cleanup_log`
- ✅ 清理方法：`MarkSoftDeleted()`, `MarkHardDeleted()`, `MarkCleanupError()`
- ✅ 查询方法：`GetSoftDeletedOldTasks()`, `GetBatchTasksCompleted()`, `GetAllSoftDeleted()`
- ✅ 统计更新：`GetStats()` 包含 soft_deleted/hard_deleted/cleanup_error 计数
- ✅ 历史数据迁移：`migratePhase3HistoricalData()`（保守策略）

---

## 📊 测试结果

```bash
$ go test ./...
?       github.com/stm/video-transcoder/cmd/stm [no test files]
ok      github.com/stm/video-transcoder/internal/app    38.718s
ok      github.com/stm/video-transcoder/internal/cleaner        0.009s
ok      github.com/stm/video-transcoder/internal/config 0.003s
ok      github.com/stm/video-transcoder/internal/database       0.051s
?       github.com/stm/video-transcoder/internal/media  [no test files]
?       github.com/stm/video-transcoder/internal/metrics        [no test files]
ok      github.com/stm/video-transcoder/internal/scanner        0.034s
ok      github.com/stm/video-transcoder/internal/web    0.012s
ok      github.com/stm/video-transcoder/internal/worker 0.005s
```

**所有测试通过** ✅

---

## 🐛 Bug 修复记录 (2026-02-12)

在 W1/W3 实现审查中发现 **10 个问题**，已全部修复 ✅（含 2 个低优先级后续改进）：

### 第一轮修复（高/中优先级，5 个）

### ✅ [高] Config.Save() 丢失 Phase 4 配置

**问题**: `internal/config/config.go` 的快照只保存了 `System/FFmpeg/Cleaning/Log/Path`，缺少 `Scheduler/Retry` 字段。Web 目录操作会触发保存，导致 Phase 4 配置在重启后丢失。

**修复**: 在 `Config.Save()` snapshot 中添加 `Scheduler` 和 `Retry` 字段。

### ✅ [高] 手动重试不清理 next_retry_at

**问题**: 原子 Claim 过滤 `next_retry_at <= NOW`，但手动重试（`UpdateTaskStatus`、`ResetFailedTasksToPending`、`ResetProcessingTasksToPending`）未清空该字段，导致任务被旧的退避时间阻塞。

**修复**: 所有手动重试路径在设置 `status=pending` 时同步清空 `next_retry_at` 和 `last_error_category`。

### ✅ [中] retryable_errors 白名单未生效

**问题**: 文档声称支持白名单，配置结构也有字段，但重试判定仅依赖 `classifyError` 的 `transient` 返回值，未读取白名单。

**修复**:
- 重构 `classifyError()` 返回英文标识符（`timeout`、`io_error`、`output_error`、`disk_space`、`invalid_data`）
- 添加 `Worker.isRetryable()` 方法实现白名单检查（未配置时降级为 `transient` 判断）
- 添加 `getCategoryDescription()` 提供中文描述用于日志和 UI
- **修改 worker.go:542 调用 `isRetryable()` 替代直接使用 `transient`** ✅

### ✅ [高] ClaimPendingTasks 未真正 claim failed 任务

**问题**: 查询包含 `status=failed` 任务，但 UPDATE 条件只允许 `status=pending`，导致 failed 任务被选中但状态未更新，可能重复调度。

**修复**: 修改 UPDATE 条件为 `(status=pending OR status=failed)`，并在更新时清空 `next_retry_at` 和 `last_error_category` ✅

### ✅ [中] 非原子模式 next_retry_at 过滤缺失

**问题**: 回滚策略建议可单独关闭原子 Claim，但关闭后调度走 `GetPendingTasks`，该查询不看 `next_retry_at`，导致指数退避失效。

**修复**:
- `GetPendingTasks()` 和 `ClaimPendingTasks()` 支持查询 `status=failed` 且 `next_retry_at <= datetime('now')` 的任务 ✅
- `ScheduleRetry()` 不改变任务状态（保持 `failed`），仅更新 `next_retry_at` 和 `last_error_category` ✅
- 时间存储统一为 UTC 格式（`nextRetryAt.UTC()`），避免 CST/UTC 时区问题 ✅
- **UPDATE 语句清空退避字段，避免 claim 后字段残留** ✅

### ✅ [中] ResetTaskToPending 未清理 next_retry_at

**问题**: 文件更新时调用 `ResetTaskToPending` 重置任务，但未清空 `next_retry_at` 和 `last_error_category`，导致任务被旧的退避时间继续阻塞。

**修复**: 在 `ResetTaskToPending` 方法中清空 `next_retry_at` 和 `last_error_category` ✅

### ✅ [低] W3 关键行为测试缺失

**问题**: `internal/worker/worker_test.go` 未覆盖 backoff 计算、白名单、`next_retry_at` 阻塞/释放等路径。

**修复**: 在 `internal/database/db_test.go` 添加 4 个测试用例：
- `TestScheduleRetry`: 验证字段设置和 UTC 转换
- `TestGetPendingTasksRespectsNextRetryAt`: 验证非原子模式过滤
- `TestClaimPendingTasksRespectsNextRetryAt`: 验证原子模式过滤
- `TestManualRetryClearsNextRetryAt`: 验证手动重试清空退避字段

### 第二轮修复（后续改进，2 个）

### ✅ [低] 默认白名单包含不存在的类别

**问题**: `config.yaml` 默认白名单包含 `probe_error`（classifyError 不会返回此类别）和 `invalid_data`（transient=false，不应重试）。

**修复**: 修改默认白名单为实际存在的可重试类别：`timeout`、`output_error`、`io_error`（均为 transient=true）✅

### ✅ [低] isRetryable 方法缺少单元测试

**问题**: `isRetryable()` 是白名单/默认分类决策的核心逻辑，但 `worker_test.go` 未包含相关测试。

**修复**: 新增 `TestIsRetryable` 测试，涵盖 5 个场景（无白名单/有白名单、类别匹配/不匹配、白名单覆盖默认逻辑）✅

**详细报告**: 已合并到本文件及 `docs/refactor/phase5-test-report.md`

---

## 🔜 待完成工作

### W2: 扫描/校验预算化与持久游标 (P1)

**设计要点**：
- 新增 `verify_max_tasks_per_scan` 和 `verify_max_seconds_per_scan` 配置
- 每轮扫描在预算耗尽时中断，保存游标（时间 + ID）到 metadata 表
- 下轮从持久游标继续

**改动范围**：
- `internal/scanner/verify.go`: 预算控制逻辑
- `internal/database/db.go`: metadata 表和读写方法
- `internal/config/config.go`: 预算配置
- `configs/config.yaml`: 配置项

### W4: FFmpeg Watchdog 细粒度治理 (P2)

**设计要点**：
- 分阶段超时（probe / transcode / post-verify）
- 输出文件增长停滞检测
- 分级 Kill 流程（SIGTERM -> grace period -> SIGKILL）
- Watchdog 指标与日志字段（reason, phase, duration）

**改动范围**：
- `internal/worker/worker.go`: watchdog 逻辑增强
- `internal/metrics/metrics.go`: 新增指标
- `internal/config/config.go`: 细粒度配置
- `configs/config.yaml`: 配置项

---

## 🎯 推荐执行顺序

✅ ~~1. W1 原子 Claim（先收敛重复入队）~~  
✅ ~~3. W3 退避重试（先止住失败抖动）~~  
2. W2 扫描预算化（控制 I/O 峰值）  
3. W4 Watchdog 细化（增强尾部异常治理）

---

## 📝 配置示例

```yaml
scheduler:
  atomic_claim: true  # 启用原子 Claim 调度（Phase 4 优化）

retry:
  backoff_enabled: true  # 启用指数退避重试（Phase 4 优化）
  base_delay_seconds: 60  # 基础延迟时间（秒）
  max_delay_seconds: 3600  # 最大延迟时间（1小时）
  retryable_errors:
    - "invalid_data"
    - "probe_error"
    - "output_error"
    - "timeout"
```

---

## 🔒 回滚策略

1. **原子 Claim**: 设置 `scheduler.atomic_claim: false` 回退传统模式
2. **指数退避**: 设置 `retry.backoff_enabled: false` 回退即时重试
3. 所有 Phase 4 开关默认支持关闭，保留旧路径代码

---

## 📋 文件清单

### 已修改文件

| 文件 | 改动类型 | 说明 |
|------|---------|------|
| `configs/config.yaml` | 新增 | scheduler + retry 配置段 |
| `internal/config/config.go` | 新增 | SchedulerConfig + RetryConfig 结构 |
| `internal/database/models.go` | 新增 | next_retry_at + last_error_category 字段 |
| `internal/database/db.go` | 新增+修改 | Phase 3 + 4 所有方法，列迁移 |
| `internal/database/db_test.go` | 已有 | 测试已通过（含 ClaimPendingTasks 测试） |
| `internal/worker/worker.go` | 修改 | 调度器 + Worker 适配原子 Claim + 指数退避 |
| `internal/cleaner/cleaner.go` | 已有 | Phase 3 改动（调用 db 方法） |
| `internal/scanner/verify.go` | 已有 | Phase 3 改动（调用 GetBatchTasksCompleted） |

### 已创建文档

- [x] `docs/refactor/phase5-test-report.md`: Bug 修复与回归测试汇总（2026-02-12）

### 待创建文档

- [ ] `docs/refactor/phase4-test-report.md`: 完整测试报告
- [ ] `docs/refactor/phase4-rollback.md`: 回滚方案

---

**完成度**: 2/4 (W1 ✅ + W3 ✅ | Bug 修复 ✅)  
**测试状态**: 所有测试通过（含 4 个新增 W3 测试）  
**下一步**: W2 扫描预算化实现

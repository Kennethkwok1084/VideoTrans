# Phase 5 测试与验收报告

**版本**: 1.0  
**日期**: 2026-02-12  
**状态**: In Progress

## 1. 本轮执行范围

1. M1: 观测基线增强（任务全状态指标 + 后台定时同步）。
2. M2: `cleanup_error` 自动重试恢复（自愈闭环）。
3. M3: claim 路径容量基线（10k 任务 benchmark + 索引优化）。

## 2. 代码变更

1. `internal/metrics/metrics.go`
- `UpdateTaskStats` 扩展为完整生命周期状态：`pending/processing/completed/failed/soft_deleted/hard_deleted/cleanup_error`。

2. `internal/web/server.go`
- 新增后台指标同步循环（15 秒刷新一次 DB 统计到 Prometheus）。
- `handleGetStats` 复用统一指标更新逻辑。

3. `internal/database/db.go`
- 新增 `GetCleanupErrorTasks(limit)`。
- 新增 `ResetCleanupErrorStatus(id, targetStatus, note)`。
- 新增复合索引（`status/next_retry_at/created_at` 等）以优化调度与清理查询。

4. `internal/cleaner/cleaner.go`
- `runCleaning()` 增加 `retryCleanupErrors()` 前置步骤。
- cleanup_error 自动恢复策略：
  - 有 `trash_path` -> 恢复 `soft_deleted`
  - 无 `trash_path` -> 恢复 `completed`
 - 自愈批次支持配置项 `cleaning.cleanup_retry_batch`（默认 200）。

## 3. 新增测试

1. `internal/database/db_test.go`
- `TestCleanupErrorRecovery`

2. `internal/cleaner/cleaner_test.go`
- `TestRetryCleanupErrors`

3. `internal/database/db_test.go`
- `BenchmarkClaimPendingTasks10k`

## 4. 测试命令与结果

1. 局部回归：
```bash
/usr/local/go/bin/go test ./internal/metrics ./internal/web ./internal/cleaner ./internal/database
```
结果：通过。

2. 全量回归：
```bash
/usr/local/go/bin/go test ./...
```
结果摘要：
- `internal/app`: pass
- `internal/cleaner`: pass
- `internal/config`: pass
- `internal/database`: pass
- `internal/scanner`: pass
- `internal/web`: pass
- `internal/worker`: pass

3. 容量基准（DB claim 层）：
```bash
./bin/phase5-capacity-bench.sh
```
结果：
- `BenchmarkClaimPendingTasks10k-16`
- `424318064 ns/op`（约 0.424s/10k tasks）
- `11688245 B/op`, `293114 allocs/op`

## 5. 验收映射

1. M1 验收项：已覆盖（指标口径统一 + 持续刷新）。
2. M2 验收项：部分覆盖（cleanup_error 自动恢复已落地，完整值班演练待执行）。
3. M3 验收项：部分覆盖（DB claim 基线已完成，端到端压测待执行）。
4. M4：未执行（需灰度与回滚演练环境）。

## 6. 当前结论

1. 代码层面无回归，M1/M2 核心能力可用，M3 已建立首轮容量基线。
2. Phase 5 仍处于执行中，尚未达到完整 DoD。
3. M4 已补灰度门禁脚本 `bin/phase5-canary-gate.sh`，待在预发/生产执行演练。

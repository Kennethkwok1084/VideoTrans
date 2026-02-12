# Phase 4 设计文档: 性能与可靠性优化

**版本**: 1.0  
**日期**: 2026-02-11  
**状态**: Planning

---

## 1. 目标

在 Phase 3 稳定性的基础上，优化吞吐、I/O 压力与失败恢复质量，重点解决：

1. 调度重复入队与抢占竞争。
2. 扫描/校验在大规模任务下的资源占用。
3. 失败任务的抖动重试。
4. FFmpeg 执行卡住与超时治理精度。

---

## 2. 当前现状与差距

### 2.1 调度器

当前 `scheduler` 通过 `GetPendingTasks` 拉取 `pending`，再在内存中用 `queuedTaskIDs` 去重；任务实际进入 `processing` 发生在 Worker 消费时。  
结果：同一任务在调度窗口内仍可能被重复拉取，且跨实例无法去重。

### 2.2 扫描与校验

已实现水位 + keyset 批量校验（`verifyCompletedOutputs` + `GetBatchTasksCompleted`），但仍缺少“单轮预算”与“持久游标”，在大批量 backlog 时单轮校验时长不可控。

### 2.3 失败重试

已有错误分类与最多 3 次重试，但重试是“快速回到 pending”，缺少指数退避、抖动、可配置白名单，容易形成短周期震荡。

### 2.4 FFmpeg watchdog

已有总超时与进度停滞检测，但缺少分阶段 watchdog、分级 kill 策略与可观测指标（超时类型、kill 原因）。

---

## 3. 工作流拆分（可并行）

## W1: 原子 Claim 调度（优先级 P0）

### 设计

1. 新增 `ClaimPendingTasks(limit int)`：数据库内“选取 + 状态迁移”为单事务原子操作。
2. Claim 成功即从 `pending -> processing`，返回 claimed 任务集合给调度器入队。
3. Worker 处理前不再重复设置 `processing`（仅更新进度、结果状态）。
4. 保留现有启动恢复逻辑（`processing -> pending`）作为 crash safety。

### 代码改动

- `internal/database/db.go`
- `internal/worker/worker.go`
- `internal/database/db_test.go`
- `internal/worker/worker_test.go`

### 验收

1. 同一任务在单实例内不会重复入队。
2. 高并发调度压力下，无重复 claim。
3. 服务重启后 `processing` 任务可回收。

---

## W2: 扫描/校验预算化与持久游标（优先级 P1）

### 设计

1. 为输出校验新增预算参数：
   - `verify_max_tasks_per_scan`
   - `verify_max_seconds_per_scan`
2. 每轮扫描在预算耗尽时中断，并保存游标（时间 + id）到数据库元信息表。
3. 下轮从持久游标继续，避免重启后重复回扫。
4. 保留 strict_check 关闭时的兼容语义（不推进校验游标）。

### 代码改动

- `internal/scanner/verify.go`
- `internal/scanner/scanner.go`
- `internal/database/db.go`（新增 metadata 读写）
- `internal/config/config.go`
- `configs/config.yaml`
- `internal/scanner/scanner_test.go`

### 验收

1. 单轮校验耗时受预算约束。
2. 重启后校验进度可续跑。
3. 大量 completed 任务下，健康检查与 worker 吞吐稳定。

---

## W3: 指数退避重试与白名单（优先级 P1）

### 设计

1. 新增任务字段：
   - `next_retry_at`
   - `last_error_category`
2. 调度器仅 claim `next_retry_at <= now` 的 pending 任务。
3. 对 transient 错误使用指数退避 + 抖动：
   - `backoff = min(base * 2^retry, max)`
4. 将“可重试错误白名单”配置化，默认沿用当前分类逻辑。

### 代码改动

- `internal/database/models.go`
- `internal/database/db.go`
- `internal/worker/worker.go`
- `internal/config/config.go`
- `configs/config.yaml`
- `internal/database/db_test.go`
- `internal/worker/worker_test.go`

### 验收

1. 相同错误不会在短周期内反复抖动。
2. 重试间隔符合配置，且不会突破上限。
3. 不可重试错误直接终态化，避免无效消耗。

---

## W4: FFmpeg Watchdog 细粒度治理（优先级 P2）

### 设计

1. 增加“阶段型超时”：
   - probe 阶段
   - transcode 阶段
   - post-verify 阶段
2. 增加“输出文件增长停滞检测”（与进度停滞互补）。
3. 增加 kill 升级流程：
   - 先发送 `SIGTERM`
   - 超过 grace period 后 `SIGKILL`
4. 增加 watchdog 指标与日志字段（reason、phase、duration）。

### 代码改动

- `internal/worker/worker.go`
- `internal/metrics/metrics.go`
- `internal/config/config.go`
- `configs/config.yaml`
- `internal/worker/worker_test.go`

### 验收

1. 卡死进程可在可控时间内回收。
2. 超时/卡死原因可在 metrics 与日志中区分。
3. 不引入正常任务的误杀回归。

---

## 4. 推荐执行顺序

1. W1 原子 Claim（先收敛重复入队）
2. W3 退避重试（先止住失败抖动）
3. W2 扫描预算化（控制 I/O 峰值）
4. W4 Watchdog 细化（增强尾部异常治理）

说明：W2/W4 可并行，但建议先合入 W1/W3 以稳定调度面。

---

## 5. 指标与观测补充

新增建议指标：

1. `stm_scheduler_claim_total{result="claimed|empty|error"}`
2. `stm_scheduler_claim_conflict_total`
3. `stm_task_retry_scheduled_total{category="..."}`
4. `stm_task_retry_delay_seconds`（histogram）
5. `stm_verify_batch_duration_seconds`
6. `stm_ffmpeg_watchdog_trigger_total{reason="..."}` 

---

## 6. 风险与回滚

### 风险

1. Claim 语义切换后，`processing` 数量可能短期上升（包含队列中任务）。
2. 新增字段与 metadata 表带来迁移兼容风险。
3. Watchdog 误判可能中断长 GOP 或低 I/O 速率任务。

### 回滚策略

1. 所有 Phase 4 开关默认可配置并支持关闭：
   - `scheduler.atomic_claim`
   - `retry.backoff_enabled`
   - `scanner.verify_budget_enabled`
   - `ffmpeg.watchdog_extended_enabled`
2. 保留旧路径代码，支持 feature flag 级回退。
3. 任何 P0 回归优先“关开关回退”，再发 hotfix。

---

## 7. 测试策略

1. 单元测试：
   - Claim 原子性与并发竞争
   - 退避间隔计算与边界
   - 校验游标推进与预算中断恢复
   - watchdog 触发与 kill 升级
2. 集成测试：
   - DB + worker 调度链路
   - scanner 校验长队列续跑
3. 压测：
   - 10k pending + 1k failed + 50 并发扫描文件集
4. 回归测试：
   - `go test ./...`

---

## 8. 交付物

1. 设计文档：`docs/refactor/phase4-design.md`（本文件）
2. 测试报告：`docs/refactor/phase4-test-report.md`
3. 回滚方案：`docs/refactor/phase4-rollback.md`
4. 配置更新：`configs/config.yaml` + `README.md`


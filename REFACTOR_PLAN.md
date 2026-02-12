# STM 系统梳理与重构开发文档

更新时间：2026-02-09  
适用版本：当前仓库 `master`（`cmd/stm/main.go` + `internal/*`）

## 1. 文档目标

这份文档用于两件事：

1. 梳理当前系统的真实实现（而不是历史规划）。
2. 给出可执行、可验收、可回滚的重构步骤，后续按此文档推进。

---

## 2. 当前系统全景梳理

## 2.1 运行时组件

进程入口：`cmd/stm/main.go`

- `config.Load()`：加载和校验配置。
- `database.Init()`：初始化 SQLite（WAL，单连接）。
- `scanner.New()`：扫描输入目录，入库/重置任务。
- `worker.New()`：任务调度 + Worker Pool + FFmpeg 执行。
- `cleaner.New()`：软删除/硬删除定时清理。
- `web.New()`：Gin API + 页面模板 + metrics。

当前启动方式是“主进程直接起多个 goroutine”，无统一 Supervisor。

## 2.2 数据模型与状态流

核心表：`tasks`（`internal/database/db.go`）

- 字段：`source_path/source_mtime/source_size/status/retry_count/progress/output_size/repair_mode/created_at/completed_at/log`。
- 状态：`pending/processing/completed/failed/irrecoverable`（`internal/database/models.go`）。

关键流转：

1. 扫描发现新文件 -> `pending`
2. Worker 领取 -> `processing`
3. FFmpeg 成功 -> `completed`
4. FFmpeg 失败 -> `pending`（可重试）或 `failed/irrecoverable`
5. Cleaner 依据 `completed_at` 清理源文件（文件系统动作，不写单独删除状态）

## 2.3 业务流程

### A. 扫描流程（`internal/scanner/scanner.go`）

- 周期扫描输入目录（默认分钟级）。
- 过滤系统目录和临时文件。
- 按绝对路径查任务；文件变更则重置为 `pending`。
- 扫描结束后，默认会校验历史 `completed` 任务输出（`internal/scanner/verify.go`）。

### B. 调度与转码（`internal/worker/worker.go`）

- 调度器定时拉取 `pending` 任务放入内存队列。
- Worker Pool 按时间窗口或 force 模式扩缩容。
- 每个任务执行：
  - 预检查（ffprobe）
  - FFmpeg 转码到临时文件
  - 进度解析入库
  - 输出校验（可选 strict）
  - 成功 rename 成正式输出

### C. 清理流程（`internal/cleaner/cleaner.go`）

- Cron 定时：
  - 一级清理：将超过 `soft_delete_days` 的源文件移入“垃圾桶”
  - 二级清理：删除垃圾桶里超过 `hard_delete_days` 的文件

### D. Web/API（`internal/web/server.go`）

- 任务查询、重试、扫描触发、Worker 控制、垃圾桶查看/删除、健康检查。
- 模板路径固定为 `/app/templates/*.html`。

---

## 3. 已确认问题与根因矩阵

以下问题是重构优先级依据。

| ID | 现象 | 根因 | 影响 | 优先级 |
|---|---|---|---|---|
| R1 | 超时文件未按预期进入/展示在回收站 | “移动路径”与“扫描垃圾桶路径”不是同一策略 | 清理看起来失效、数据治理失真 | P0 |
| R2 | 转码中途“死掉”或任务状态不一致 | 进程关闭等待固定 10 秒，生命周期控制割裂 | 任务中断、体验差 | P0 |
| R3 | 某些错误导致整进程退出 | `log.Fatalf` 在 goroutine 中直接终止进程 | 单点故障放大 | P0 |
| R4 | 扫描阶段 I/O 压力高，可能拖垮转码 | 每轮扫描后对 completed 任务做全量校验 | 卡顿、超时、误报 | P1 |
| R5 | 本地运行易因模板路径失败 | 模板路径写死 `/app/templates/*.html` | 开发/部署环境行为不一致 | P1 |
| R6 | 状态机缺“删除生命周期状态” | 清理动作不落库，只做文件系统操作 | 无法审计、难以追踪 | P2 |
| R7 | 组件无统一监督机制 | 无 errgroup/supervisor/panic 恢复统一治理 | 可维护性差 | P2 |

---

## 4. 重构目标与边界

## 4.1 重构目标

1. 稳定性优先：不再出现“系统自杀”与“半途停工”。
2. 生命周期可控：所有后台组件支持统一启动、停止、等待和超时策略。
3. 路径策略一致：扫描/转码/清理对输入输出和垃圾桶使用同一规则。
4. 状态可追踪：任务从发现到清理可审计。
5. 可验证：每阶段有明确测试与验收标准。

## 4.2 非目标（本轮不做）

1. 不做分布式多实例。
2. 不做消息队列中间件替换。
3. 不做 UI 大改。

---

## 5. 目标架构（重构后）

## 5.1 分层结构

1. `app` 层：统一进程生命周期（Supervisor）。
2. `domain/service` 层：ScannerService、SchedulerService、TranscodeService、CleanupService。
3. `infra` 层：DB、FFmpeg、文件系统、HTTP、Metrics。
4. `api` 层：仅做参数校验与服务调用，不承载业务细节。

## 5.2 统一生命周期

- 使用 `errgroup.WithContext` 管理 scanner/worker/cleaner/web。
- 每个组件实现 `Run(ctx)` + `Shutdown(timeout)`（或通过 ctx + WaitGroup 保证可等待）。
- 主程序只在“不可恢复错误”时退出；可降级错误不杀全进程。

## 5.3 任务状态机增强

建议增加（数据库迁移）：

- `soft_deleted`：源文件已进垃圾桶
- `hard_deleted`：源文件已彻底删除
- `cleanup_error`：清理动作失败（可重试）

并新增字段（建议）：

- `source_deleted_at`
- `trash_path`
- `cleanup_log`

---

## 6. 分阶段重构步骤（开发执行计划）

## Phase 0：基线冻结与观测补齐（先做）

目标：在改代码前拿到“可对比基线”。

步骤：

1. 冻结当前配置与运行参数（保存运行时 config 快照）。
2. 增加关键日志上下文：`task_id/source_path/worker_id`。
3. 确认 metrics 最小集合：任务状态计数、worker 数、失败原因分类、清理成功/失败计数。
4. 建立问题复现用例（至少覆盖：回收站异常、转码中断）。

产出：

- `docs/baseline/*.md`（运行参数、复现步骤、样本日志）。

验收：

- 所有 P0 问题都有“复现步骤 + 预期 + 实际”。

---

## Phase 1：稳定性热修（P0）

目标：先止血，不改大结构。

步骤：

1. 去除致命退出路径：
   - 把 `main` 中 goroutine 内 `log.Fatalf` 改为错误上报到主协程。
2. 真正优雅停机：
   - 取消“固定睡 10 秒退出”的方式，改为等待 worker drain（带上限超时和告警）。
3. 统一垃圾桶路径策略：
   - 软删除写入路径、垃圾桶列表读取路径、硬删除扫描路径三者统一。
4. 降低扫描后全量校验压力：
   - 默认改为“仅新完成任务校验”或“抽样校验”。
5. 模板路径改为可配置/自适应（容器和本地一致）。

建议改动文件：

- `cmd/stm/main.go`
- `internal/cleaner/cleaner.go`
- `internal/scanner/verify.go`
- `internal/web/server.go`
- `internal/config/config.go`（新增路径配置项）

验收：

1. 人工触发停止时，进行中的转码可按策略完成或可控终止，不出现状态错乱。
2. 超过软删除天数的文件可以稳定进入并展示在垃圾桶。
3. Web 初始化失败不应直接导致所有后台任务退出（除非配置明确要求 hard-fail）。

---

## Phase 2：生命周期重构（P1/P2）

目标：建立统一 Supervisor，收敛并发与错误管理。

步骤：

1. 引入 `internal/app`（或同等目录）：
   - 统一 `Start/Run/Stop`。
2. 所有后台模块实现一致接口：
   - `Run(ctx context.Context) error`
3. 错误分级：
   - fatal：主程序退出
   - recoverable：记录并重试/降级
4. 加入 panic recover 包装，避免单 goroutine panic 造成全局异常。

建议改动文件：

- 新增：`internal/app/app.go`
- 调整：`cmd/stm/main.go`
- 调整：`internal/scanner/*`, `internal/worker/*`, `internal/cleaner/*`, `internal/web/*`

验收：

1. 任意单模块失败时，系统行为符合错误分级策略。
2. `SIGTERM` 时有完整停止日志链路，且不丢状态。

---

## Phase 3：状态机与数据模型重构（P2）

目标：把“清理动作”纳入任务生命周期，形成可审计闭环。

步骤：

1. 设计并执行 DB migration（新增状态和字段）。
2. 清理模块改为“先落库状态，再文件系统动作，再落终态”。
3. Web/API 增加清理态展示和筛选。
4. 对历史数据做兼容迁移脚本。

建议改动文件：

- `internal/database/db.go`
- `internal/database/models.go`
- `internal/cleaner/cleaner.go`
- `internal/web/server.go`

验收：

1. 任一任务可追踪：`completed -> soft_deleted -> hard_deleted`。
2. 清理失败有明确状态和日志，不再“静默失败”。

---

## Phase 4：性能与可靠性优化（可并行）

目标：稳定后再优化吞吐和资源占用。

设计文档：`docs/refactor/phase4-design.md`

步骤：

1. 调度器改为“原子 claim”避免重复入队竞争。
2. 扫描校验采用分片/游标/增量策略，限制单轮 I/O。
3. 完善失败重试策略（指数退避 + 原因白名单）。
4. 增加 FFmpeg 执行 watchdog 与更细粒度超时策略。

验收：

1. 大量任务场景下，扫描不会明显压制转码吞吐。
2. 异常重试不会造成队列抖动。

---

## Phase 5：可运维化与规模化落地（执行与验收基线）

目标：将 Phase 4 能力沉淀为生产长期运营能力（SLO、告警、容量、回滚、Runbook）。

执行与验收基线：`docs/refactor/phase5-plan.md`

Phase 5 完成门禁（Go/No-Go）：

1. `docs/refactor/phase5-plan.md` 中 M1-M4 验收标准全部通过。
2. `docs/refactor/phase5-plan.md` 第 7 节“阶段验证清单”全部通过。
3. M4 灰度与回滚演练通过后，方可标记 Phase 5 完成。

执行入口：

1. 基线文档：`docs/refactor/phase5-plan.md`
2. 自动验收脚本：`bin/phase5-acceptance-check.sh`
3. 容量基准脚本：`bin/phase5-capacity-bench.sh`
4. 灰度门禁脚本：`bin/phase5-canary-gate.sh`

---

## 7. 测试与验收策略

## 7.1 测试分层

1. 单元测试：状态流转、路径解析、错误分类。
2. 集成测试：DB + 文件系统 + ffmpeg mock。
3. 端到端测试：容器内跑“扫描 -> 转码 -> 软删 -> 硬删”全流程。
4. 回归测试：覆盖历史问题 R1-R7。

## 7.2 必测场景

1. 软删后文件能在垃圾桶 API 中看到。
2. 硬删触发后，文件和任务状态同步更新。
3. 进程停机时，处理中任务行为符合配置策略。
4. Web 端异常不会把 worker/scanner 一起带死。
5. 大目录扫描时，系统可持续响应健康检查。

---

## 8. 交付规范（每个阶段）

每个阶段必须交付：

1. 设计说明：`docs/refactor/phaseX-design.md`
2. 代码变更：PR（含风险点说明）
3. 测试报告：`docs/refactor/phaseX-test-report.md`
4. 回滚说明：`docs/refactor/phaseX-rollback.md`

DoD（完成定义）：

1. 功能验收通过。
2. 回归用例通过。
3. 关键日志和指标可观测。
4. 文档已更新（README/运维说明/API 说明）。

---

## 9. 推荐实施顺序（本周起步）

第 1 周：

1. Phase 0 全部完成。
2. Phase 1 的 R1/R2/R3（最高优先级）完成并上线验证。

第 2 周：

1. 完成 Phase 1 剩余项（R4/R5）。
2. 启动 Phase 2（先落 Supervisor 骨架）。

第 3 周：

1. 完成 Phase 2。
2. 评估并启动 Phase 3 的 migration 方案。

---

## 10. 本文档后的执行方式

后续重构按以下流程推进：

1. 从 Phase 1 的单项任务开工（建议先修 R1：垃圾桶路径统一）。
2. 每完成一项，补对应测试和阶段文档。
3. 阶段验收通过后再进入下一阶段，避免“大爆炸式重写”。

这份文档作为主线索引，后续每个阶段的设计和报告都挂在 `docs/refactor/` 下进行归档。

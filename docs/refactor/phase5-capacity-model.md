# Phase 5 容量模型与扩容建议

**版本**: 1.0  
**日期**: 2026-02-12  
**状态**: In Progress

## 1. 当前基线参数

来自 `configs/config.yaml`：

1. `max_workers = 3`
2. `scheduler_interval = 10s`
3. `task_queue_size = 10`
4. `min_disk_space_gb = 5`

## 2. 容量模型（经验公式）

1. CPU 约束：`recommended_workers = max(1, floor(cpu_cores * 0.5))`
2. 队列容量：`task_queue_size >= recommended_workers * 3`
3. 磁盘余量：预留 >= 单日输入数据峰值的 1.5 倍。

## 3. 建议映射

1. 4 核主机：`max_workers=2`, `task_queue_size=8`
2. 6 核主机：`max_workers=3`, `task_queue_size=10`
3. 8 核主机：`max_workers=4`, `task_queue_size=16`
4. 12 核主机：`max_workers=6`, `task_queue_size=24`

## 4. 压测基线（执行要求）

1. 数据集：10k 任务，包含 10% 失败样本。
2. 场景：扫描并发 + 转码并发 + 清理任务并发。
3. 观察：吞吐、失败率、队列稳定性、磁盘占用趋势。

## 5. 当前基准结果（2026-02-12）

执行命令：

```bash
./bin/phase5-capacity-bench.sh
```

结果（当前环境）：

1. `BenchmarkClaimPendingTasks10k-16`
2. `424318064 ns/op`（约 0.424s 完成 10k claim）
3. `11688245 B/op`, `293114 allocs/op`
4. 折算吞吐约 `23k tasks/s`（仅 DB claim 层，非端到端）

说明：

1. 该结果用于 M3 的容量基线起点，不代表生产端到端吞吐。
2. 生产容量评估仍需补充扫描/转码/清理并发压测。

## 6. 扩容触发条件

1. `pending` 连续 30 分钟增长且转码成功率低于 98%。
2. `processing` 长期顶满且 CPU 平均 > 80%。
3. 清理延迟超过 `soft_delete_days + 1` 天。

## 7. 扩容执行

1. 先提升 `max_workers`（步长 1），观察 30 分钟。
2. 再调整 `task_queue_size` 和调度间隔。
3. 若吞吐无提升或失败率上升，立即回退到前一档。

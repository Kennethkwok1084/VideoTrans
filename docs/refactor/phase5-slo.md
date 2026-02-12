# Phase 5 SLO/SLI 基线

**版本**: 1.0  
**日期**: 2026-02-12  
**状态**: Active Baseline

---

## 1. 目标

本文件定义 Phase 5 的生产可用性与稳定性基线，用于告警、灰度门禁和上线验收。

## 2. 数据源

1. Prometheus 抓取 `GET /metrics`。
2. 健康检查探测 `GET /api/health`。
3. 应用日志（任务 ID、错误类别、清理日志）。

## 3. 核心 SLI 定义

1. 服务可用率（30d）：
`availability = sum_over_time(up{job="stm"}[30d]) / count_over_time(up{job="stm"}[30d])`

2. 转码成功率（24h rolling）：
`transcode_success_rate = increase(stm_transcode_success_total[24h]) / (increase(stm_transcode_success_total[24h]) + increase(stm_transcode_failed_total[24h]))`

3. 清理失败堆积：
`cleanup_error_backlog = stm_tasks_total{status="cleanup_error"}`

4. 处理中任务堆积：
`processing_backlog = stm_tasks_total{status="processing"}`

5. 可重试失败堆积（短期）：
`retry_pressure = increase(stm_transcode_failed_total[30m])`

## 4. SLO 目标

1. 可用率（30d）>= 99.9%。
2. 转码成功率（24h）>= 98%。
3. `cleanup_error_backlog` 不连续增长超过 30 分钟。
4. `processing_backlog` 不连续增长超过 15 分钟。

## 5. 告警阈值（建议）

1. P0: `up{job="stm"} == 0` 持续 2 分钟。
2. P1: 转码成功率 < 95% 持续 15 分钟。
3. P1: `cleanup_error_backlog >= 5` 持续 10 分钟。
4. P2: `processing_backlog >= 20` 持续 15 分钟。

## 6. 口径说明

1. `stm_tasks_total` 由 Web 后台定时同步（15 秒）和 `/api/stats` 调用共同刷新。
2. 清理生命周期状态已纳入指标标签：`soft_deleted/hard_deleted/cleanup_error`。
3. 所有告警表达式需在 Prometheus 规则文件中与本文件保持一致。

## 7. 验收要求

1. 所有 SLI 在 Dashboard 可视化。
2. 至少一次故障注入验证告警路由与等级正确。
3. 灰度放量阶段使用本文件阈值作为 Go/No-Go 门禁。

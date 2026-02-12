# Phase 5 回滚方案

**版本**: 1.0  
**日期**: 2026-02-12

## 1. 回滚触发条件

1. P0 告警持续 5 分钟未恢复。
2. 转码成功率低于 95% 且持续 15 分钟。
3. `cleanup_error` 堆积持续增长且自动恢复无效。

## 2. 配置级回滚（优先）

1. 回退调度优化：`scheduler.atomic_claim: false`
2. 回退重试策略：`retry.backoff_enabled: false`
3. 降低并发：下调 `system.max_workers`

执行步骤：

1. 备份当前配置：`cp configs/config.yaml configs/config.yaml.bak.$(date +%Y%m%d%H%M%S)`
2. 修改配置并重启服务。
3. 观察 15 分钟，确认告警恢复。

## 3. 版本级回滚

1. 切换到上一稳定版本二进制。
2. 保留当前数据库，不执行 destructive 操作。
3. 恢复后立刻执行健康检查和核心链路验证。

## 4. 回滚后验证

1. `GET /api/health` 返回 `status=healthy`。
2. `go test ./...` 在回滚分支通过。
3. 关键指标恢复到 SLO 阈值内。

## 5. 记录要求

1. 记录触发时间、执行人、回滚版本。
2. 记录回滚前后核心指标对比。
3. 24 小时内完成复盘并输出行动项。

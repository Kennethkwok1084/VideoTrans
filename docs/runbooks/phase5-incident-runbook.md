# Phase 5 事故处置 Runbook

**版本**: 1.0  
**日期**: 2026-02-12

## 1. 通用流程

1. 确认告警等级（P0/P1/P2）。
2. 执行健康检查：`curl -s http://<host>:9999/api/health`。
3. 查看核心指标：`/metrics` 与 Dashboard。
4. 记录时间线：发现时间、定位时间、恢复时间。

## 2. 高频场景处置（10 项）

1. 服务不可用（P0）
- 现象: `up==0`。
- 动作: 重启进程，若 2 分钟内未恢复，执行版本回滚。

2. 数据库不可用（P0）
- 现象: `/api/health` 返回 `database=false`。
- 动作: 检查 DB 文件路径与磁盘可写性；必要时切换到备份 DB。

3. 转码失败率升高（P1）
- 现象: `stm_transcode_failed_total` 快速增长。
- 动作: 查看失败类别，优先排查输入损坏、磁盘空间、ffmpeg 进程超时。

4. Worker 卡住（P1）
- 现象: `processing` 长时间不下降。
- 动作: 执行 `POST /api/worker/force-stop` 后再 `force-start`，观察 5 分钟。

5. cleanup_error 堆积（P1）
- 现象: `stm_tasks_total{status="cleanup_error"}` 连续上升。
- 动作: 检查权限/路径挂载；系统已自动将 `cleanup_error` 回退并重试。

6. 待处理队列持续增长（P2）
- 现象: `pending` 持续增长 15 分钟以上。
- 动作: 临时提高 `max_workers` 或缩短调度间隔，观察资源占用。

7. 磁盘空间不足（P1）
- 现象: 可用空间接近阈值。
- 动作: 触发临时清理策略并暂停高码率任务。

8. Web 不可用但后台运行（P1）
- 现象: API/页面不可访问，任务仍在处理。
- 动作: 按 app supervisor 策略等待自动恢复；失败则仅重启 Web 进程。

9. 手动重试后任务未执行（P2）
- 现象: 任务停留 pending。
- 动作: 检查时间窗口与 force 模式；确认调度器间隔和队列容量。

10. 灰度期间异常回归（P0/P1）
- 现象: 指标超阈值。
- 动作: 立即停止放量，按 `phase5-rollback.md` 执行回滚。

## 3. 常用接口

1. 健康检查: `GET /api/health`
2. 统计信息: `GET /api/stats`
3. 一键重试失败: `POST /api/tasks/retry-failed`
4. 恢复处理中: `POST /api/tasks/retry-processing`
5. Worker 强制启动: `POST /api/worker/force-start`
6. Worker 强制停止: `POST /api/worker/force-stop`

## 4. 退出条件

1. 指标恢复到 SLO 阈值内。
2. 告警消除且 30 分钟内无复发。
3. 复盘条目已创建并指定 owner。

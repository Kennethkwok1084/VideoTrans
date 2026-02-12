# Phase 3 设计文档: 状态机与数据模型重构

**版本**: 1.0  
**日期**: 2026-02-11  
**作者**: Copilot Agent  

---

## 1. 目标

将清理流程纳入任务状态机，实现 `completed -> soft_deleted -> hard_deleted` 的完整生命周期可追踪。

### 具体目标
1. 清理动作必须落库：文件系统操作与数据库状态同步。
2. 清理失败可追踪：引入 `cleanup_error` 状态和 `cleanup_log` 字段。
3. Web/API 可见：清理生命周期状态可筛选、展示。
4. 兼容历史数据：平滑升级，不破坏已运行系统。

---

## 2. 数据模型变更

### 2.1 新增状态

在 `internal/database/models.go` 中添加：

```go
const (
    // 已有状态
    StatusPending       TaskStatus = "pending"
    StatusProcessing    TaskStatus = "processing"
    StatusCompleted     TaskStatus = "completed"
    StatusFailed        TaskStatus = "failed"
    StatusIrrecoverable TaskStatus = "irrecoverable"
    
    // 新增清理生命周期状态
    StatusSoftDeleted  TaskStatus = "soft_deleted"  // 源文件已移入垃圾桶
    StatusHardDeleted  TaskStatus = "hard_deleted"  // 源文件已彻底删除
    StatusCleanupError TaskStatus = "cleanup_error" // 清理动作失败
)
```

### 2.2 新增字段

在 `Task` 结构体中添加：

```go
type Task struct {
    // 已有字段...
    
    // 清理生命周期字段
    SourceDeletedAt sql.NullTime   `db:"source_deleted_at"`
    TrashPath       sql.NullString `db:"trash_path"`
    CleanupLog      sql.NullString `db:"cleanup_log"`
}
```

### 2.3 数据库迁移

通过 `ensureColumns()` 实现增量迁移（幂等）：

```sql
ALTER TABLE tasks ADD COLUMN source_deleted_at DATETIME;
ALTER TABLE tasks ADD COLUMN trash_path TEXT;
ALTER TABLE tasks ADD COLUMN cleanup_log TEXT;
```

---

## 3. 核心流程变更

### 3.1 软删除流程（moveToTrash）

**旧逻辑**：  
- 查询 `completed` 且超时的任务
- 移动文件到垃圾桶
- 无数据库状态更新

**新逻辑**：  
1. 查询 `completed` 且超时的任务
2. 移动文件到垃圾桶，返回 `trash_path`
3. **落库**：`db.MarkSoftDeleted(task_id, trash_path)`
4. 失败时：`db.MarkCleanupError(task_id, error_msg)`
5. 支持回滚：移动失败时尝试恢复原文件

**关键变更**：  
- `safeMoveToTrash` 返回值从 `error` 改为 `(string, error)`
- 增加事务性：先文件操作，再数据库操作，失败则回滚

### 3.2 硬删除流程（emptyTrash）

**旧逻辑**：  
- 扫描文件系统垃圾桶目录
- 按时间戳删除过期文件
- 无数据库状态更新

**新逻辑**：  
1. 查询 `soft_deleted` 且超时的任务：`db.GetSoftDeletedOldTasks(cutoff)`
2. 删除垃圾桶文件
3. **落库**：`db.MarkHardDeleted(task_id)`
4. 失败时：`db.MarkCleanupError(task_id, error_msg)`
5. 文件不存在时：直接标记 `hard_deleted`（兼容手动删除场景）

**关键变更**：  
- 数据驱动：不再依赖文件系统扫描
- 状态一致性：文件与数据库同步

### 3.3 垃圾桶列表（ListTrashFiles）

**新逻辑**（双路径）：  
1. 优先从数据库查询 `soft_deleted` 任务
2. 兜底扫描文件系统（检测未记录文件）
3. 去重后返回

**好处**：  
- 主路径：基于数据库，状态准确
- 降级路径：兼容手动移入的文件

---

## 4. API 与 UI 变更

### 4.1 API 响应扩展

**`/api/stats`** 响应增加字段：

```json
{
    "pending": 10,
    "processing": 2,
    "completed": 50,
    "failed": 1,
    "soft_deleted": 15,    // 新增
    "hard_deleted": 30,    // 新增
    "cleanup_error": 2,    // 新增
    "saved_gb": 12.34
}
```

### 4.2 任务列表筛选

在 `tasks.html` 中添加筛选按钮：

- "软删除" → 筛选 `soft_deleted`
- "硬删除" → 筛选 `hard_deleted`
- "清理失败" → 筛选 `cleanup_error`

**状态徽章颜色**：

- `soft_deleted`: 紫色
- `hard_deleted`: 灰色
- `cleanup_error`: 橙色

---

## 5. 历史数据兼容策略

### 5.1 迁移原则

**保守策略**：  
- 不推断历史 `completed` 任务的清理状态
- 保持历史任务状态不变
- 由 Cleaner 按新逻辑自然处理

**原因**：  
- 无法可靠判断文件是否已在垃圾桶或被手动删除
- 避免误判导致数据不一致

### 5.2 迁移触发机制

在 `database.Init()` 中调用 `migratePhase3HistoricalData()`：

1. 检查是否已有清理状态任务 → 跳过
2. 检查是否有 `completed` 任务 → 无则跳过
3. 记录迁移日志，说明保守策略
4. 返回成功（即使跳过）

**幂等性**：  
- 可多次执行
- 已迁移系统不受影响
- 新系统直接跳过

---

## 6. 测试覆盖

### 6.1 单元测试

**新增测试（`internal/database/db_test.go`）**：

- `TestCleanupLifecycle`: 测试 `completed -> soft_deleted -> hard_deleted`
- `TestMarkCleanupError`: 测试清理失败标记
- `TestGetSoftDeletedOldTasks`: 测试超时软删除任务查询
- `TestStatsWithCleanup`: 测试统计包含清理状态
- `TestEnsureColumnsIdempotent`: 测试列迁移幂等性

**修复测试（`internal/cleaner/cleaner_test.go`）**：

- 适配 `safeMoveToTrash` 返回值变更

### 6.2 集成测试场景

1. 新任务完整生命周期：`pending -> completed -> soft_deleted -> hard_deleted`
2. 软删除失败：文件权限不足 → `cleanup_error`
3. 硬删除时文件丢失：直接 `hard_deleted`
4. 历史数据迁移：升级后系统正常启动
5. Web UI：清理状态可筛选和展示

---

## 7. 风险点与缓解

### 7.1 风险：回滚失败导致文件孤岛

**场景**：  
- 文件移入垃圾桶成功
- 数据库更新失败
- 回滚时文件移动失败

**缓解**：  
- 记录详细错误日志（含源路径和垃圾桶路径）
- 标记为 `cleanup_error`
- 提供手动修复路径

### 7.2 风险：数据库与文件系统不一致

**场景**：  
- 用户手动删除垃圾桶文件
- 数据库仍显示 `soft_deleted`

**缓解**：  
- 硬删除时检查文件存在性，不存在则直接 `hard_deleted`
- `ListTrashFiles` 双路径机制检测不一致

### 7.3 风险：迁移逻辑触发异常

**场景**：  
- 迁移代码 bug 导致系统启动失败

**缓解**：  
- 迁移错误不阻塞系统启动（仅记录警告）
- 保守策略：不修改任何状态
- 充分测试迁移幂等性

---

## 8. 性能影响

### 8.1 查询变化

- 软删除：从文件扫描改为数据库查询 → **性能提升**
- 硬删除：从文件遍历改为数据库查询 → **性能提升**
- 垃圾桶列表：优先数据库 → **性能稳定**

### 8.2 写入增量

- 每次清理操作增加 1 次数据库更新
- 引入索引：`CREATE INDEX IF NOT EXISTS idx_source_deleted_at ON tasks(source_deleted_at)`（未在本次实现）

**建议后续优化**：  
- 添加 `source_deleted_at` 索引
- 批量更新策略

---

## 9. 后续增强建议

### 9.1 短期（Phase 4）

- 添加清理失败重试逻辑
- 清理状态 metrics 导出

### 9.2 长期

- 清理策略可配置（保留天数按路径定制）
- 清理审计日志导出
- 垃圾桶容量告警

---

## 10. 结论

Phase 3 实现了**清理流程的状态机化**，核心价值：

1. **可观测性**：每个任务从创建到删除可全程追踪
2. **可靠性**：文件操作与数据库状态同步，支持错误恢复
3. **可维护性**：清理失败有明确状态和日志，不再"静默失败"
4. **兼容性**：历史数据平滑升级，不破坏已运行系统

**DoD 完成标准**：
- ✅ 所有单元测试通过
- ✅ 数据库迁移幂等
- ✅ Web UI 清理状态可见
- ✅ 文档完整

---

**相关文档**：
- [REFACTOR_PLAN.md](../../REFACTOR_PLAN.md)
- [phase3-test-report.md](./phase3-test-report.md)
- [phase3-rollback.md](./phase3-rollback.md)

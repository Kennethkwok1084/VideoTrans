# Phase 3 回滚方案

**版本**: 1.0  
**日期**: 2026-02-11  
**适用场景**: Phase 3 上线后发现严重问题需要回滚  

---

## 1. 回滚概述

### 1.1 回滚触发条件

**必须回滚（P0）**：
- 系统无法启动
- 数据库迁移导致数据丢失
- 清理模块导致误删除文件
- 清理流程完全失效（停止清理）

**建议回滚（P1）**：
- `cleanup_error` 比例 > 10%
- 清理性能严重下降（比文件扫描慢 10 倍以上）
- Web UI 清理状态显示异常

**不需要回滚**：
- 个别任务清理失败 → 查看日志手动修复
- Web UI 样式问题 → 热修复
- 日志输出过多 → 调整日志级别

---

## 2. 回滚方式选择

### 2.1 方式一：代码回滚（推荐）

**适用场景**: 代码逻辑错误，数据库结构未破坏

**步骤**:  
1. 回退代码到 Phase 3 前的 commit
2. 重新编译并部署
3. 重启服务

**优点**:  
- 简单快速
- 数据库新增字段保留（不影响旧代码）
- 可以保留已清理的任务状态

**缺点**:  
- 新增的清理状态任务将返回 `completed`（状态回退，但不丢失）

---

### 2.2 方式二：数据库回滚 + 代码回滚

**适用场景**: 数据库迁移导致数据异常

**步骤**:  
1. 停止服务
2. 恢复数据库备份（回滚前备份）
3. 回退代码到 Phase 3 前的 commit
4. 重新编译并部署
5. 重启服务

**优点**:  
- 完全回到 Phase 3 前的状态
- 数据一致性保证

**缺点**:  
- 需要数据库备份
- Phase 3 之后的所有任务记录丢失

---

### 2.3 方式三：部分回滚（热修复）

**适用场景**: 清理模块有 bug，其他模块正常

**步骤**:  
1. 禁用 Cleaner 组件（注释掉启动代码）
2. 回退 `internal/cleaner` 到旧逻辑
3. 保留数据库新字段和状态
4. 重新编译并部署

**优点**:  
- 其他功能不受影响
- 可以保留已生成的清理状态数据
- 后续修复后再次启用

**缺点**:  
- 清理功能回退到文件系统扫描模式
- 需要手动维护

---

## 3. 详细回滚步骤

### 3.1 预备工作

**备份关键数据**（回滚前必做）:  
```bash
# 备份数据库
cp /data/stm/tasks.db /data/stm/tasks.db.backup-$(date +%Y%m%d_%H%M%S)

# 备份配置
cp /app/configs/config.yaml /app/configs/config.yaml.backup

# 记录当前版本
git log -1 --oneline > /tmp/phase3-version.txt
```

### 3.2 代码回滚步骤

#### 步骤 1: 停止服务

```bash
# Docker 环境
docker-compose down

# 或直接停止进程
pkill stm
```

#### 步骤 2: 回退代码

```bash
cd /srv/code/Video_Trans

# 查找 Phase 3 前的 commit（假设是 abc123）
git log --oneline --grep="Phase 2" | head -1

# 回退到该 commit（假设 hash 是 abc123）
git checkout abc123

# 或者回退特定文件
git checkout abc123 -- internal/database/models.go
git checkout abc123 -- internal/database/db.go
git checkout abc123 -- internal/cleaner/cleaner.go
git checkout abc123 -- internal/web/server.go
git checkout abc123 -- internal/web/templates/
```

#### 步骤 3: 重新编译

```bash
go mod tidy
go build -o bin/stm cmd/stm/main.go
```

#### 步骤 4: 验证编译

```bash
# 测试编译产物
./bin/stm --version

# 运行基础测试
go test ./internal/database/... -short
```

#### 步骤 5: 重新部署

```bash
# Docker 环境
docker-compose build --no-cache
docker-compose up -d

# 或直接启动
./bin/stm
```

#### 步骤 6: 验证回滚

```bash
# 检查服务启动
curl http://localhost:9999/api/stats

# 检查日志无错误
tail -f /data/stm/logs/stm.log

# 验证清理功能
ls /mnt/pve/media/downloads/.stm_trash/
```

---

### 3.3 数据库回滚步骤（如需要）

#### 步骤 1: 停止服务（同上）

#### 步骤 2: 恢复数据库备份

```bash
# 备份当前数据库（双重保险）
mv /data/stm/tasks.db /data/stm/tasks.db.phase3-broken

# 恢复旧备份
cp /data/stm/tasks.db.backup-20260210_120000 /data/stm/tasks.db

# 验证数据库完整性
sqlite3 /data/stm/tasks.db "PRAGMA integrity_check;"
```

#### 步骤 3: 检查数据

```bash
sqlite3 /data/stm/tasks.db <<EOF
.mode column
SELECT status, COUNT(*) FROM tasks GROUP BY status;
EOF
```

#### 步骤 4: 继续代码回滚步骤 2-6

---

### 3.4 部分回滚（仅 Cleaner）

#### 步骤 1: 临时禁用 Cleaner

编辑 `cmd/stm/main.go`：

```go
// 注释掉 Cleaner 启动
// app.AddComponent("Cleaner", func(ctx context.Context) error {
//     return cleanerInstance.Run(ctx)
// })
```

#### 步骤 2: 回退 Cleaner 逻辑

```bash
git checkout <phase2-commit> -- internal/cleaner/cleaner.go
```

#### 步骤 3: 重新编译并部署

```bash
go build -o bin/stm cmd/stm/main.go
# ... 部署步骤
```

---

## 4. 数据一致性处理

### 4.1 回滚后的数据状态

**场景**: 代码回滚，但数据库保留新字段和状态

| 数据库状态 | 旧代码行为 | 影响 |
|-----------|----------|------|
| `soft_deleted` | 视为 `completed` | 可能重复清理（但会跳过已删除文件） |
| `hard_deleted` | 视为 `completed` | 无影响（文件已不存在） |
| `cleanup_error` | 视为 `completed` | 可能重试清理 |
| 新字段（trash_path 等） | 忽略 | 无影响 |

**结论**: 代码回滚后，旧代码可以正常运行，新状态任务会被当作 `completed` 处理。

### 4.2 手动修复（如需要）

**如果需要将新状态任务改回 `completed`**:

```sql
-- 备份当前状态
CREATE TABLE tasks_backup AS SELECT * FROM tasks WHERE status IN ('soft_deleted', 'hard_deleted', 'cleanup_error');

-- 重置为 completed
UPDATE tasks SET status = 'completed' WHERE status IN ('soft_deleted', 'hard_deleted', 'cleanup_error');

-- 验证
SELECT status, COUNT(*) FROM tasks GROUP BY status;
```

**警告**: 仅在数据库回滚方案无法执行时使用。

---

## 5. 常见问题与处理

### 5.1 问题：回滚后系统仍无法启动

**症状**: 服务启动报错，提示数据库字段缺失

**原因**: 数据库已添加新字段，旧代码未适配

**处理方式 1**（推荐）:  
- 检查回滚代码是否完整
- 确认 `internal/database/db.go` 中的 `ensureColumns()` 逻辑已回退

**处理方式 2**（降级）:  
- 使用数据库备份恢复

---

### 5.2 问题：回滚后清理功能不工作

**症状**: 文件不再移入垃圾桶

**原因**: Cleaner 逻辑回退到文件扫描模式，但配置可能不一致

**处理**:  
- 检查配置 `cleaning.soft_delete_days` 和 `cleaning.hard_delete_days`
- 手动触发清理测试：
  ```bash
  # 检查垃圾桶目录是否存在
  ls /mnt/pve/media/downloads/.stm_trash/
  
  # 查看 Cleaner 日志
  grep "\[Cleaner\]" /data/stm/logs/stm.log
  ```

---

### 5.3 问题：已清理的文件状态异常

**症状**: Web UI 显示 `soft_deleted` 但文件不在垃圾桶

**原因**: 回滚前已执行清理，回滚后状态不同步

**处理**:  
- 使用 `ListTrashFiles` 兜底机制（会扫描文件系统）
- 或手动修复状态（见 4.2）

---

## 6. 回滚验证清单

回滚完成后，逐项验证：

- [ ] 服务正常启动，无启动错误
- [ ] 数据库连接正常
- [ ] Web UI 可访问（http://localhost:9999）
- [ ] 任务列表正常显示
- [ ] 统计数据正确（`/api/stats`）
- [ ] 扫描功能正常
- [ ] Worker 可正常转码
- [ ] 清理功能正常（新完成任务可清理）
- [ ] 日志无异常错误
- [ ] 垃圾桶目录可访问

---

## 7. 后续处理

### 7.1 回滚后的监控

- 持续监控系统稳定性 24 小时
- 检查清理功能是否正常执行
- 观察是否有数据不一致告警

### 7.2 问题分析与修复

- 收集回滚前的错误日志
- 分析根本原因
- 在测试环境复现并修复
- 准备 Phase 3 的 Hotfix 版本

### 7.3 重新上线准备

- 修复后在测试环境充分验证
- 准备新的回滚方案（基于本次经验）
- 准备更详细的监控指标
- 制定灰度发布策略（如有条件）

---

## 8. 应急联系

**问题升级路径**:  
1. 尝试代码回滚（方式一）
2. 如无效，尝试数据库回滚（方式二）
3. 如仍有问题，联系开发负责人

**关键数据备份位置**:  
- 数据库备份: `/data/stm/tasks.db.backup-*`
- 日志归档: `/data/stm/logs/*.log`
- 代码版本记录: `/tmp/phase3-version.txt`

---

## 9. 附录：快速回滚命令

```bash
#!/bin/bash
# phase3-rollback.sh - 快速回滚 Phase 3

set -e

# 配置
REPO_DIR=/srv/code/Video_Trans
DB_PATH=/data/stm/tasks.db
BACKUP_DIR=/data/stm/backups
PHASE2_COMMIT=abc123  # 替换为实际 commit hash

echo "=== Phase 3 快速回滚脚本 ==="
echo "按 Ctrl+C 取消，或按 Enter 继续..."
read

# 1. 停止服务
echo "[1/6] 停止服务..."
docker-compose -f $REPO_DIR/docker-compose.yml down || pkill stm

# 2. 备份当前数据库
echo "[2/6] 备份当前数据库..."
mkdir -p $BACKUP_DIR
cp $DB_PATH $BACKUP_DIR/tasks.db.phase3-$(date +%Y%m%d_%H%M%S)

# 3. 回退代码
echo "[3/6] 回退代码..."
cd $REPO_DIR
git checkout $PHASE2_COMMIT

# 4. 重新编译
echo "[4/6] 重新编译..."
go build -o bin/stm cmd/stm/main.go

# 5. 重新部署
echo "[5/6] 重新部署..."
docker-compose -f $REPO_DIR/docker-compose.yml up --build -d

# 6. 验证
echo "[6/6] 验证服务..."
sleep 5
curl -f http://localhost:9999/api/stats || echo "警告：服务未正常响应"

echo "=== 回滚完成 ==="
echo "请检查日志：tail -f /data/stm/logs/stm.log"
```

**使用方式**:  
```bash
chmod +x phase3-rollback.sh
./phase3-rollback.sh
```

---

**相关文档**:
- [phase3-design.md](./phase3-design.md)
- [phase3-test-report.md](./phase3-test-report.md)
- [REFACTOR_PLAN.md](../../REFACTOR_PLAN.md)

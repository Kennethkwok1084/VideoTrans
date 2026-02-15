# 安全配置指南

## 概述

本文档说明了 STM（视频转码管理系统）的安全配置和最佳实践。

## P0 级安全修复（已完成）

### 1. API 认证机制

**问题：** Web API 原本完全开放，无任何认证机制，存在严重安全风险。

**修复：** 新增基于 API Key 的认证机制。

#### 配置方法

在 `config.yaml` 中设置 API Key：

```yaml
web:
  api_key: "your-secure-api-key-here"  # 建议使用 32 位以上的随机字符串
```

生成强密钥示例：

```bash
# Linux/macOS
openssl rand -base64 32

# 或使用 Python
python3 -c "import secrets; print(secrets.token_urlsafe(32))"
```

#### 使用方法

所有 API 请求需要在 HTTP 头中包含 `X-API-Key`：

```bash
curl -H "X-API-Key: your-secure-api-key-here" http://localhost:8080/api/stats
```

**注意：**
- 如果 `api_key` 为空字符串或未配置，则**禁用认证**（仅适用于开发环境）
- **生产环境务必设置强密钥**
- 前端页面（HTML）不受认证限制，仅 `/api/*` 端点需要认证

#### Docker Compose 配置示例

```yaml
services:
  stm:
    image: your-registry/stm:latest
    environment:
      - STM_WEB_API_KEY=your-secure-api-key-here
    # 或通过配置文件挂载
    volumes:
      - ./config.yaml:/app/config.yaml:ro
```

如果使用环境变量，需要在应用启动时支持环境变量覆盖（建议后续增强）。

---

### 2. 路径遍历防护

**问题：** `handleBrowseDirectory` 等文件操作 API 允许访问任意路径，存在路径遍历攻击风险。

**修复：**
- 新增 `isPathSafeForBrowsing()` 函数，限制目录浏览范围
- 只允许访问已配置的输入/输出目录及其父目录（向上最多 3 层）
- 如果尚未配置目录，仅允许浏览常见根目录（`/mnt`, `/media`, `/home` 等）

**影响：**
- API 调用者无法再通过 `../` 等方式访问系统敏感目录
- 尝试访问非法路径会返回 `403 Forbidden` 错误并记录日志

---

### 3. Docker 容器安全

**问题：** 应用在容器中以 `root` 用户运行，安全风险高。

**修复：**
- Dockerfile 中新增非 root 用户 `stm:stm`（UID/GID: 1000）
- 应用以该用户身份运行

**注意事项：**

如果挂载的宿主机目录权限不匹配，可能导致读写失败：

```bash
# 宿主机上调整目录权限
sudo chown -R 1000:1000 /path/to/input /path/to/output /path/to/data
```

或在 `docker-compose.yml` 中指定：

```yaml
services:
  stm:
    user: "1000:1000"
    volumes:
      - /path/to/input:/mnt/input
      - /path/to/output:/mnt/output
```

---

### 4. 健康检查修复

**问题：** `Dockerfile` 中的 `HEALTHCHECK` 使用 `wget`，但基础镜像未安装，导致容器无限重启。

**修复：**
- 在 Dockerfile 中安装 `wget`
- 健康检查命令：`wget --quiet --tries=1 --spider http://localhost:8080/api/health`

---

### 5. 其他代码质量改进

#### handleAddDirectory Context 修复

**问题：** 新增目录后触发的后台扫描使用了 API 请求的 context，可能在请求结束后被取消。

**修复：** 改用 `context.Background()` 创建独立 context，确保扫描任务不受 API 请求生命周期影响。

#### Worker nil 指针防护

**问题：** `SetForceRun` 和 `SetMaxWorkers` 中，如果 `mainCtx` 为 nil 可能导致未预期行为。

**修复：**
- 增加 nil 检查和警告日志
- 将 `mainCtx` 的读取移到锁内，避免并发问题

---

## 剩余安全建议（P1-P3）

### P1 - 基础加固

1. **HTTPS 支持**
   - 建议在生产环境使用反向代理（如 Nginx/Traefik）提供 HTTPS
   - 或在应用中增加 TLS 配置选项

2. **审计日志**
   - 记录所有 API 调用（尤其是敏感操作：删除任务、修改配置等）
   - 记录来源 IP、操作时间、操作结果

3. **速率限制**
   - 防止暴力破解 API Key
   - 建议使用 Nginx 的 `limit_req` 模块或 Go 中间件

### P2 - 增强认证

1. **多用户支持**
   - 引入用户表和角色（只读/管理员）
   - 使用 JWT 或 Session 管理

2. **API Key 轮换**
   - 支持多个有效 API Key
   - 定期轮换和失效机制

### P3 - 合规性

1. **敏感信息脱敏**
   - 日志中不输出完整文件路径（可选）
   - 不在错误信息中暴露系统路径

2. **依赖漏洞扫描**
   - 集成 `govulncheck` 或 `trivy` 到 CI/CD
   - 定期更新依赖包

---

## 快速安全检查清单

部署前请确认：

- [ ] 已设置强 API Key（`web.api_key` 不为空）
- [ ] Docker 容器以非 root 用户运行（UID 1000）
- [ ] 配置文件权限正确（`chmod 600 config.yaml`）
- [ ] 数据目录权限匹配容器用户（UID 1000）
- [ ] 生产环境使用 HTTPS（通过反向代理）
- [ ] 限制 Web 端口仅监听内网 IP（如 `127.0.0.1:8080`）
- [ ] 定期备份数据库（`tasks.db`）

---

## 报告安全问题

如果发现新的安全问题，请通过以下方式报告：

1. **邮件：** security@example.com
2. **Issue：** 标记为 `[Security]`，避免公开详细利用方式

我们将在 48 小时内响应并评估问题的严重性。

---

## 变更日志

| 日期       | 版本   | 修复内容                              |
|------------|--------|---------------------------------------|
| 2026-02-16 | v1.0.0 | 初始安全加固：API 认证、路径遍历防护等 |

# P0 级安全修复完成报告

**修复日期:** 2026年2月16日  
**严重性级别:** P0（紧急）  
**测试状态:** ✅ 所有测试通过（`go test ./...` + `go vet ./...` + `gofmt -l` 全部绿灯）

---

## 修复内容总览

本次修复解决了代码评估报告中标记的所有 P0 级别安全漏洞和稳定性问题。

### ✅ 1. API 认证机制（严重）

**文件修改:**
- [configs/config.yaml](configs/config.yaml) - 新增 `web.api_key` 配置项
- [internal/config/config.go](internal/config/config.go) - 新增 `WebConfig` 结构体
- [internal/web/server.go](internal/web/server.go) - 新增 `apiKeyAuth()` 中间件

**防护效果:**
- 所有 `/api/*` 端点需要 `X-API-Key` 请求头认证
- 未授权访问返回 `401 Unauthorized` 并记录日志
- 生产环境强制要求设置密钥

**使用示例:**
```bash
# 配置文件中设置
web:
  api_key: "Xp9k2mZv8Qr3hN6c4B7dF1sA0wE5tY"

# API 调用
curl -H "X-API-Key: Xp9k2mZv8Qr3hN6c4B7dF1sA0wE5tY" \
     http://localhost:8080/api/stats
```

---

### ✅ 2. 路径遍历防护（高）

**文件修改:**
- [internal/web/server.go](internal/web/server.go) - 新增 `isPathSafeForBrowsing()` 函数

**防护效果:**
- 限制目录浏览仅在已配置的输入/输出目录范围内
- 阻止 `../` 等路径穿越攻击
- 非法访问返回 `403 Forbidden` 并记录来源 IP

**安全策略:**
- 已配置目录：允许访问该目录及其子目录
- 未配置目录：仅允许浏览白名单根目录（`/mnt`, `/media`, `/home` 等）
- 父目录访问：最多向上 3 层

---

### ✅ 3. Docker 容器安全（中）

**文件修改:**
- [Dockerfile](Dockerfile) - 新增非 root 用户 `stm:stm` (UID/GID: 1000)

**安全加固:**
```dockerfile
# 创建非特权用户
RUN addgroup -g 1000 stm && \
    adduser -D -u 1000 -G stm stm && \
    chown -R stm:stm /app /data /input /output

# 切换用户
USER stm
```

**部署注意:**
- 宿主机挂载目录需要授权给 UID 1000：`sudo chown -R 1000:1000 /path/to/data`
- Docker Compose 可指定 `user: "1000:1000"` 覆盖

---

### ✅ 4. 健康检查修复（高）

**文件修改:**
- [Dockerfile](Dockerfile) - 安装 `wget` 工具

**问题原因:**
- 基础镜像 (`jrottenberg/ffmpeg:6.1-alpine`) 缺少 `wget`
- 健康检查命令失败导致容器无限重启

**修复方案:**
```dockerfile
# 安装时区数据和 wget（健康检查需要）
RUN apk add --no-cache tzdata wget

HEALTHCHECK --interval=30s --timeout=3s \
  CMD wget --quiet --tries=1 --spider http://localhost:8080/api/health || exit 1
```

---

### ✅ 5. Context 生命周期修复（中）

**问题:** `handleAddDirectory` 中异步扫描使用请求级 context，可能被提前取消

**文件修改:**
```go
// 修复前（错误）
go func() {
    ctx := s.ctx  // 可能为 nil 或被取消
    s.scanner.Scan(ctx)
}()

// 修复后（正确）
go func() {
    s.scanner.Scan(context.Background())  // 独立生命周期
}()
```

---

### ✅ 6. Worker Nil 指针防护（中）

**文件修改:**
- [internal/worker/worker.go](internal/worker/worker.go) - `SetForceRun()` 和 `SetMaxWorkers()`

**防护措施:**
```go
// 读取 mainCtx 时加锁
w.mu.Lock()
mainCtx := w.mainCtx
w.mu.Unlock()

// 使用前检查
if mainCtx != nil {
    go w.adjustWorkerPool(mainCtx, targetWorkers)
} else {
    log.Println("[Worker] 警告：mainCtx 未设置，无法立即调整 Worker Pool")
}
```

---

## 测试验证

### 运行结果

```bash
$ go test ./...
ok      github.com/stm/video-transcoder/internal/app        38.724s
ok      github.com/stm/video-transcoder/internal/cleaner     0.025s
ok      github.com/stm/video-transcoder/internal/config      0.004s
ok      github.com/stm/video-transcoder/internal/database    0.130s
ok      github.com/stm/video-transcoder/internal/scanner     0.058s
ok      github.com/stm/video-transcoder/internal/web         0.013s
ok      github.com/stm/video-transcoder/internal/worker      0.006s
```

**结论:** ✅ 所有测试通过，修复未引入新问题

---

## 部署清单

### 必做项

- [ ] **设置 API Key:** 在 `config.yaml` 中配置 `web.api_key`（32 位以上随机字符串）
- [ ] **调整目录权限:** `sudo chown -R 1000:1000 /path/to/{data,input,output}`
- [ ] **重新构建镜像:** `docker build -t stm:v1.0.1 .`
- [ ] **更新 docker-compose:** 配置 `api_key` 环境变量或挂载新配置文件
- [ ] **验证健康检查:** `docker ps` 确认容器状态为 `healthy`

### 建议项

- [ ] 使用反向代理（Nginx/Traefik）提供 HTTPS
- [ ] 限制 Web 端口仅监听内网：`0.0.0.0:8080` → `127.0.0.1:8080`
- [ ] 启用审计日志记录敏感操作
- [ ] 定期备份 `tasks.db` 数据库

---

## 回滚方案

如遇问题，可快速回滚：

```bash
# 1. 禁用 API 认证（应急）
web:
  api_key: ""  # 设为空字符串

# 2. 容器权限问题临时解决
docker-compose.yml:
  services:
    stm:
      user: "0:0"  # 以 root 运行（不推荐）

# 3. 恢复旧镜像
docker pull stm:v1.0.0
```

---

## 后续建议（P1-P3）

详见 [docs/SECURITY.md](docs/SECURITY.md)：

1. **P1:** 审计日志、速率限制、HTTPS 加固
2. **P2:** 多用户支持、JWT 认证、API Key 轮换
3. **P3:** 依赖漏洞扫描、敏感信息脱敏

---

## 参考文档

- [docs/SECURITY.md](docs/SECURITY.md) - 完整安全配置指南
- [REFACTOR_PLAN.md](REFACTOR_PLAN.md) - 架构重构计划
- [README.md](README.md) - 项目使用文档

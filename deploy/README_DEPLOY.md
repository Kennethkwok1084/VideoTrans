# STM 视频转码系统 - 部署指南

## 📦 部署包内容
- `stm-v1.0.1.tar.gz` - 完整部署包（35KB）

## 🚀 快速部署步骤

### 1. 连接到远程服务器
```bash
ssh kwok@192.168.31.124
```

### 2. 创建部署目录并解压
```bash
# 创建目录（需要sudo权限）
sudo mkdir -p /opt/stm
sudo chown $USER:$USER /opt/stm

# 解压部署包
cd /opt/stm
tar -xzf /tmp/stm-v1.0.1.tar.gz

# 创建数据目录
mkdir -p data
```

### 3. 配置挂载目录

编辑 `docker-compose.yml`：
```bash
vi /opt/stm/docker-compose.yml
```

修改 `volumes` 部分（根据你的实际目录）：
```yaml
volumes:
  - /mnt:/mnt                        # 推荐：挂载整个 /mnt（与默认配置一致）
  - ./data:/data
  - ./configs/config.yaml:/app/config.yaml:ro

ports:
  - "9999:8080"
```

### 4. 配置转码参数

编辑配置文件：
```bash
vi /opt/stm/configs/config.yaml
```

关键配置项：
```yaml
path:
  pairs:
    - input: "/mnt/pve/media/downloads"
      output: "/mnt/pve/media/archive"
  trash: ".stm_trash"
  database: "/data/tasks.db"         # 数据库路径

ffmpeg:
  codec: "libx264"                   # 视频编码器
  crf: 28                            # 质量参数（18-28）
  preset: "veryslow"                 # 速度预设

system:
  max_workers: 3                     # 最大并发数（根据CPU核心数调整）
  scan_interval: 10                  # 扫描间隔（分钟）
```

### 5. 构建并启动服务

```bash
cd /opt/stm

# 构建Docker镜像
docker compose build

# 启动服务（后台运行）
docker compose up -d

# 查看日志
docker compose logs -f
```

### 6. 验证部署

访问Web界面：
```
http://192.168.31.124:9999
```

检查健康状态：
```bash
curl http://localhost:9999/api/health
# 预期输出: {"status":"ok"}
```

## 🔧 常用管理命令

```bash
# 查看容器状态
docker compose ps

# 查看实时日志
docker compose logs -f

# 重启服务
docker compose restart

# 停止服务
docker compose stop

# 停止并删除容器
docker compose down

# 查看资源占用
docker stats stm

# 进入容器
docker exec -it stm sh
```

## 📊 Prometheus 指标

访问 Prometheus 指标端点：
```
http://192.168.31.124:9999/metrics
```

主要指标：
- `stm_tasks_total{status="..."}` - 任务总数
- `stm_workers_active` - 活跃Worker数
- `stm_transcode_duration_seconds` - 转码耗时
- `stm_disk_space_available_bytes` - 可用磁盘空间
- `stm_space_saved_bytes` - 节省的存储空间

## 🗑️ 垃圾回收机制

- **软删除**：转码完成后7天内保留源文件到回收站
- **硬删除**：回收站中超过30天的文件会被永久删除
- **定时清理**：每天上午10:00自动执行

访问回收站管理：
```
http://192.168.31.124:9999/trash
```

## ⚠️ 故障排查

### 容器无法启动
```bash
# 查看详细日志
docker compose logs stm

# 检查配置文件语法
docker run --rm -v $(pwd)/configs:/configs \
  alpine:latest cat /configs/config.yaml
```

### FFmpeg转码失败
```bash
# 查看FFmpeg版本
docker exec stm ffmpeg -version

# 手动测试转码
docker exec stm ffmpeg -i /mnt/pve/media/downloads/test.mkv \
  -c:v libx264 -crf 28 -preset veryslow \
  -c:a aac -b:a 128k /tmp/test_output.mp4
```

### 磁盘空间不足
```bash
# 检查容器内磁盘使用
docker exec stm df -h

# 手动清理回收站
docker exec stm rm -rf /mnt/pve/media/downloads/.stm_trash/*
```

## 🔄 升级部署

```bash
# 1. 停止服务
cd /opt/stm
docker compose down

# 2. 备份数据
cp -r data data.backup.$(date +%Y%m%d)

# 3. 更新代码
tar -xzf /tmp/stm-v1.0.2.tar.gz -C /opt/stm/

# 4. 重新构建并启动
docker compose build
docker compose up -d
```

## 📝 生产环境建议

1. **资源限制**：根据硬件调整 `docker-compose.yml` 中的CPU和内存限制
2. **监控集成**：将 `/metrics` 接入Prometheus + Grafana
3. **日志管理**：配置日志滚动（限制大小和数量）
4. **定期备份**：备份 `data/tasks.db` 数据库
5. **安全加固**：使用反向代理（Nginx）+ HTTPS

## 📞 技术支持

- 项目文档：查看 `/opt/stm/README.md`
- 配置说明：查看 `/opt/stm/configs/config.yaml` 注释
- 测试与问题记录：查看 `docs/refactor/phase5-test-report.md`

# STM - 视频自动化转码中心

[![Go Version](https://img.shields.io/badge/Go-1.24-blue.svg)](https://golang.org/)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
![Tests](https://img.shields.io/badge/Tests-23%2F23-success.svg)

## 📖 项目简介

STM（Smart Transcode Manager）是一个全自动视频转码系统，专为 NAS 环境设计。它能够在夜间利用闲置算力自动将视频文件压缩为高效的 H.264 格式，节省存储空间的同时保持良好的画质。

### 核心特性

- 🌙 **智能时间调度**：夜间自动转码（22:00-07:00），不影响日常使用
- 🗑️ **双层垃圾桶**：7天缓冲期 + 30天彻底删除，支持跨分区安全移动
- 🔄 **增量扫描**：基于元数据的去重策略，智能识别新文件和更新
- ⚡ **Worker Pool 并发**：可配置并发数，充分利用多核 CPU
- 📊 **现代化 Web 界面**：实时仪表盘、任务列表、垃圾桶管理
- 📈 **Prometheus 监控**：内置 metrics 导出，支持 Grafana 可视化
- 🐳 **Docker 一键部署**：多阶段构建，开箱即用
- 💾 **群晖优化**：自动过滤 Synology Photos 缩略图和系统文件

## 🎯 适用场景

- ✅ NAS 存储空间紧张，需要压缩旧视频
- ✅ 夜间算力闲置，希望自动化利用
- ✅ 需要保留原始文件的缓冲期（误删恢复）
- ✅ 大量视频文件需要批量转码
- ✅ 需要监控转码进度和历史记录

## 🚀 快速开始

### 使用 Docker Compose（推荐）

1. **克隆项目**

```bash
git clone https://github.com/yourname/stm.git
cd stm
```

2. **修改配置**

编辑 `docker-compose.yml`，确认挂载与端口：

```yaml
volumes:
  - /mnt:/mnt                               # 媒体目录根路径
  - ./data:/data                            # 数据库和日志
  - ./configs/config.yaml:/app/config.yaml:ro

ports:
  - "9999:8080"
```

编辑 `configs/config.yaml` 中的 `path.pairs`，设置输入/输出目录映射。

3. **启动服务**

```bash
# 构建镜像
docker-compose build

# 启动服务
docker-compose up -d

# 查看日志
docker-compose logs -f
```

4. **访问 Web 界面**

打开浏览器访问：`http://your-server-ip:9999`

### 本地运行（开发环境）

**前置要求**：
- Go 1.24+
- FFmpeg 6.1+

```bash
# 安装依赖
go mod download

# 编译
go build -o stm ./cmd/stm

# 修改配置
cp configs/config.yaml my-config.yaml
# 编辑 my-config.yaml 设置路径

# 运行
./stm --config my-config.yaml
```

## ⚙️ 配置说明

### 核心配置项

```yaml
# 路径配置（configs/config.yaml）
path:
  pairs:
    - input: "/mnt/pve/media/downloads"   # 输入目录
      output: "/mnt/pve/media/archive"    # 输出目录
  trash: ".stm_trash"        # 垃圾桶目录名
  database: "/data/tasks.db" # 数据库路径

# 系统配置
system:
  max_workers: 3             # 并发转码数
  scan_interval: 10          # 扫描间隔（分钟）
  scheduler_interval: 10     # 调度器检查间隔（秒）
  task_queue_size: 10        # 任务队列容量
  min_disk_space_gb: 5       # 最小磁盘空间要求（GB）

# FFmpeg 配置
ffmpeg:
  codec: "libx264"           # 视频编码器
  preset: "veryslow"         # 预设（slow/veryslow）
  crf: 28                    # 质量（18-28，越大体积越小）
  audio: "aac"               # 音频编码器
  audio_bitrate: "128k"      # 音频比特率

# 清理配置
cleaning:
  soft_delete_days: 7        # 移入垃圾桶天数
  hard_delete_days: 30       # 彻底删除天数
  cron: "0 10 * * *"         # 清理任务时间（Cron 表达式）

# 对外访问端口（docker-compose.yml）
ports:
  - "9999:8080"
```

### 环境变量覆盖

```bash
STM_MAX_WORKERS=3           # 覆盖并发数
STM_INPUT_PATH=/mnt/pve/media/downloads
STM_OUTPUT_PATH=/mnt/pve/media/archive
```

## 📊 使用指南

### Web 界面功能

#### 1. 仪表盘 (`http://localhost:9999`)
- **实时统计卡片**：
  - 待处理任务数（黄色）
  - 处理中任务数（蓝色）
  - 已完成任务数（绿色）
  - 节省空间 GB（紫色）
- **Worker 状态**：实时显示工作模式（自动运行/强制运行/休眠中）
- **失败任务列表**：快速查看和重试失败任务
- **控制按钮**：扫描目录、强制启动、停止强制

#### 2. 任务列表 (`http://localhost:9999/tasks`)
- **状态筛选**：全部/待处理/处理中/已完成/失败
- **任务表格**：文件名、状态、进度条、大小变化、创建时间
- **操作按钮**：重试失败任务、删除任务记录
- **分页控件**：每页 20 条，支持翻页

#### 3. 垃圾桶 (`http://localhost:9999/trash`)
- **警告提示**：30 天自动删除提醒
- **文件列表**：文件名、大小、删除时间、剩余天数
- **倒计时提示**：3天内红色，7天内黄色
- **手动删除**：二次确认对话框

### API 接口

```bash
# 获取统计信息
GET /api/stats

# 获取任务列表
GET /api/tasks?status=pending&page=1&limit=20
# status 可选: pending/processing/completed/failed/irrecoverable

# 手动触发扫描
POST /api/scan

# 强制启动 Worker
POST /api/worker/force-start

# 停止强制运行
POST /api/worker/force-stop

# 获取 Worker 状态
GET /api/worker/status

# 获取垃圾桶列表
GET /api/trash

# 删除垃圾桶文件
DELETE /api/trash/:filename

# 健康检查
GET /api/health

# Prometheus Metrics
GET /metrics
```

### Prometheus 监控

在 `prometheus.yml` 中添加：

```yaml
scrape_configs:
  - job_name: 'stm'
    static_configs:
      - targets: ['localhost:9999']
    metrics_path: /metrics
    scrape_interval: 15s
```

**可用指标**：
- `stm_tasks_total{status}` - 任务总数（按状态分类）
- `stm_workers_active` - 当前活跃 Worker 数
- `stm_transcode_duration_seconds` - 转码耗时分布
- `stm_transcode_success_total` - 转码成功总数
- `stm_transcode_failed_total` - 转码失败总数
- `stm_space_saved_bytes` - 节省存储空间（字节）
- `stm_files_soft_deleted_total` - 移入垃圾桶文件数
- `stm_files_hard_deleted_total` - 彻底删除文件数

## 🔧 工作流程

### 1. 扫描阶段
- **频率**：每 10 分钟扫描一次输入目录
- **过滤规则**：
  - 群晖系统文件：`@eaDir`, `#recycle`, `.DS_Store`
  - 缩略图：`SYNOPHOTO_FILM_*`, `SYNOPHOTO_THUMB_*`
  - 临时文件：`.tmp`, `.part`, `.lock`
- **去重策略**：基于文件路径、大小、修改时间（MD5）

### 2. 转码阶段
- **时间窗口**：配置的工作时间（默认 22:00-07:00）
- **并发控制**：Worker Pool 模式（默认 3 并发）
- **任务队列**：缓冲 10 个待处理任务
- **进度解析**：FFmpeg `-progress pipe:1` 实时输出
- **进度优化**：仅当变化 ≥5% 或间隔 ≥5s 时更新数据库
- **磁盘检查**：转码前检查可用空间（默认最少 5GB）

### 3. 清理阶段（每天 10:00 执行）
- **一级清理**（7天后）：
  - 移入同级目录的 `.stm_trash` 文件夹
  - 文件命名：`原文件名_del_20260105_120000`
  - 同分区：`os.Rename()`（毫秒级）
  - 跨分区：`io.Copy()` + `os.Remove()`（自动降级）
  
- **二级清理**（30天后）：
  - 解析文件名时间戳
  - 超过阈值直接 `os.Remove()`
  - 彻底删除，不可恢复

## 📁 目录结构

```
stm/
├── cmd/
│   └── stm/
│       └── main.go              # 主程序入口
├── internal/
│   ├── config/                  # 配置管理
│   │   ├── config.go
│   │   └── config_test.go
│   ├── database/                # 数据库层
│   │   ├── db.go
│   │   ├── models.go
│   │   └── db_test.go
│   ├── scanner/                 # 扫描器
│   │   ├── scanner.go
│   │   └── scanner_test.go
│   ├── worker/                  # 转码执行器
│   │   ├── worker.go
│   │   └── worker_test.go
│   ├── cleaner/                 # 清理模块
│   │   ├── cleaner.go
│   │   └── cleaner_test.go
│   ├── web/                     # Web 服务
│   │   ├── server.go
│   │   └── templates/
│   │       ├── index.html       # 仪表盘
│   │       ├── tasks.html       # 任务列表
│   │       └── trash.html       # 垃圾桶
│   └── metrics/                 # Prometheus 指标
│       └── metrics.go
├── configs/
│   └── config.yaml              # 默认配置
├── Dockerfile                   # 多阶段构建
├── docker-compose.yml           # Docker Compose 配置
├── go.mod
├── go.sum
└── README.md
```

## 🛠️ 技术栈

- **语言**：Go 1.24
- **数据库**：SQLite (modernc.org/sqlite)
- **Web 框架**：Gin
- **定时任务**：robfig/cron/v3
- **监控指标**：Prometheus client_golang
- **前端**：HTML5 + TailwindCSS CDN + Fetch API
- **视频处理**：FFmpeg 6.1
- **部署**：Docker + Docker Compose

## 📈 性能参数

### 转码性能（Ryzen 3500X + preset=veryslow + crf=27）

| 视频规格 | 原始大小 | 转码后 | 压缩率 | 耗时 |
|---------|---------|--------|--------|------|
| 1080p/30fps/10min | 2.5 GB | 450 MB | 82% | ~20 分钟 |
| 1080p/60fps/10min | 4 GB | 750 MB | 81% | ~35 分钟 |
| 4K/30fps/10min | 6 GB | 1.0 GB | 83% | ~60 分钟 |
| 4K/60fps/10min | 10 GB | 1.8 GB | 82% | ~120 分钟 |

### 资源占用

- **CPU**：80%-90% (3 workers，转码期间)
- **内存**：
  - 运行时：30-50 MB
  - 休眠时：15-20 MB
- **磁盘 I/O**：50-100 MB/s (读写)
- **网络**：0 (本地处理)

### 数据库性能
- **1万任务**：数据库大小 < 10MB
- **查询速度**：< 1ms (索引优化)
- **进度更新**：98% 减少（5% 变化或 5s 间隔）

## 🐛 故障排查

### 常见问题

**Q: 转码失败，提示找不到 FFmpeg**

A: 确保 Docker 镜像使用 `jrottenberg/ffmpeg:6.1-alpine` 作为基础镜像。

**Q: 群晖缩略图被转码**

A: Scanner 已内置过滤 `SYNOPHOTO_*` 文件，检查日志确认是否跳过。

**Q: 跨分区移动失败**

A: 程序会自动降级为复制+删除模式，检查日志中是否有 "跨分区" 提示。

**Q: Worker 在白天不停止**

A: 检查配置 `cron_start` 和 `cron_end`，确保时间窗口正确（例如 22-7 表示 22:00-07:00）。

**Q: 磁盘空间不足导致转码失败**

A: 转码前会检查磁盘空间（默认最少 5GB），可调整 `min_disk_space_gb` 配置。

**Q: 数据库锁定错误**

A: SQLite 已启用 WAL 模式，如仍有问题请减少并发数。

### 日志查看

```bash
# Docker 日志
docker-compose logs -f stm

# 应用日志
tail -f ./data/stm.log
```

## 🤝 贡献

欢迎提交 Issue 和 Pull Request！

## 📄 许可证

MIT License

## 🙏 致谢

- [FFmpeg](https://ffmpeg.org/) - 视频处理核心
- [Gin](https://gin-gonic.com/) - Web 框架
- [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) - 纯 Go SQLite 实现

---

**开发者**: STM Team  
**版本**: v1.0  
**最后更新**: 2026-01-05

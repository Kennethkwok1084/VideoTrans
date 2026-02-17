# STM Systemd 快速参考

## 📦 快速部署

```bash
# 方式 1: 使用快速部署脚本
sudo deploy/systemd/quickstart.sh

# 方式 2: 使用 Makefile
make quick-deploy

# 方式 3: 手动安装
make install
make enable
make start
```

## 🛠️ 常用命令

### 服务管理
```bash
sudo systemctl start stm      # 启动服务
sudo systemctl stop stm       # 停止服务
sudo systemctl restart stm    # 重启服务
sudo systemctl status stm     # 查看状态
sudo systemctl enable stm     # 启用开机自启
sudo systemctl disable stm    # 禁用开机自启
```

### Makefile 快捷命令
```bash
make status        # 查看服务状态
make start         # 启动服务
make stop          # 停止服务
make restart       # 重启服务
make logs          # 查看实时日志
make logs-recent   # 查看最近100条日志
make enable        # 启用开机自启
make disable       # 禁用开机自启
```

## 📋 日志查看

```bash
# 实时日志
journalctl -u stm -f

# 最近 100 条
journalctl -u stm -n 100

# 仅错误日志
journalctl -u stm -p err

# 指定时间范围
journalctl -u stm --since "2024-01-01" --until "2024-01-02"

# 应用日志文件
tail -f /data/stm.log

# Makefile
make logs          # 实时日志
make logs-recent   # 最近日志
make tail-errors   # 错误日志
```

## 🔧 配置管理

```bash
# 编辑配置
sudo nano /opt/stm/configs/config.yaml

# 重启使配置生效
sudo systemctl restart stm

# 备份配置
sudo cp /opt/stm/configs/config.yaml ~/stm-config-backup.yaml

# Makefile
make update-config  # 更新配置文件
```

## 🖥️ 硬件检测

```bash
# 完整硬件检测
sudo deploy/systemd/check-hardware.sh

# NVIDIA GPU 信息
nvidia-smi
make nvidia-info

# Intel GPU 信息
vainfo
make intel-info

# Makefile
make check-hardware  # 完整硬件检测
```

## 🔄 更新程序

```bash
# 更新代码
cd /home/kwok/coder/Video_Trans
git pull

# 编译并更新
go build -o bin/stm ./cmd/stm
sudo cp bin/stm /opt/stm/bin/
sudo systemctl restart stm

# Makefile (推荐)
make update         # 自动编译、停止、更新、启动
```

## 📊 监控

```bash
# 查看服务状态
systemctl status stm

# 实时监控（自动刷新）
make monitor

# 访问 Web 界面
http://localhost:8080

# 查看 Prometheus metrics
curl http://localhost:8080/metrics
```

## 🗑️ 卸载

```bash
# 使用卸载脚本
sudo deploy/systemd/uninstall.sh

# Makefile
make uninstall
```

## 📂 重要路径

```bash
/opt/stm/bin/stm                    # 程序文件
/opt/stm/configs/config.yaml        # 配置文件
/data/tasks.db                      # 数据库
/data/stm.log                       # 应用日志
/etc/systemd/system/stm.service     # Service 文件
```

## 💾 数据库管理

```bash
# 备份数据库
make backup-db

# 手动备份
sudo cp /data/tasks.db /backup/tasks-$(date +%Y%m%d).db

# 恢复数据库
make restore-db DB_FILE=/path/to/backup.db

# 手动恢复
sudo systemctl stop stm
sudo cp /backup/tasks.db /data/tasks.db
sudo systemctl start stm
```

## 🔍 故障排查

### 检查服务状态
```bash
systemctl status stm --no-pager
journalctl -u stm -n 50
```

### 测试 NVENC
```bash
ffmpeg -f lavfi -i testsrc=duration=1:size=320x240:rate=30 \
  -c:v h264_nvenc -preset fast -f null -
```

### 测试 QSV
```bash
ffmpeg -f lavfi -i testsrc=duration=1:size=320x240:rate=30 \
  -c:v h264_qsv -preset fast -f null -
```

### 检查权限
```bash
ls -l /dev/dri/
groups  # 应包含 video 和 render
```

### 添加用户到组
```bash
sudo usermod -aG video,render $USER
# 重新登录生效
```

## ⚙️ 快速配置模板

### NVIDIA 优先
```yaml
run_mode: "systemd"
cpu_fallback: true
worker_mapping:
  - "nvidia_high"
  - "nvidia_high"
  - "cpu_baseline"
```

### Intel QSV 优先
```yaml
run_mode: "systemd"
cpu_fallback: true
worker_mapping:
  - "intel_balanced"
  - "intel_balanced"
  - "cpu_baseline"
```

### 混合模式
```yaml
run_mode: "systemd"
cpu_fallback: true
worker_mapping:
  - "nvidia_high"
  - "intel_balanced"
  - "cpu_baseline"
```

更多配置示例: [config-examples.md](config-examples.md)

## 📚 完整文档

- [Systemd 部署完整指南](README_SYSTEMD.md)
- [配置示例集合](config-examples.md)
- [主项目 README](../../README.md)

## 🆘 获取帮助

```bash
# 查看所有 Makefile 命令
make help

# 查看系统信息
make info

# 运行硬件检测
make check-hardware
```

## 常见问题

**Q: 服务启动失败怎么办?**
```bash
# 查看详细错误
journalctl -u stm -n 50
# 检查配置文件
sudo /opt/stm/bin/stm -config /opt/stm/configs/config.yaml
```

**Q: 硬件编码不工作?**
```bash
# 运行硬件检测脚本
make check-hardware
# 查看应用日志中的错误
journalctl -u stm | grep -i "encode\|nvenc\|qsv"
```

**Q: 如何修改工作时间?**
```yaml
# 编辑 config.yaml
system:
  cron_start: 22  # 晚上10点
  cron_end: 7     # 早上7点
```

**Q: 如何增加并发数?**
```yaml
# 编辑 config.yaml
system:
  max_workers: 6  # 增加到 6 个
```

---

**快速开始**: `sudo deploy/systemd/quickstart.sh`

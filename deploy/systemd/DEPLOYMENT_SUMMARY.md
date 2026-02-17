# STM Systemd 部署文件清单

## 📁 已创建的文件

### 核心部署文件

1. **`stm.service`** - Systemd 服务配置文件
   - 定义服务运行方式
   - 配置 NVIDIA 和 Intel 硬件访问权限
   - 设置环境变量和资源限制

2. **`install.sh`** - 自动安装脚本
   - 构建 Go 项目
   - 安装文件到系统目录
   - 配置 systemd 服务
   - 检查硬件支持

3. **`uninstall.sh`** - 卸载脚本
   - 停止和禁用服务
   - 删除服务文件
   - 可选清理程序和数据

4. **`check-hardware.sh`** - 硬件检测脚本
   - 检测 NVIDIA GPU 和驱动
   - 检测 Intel QSV 支持
   - 验证 FFmpeg 硬件编码能力
   - 测试编码功能
   - 提供配置建议

5. **`quickstart.sh`** - 快速部署脚本
   - 交互式部署向导
   - 自动硬件检测和配置
   - 一键完成部署

### 文档文件

6. **`README_SYSTEMD.md`** - 完整部署文档 (14KB)
   - 系统要求说明
   - NVIDIA GPU 配置详细步骤
   - Intel QSV 配置详细步骤
   - 手动/自动部署流程
   - 配置说明和示例
   - 服务管理命令
   - 完整故障排查指南

7. **`config-examples.md`** - 配置示例集合 (8KB)
   - 10+ 种不同场景的配置示例
   - NVIDIA/Intel/混合模式配置
   - 质量/速度优化配置
   - 多GPU、低功耗等特殊场景

8. **`QUICK_REFERENCE.md`** - 快速参考卡 (4KB)
   - 常用命令速查
   - 快速故障排查
   - 配置模板
   - FAQ

### 项目配置文件

9. **`Makefile`** - 项目管理工具
   - 30+ 管理命令
   - 简化日常操作
   - 一键部署、更新、监控

10. **`configs/config.yaml`** - 已更新配置文件
    - 设置 `run_mode: "systemd"`
    - 启用硬件编码 profiles
    - 配置 worker_mapping

## 🚀 部署流程

### 方式 1: 快速部署（推荐）

```bash
# 1. 进入项目目录
cd /home/kwok/coder/Video_Trans

# 2. 运行快速部署脚本
sudo deploy/systemd/quickstart.sh
```

脚本会自动:
- ✅ 检测系统环境
- ✅ 检测硬件加速支持
- ✅ 构建项目
- ✅ 配置系统
- ✅ 安装服务
- ✅ 启动服务

### 方式 2: 使用 Makefile

```bash
# 1. 检查硬件
make check-hardware

# 2. 快速部署
make quick-deploy

# 或分步操作
make build
make install
make enable
make start
```

### 方式 3: 手动部署

```bash
# 1. 检查硬件
sudo deploy/systemd/check-hardware.sh

# 2. 安装
sudo deploy/systemd/install.sh

# 3. 编辑配置
sudo nano /opt/stm/configs/config.yaml

# 4. 启动服务
sudo systemctl enable stm
sudo systemctl start stm

# 5. 查看状态
sudo systemctl status stm
```

## 📋 部署前检查清单

### 必须项
- [ ] 已安装 Go 1.20+
- [ ] 已安装 FFmpeg
- [ ] 有 root/sudo 权限
- [ ] 系统支持 systemd

### 硬件加速（可选）

#### NVIDIA GPU
- [ ] NVIDIA 驱动已安装 (≥470.x)
- [ ] `nvidia-smi` 可用
- [ ] FFmpeg 支持 h264_nvenc
- [ ] 运行 `make nvidia-info` 验证

#### Intel QSV
- [ ] BIOS 启用集成显卡
- [ ] `/dev/dri` 设备存在
- [ ] 已安装 VA-API 库 (libva2)
- [ ] 已安装 Intel Media Driver
- [ ] FFmpeg 支持 h264_qsv
- [ ] 运行 `make intel-info` 验证

## 🔧 部署后配置

### 1. 编辑配置文件

```bash
sudo nano /opt/stm/configs/config.yaml
```

关键配置项:
```yaml
# 运行模式 - 必须设置为 systemd
run_mode: "systemd"

# 硬件编码失败时回退到 CPU
cpu_fallback: true

# 根据硬件配置 worker
worker_mapping:
  - "nvidia_high"      # 如果有 NVIDIA
  - "intel_balanced"   # 如果有 Intel
  - "cpu_baseline"     # CPU 回退

# 配置输入输出路径
path:
  pairs:
    - input: "/your/video/source"
      output: "/your/output/path"
```

### 2. 重启服务

```bash
sudo systemctl restart stm
# 或
make restart
```

### 3. 验证运行

```bash
# 查看状态
make status

# 查看日志
make logs

# 访问 Web 界面
http://localhost:8080
```

## 📊 常用管理命令

```bash
# 服务管理
make start          # 启动
make stop           # 停止
make restart        # 重启
make status         # 状态

# 日志查看
make logs           # 实时日志
make logs-recent    # 最近日志
make tail-errors    # 错误日志

# 维护操作
make update         # 更新程序
make update-config  # 更新配置
make backup-db      # 备份数据库

# 监控
make monitor        # 实时监控
make nvidia-info    # GPU 信息
make intel-info     # QSV 信息

# 帮助
make help          # 查看所有命令
make info          # 安装信息
```

## 📚 文档索引

根据需求查看对应文档:

| 需求 | 文档 |
|------|------|
| 完整部署指南 | [README_SYSTEMD.md](README_SYSTEMD.md) |
| 配置示例 | [config-examples.md](config-examples.md) |
| 快速参考 | [QUICK_REFERENCE.md](QUICK_REFERENCE.md) |
| 命令速查 | `make help` |
| 硬件检测 | `make check-hardware` |

## 🔍 故障排查

### 服务无法启动

```bash
# 查看详细错误
journalctl -u stm -n 50

# 测试配置
/opt/stm/bin/stm -config /opt/stm/configs/config.yaml
```

### 硬件编码失败

```bash
# 运行硬件检测
make check-hardware

# 查看硬件相关日志
journalctl -u stm | grep -i "nvenc\|qsv\|encode"
```

### 性能问题

- 减少 `max_workers` 数量
- 使用更快的编码预设
- 检查磁盘 I/O 性能
- 查看资源使用: `htop`, `nvidia-smi`, `intel_gpu_top`

完整故障排查指南: [README_SYSTEMD.md](README_SYSTEMD.md#故障排查)

## 🎯 下一步

1. **验证部署**
   ```bash
   make status
   make logs
   ```

2. **访问 Web 界面**
   ```
   http://localhost:8080
   ```

3. **查看转码任务**
   - Web 界面 -> 任务列表
   - 或查看数据库: `sqlite3 /data/tasks.db "SELECT * FROM tasks LIMIT 10;"`

4. **监控运行状态**
   ```bash
   make monitor
   ```

5. **优化配置** (可选)
   - 查看 [config-examples.md](config-examples.md)
   - 根据实际情况调整 worker 数量和编码参数
   - 配置工作时间窗口

## 📝 备注

- 所有脚本已设置执行权限
- 配置文件已更新为 systemd 模式
- 示例配置启用了 NVIDIA 和 Intel 硬件加速
- 如硬件不可用，会自动回退到 CPU 编码

## 🆘 获取支持

如遇问题:
1. 运行 `make check-hardware` 检查环境
2. 查看日志 `make logs`
3. 参考文档 [README_SYSTEMD.md](README_SYSTEMD.md)
4. 提交 Issue 并附上日志和系统信息

---

**快速开始**: `sudo deploy/systemd/quickstart.sh`

**文档**: [README_SYSTEMD.md](README_SYSTEMD.md)

**帮助**: `make help`

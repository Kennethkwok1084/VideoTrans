# STM Systemd 部署指南

本文档描述如何使用 systemd 方式部署 STM，并启用 NVIDIA 和 Intel 硬件加速。

## 📋 目录

- [系统要求](#系统要求)
- [硬件加速支持](#硬件加速支持)
- [快速部署](#快速部署)
- [手动部署](#手动部署)
- [配置说明](#配置说明)
- [服务管理](#服务管理)
- [故障排查](#故障排查)

## 系统要求

### 基础要求

- **操作系统**: Linux (推荐 Ubuntu 20.04+, Debian 11+, CentOS 8+)
- **Go 版本**: 1.20 或更高
- **FFmpeg**: 已安装并支持所需的硬件编码器
- **权限**: root 或 sudo 权限

### 硬件加速要求

#### NVIDIA GPU 加速

- NVIDIA GPU 支持 NVENC (GeForce GTX 6xx 或更新)
- NVIDIA 驱动版本 ≥ 470.x
- FFmpeg 编译时启用 NVENC 支持

#### Intel QSV 加速

- Intel CPU 支持 Quick Sync Video (第 2 代酷睿或更新)
- 已启用集成显卡
- 安装 VA-API 和 Intel Media Driver
- FFmpeg 编译时启用 QSV 支持

## 硬件加速支持

### 检查硬件支持

在部署前，强烈建议运行硬件检测脚本：

```bash
cd /home/kwok/coder/Video_Trans
chmod +x deploy/systemd/check-hardware.sh
sudo deploy/systemd/check-hardware.sh
```

该脚本会检测：
- NVIDIA GPU 和驱动状态
- Intel QSV 设备和驱动
- FFmpeg 硬件编码支持
- 系统权限配置

### NVIDIA 配置

#### 1. 安装 NVIDIA 驱动

**Ubuntu/Debian:**
```bash
# 添加 NVIDIA 仓库
sudo add-apt-repository ppa:graphics-drivers/ppa
sudo apt update

# 安装推荐驱动 (自动选择)
sudo apt install ubuntu-drivers-common
sudo ubuntu-drivers autoinstall

# 或手动安装特定版本
sudo apt install nvidia-driver-525
```

**手动下载安装:**
访问 [NVIDIA 官网](https://www.nvidia.com/Download/index.aspx) 下载对应驱动。

#### 2. 验证驱动

```bash
nvidia-smi
```

应显示 GPU 信息和驱动版本。

#### 3. 安装支持 NVENC 的 FFmpeg

**使用系统包 (Ubuntu 22.04+):**
```bash
sudo apt install ffmpeg
ffmpeg -encoders | grep nvenc
```

**从源码编译 (推荐):**
```bash
# 安装依赖
sudo apt install build-essential yasm cmake nasm \
  git-core libass-dev libfreetype6-dev libgnutls28-dev \
  libtool pkg-config texinfo wget zlib1g-dev

# 安装 NVIDIA 编解码 SDK
git clone https://git.videolan.org/git/ffmpeg/nv-codec-headers.git
cd nv-codec-headers
sudo make install

# 编译 FFmpeg
git clone https://git.ffmpeg.org/ffmpeg.git
cd ffmpeg
./configure \
  --enable-gpl \
  --enable-nonfree \
  --enable-cuda-nvcc \
  --enable-cuvid \
  --enable-nvenc \
  --enable-libnpp \
  --extra-cflags=-I/usr/local/cuda/include \
  --extra-ldflags=-L/usr/local/cuda/lib64

make -j$(nproc)
sudo make install
```

### Intel QSV 配置

#### 1. 启用集成显卡

进入 BIOS/UEFI，确保：
- 集成显卡 (iGPU) 已启用
- 如有独立显卡，设置为优先使用集成显卡或双显卡模式

#### 2. 安装驱动和库

**Ubuntu/Debian:**
```bash
# 安装 VA-API 库
sudo apt install vainfo libva2 libva-drm2

# 新硬件 (Broadwell 及以后)
sudo apt install intel-media-va-driver-non-free

# 旧硬件 (Broadwell 之前)
sudo apt install i965-va-driver

# 验证安装
vainfo
```

#### 3. 检查设备节点

```bash
ls -l /dev/dri/
```

应该看到 `card0`, `renderD128` 等设备。

#### 4. 配置用户权限

```bash
# 添加用户到 video 和 render 组
sudo usermod -aG video $USER
sudo usermod -aG render $USER

# 重新登录生效
```

#### 5. 安装支持 QSV 的 FFmpeg

**从源码编译:**
```bash
# 安装 Intel Media SDK (旧版) 或 Intel VPL (新版)
# 对于 Ubuntu 22.04+
sudo apt install libmfx-dev

# 或安装 Intel oneVPL (推荐)
git clone https://github.com/oneapi-src/oneVPL.git
cd oneVPL
mkdir build && cd build
cmake ..
make -j$(nproc)
sudo make install

# 编译 FFmpeg
./configure \
  --enable-gpl \
  --enable-libmfx \
  --enable-vaapi

make -j$(nproc)
sudo make install
```

## 快速部署

### 自动安装脚本

```bash
cd /home/kwok/coder/Video_Trans
chmod +x deploy/systemd/install.sh
sudo deploy/systemd/install.sh
```

脚本会自动：
1. 构建 Go 项目
2. 创建安装目录
3. 复制文件到 `/opt/stm`
4. 安装 systemd service
5. 检查硬件支持
6. 提供后续配置建议

### 配置文件

编辑 `/opt/stm/configs/config.yaml`:

```bash
sudo nano /opt/stm/configs/config.yaml
```

关键配置项：

```yaml
# 运行模式 - 必须设置为 systemd
run_mode: "systemd"

# 启用硬件编码失败时的 CPU 回退
cpu_fallback: true

# 根据实际硬件情况配置 worker_mapping
worker_mapping:
  - "nvidia_high"      # 如果有 NVIDIA GPU
  - "intel_balanced"   # 如果有 Intel QSV
  - "cpu_baseline"     # CPU 回退

# 配置输入输出路径
path:
  pairs:
    - input: "/mnt/pve/media/downloads"
      output: "/mnt/pve/media/archive"
```

### 启动服务

```bash
# 启动服务
sudo systemctl start stm

# 查看状态
sudo systemctl status stm

# 启用开机自启
sudo systemctl enable stm

# 查看日志
sudo journalctl -u stm -f
```

## 手动部署

如果不使用自动脚本，可以手动部署：

### 1. 构建项目

```bash
cd /home/kwok/coder/Video_Trans
go build -o bin/stm ./cmd/stm
```

### 2. 创建目录

```bash
sudo mkdir -p /opt/stm/bin
sudo mkdir -p /opt/stm/configs
sudo mkdir -p /data
```

### 3. 复制文件

```bash
sudo cp bin/stm /opt/stm/bin/
sudo cp configs/config.yaml /opt/stm/configs/
```

### 4. 安装 service

```bash
sudo cp deploy/systemd/stm.service /etc/systemd/system/
sudo systemctl daemon-reload
```

### 5. 配置和启动

参考 [配置文件](#配置文件) 和 [启动服务](#启动服务)。

## 配置说明

### 硬件编码配置详解

#### 运行模式

```yaml
run_mode: "systemd"  # 启用硬件编码支持
```

- `docker`: 仅支持 CPU 编码
- `systemd`: 支持 NVIDIA、Intel 和 CPU 编码

#### 编码器 Profile

系统提供三种预设 Profile：

**CPU 编码 (cpu_baseline):**
```yaml
- name: "cpu_baseline"
  type: "cpu"
  codec: "libx264"
  params:
    preset: "veryslow"    # ultrafast|superfast|veryfast|faster|fast|medium|slow|slower|veryslow
    crf: "28"             # 质量参数 (0-51, 越小质量越好)
    audio: "aac"
    audio_bitrate: "128k"
```

**NVIDIA 编码 (nvidia_high):**
```yaml
- name: "nvidia_high"
  type: "nvidia"
  codec: "h264_nvenc"
  params:
    preset: "p7"          # p1-p7, p7质量最高
    cq: "23"              # 恒定质量模式 (0-51)
    audio: "aac"
    audio_bitrate: "128k"
```

**Intel QSV 编码 (intel_balanced):**
```yaml
- name: "intel_balanced"
  type: "intel"
  codec: "h264_qsv"
  params:
    preset: "veryslow"
    global_quality: "23"  # 质量参数
    audio: "aac"
    audio_bitrate: "128k"
```

#### Worker 映射

`worker_mapping` 定义了每个 worker 使用的编码器，按顺序循环分配：

```yaml
worker_mapping:
  - "nvidia_high"       # worker-1, worker-4, worker-7...
  - "intel_balanced"    # worker-2, worker-5, worker-8...
  - "cpu_baseline"      # worker-3, worker-6, worker-9...
```

**示例场景:**

1. **仅 NVIDIA GPU:**
   ```yaml
   worker_mapping:
     - "nvidia_high"
     - "nvidia_high"
     - "cpu_baseline"   # 留一个CPU作为回退
   ```

2. **仅 Intel QSV:**
   ```yaml
   worker_mapping:
     - "intel_balanced"
     - "intel_balanced"
     - "cpu_baseline"
   ```

3. **混合配置 (1x NVIDIA + 1x Intel + 1x CPU):**
   ```yaml
   worker_mapping:
     - "nvidia_high"
     - "intel_balanced"
     - "cpu_baseline"
   ```

#### CPU 回退

```yaml
cpu_fallback: true
```

当硬件编码失败时自动回退到 CPU 编码，确保任务不会失败。

### 系统配置

```yaml
system:
  cron_start: 22  # 工作时间开始 (22:00)
  cron_end: 7     # 工作时间结束 (07:00)
  max_workers: 3  # 最大并发 Worker 数
  scan_interval: 10
  scheduler_interval: 10
  min_disk_space_gb: 5
```

### 路径配置

```yaml
path:
  pairs:
    - input: "/mnt/pve/media/downloads"
      output: "/mnt/pve/media/archive"
    - input: "/mnt/nas/videos"
      output: "/mnt/nas/compressed"
  trash: ".stm_trash"
  database: "/data/tasks.db"
```

## 服务管理

### 常用命令

```bash
# 启动服务
sudo systemctl start stm

# 停止服务
sudo systemctl stop stm

# 重启服务
sudo systemctl restart stm

# 查看状态
sudo systemctl status stm

# 启用开机自启
sudo systemctl enable stm

# 禁用开机自启
sudo systemctl disable stm

# 查看实时日志
sudo journalctl -u stm -f

# 查看最近日志
sudo journalctl -u stm -n 100

# 查看特定时间日志
sudo journalctl -u stm --since "2024-01-01" --until "2024-01-02"
```

### 修改配置后重载

```bash
# 修改配置文件
sudo nano /opt/stm/configs/config.yaml

# 重启服务使配置生效
sudo systemctl restart stm

# 验证状态
sudo systemctl status stm
```

### 更新程序

```bash
cd /home/kwok/coder/Video_Trans
git pull
go build -o bin/stm ./cmd/stm
sudo cp bin/stm /opt/stm/bin/
sudo systemctl restart stm
```

### 卸载服务

```bash
cd /home/kwok/coder/Video_Trans
chmod +x deploy/systemd/uninstall.sh
sudo deploy/systemd/uninstall.sh
```

## 故障排查

### 查看日志

```bash
# 查看系统日志
sudo journalctl -u stm -f

# 查看应用日志
sudo tail -f /data/stm.log
```

### 常见问题

#### 1. NVIDIA 编码失败

**症状:** 日志显示 "NVENC initialization failed"

**解决方法:**
```bash
# 检查驱动
nvidia-smi

# 测试 NVENC
ffmpeg -f lavfi -i testsrc=duration=1:size=320x240:rate=30 \
  -c:v h264_nvenc -preset fast -f null -

# 检查 FFmpeg 支持
ffmpeg -encoders | grep nvenc
```

如果测试失败：
- 确认驱动版本 ≥ 470.x
- 重新安装支持 NVENC 的 FFmpeg
- 确认 GPU 支持 NVENC (GeForce GTX 6xx+)

#### 2. Intel QSV 编码失败

**症状:** 日志显示 "QSV initialization failed" 或 "Cannot load libmfx"

**解决方法:**
```bash
# 检查设备节点
ls -l /dev/dri/

# 检查权限
groups
# 应该包含 video 和 render

# 测试 VA-API
vainfo

# 测试 QSV
ffmpeg -f lavfi -i testsrc=duration=1:size=320x240:rate=30 \
  -c:v h264_qsv -preset fast -f null -
```

如果测试失败：
- 确认 BIOS 已启用集成显卡
- 添加用户到 video 和 render 组
- 重新安装 Intel Media Driver
- 检查 FFmpeg 是否编译了 QSV 支持

#### 3. 权限问题

**症状:** "Permission denied" 访问 /dev/dri

**解决方法:**
```bash
# 检查当前权限
ls -l /dev/dri/

# 添加用户到组
sudo usermod -aG video,render $USER

# 如果服务以 root 运行，检查 service 文件
sudo nano /etc/systemd/system/stm.service

# 确保包含
SupplementaryGroups=video render

# 重载并重启
sudo systemctl daemon-reload
sudo systemctl restart stm
```

#### 4. 服务启动失败

**症状:** `systemctl status stm` 显示 failed

**解决方法:**
```bash
# 查看详细错误
sudo journalctl -u stm -n 50

# 检查配置文件
sudo /opt/stm/bin/stm -config /opt/stm/configs/config.yaml

# 检查文件权限
ls -l /opt/stm/bin/stm
sudo chmod +x /opt/stm/bin/stm

# 检查数据目录
ls -l /data
sudo mkdir -p /data
sudo chown -R root:root /data
```

#### 5. CPU 使用率过高

**解决方法:**
- 减少 `max_workers` 数量
- 使用硬件编码代替 CPU 编码
- 调整 FFmpeg preset (如 veryslow → fast)
- 启用工作时间窗口 (`cron_start`/`cron_end`)

#### 6. 磁盘空间不足

**症状:** 日志显示 "insufficient disk space"

**解决方法:**
- 增加 `min_disk_space_gb` 阈值
- 清理垃圾桶: 访问 Web 界面清空回收站
- 调整 FFmpeg CRF 参数 (增大数值降低质量和大小)
- 清理旧日志和数据库

### 性能监控

#### 使用 nvidia-smi 监控 GPU

```bash
# 实时监控
watch -n 1 nvidia-smi

# 查看编码器使用率
nvidia-smi dmon -s u
```

#### 使用 intel_gpu_top 监控 Intel GPU

```bash
# 安装工具
sudo apt install intel-gpu-tools

# 监控
sudo intel_gpu_top
```

#### Prometheus 监控

STM 内置 Prometheus metrics 导出：

```bash
# 访问 metrics 端点
curl http://localhost:8080/metrics
```

配置 Prometheus 和 Grafana 可视化，参考 [deploy/monitoring/phase5-alert-rules.yaml](../monitoring/phase5-alert-rules.yaml)。

## Web 界面

服务启动后访问：

```
http://localhost:8080
```

功能包括：
- 实时仪表盘
- 任务列表查看
- 垃圾桶管理
- 配置动态调整
- 手动任务控制

## 安全建议

1. **设置 API 密钥**
   ```yaml
   web:
     api_key: "your-secret-key"
   ```

2. **限制网络访问**
   ```bash
   # 仅本地访问
   sudo ufw allow from 127.0.0.1 to any port 8080
   
   # 或使用 Nginx 反向代理
   ```

3. **定期备份数据库**
   ```bash
   sudo cp /data/tasks.db /backup/tasks.db.$(date +%Y%m%d)
   ```

## 参考资源

- [NVIDIA NVENC 文档](https://docs.nvidia.com/video-technologies/video-codec-sdk/)
- [Intel Quick Sync Video](https://www.intel.com/content/www/us/en/architecture-and-technology/quick-sync-video/quick-sync-video-general.html)
- [FFmpeg 硬件加速指南](https://trac.ffmpeg.org/wiki/HWAccelIntro)
- [VA-API 文档](https://github.com/intel/libva)
- [项目 GitHub](https://github.com/yourname/stm)

## 支持

如遇问题，请：
1. 运行 `deploy/systemd/check-hardware.sh` 检查硬件
2. 查看日志 `journalctl -u stm -n 100`
3. 提交 Issue 并附上日志和系统信息

---

**文档版本:** 1.0  
**更新日期:** 2024-01-01  
**维护者:** STM Team

# STM Systemd 配置示例

# 本文件展示了如何为不同的硬件环境配置 STM

## 配置 1: NVIDIA GPU 优先

```yaml
run_mode: "systemd"
cpu_fallback: true

system:
  max_workers: 3

worker_mapping:
  - "nvidia_high"    # Worker 1, 4, 7...
  - "nvidia_high"    # Worker 2, 5, 8...
  - "cpu_baseline"   # Worker 3, 6, 9... (回退)
```

**适用场景**: 拥有 NVIDIA GPU，希望最大化 GPU 利用率

---

## 配置 2: Intel QSV 优先

```yaml
run_mode: "systemd"
cpu_fallback: true

system:
  max_workers: 3

worker_mapping:
  - "intel_balanced"  # Worker 1, 4, 7...
  - "intel_balanced"  # Worker 2, 5, 8...
  - "cpu_baseline"    # Worker 3, 6, 9... (回退)
```

**适用场景**: 使用 Intel CPU 集成显卡

---

## 配置 3: 混合硬件加速 (推荐)

```yaml
run_mode: "systemd"
cpu_fallback: true

system:
  max_workers: 3

worker_mapping:
  - "nvidia_high"     # Worker 1: NVIDIA GPU
  - "intel_balanced"  # Worker 2: Intel QSV
  - "cpu_baseline"    # Worker 3: CPU 回退
```

**适用场景**: 同时拥有 NVIDIA GPU 和 Intel 集成显卡

---

## 配置 4: 多 GPU 负载均衡

```yaml
run_mode: "systemd"
cpu_fallback: true

system:
  max_workers: 6  # 更多 worker

encoder_profiles:
  # ... 默认 profiles ...
  
  # 为第二块 GPU 创建新 profile
  - name: "nvidia_high_gpu1"
    type: "nvidia"
    codec: "h264_nvenc"
    params:
      preset: "p7"
      cq: "23"
      audio: "aac"
      audio_bitrate: "128k"
      gpu: "1"  # 指定 GPU 索引

worker_mapping:
  - "nvidia_high"        # GPU 0
  - "nvidia_high"        # GPU 0
  - "nvidia_high"        # GPU 0
  - "nvidia_high_gpu1"   # GPU 1
  - "nvidia_high_gpu1"   # GPU 1
  - "cpu_baseline"       # CPU 回退
```

**适用场景**: 多 GPU 服务器

---

## 配置 5: 质量优先 (慢速高质量)

```yaml
run_mode: "systemd"
cpu_fallback: true

system:
  max_workers: 2  # 降低并发

encoder_profiles:
  - name: "nvidia_ultra"
    type: "nvidia"
    codec: "h264_nvenc"
    params:
      preset: "p7"        # 最慢预设
      cq: "18"            # 更低 CQ = 更高质量
      profile: "high"
      level: "4.1"
      audio: "aac"
      audio_bitrate: "192k"  # 更高音频码率

worker_mapping:
  - "nvidia_ultra"
  - "cpu_baseline"
```

**适用场景**: 对画质要求极高，转码速度不敏感

---

## 配置 6: 速度优先 (快速转码)

```yaml
run_mode: "systemd"
cpu_fallback: true

system:
  max_workers: 4  # 更多并发

encoder_profiles:
  - name: "nvidia_fast"
    type: "nvidia"
    codec: "h264_nvenc"
    params:
      preset: "p1"        # 最快预设
      cq: "28"
      audio: "copy"       # 音频直接复制，不重新编码

worker_mapping:
  - "nvidia_fast"
  - "nvidia_fast"
  - "nvidia_fast"
  - "nvidia_fast"
```

**适用场景**: 需要快速批量转码，画质要求不高

---

## 配置 7: 夜间自动转码

```yaml
run_mode: "systemd"
cpu_fallback: true

system:
  cron_start: 22  # 晚上10点开始
  cron_end: 7     # 早上7点结束
  max_workers: 4  # 夜间可用更多资源

worker_mapping:
  - "nvidia_high"
  - "nvidia_high"
  - "intel_balanced"
  - "cpu_baseline"
```

**适用场景**: 只在夜间进行转码，不影响白天使用

---

## 配置 8: 多输入输出目录

```yaml
run_mode: "systemd"
cpu_fallback: true

path:
  pairs:
    # 下载目录 -> 归档目录
    - input: "/mnt/nas/downloads"
      output: "/mnt/nas/archive"
    
    # 录像目录 -> 压缩目录
    - input: "/mnt/nas/recordings"
      output: "/mnt/nas/compressed"
    
    # 临时目录 -> 处理目录
    - input: "/tmp/videos"
      output: "/mnt/storage/processed"
  
  trash: ".stm_trash"
  database: "/data/tasks.db"

worker_mapping:
  - "nvidia_high"
  - "intel_balanced"
  - "cpu_baseline"
```

**适用场景**: 多个视频来源需要不同的处理

---

## 配置 9: 特定文件类型优化

```yaml
run_mode: "systemd"
cpu_fallback: true

ffmpeg:
  codec: "libx264"
  preset: "veryslow"
  crf: 28
  audio: "aac"
  audio_bitrate: "128k"
  output_extension: ".mp4"
  
  # 只处理特定格式
  extensions: [".avi", ".flv", ".wmv"]  # 只转码这些格式
  
  # 排除规则
  exclude_patterns:
    - "*4K*"           # 跳过 4K 视频
    - "*HEVC*"         # 跳过已是 HEVC 的视频
    - "SYNOPHOTO_*"
    - "@eaDir/*"

worker_mapping:
  - "nvidia_high"
  - "cpu_baseline"
```

**适用场景**: 只转码特定类型的视频文件

---

## 配置 10: 低功耗模式

```yaml
run_mode: "systemd"
cpu_fallback: true

system:
  max_workers: 1  # 单线程
  scan_interval: 30  # 降低扫描频率

encoder_profiles:
  - name: "intel_lowpower"
    type: "intel"
    codec: "h264_qsv"
    params:
      preset: "fast"          # 更快的预设
      global_quality: "25"    # 稍低质量
      low_power: "1"          # 启用低功耗模式
      audio: "aac"
      audio_bitrate: "96k"    # 更低码率

worker_mapping:
  - "intel_lowpower"
```

**适用场景**: 低功耗设备，如 NAS 或迷你主机

---

## 完整配置模板

查看 `/opt/stm/configs/config.yaml` 获取完整的配置选项。

## 修改配置后

```bash
# 编辑配置
sudo nano /opt/stm/configs/config.yaml

# 重启服务使配置生效
sudo systemctl restart stm

# 验证状态
sudo systemctl status stm
```

## 动态调整

某些配置可以通过 Web 界面动态调整，无需重启：
- Worker 数量
- 编码参数
- 路径映射

访问: http://localhost:8080

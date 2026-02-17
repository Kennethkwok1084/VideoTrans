# 硬件编码实现说明

## 概述

根据[硬件编码需求文档](hardware-encoding-requirements.md)，系统已完成硬件编码支持的实现，支持 Docker 和 Systemd 两种运行模式，允许不同 worker 使用不同的编码后端。

## 实现内容

### 1. 配置结构重构

#### 新增配置项

在 `internal/config/config.go` 中新增以下配置结构：

- **EncoderType**: 编码器类型枚举
  - `cpu`: CPU 编码（libx264/libx265）
  - `nvidia`: NVIDIA 硬件编码（h264_nvenc/hevc_nvenc）
  - `intel`: Intel QSV 硬件编码（h264_qsv/hevc_qsv）

- **EncoderProfile**: 编码器配置描述
  ```go
  type EncoderProfile struct {
      Name   string            // Profile 名称
      Type   EncoderType       // 编码器类型
      Codec  string            // FFmpeg 编码器名称
      Params map[string]string // 编码参数键值对
  }
  ```

- **Config 新增字段**:
  - `RunMode`: 运行模式（"docker" 或 "systemd"）
  - `EncoderProfiles`: 编码器配置列表
  - `WorkerMapping`: Worker 到 Profile 名称的映射
  - `CPUFallback`: 硬件编码失败时是否回退到 CPU

#### 配置验证增强

- Docker 模式下自动将硬件编码 profile 降级为 CPU
- 自动创建默认 CPU profile（如果未配置）
- 验证 worker_mapping 引用的 profile 存在
- 根据 codec 名称自动推断编码器类型

### 2. Worker 调度修改

#### transcode 函数签名变更

```go
// 旧签名
func (w *Worker) transcode(ctx context.Context, task *database.Task, workerID int) error

// 新签名
func (w *Worker) transcode(ctx context.Context, task *database.Task, workerID int, profile *config.EncoderProfile) error
```

#### Profile 分配逻辑

- 在 `processWorker` 中根据 `workerID` 获取对应的 EncoderProfile
- 使用取模运算实现循环分配：`workerID % len(WorkerMapping)`
- 在日志中记录每个 worker 使用的编码器

#### FFmpeg 命令生成改进

- 根据 profile 参数动态构建 FFmpeg 命令
- 支持不同编码器的特定参数：
  - CPU: `-crf` (质量参数)
  - NVIDIA: `-cq` (质量参数)
  - Intel QSV: `-global_quality` (质量参数)
- 保持音频编码参数的兼容性

### 3. 硬件编码失败回退

#### 错误检测

新增 `isHardwareEncodingError` 函数，检测以下硬件编码错误特征：

**NVIDIA 错误**:
- nvenc、cuda、no cuda-capable device
- nvenc_open_encode_session
- cannot load libcuda/libnvcuvid
- driver not found、insufficient driver version

**Intel QSV 错误**:
- qsv、mfx、failed to initialize mfx
- /dev/dri、vaapi
- cannot open shared object

#### 回退流程

1. 尝试使用指定的硬件 profile 进行转码
2. 如果失败且 `cpu_fallback` 启用且当前 profile 为硬件编码（`type` 或 `codec` 推断为硬件）
3. 检查错误是否为硬件编码相关
4. 使用 CPU 回退 profile 重试
5. 记录完整的回退过程到日志

### 4. 配置示例

更新 `configs/config.yaml` 提供完整的硬件编码配置示例，包括：

- Docker 模式示例（仅 CPU）
- Systemd 模式示例（混合编码）
- 三种编码器 profile 配置
- Worker mapping 配置示例

## 使用指南

### Docker 模式

```yaml
run_mode: "docker"
cpu_fallback: true

encoder_profiles:
  - name: "cpu_baseline"
    type: "cpu"
    codec: "libx264"
    params:
      preset: "veryslow"
      crf: "28"
      audio: "aac"
      audio_bitrate: "128k"

worker_mapping:
  - "cpu_baseline"
```

### Systemd 模式（混合编码）

```yaml
run_mode: "systemd"
cpu_fallback: true

encoder_profiles:
  - name: "cpu_baseline"
    type: "cpu"
    codec: "libx264"
    params:
      preset: "veryslow"
      crf: "28"

  - name: "nvidia_high"
    type: "nvidia"
    codec: "h264_nvenc"
    params:
      preset: "p7"
      cq: "23"

  - name: "intel_balanced"
    type: "intel"
    codec: "h264_qsv"
    params:
      preset: "veryslow"
      global_quality: "23"

worker_mapping:
  - "nvidia_high"     # worker-1, 4, 7...
  - "intel_balanced"  # worker-2, 5, 8...
  - "cpu_baseline"    # worker-3, 6, 9...
```

## 验证方法

### 日志检查

启动系统后，查看日志验证编码器分配：

```
[Worker-1] 🎬 使用编码器: nvidia_high (类型: nvidia, codec: h264_nvenc)
[Worker-2] 🎬 使用编码器: intel_balanced (类型: intel, codec: h264_qsv)
[Worker-3] 🎬 使用编码器: cpu_baseline (类型: cpu, codec: libx264)
```

### 回退测试

模拟硬件编码失败，验证回退逻辑：

```
[Worker-1] ⚠️ 硬件编码失败，回退到 CPU 编码: cannot load libcuda.so
[Worker-1] 🔄 使用 CPU 回退编码器: cpu_baseline (codec: libx264)
[Worker-1] ✅ CPU 回退编码成功 #123
```

## 注意事项

1. **Docker 模式限制**: Docker 模式下不支持硬件编码，所有 profile 会自动降级为 CPU
2. **驱动依赖**: Systemd 模式下需要宿主机安装相应驱动（NVIDIA 驱动或 Intel 媒体驱动）
3. **设备权限**: 确保容器或进程有权限访问硬件设备（/dev/dri、/dev/nvidia*）
4. **Profile 数量**: worker_mapping 数组大小不必等于 worker 数量，会自动循环分配
5. **type/codec 一致性**: `type` 与 `codec` 必须匹配；Docker 模式会基于 `type` 与 `codec` 联合判断并执行降级
6. **兼容性**: 保持与旧配置的兼容性，未配置编码器时自动使用默认 CPU profile

## 测试覆盖

- ✅ 配置验证测试（config_test.go）
- ✅ Worker 调度测试（worker_test.go）
- ✅ 编译通过验证
- ✅ 多 profile 分配测试
- ✅ 超时和错误处理测试

## 下一步

1. 在实际环境中测试 NVIDIA 和 Intel 硬件编码
2. 根据实际性能调整编码参数
3. 添加性能监控指标（各编码器使用率、成功率）
4. 可选：添加 Web UI 支持查看 worker->encoder 映射状态

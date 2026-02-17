# 硬件编码需求说明（草案）

## 1. 背景
- 当前系统以 `libx264` 为主，编码参数为单一全局配置。
- 目标是引入硬件编码能力，同时保留 CPU 编码作为稳定兜底。

## 2. 需求目标
- 支持两种运行模式：
1. `docker` 模式：仅支持 `libx264`（CPU 编码）。
2. `systemd` 模式：支持 `nvidia`、`intel`、`cpu` 编码能力。
- 支持硬件资源共用与并行混跑：
1. 不同 worker 线程可使用不同编码后端。
2. 示例：第 1 线程 `nvidia`、第 2 线程 `intel`、第 3 线程 `cpu`。

## 3. 运行模式定义
### 3.1 Docker 模式
- 只允许 CPU 编码链路。
- 配置中若出现 `nvidia/intel` 编码器，需拒绝启动或降级为 CPU（实现阶段二选一）。
- 目的是保持容器部署的兼容性与稳定性，避免引入额外驱动依赖。

### 3.2 Systemd 模式
- 允许使用硬件编码器：
1. NVIDIA：`h264_nvenc` / `hevc_nvenc`。
2. Intel：`h264_qsv` / `hevc_qsv`。
3. CPU：`libx264`（兜底）。
- 允许按 worker 维度绑定编码配置，实现混合并发。

## 4. 编码调度需求
- 编码策略应支持“按 worker 槽位分配 profile”。
- 当 worker 并发数变化时，分配策略需可预测（避免随机变化导致性能波动）。
- 可选能力（建议）：
1. 硬编失败自动回退 CPU。
2. 回退后写明错误类别与回退原因，便于排查。

## 5. 配置层需求
- 需要新增可表达以下信息的配置结构：
1. 运行模式（`docker/systemd`）。
2. worker 与编码 profile 的绑定关系（如 `[nvidia, intel, cpu]`）。
3. 各 profile 的编码参数模板（CPU/NVENC/QSV 各自参数）。
4. 是否启用硬编失败回退 CPU。

## 6. 约束与边界
- 单个 FFmpeg 进程只使用一种视频编码器；“混用”指不同 worker 并发使用不同编码器，不是单任务多编码器并行。
- Docker 模式不承诺硬件编码可用性。
- Systemd 模式依赖宿主机驱动与设备权限（如 `/dev/dri`、NVIDIA runtime/驱动）。

## 7. 验收标准（需求口径）
- Docker 模式：
1. 服务可稳定运行。
2. 实际编码仅使用 `libx264`。
- Systemd 模式：
1. 可在同一时间并发跑 `nvidia`、`intel`、`cpu` 三类任务（按配置绑定）。
2. 日志可清晰看到每个任务/worker 使用的编码器。
3. 任一硬件编码失败时，系统行为符合配置（失败或回退 CPU）。

## 8. 非目标（当前阶段）
- 不做单个视频任务的多设备切片并行编码。
- 不做跨机器分布式调度。

## 9. 待确认项
- Docker 模式遇到硬编配置时，采取“启动失败”还是“自动降级 CPU”。
- worker 绑定规则：固定按 worker ID 绑定，还是按任务轮询绑定 profile。
- 是否需要在 Web/API 暴露当前 worker->encoder 映射与实时状态。

## 10. 实施难度评估

根据代码库分析，整体实施难度为 **中高级**。主要修改将集中在配置、worker 调度和 FFmpeg 命令构建上。

- **配置重构 (高难度)**: 这是最复杂的部分。当前 `config.yaml` 中的 `ffmpeg` 部分为全局配置，需要重构为支持多“编码 profile”的结构。这要求：
    1.  在 `internal/config/config.go` 中定义新的 `struct`，用于表示编码 profile（如名称、类型、编码器、参数集）。
    2.  引入 `worker_mapping` 数组，用于将 profile 指派给 worker。
    3.  修改 YAML 解析逻辑以适应新结构。

- **Worker 逻辑修改 (中等难度)**: `internal/worker/worker.go` 中的 `processWorker` 需要调整。
    1.  `transcode` 函数签名需变更，使其能接收一个“编码 profile”作为参数，而非依赖全局配置。
    2.  `processWorker` 需要根据自身的 `workerID` 和新的 `worker_mapping` 配置来决定使用哪个 profile。

- **FFmpeg 命令生成 (中低难度)**: `transcode` 函数中构建 FFmpeg 参数的逻辑需要更新。
    1.  必须根据传入 profile 的参数动态生成命令行（例如，为 `h264_nvenc` 使用 `-cq`，为 `libx264` 使用 `-crf`）。
    2.  这需要对不同硬件编码器的 FFmpeg 参数有清晰的了解。

- **硬编失败回退 (中等难度)**:
    1.  需要在 `transcode` 的错误处理部分增加逻辑。
    2.  当 FFmpeg 执行失败后，需要分析错误输出，判断是否为可恢复的硬件相关错误。
    3.  若是，则尝试使用预定义的 CPU profile 重新执行转码，这增加了流程的复杂性。

- **运行模式切换 (低难度)**:
    1.  增加一个顶层配置项 `run_mode: docker|systemd`。
    2.  应用启动时检查此模式。若为 `docker` 模式，则在加载配置时自动将所有硬件编码 profile 降级为 CPU profile，或在检测到硬编配置时报错退出。建议采用“自动降级”，因为它更具弹性。

## 11. 建议处理路径

建议分阶段实施，以降低风险、便于测试。

- **第一阶段：配置结构重构**
    1.  **定义新结构**: 在 `internal/config/config.go` 中设计并实现新的配置结构。应包含 `EncoderProfile` 列表和 `WorkerMapping` 列表。
        ```go
        // 示例结构
        type EncoderProfile struct {
            Name      string            `yaml:"name"`
            Type      string            `yaml:"type"` // "cpu", "nvidia", "intel"
            Codec     string            `yaml:"codec"` // "libx264", "h264_nvenc", ...
            Params    map[string]string `yaml:"params"`
        }

        type Config struct {
            // ...
            RunMode         string           `yaml:"run_mode"`
            EncoderProfiles []EncoderProfile `yaml:"encoder_profiles"`
            WorkerMapping   []string         `yaml:"worker_mapping"`
            CPUFallback     bool             `yaml:"cpu_fallback"`
        }
        ```
    2.  **更新配置文件**: 在 `config.yaml.example` 中提供一个使用新结构的完整示例。
    3.  **验证解析**: 编写单元测试，确保新的 YAML 配置可以被正确解析到 Go `struct` 中。

- **第二阶段：将 Profile 应用于 Worker**
    1.  **修改函数签名**: 将 `internal/worker/worker.go` 中的 `transcode` 函数签名修改为 `transcode(..., profile EncoderProfile)`。
    2.  **分配 Profile**: 在 `processWorker` 循环中，根据 `workerID` 从 `WorkerMapping` 中获取对应的 profile 名称，并找到完整的 `EncoderProfile` 对象。使用取模运算 `workerID % len(mapping)` 来确保安全访问。
    3.  **动态构建命令**: 修改 `transcode` 内部的 FFmpeg 参数构建逻辑，使其完全基于传入的 `profile` 对象来生成参数。

- **第三阶段：实现模式切换与回退逻辑**
    1.  **实现 `run_mode`**: 在应用启动的最初阶段，检查 `run_mode`。如果是 `docker`，则遍历 `WorkerMapping`，将所有非 `cpu` 类型的 profile 替换为一个默认的 `cpu` profile。
    2.  **实现失败回退**: 在 `transcode` 的错误处理部分，如果 `CPUFallback` 为 `true` 且当前 profile 是硬件类型，则记录错误并使用一个预设的 CPU profile 进行重试。注意避免无限重试循环。

- **第四阶段：完善与测试**
    1.  **日志记录**: 在 `transcode` 开始时，明确记录当前任务所使用的编码器 profile，便于调试。
    2.  **端到端测试**:
        - **Docker 模式**: 验证即使配置了硬件编码，实际也只使用 CPU。
        - **Systemd 模式**: 设置混合 `WorkerMapping`（如 `[nvidia, intel, cpu]`），验证系统可以同时使用不同的编码器进行转码。
        - **失败回退测试**: 模拟硬件编码失败（如提供一个无效的 GPU 参数），验证系统是否能按预期回退到 CPU 编码。


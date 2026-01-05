# 📄 视频自动化转码中心 (STM) 开发规格说明书 v3.0

## 1. 项目愿景

构建一个**“不知疲倦的夜间搬运工”**。它自动扫描 NAS/主机上的原始视频目录，在深夜利用闲置算力（Ryzen 3500X）将其压缩为高效率的 H.264 格式，并归档到目标目录。同时，它具备“后悔药”机制（垃圾桶），并通过 Web 界面提供极简的可视化管理。

## 2. 系统核心策略

### 2.1 扫描与去重策略 (No-Hash Mode)

放弃内容指纹，采用 **“元数据比对”** 机制。

* **唯一标识 (Identity)**: `文件相对路径` (RelPath)。
* **变化检测**: 依靠 `文件大小 (Size)` 和 `修改时间 (Mtime)`。
* **判断逻辑**:
1. **新文件**: 数据库里没有这个路径 -> **加入队列**。
2. **文件更新**: 数据库里有，但 `DB记录的Mtime` != `当前文件Mtime` -> **重置任务为 Pending** (视为源文件被替换)。
3. **已完成**: 数据库显示 `Completed`，且目标目录存在对应文件 -> **跳过**。



### 2.2 转码策略 (画质/体积平衡)

* **核心目标**: 极致压缩体积，适合移动端播放和长期归档。
* **编码器**: `libx264` (CPU软解)。
* **预设 (Preset)**: `veryslow` (利用 3500X 的算力换取最小体积)。
* **质量 (CRF)**: `27` 或 `28`。
* **并发控制**:
* **夜间模式 (02:00 - 08:00)**: 开启 **3** 个并发线程 (CPU占用率约80%-90%)。
* **日间模式**: 暂停领取新任务，或限制为 **1** 个线程（低优先级）。



### 2.3 安全删除策略 (垃圾桶机制)

* **原则**: 程序永远不直接执行 `rm` 删除源文件。
* **一级清理 (归档)**: 任务成功完成 **7天** 后，将源文件 `mv` 移动到同级目录下的 `.stm_trash` 文件夹。
* **二级清理 (销毁)**: 扫描 `.stm_trash`，删除其中移动时间超过 **30天** 的文件。

---

## 3. 功能模块详细设计

### 3.1 数据库设计 (SQLite)

只需要一张核心表 `tasks`。

* `ID`: 主键
* `SourcePath`: 字符串 (索引, 如 `2024/anime/ep01.mkv`)
* `SourceMtime`: 时间戳 (用于检测文件是否更新)
* `Status`: 枚举 (`pending`, `processing`, `completed`, `failed`)
* `RetryCount`: 整数 (重试次数)
* `CreatedAt`: 入库时间
* `CompletedAt`: 转码完成时间 (用于计算何时移动到垃圾桶)
* `Log`: 文本 (存储 FFmpeg 报错信息)

### 3.2 扫描器模块 (Scanner)

* **触发方式**: 周期性运行 (如每 10 分钟) 或 Web 手动触发。
* **流程**:
1. 递归遍历 `/mnt/demo2`。
2. **过滤排除项**:
   - 跳过 `.stm_trash` 目录（自有垃圾桶）
   - 跳过 `@eaDir`、`#recycle` 等群晖系统目录
   - 跳过 `SYNOPHOTO_*` 文件（群晖自动生成的缩略图/视频）
   - 跳过隐藏文件和临时文件
3. 获取当前文件的 `Size` 和 `Mtime`。
4. 查询 DB，执行 [2.1] 中的比对逻辑。
5. 如果是新任务，写入 DB，状态设为 `pending`。

* **支持的视频格式**: 所有常见视频格式（`.mp4`, `.mkv`, `.avi`, `.ts`, `.mov`, `.flv`, `.wmv`, `.m4v`, `.webm` 等），无需预先过滤。



### 3.3 调度与执行模块 (Worker)

* **守护进程**: 常驻后台。
* **时间窗口检查**:
* 每分钟检查一次当前时间。
* 如果在 `WorkStart` (02:00) 和 `WorkEnd` (08:00) 之间 -> 激活 Worker。
* 如果不在 -> 除非用户在 Web 点击“强制运行”，否则休眠。


* **执行流程**:
1. 从 DB 锁单: `SELECT * FROM tasks WHERE status='pending' LIMIT 1`。
2. 预检查: 探测源文件是否完整 (ffprobe)。
3. 构建命令:
```bash
ffmpeg -y -i [Source] -c:v libx264 -preset veryslow -crf 28 -c:a aac -b:a 128k -movflags +faststart [Target]

```


4. 执行并捕获输出。
5. 成功: 更新状态 `completed`，记录 `CompletedAt`。
6. 失败: 更新状态 `failed`，写入 `Log`，`RetryCount +1`。



### 3.4 清理模块 (Janitor)

* **触发**: 每天一次 (如上午 10:00)。
* **动作 1 (移入垃圾桶)**:
* 查询 `CompletedAt < (Now - 7 Days)` 且源文件还在原位的任务。
* `mv /mnt/demo2/video.mp4 /mnt/demo2/.stm_trash/video.mp4_del_20260105`
* 注意：为了防止文件名冲突，在垃圾桶内追加时间后缀。


* **动作 2 (清空垃圾桶)**:
* 遍历 `/mnt/demo2/.stm_trash`。
* 解析文件名后缀时间，或读取文件系统 `ctime`。
* 超过 30 天 -> 删除。



### 3.5 Web 管理端

* **不需要登录**。
* **首页仪表盘**:
* **统计卡片**: 待处理数 / 今日完成 / 累计节省空间(GB)。
* **运行模式**: 显示当前是“正在睡觉”还是“正在干活”。
* **控制区**: [立即扫描] [强制开始] [暂停]。


* **任务列表页**:
* 表格展示: 文件名 | 状态 | 进度/结果 | 操作。
* 操作: [重试] (针对失败任务), [删除记录] (不删文件)。


* **垃圾桶视图 (Feature)**:
* 简单列表，显示垃圾桶里有哪些文件，支持 [立即彻底删除]。



---

## 4. 目录与部署规划

### 4.1 目录映射 (Docker Compose)

建议使用 **Bind Mount** (本地直连) 或 **SMB Mount**。

```yaml
version: '3'
services:
  stm:
    image: alpine:latest  # 实际使用构建好的Go镜像
    container_name: stm-transcoder
    volumes:
      - /mnt/pve/media/downloads:/input   # 源目录 (demo2)
      - /mnt/pve/media/archive:/output    # 目标目录 (demo3)
      - ./data:/data                      # 存放 tasks.db 和 config.yaml
    environment:
      - PUID=1000
      - PGID=1000
      - TZ=Asia/Shanghai
    restart: unless-stopped

```

### 4.2 配置文件 (config.yaml)

```yaml
system:
  cron_start: 2
  cron_end: 8
  max_workers: 3  # Ryzen 3500X 建议设为 3

path:
  input: "/input"
  output: "/output"
  trash: ".stm_trash" # 相对路径，实际在 /input/.stm_trash

ffmpeg:
  codec: "libx264"
  preset: "veryslow"
  crf: 28
  audio: "aac"
  extensions: [".mp4", ".mkv", ".avi", ".ts", ".mov", ".flv", ".wmv", ".m4v", ".webm"]
  # 排除规则（支持通配符）
  exclude_patterns:
    - "SYNOPHOTO_*"           # 群晖缩略图/视频
    - "@eaDir/*"               # 群晖索引目录
    - "#recycle/*"             # 群晖回收站
    - ".*"                     # 隐藏文件
    - "*.tmp"                  # 临时文件

cleaning:
  soft_delete_days: 7   # 移入垃圾桶天数
  hard_delete_days: 30  # 彻底删除天数

```

---

## 5. 开发阶段划分

1. **Phase 1 (MVP)**:
* 实现 Go 程序，能扫描目录，入库 SQLite。
* 实现单线程 FFmpeg 转码。
* 完成 Web 界面查看列表。


2. **Phase 2 (Optimization)**:
* 加入多线程并发。
* 加入时间窗口控制 (夜间模式)。
* 调整 FFmpeg 参数为 `veryslow`。


3. **Phase 3 (Safety)**:
* 实现垃圾桶移动逻辑。
* 实现自动清理逻辑。

---

## 6. 详细开发步骤与实施计划

### **Phase 1: MVP 基础架构 (预计 3-5 天)**

#### 步骤 1: 项目结构初始化

创建完整的 Go 项目目录结构：

```
stm/
├── cmd/
│   └── stm/
│       └── main.go              # 主程序入口
├── internal/
│   ├── config/
│   │   └── config.go            # 配置文件解析
│   ├── database/
│   │   ├── db.go               # SQLite 连接管理
│   │   └── models.go           # 数据模型定义
│   ├── scanner/
│   │   └── scanner.go          # 目录扫描器
│   ├── worker/
│   │   └── worker.go           # 转码执行器
│   ├── cleaner/
│   │   └── cleaner.go          # 清理模块
│   └── web/
│       ├── server.go           # HTTP 服务器
│       ├── handlers.go         # API 处理器
│       └── templates/          # HTML 模板
├── configs/
│   └── config.yaml             # 默认配置
├── Dockerfile
├── docker-compose.yml
├── go.mod
└── README.md
```

#### 步骤 2: 数据库层 (internal/database)

**核心功能实现**:
- 定义 `Task` 结构体（对应 3.1 章节的表结构）
- 实现 SQLite 初始化逻辑（使用 `modernc.org/sqlite`）
- 编写 CRUD 方法:
  - `CreateTask()` - 创建新任务
  - `UpdateTaskStatus()` - 更新状态（使用事务确保原子性）
  - `GetPendingTasks()` - 获取待处理任务
  - `GetTaskByPath()` - 通过路径查询（用于去重）
  - `GetCompletedOldTasks(days int)` - 查询 N 天前完成的任务

**注意事项**:
- 所有写操作必须使用事务
- 为 `SourcePath` 字段创建唯一索引
- 实现数据库连接池管理

#### 步骤 3: 配置管理 (internal/config)

**实现细节**:
- 使用 `gopkg.in/yaml.v3` 解析 config.yaml
- 定义配置结构体（对应 4.2 章节）
- 实现配置验证逻辑:
  - 检查必需路径是否存在
  - 验证 `max_workers` 范围 (1-10)
  - 验证时间窗口合法性
- 支持环境变量覆盖配置（如 `STM_MAX_WORKERS`）

#### 步骤 4: 扫描器模块 (internal/scanner)

**核心逻辑**:
- 使用 `filepath.WalkDir` 递归遍历输入目录
- 过滤逻辑:
  - 排除 `.stm_trash` 目录（自有垃圾桶）
  - 排除群晖系统目录: `@eaDir`, `#recycle`
  - 排除群晖自动生成文件: `SYNOPHOTO_*` 模式匹配
  - 支持所有视频扩展名（`.mp4`, `.mkv`, `.avi`, `.ts`, `.mov`, `.flv`, `.wmv`, `.m4v`, `.webm` 等）
  - 忽略隐藏文件（以 `.` 开头）和临时文件（`.tmp`）
- 元数据提取:
  - 获取文件 Size 和 Mtime
  - 计算相对路径作为唯一标识
- 与数据库比对（实现 2.1 章节的三种判断逻辑）

**示例代码结构**:
```go
func (s *Scanner) Scan(ctx context.Context) error {
    return filepath.WalkDir(s.config.InputPath, func(path string, d fs.DirEntry, err error) error {
        // 1. 跳过目录
        if d.IsDir() {
            // 跳过系统目录
            if shouldSkipDir(d.Name()) {
                return filepath.SkipDir
            }
            return nil
        }
        
        // 2. 文件过滤
        if shouldSkipFile(d.Name()) {
            return nil  // 跳过群晖缩略图、隐藏文件等
        }
        
        // 3. 检查是否为视频文件（通过扩展名）
        if !isVideoFile(path) {
            return nil
        }
        
        // 4. 提取元数据
        info, _ := d.Info()
        relPath := getRelativePath(path, s.config.InputPath)
        
        // 5. 查询数据库并决策
        // ...
    })
}

// 群晖目录过滤
func shouldSkipDir(name string) bool {
    skipDirs := []string{".stm_trash", "@eaDir", "#recycle", ".DS_Store"}
    for _, dir := range skipDirs {
        if name == dir {
            return true
        }
    }
    return false
}

// 群晖文件过滤（支持通配符）
func shouldSkipFile(name string) bool {
    // 群晖缩略图/视频
    if strings.HasPrefix(name, "SYNOPHOTO_") {
        return true
    }
    // 隐藏文件
    if strings.HasPrefix(name, ".") {
        return true
    }
    // 临时文件
    if strings.HasSuffix(name, ".tmp") || strings.HasSuffix(name, ".part") {
        return true
    }
    return false
}

// 检查是否为视频文件
func isVideoFile(path string) bool {
    ext := strings.ToLower(filepath.Ext(path))
    videoExts := []string{".mp4", ".mkv", ".avi", ".ts", ".mov", ".flv", ".wmv", ".m4v", ".webm"}
    for _, validExt := range videoExts {
        if ext == validExt {
            return true
        }
    }
    return false
```

#### 步骤 5: 转码执行器 (internal/worker)

**基础实现（单线程版本）**:
- FFmpeg 命令构建（使用 `os/exec`）
- 参数设置:
  ```bash
  ffmpeg -y -progress pipe:1 -i [Source] \
    -c:v libx264 -preset veryslow -crf 28 \
    -c:a aac -b:a 128k \
    -movflags +faststart [Target]
  ```
- 错误处理:
  - 捕获 stderr 输出到任务 Log
  - 重试逻辑（最多 3 次）
  - 超时控制（防止卡住）

**进度解析**:
- 使用 `-progress pipe:1` 输出到 stdout
- 解析 `out_time_ms` 和 `total_duration` 计算百分比
- 实时更新数据库进度字段（可选，Phase 2 实现）

**关键代码示例**:
```go
stdout, _ := cmd.StdoutPipe()
stderr, _ := cmd.StderrPipe()
cmd.Start()

// 解析进度
scanner := bufio.NewScanner(stdout)
for scanner.Scan() {
    line := scanner.Text()
    if strings.HasPrefix(line, "out_time_ms=") {
        // 更新进度
    }
}
```

#### 步骤 6: Web 界面 (internal/web)

**后端 API 设计** (使用 Gin 框架):
- `GET /api/stats` - 仪表盘统计（待处理/今日完成/节省空间）
- `GET /api/tasks?status=pending&page=1` - 任务列表（支持分页和筛选）
- `POST /api/scan` - 手动触发扫描
- `POST /api/tasks/:id/retry` - 重试失败任务
- `DELETE /api/tasks/:id` - 删除任务记录
- `GET /api/worker/status` - 获取 Worker 运行状态

**前端页面** (使用 HTML + TailwindCSS CDN):
- **仪表盘页面** (`/`):
  - 统计卡片（使用 Card 组件）
  - 运行模式指示器（睡眠/工作中）
  - 控制按钮（扫描/强制启动/暂停）
- **任务列表页** (`/tasks`):
  - 表格展示（文件名/状态/进度/操作）
  - 状态筛选器（All/Pending/Processing/Completed/Failed）
  - 分页控件
- 使用原生 Fetch API 或 HTMX 实现交互

#### 步骤 7: 主程序入口 (cmd/stm/main.go)

**启动流程**:
```go
func main() {
    // 1. 加载配置
    cfg := config.Load()
    
    // 2. 初始化数据库
    db := database.Init(cfg.DBPath)
    
    // 3. 启动 Goroutines
    go scanner.Run(cfg, db)       // 每 10 分钟扫描
    go worker.Run(cfg, db)        // Worker 守护进程
    go cleaner.Run(cfg, db)       // 清理模块（每天一次）
    
    // 4. 启动 Web 服务器
    web.Start(cfg, db)            // 阻塞在这里
}
```

**优雅关闭**:
- 监听 `SIGTERM` 和 `SIGINT` 信号
- 关闭时等待正在执行的转码任务完成（或超时强制终止）
- 关闭数据库连接

---

### **Phase 2: 性能优化 (预计 2-3 天)**

#### 步骤 8: 多线程并发转码

**Worker Pool 模式实现**:
```go
type WorkerPool struct {
    workers   int
    taskQueue chan *Task
    wg        sync.WaitGroup
}

func (wp *WorkerPool) Start() {
    for i := 0; i < wp.workers; i++ {
        wp.wg.Add(1)
        go wp.processTask()
    }
}

func (wp *WorkerPool) processTask() {
    defer wp.wg.Done()
    for task := range wp.taskQueue {
        // 执行转码
    }
}
```

**并发控制**:
- 使用 `max_workers` 配置项控制并发数
- 通过带缓冲的 Channel 实现任务队列
- 使用 `sync.WaitGroup` 管理 Goroutine 生命周期

#### 步骤 9: 时间窗口控制

**实现逻辑**:
```go
func (w *Worker) isWorkingHours() bool {
    now := time.Now()
    hour := now.Hour()
    return hour >= w.config.CronStart && hour < w.config.CronEnd
}

func (w *Worker) Run() {
    ticker := time.NewTicker(1 * time.Minute)
    for {
        select {
        case <-ticker.C:
            if w.isWorkingHours() || w.forceRun {
                // 启动 Worker Pool
            } else {
                // 休眠或降级到 1 个 Worker
            }
        }
    }
}
```

**Web 强制启动开关**:
- 添加 `POST /api/worker/force-start` 接口
- 设置全局标志位 `forceRun = true`
- Web 页面显示当前模式（自动/强制）

#### 步骤 10: FFmpeg 参数优化

**已在步骤 5 中实现，此处进行微调**:
- 确认 `preset` 设置为 `veryslow`
- CRF 可通过配置文件调整（27 或 28）
- 添加 `-movflags +faststart`（已包含）
- 可选优化参数:
  - `-tune film` (针对影视内容)
  - `-x264-params ref=4:bframes=3` (高级调优)

**进度回调优化**:
- 使用 `ffprobe` 预先获取视频总时长
- 解析 `-progress pipe:1` 输出的 `out_time_ms`
- 计算百分比: `progress = (out_time_ms / total_duration_ms) * 100`
- 每 5% 更新一次数据库（减少写入频率）

---

### **Phase 3: 安全机制 (预计 2 天)**

#### 步骤 11: 清理模块 (internal/cleaner)

**一级清理 - 移入垃圾桶**:

**安全移动函数实现**:
```go
func safeMoveToTrash(srcPath string) error {
    // 1. 构建垃圾桶路径（同级目录，避免跨分区）
    srcDir := filepath.Dir(srcPath)
    trashDir := filepath.Join(srcDir, ".stm_trash")
    
    // 2. 确保垃圾桶目录存在
    os.MkdirAll(trashDir, 0755)
    
    // 3. 生成带时间戳的目标文件名
    filename := filepath.Base(srcPath)
    timestamp := time.Now().Format("20060102_150405")
    trashPath := filepath.Join(trashDir, filename+"_del_"+timestamp)
    
    // 4. 尝试直接移动（同分区快速操作）
    err := os.Rename(srcPath, trashPath)
    if err == nil {
        log.Info("文件已移入垃圾桶（os.Rename）: %s", trashPath)
        return nil
    }
    
    // 5. 如果失败（跨分区），使用复制+删除
    if isLinkError(err) {
        log.Warn("检测到跨分区，使用复制+删除模式: %s", srcPath)
        return copyAndDelete(srcPath, trashPath)
    }
    
    return err
}

func copyAndDelete(src, dst string) error {
    // 实现文件复制
    in, _ := os.Open(src)
    defer in.Close()
    
    out, _ := os.Create(dst)
    defer out.Close()
    
    _, err := io.Copy(out, in)
    if err != nil {
        return err
    }
    
    // 验证复制成功后再删除
    return os.Remove(src)
}
```

**触发逻辑**:
```go
func (c *Cleaner) MoveToTrash() error {
    // 查询 7 天前完成的任务
    cutoffTime := time.Now().AddDate(0, 0, -c.config.SoftDeleteDays)
    tasks := c.db.GetCompletedOldTasks(cutoffTime)
    
    for _, task := range tasks {
        srcPath := filepath.Join(c.config.InputPath, task.SourcePath)
        if fileExists(srcPath) {
            safeMoveToTrash(srcPath)
        }
    }
}
```

**二级清理 - 彻底删除**:
```go
func (c *Cleaner) EmptyTrash() error {
    trashRoot := filepath.Join(c.config.InputPath, c.config.TrashDir)
    cutoffTime := time.Now().AddDate(0, 0, -c.config.HardDeleteDays)
    
    return filepath.WalkDir(trashRoot, func(path string, d fs.DirEntry, err error) error {
        if d.IsDir() {
            return nil
        }
        
        // 解析文件名时间戳 "video.mp4_del_20260105_120000"
        parts := strings.Split(filepath.Base(path), "_del_")
        if len(parts) < 2 {
            return nil
        }
        
        timestamp, err := time.Parse("20060102_150405", parts[1])
        if err != nil {
            // 降级到使用文件系统 ctime
            info, _ := d.Info()
            timestamp = info.ModTime()
        }
        
        if timestamp.Before(cutoffTime) {
            log.Info("彻底删除过期文件: %s", path)
            return os.Remove(path)
        }
        
        return nil
    })
}
```

**定时触发**:
- 使用 `robfig/cron/v3` 库
- 每天上午 10:00 执行一次
- Cron 表达式: `0 10 * * *`

#### 步骤 12: 垃圾桶 Web 视图

**后端 API**:
- `GET /api/trash` - 列出垃圾桶文件
  ```json
  {
    "files": [
      {
        "name": "video.mp4_del_20260105_120000",
        "size": 1024000,
        "deleteTime": "2026-01-05T12:00:00Z",
        "daysLeft": 23
      }
    ],
    "totalSize": 10240000
  }
  ```
- `DELETE /api/trash/:filename` - 立即删除指定文件

**前端页面**:
- 新增路由 `/trash`
- 表格显示文件列表（文件名/大小/删除倒计时/操作）
- 危险操作二次确认（弹窗提示）

---

### **Phase 4: 部署与测试 (预计 1-2 天)**

#### 步骤 13: Docker 化

**多阶段 Dockerfile**:
```dockerfile
# ============ Stage 1: 构建 Go 二进制 ============
FROM golang:1.23-alpine AS builder

WORKDIR /build

# 安装依赖
RUN apk add --no-cache git

# 复制依赖文件
COPY go.mod go.sum ./
RUN go mod download

# 复制源码
COPY . .

# 编译（禁用 CGO，静态链接）
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o stm ./cmd/stm

# ============ Stage 2: 运行时环境 ============
FROM linuxserver/ffmpeg:latest
# 或使用: FROM jrottenberg/ffmpeg:6.1-alpine

# 创建应用目录
WORKDIR /app

# 从构建阶段复制二进制文件
COPY --from=builder /build/stm /usr/local/bin/stm

# 复制默认配置
COPY configs/config.yaml /app/config.yaml

# 暴露 Web 端口
EXPOSE 8080

# 健康检查
HEALTHCHECK --interval=30s --timeout=3s \
  CMD wget --quiet --tries=1 --spider http://localhost:8080/api/health || exit 1

# 启动程序
ENTRYPOINT ["/usr/local/bin/stm"]
CMD ["--config", "/app/config.yaml"]
```

**docker-compose.yml**:
```yaml
version: '3.8'

services:
  stm:
    build:
      context: .
      dockerfile: Dockerfile
    image: stm-transcoder:latest
    container_name: stm
    
    volumes:
      - /mnt/pve/media/downloads:/input     # 源目录
      - /mnt/pve/media/archive:/output      # 目标目录
      - ./data:/data                        # 数据库和日志
      - ./configs/config.yaml:/app/config.yaml:ro
    
    environment:
      - PUID=1000
      - PGID=1000
      - TZ=Asia/Shanghai
      - STM_MAX_WORKERS=3                   # 环境变量覆盖
    
    ports:
      - "8080:8080"
    
    restart: unless-stopped
    
    # 资源限制（可选）
    deploy:
      resources:
        limits:
          cpus: '6'        # 3500X 6核心
          memory: 4G
```

**构建与运行**:
```bash
# 构建镜像
docker-compose build

# 启动服务
docker-compose up -d

# 查看日志
docker-compose logs -f stm
```

#### 步骤 14: 集成测试

**测试用例清单**:

1. **扫描功能测试**:
   - [ ] 新文件正确入库
   - [ ] 更新文件触发重新转码
   - [ ] 已完成文件被跳过
   - [ ] `.stm_trash` 目录被排除
   - [ ] 群晖系统目录被排除（`@eaDir`, `#recycle`）
   - [ ] 群晖缩略图文件被跳过（`SYNOPHOTO_FILM_M.mp4` 等）
   - [ ] 各种视频格式均可正常入库（`.mp4`, `.mkv`, `.avi`, `.ts`, `.mov`, `.flv`, `.wmv`, `.m4v`, `.webm`）

2. **转码功能测试**:
   - [ ] 单线程转码成功
   - [ ] 多线程并发转码（3个任务）
   - [ ] 失败任务重试机制
   - [ ] 进度解析正确

3. **时间窗口测试**:
   - [ ] 夜间自动启动
   - [ ] 日间自动暂停
   - [ ] 强制启动功能

4. **清理功能测试**:
   - [ ] 7天后文件移入垃圾桶
   - [ ] 30天后彻底删除
   - [ ] 跨分区移动降级（模拟测试）

5. **Web 界面测试**:
   - [ ] 仪表盘数据准确
   - [ ] 任务列表分页正常
   - [ ] 手动扫描触发
   - [ ] 垃圾桶视图展示

**压力测试**:
```bash
# 准备 100 个测试视频文件
for i in {1..100}; do
  cp sample.mp4 /input/test_$i.mp4
done

# 观察系统资源占用
docker stats stm

# 预期结果：
# - CPU 占用 80%-90% (3 workers)
# - 内存占用 < 2GB
# - 任务按序完成，无死锁
```

#### 步骤 15: 文档完善

**README.md 内容**:
```markdown
# STM - 视频自动化转码中心

## 快速开始

### 1. 准备配置文件
\`\`\`bash
cp configs/config.yaml.example configs/config.yaml
# 编辑配置文件，设置输入/输出路径
\`\`\`

### 2. 启动服务
\`\`\`bash
docker-compose up -d
\`\`\`

### 3. 访问 Web 界面
打开浏览器访问: http://localhost:8080

## 配置说明
（详细解释每个配置项）

## 常见问题
Q: 转码失败怎么办？
A: 检查日志中的 FFmpeg 报错...
```

**API 文档** (可选，使用 Swagger):
- 生成 OpenAPI 规范
- 提供交互式 API 测试界面

**systemd 服务文件** (非 Docker 部署):
```ini
[Unit]
Description=STM Video Transcoder
After=network.target

[Service]
Type=simple
User=media
ExecStart=/usr/local/bin/stm --config /etc/stm/config.yaml
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

---

## 7. 技术栈与依赖

### 7.1 核心技术栈

| 组件 | 技术选型 | 版本 |
|------|---------|------|
| 语言 | Go | 1.23+ |
| 数据库 | SQLite (纯 Go 实现) | `modernc.org/sqlite` v1.29+ |
| Web 框架 | Gin | `github.com/gin-gonic/gin` v1.10+ |
| 定时任务 | Cron | `github.com/robfig/cron/v3` v3.0.1 |
| 配置解析 | YAML | `gopkg.in/yaml.v3` v3.0+ |
| 日志 | Zap | `go.uber.org/zap` v1.27+ |
| FFmpeg | 预装镜像 | 6.1+ (linuxserver/ffmpeg) |

### 7.2 Go 依赖包

```go
// go.mod
module github.com/yourname/stm

go 1.23

require (
    github.com/gin-gonic/gin v1.10.0
    github.com/robfig/cron/v3 v3.0.1
    go.uber.org/zap v1.27.0
    gopkg.in/yaml.v3 v3.0.1
    modernc.org/sqlite v1.29.0
)
```

### 7.3 前端依赖 (CDN 引入)

- TailwindCSS 3.x
- Alpine.js (可选，用于简单交互)
- Chart.js (可选，用于数据可视化)

---

## 8. 关键技术要点

### 8.1 跨分区移动处理

**问题**: `os.Rename` 在跨分区时会失败（返回 `EXDEV` 错误）

**解决方案**:
1. 优先使用 `os.Rename`（同分区快速移动，仅修改目录项）
2. 捕获错误后降级到"复制+删除"模式
3. 在垃圾桶路径设计上确保同级目录（避免跨分区）

**代码示例** (见步骤 11)

### 8.2 FFmpeg 进度解析

**传统方式的问题**:
- 输出格式复杂，需要正则匹配 `time=00:12:34.56`
- 输出频繁，解析性能差

**优化方案**:
使用 `-progress pipe:1` 参数，输出格式为：
```
frame=1234
fps=30.00
stream_0_0_q=28.0
total_size=12345678
out_time_us=12345678
out_time_ms=12345
out_time=00:00:12.345000
progress=continue
```

**解析代码**:
```go
scanner := bufio.NewScanner(stdout)
for scanner.Scan() {
    line := scanner.Text()
    parts := strings.SplitN(line, "=", 2)
    if len(parts) == 2 {
        key, value := parts[0], parts[1]
        switch key {
        case "out_time_ms":
            currentMs, _ := strconv.ParseInt(value, 10, 64)
            progress := float64(currentMs) / float64(totalMs) * 100
            // 更新进度
        }
    }
}
```

### 8.3 Docker 镜像优化

**为什么使用 `linuxserver/ffmpeg` 作为基础镜像**:
- ✅ 包含完整的 FFmpeg + 所有编解码器（libx264, aac, etc.）
- ✅ 无需手动编译安装依赖
- ✅ 社区维护，定期更新
- ✅ 支持硬件加速（可选，未来可扩展 NVENC）

**镜像大小对比**:
- `alpine:latest` + 手动装 FFmpeg: ~300MB
- `linuxserver/ffmpeg:latest`: ~250MB
- `jrottenberg/ffmpeg:alpine`: ~180MB (推荐)

### 8.4 并发安全

**数据库操作**:
- 所有写操作使用事务
- 任务状态更新加行锁: `SELECT ... FOR UPDATE`

**全局状态管理**:
```go
type SafeState struct {
    mu         sync.RWMutex
    forceRun   bool
    activeJobs int
}

func (s *SafeState) SetForceRun(val bool) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.forceRun = val
}
```

### 8.5 优雅关闭

```go
func main() {
    // 捕获信号
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)
    
    // 启动服务
    go startServices()
    
    // 等待信号
    <-sigChan
    log.Info("收到关闭信号，开始优雅关闭...")
    
    // 1. 停止接收新任务
    stopAcceptingTasks()
    
    // 2. 等待正在执行的任务完成（最多等待 5 分钟）
    waitForTasksWithTimeout(5 * time.Minute)
    
    // 3. 关闭数据库
    db.Close()
    
    log.Info("服务已安全关闭")
}
```

---

## 9. 性能指标与预期

### 9.1 转码性能 (Ryzen 3500X)

**参数**: `preset=veryslow`, `crf=28`, 3 并发

| 视频规格 | 原始大小 | 转码后大小 | 压缩率 | 耗时 (单个) |
|---------|---------|-----------|--------|------------|
| 1080p/30fps/10min | 2.5 GB | 500 MB | 80% | ~25 分钟 |
| 4K/60fps/10min | 8 GB | 1.2 GB | 85% | ~90 分钟 |

**夜间模式吞吐量** (02:00-08:00, 6小时):
- 理论处理: 约 15-20 个 1080p/10min 视频
- 实际处理: 考虑队列调度，约 12-18 个

### 9.2 资源占用

- **CPU**: 80%-90% (3 workers × ~30% 单核)
- **内存**: 1.5 GB - 2.5 GB (FFmpeg 缓冲 + Go Runtime)
- **磁盘 I/O**: 顺序读写，约 50-100 MB/s

### 9.3 Web 响应性能

- **仪表盘加载**: < 100ms
- **任务列表查询**: < 50ms (1000 条记录以内)
- **手动扫描触发**: 异步执行，立即返回

---

## 10. 风险与备选方案

### 10.1 可能遇到的问题

| 风险 | 影响 | 解决方案 |
|------|------|---------|
| FFmpeg 版本不兼容 | 转码失败 | 在 Dockerfile 中锁定 FFmpeg 版本 |
| 磁盘空间不足 | 转码中断 | 添加磁盘空间检查（预留 10GB） |
| 数据库锁冲突 | 并发写入失败 | 使用 WAL 模式 + 重试机制 |
| 网络挂载延迟 | 扫描/转码慢 | 增加超时时间，添加健康检查 |

### 10.2 未来扩展方向

- **Phase 4+**: 支持 NVENC 硬件加速（检测 NVIDIA GPU）
- **Phase 5**: WebSocket 实时推送任务进度
- **Phase 6**: 多节点分布式转码（使用 Redis 队列）
- **Phase 7**: 支持自定义 FFmpeg 预设（用户可配置）

---

## 11. 开发检查清单

### Phase 1 检查项
- [ ] 项目结构创建完成
- [ ] 数据库表结构正确，索引已创建
- [ ] 配置文件解析正常
- [ ] 扫描器能正确识别新文件/更新文件
- [ ] 群晖系统文件正确过滤（`@eaDir`, `SYNOPHOTO_*` 等）
- [ ] 所有视频格式均被识别（`.mp4`, `.mkv`, `.avi`, `.ts`, `.mov`, `.flv`, `.wmv`, `.m4v`, `.webm`）
- [ ] 单线程转码成功，错误日志记录完整
- [ ] Web 界面能显示任务列表
- [ ] 手动扫描按钮功能正常

### Phase 2 检查项
- [ ] Worker Pool 并发转码正常
- [ ] 时间窗口控制生效
- [ ] 强制启动开关功能正常
- [ ] FFmpeg 进度解析准确
- [ ] CPU 占用符合预期 (80%-90%)

### Phase 3 检查项
- [ ] 7天后文件移入垃圾桶
- [ ] 跨分区降级逻辑测试通过
- [ ] 30天后文件彻底删除
- [ ] 垃圾桶 Web 视图显示正常
- [ ] 手动删除功能有二次确认

### Phase 4 检查项
- [ ] Docker 镜像构建成功
- [ ] docker-compose 启动正常
- [ ] 健康检查通过
- [ ] 卷映射路径正确
- [ ] 集成测试全部通过
- [ ] README 文档完善

---

**文档版本**: v3.1  
**最后更新**: 2026-01-05  
**维护者**: STM 开发团队


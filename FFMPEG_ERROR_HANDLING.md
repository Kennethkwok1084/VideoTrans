# FFmpeg 错误处理说明

## 问题概述

转码过程中可能遇到各种 FFmpeg 错误，主要分为以下几类：

### 1. 文件损坏错误

**特征：**
- `Invalid NAL unit size` - H.264 编码单元损坏
- `Error splitting the input into NAL units` - 视频流解析失败
- `channel element X.X is not allocated` - AAC 音频流损坏
- `Decode error rate exceeds maximum` - 解码错误率过高

**示例日志：**
```
[h264 @ 0x...] Invalid NAL unit size (1915975546 > 87).
[aac @ 0x...] channel element 2.7 is not allocated
[vist#0:0/h264 @ 0x...] Error submitting packet to decoder: Invalid data found
Decode error rate 1 exceeds maximum 0.666667
Nothing was written into output file
```

**原因：**
- 源文件下载不完整
- 存储设备损坏
- 传输过程中数据损坏
- 文件被中断或截断

**解决方案：**
1. **自动跳过**：启用 `strict_check: true`（已在 config.yaml 中启用）
2. **手动检查**：查看失败任务列表，检查源文件
3. **重新下载**：如果可能，重新获取源文件

---

### 2. 格式不支持错误

**特征：**
- `Not yet implemented in FFmpeg` - 功能未实现
- `Too large remapped id` - ID 超出范围
- 特殊编码参数不兼容

**示例日志：**
```
[aac @ 0x...] Too large remapped id is not implemented
If you want to help, upload a sample of this file to https://streams.videolan.org/upload/
```

**原因：**
- 使用了 FFmpeg 不支持的编码特性
- 文件使用了非标准编码参数
- FFmpeg 版本过旧

**解决方案：**
1. 升级 FFmpeg 版本（在 Dockerfile 中指定）
2. 手动使用其他工具转码
3. 跳过该文件

---

### 3. 音频通道异常

**特征：**
- `23 channels` - 检测到异常多的声道数
- `Rematrix is needed between X channels and stereo` - 声道转换失败

**示例日志：**
```
[SWR @ 0x...] Rematrix is needed between 23 channels and stereo but there is not enough information
Failed to configure output pad on auto_aresample_0
```

**原因：**
- 音频流损坏，错误识别为 23 声道
- 文件元数据异常

**解决方案：**
1. 文件已损坏，自动跳过
2. 尝试使用 `-ac 2` 强制双声道输出（需修改 FFmpeg 参数）

---

## STM 的处理机制

### 当前实现

1. **转码前检查**（`strict_check: true`）：
   ```go
   // 检查视频流
   ffprobe -v error -select_streams v:0 -show_entries stream=codec_name,duration
   
   // 解码测试前 2 秒
   ffmpeg -v error -t 2 -i input.mp4 -f null -
   ```

2. **错误分类**：
   - ✅ 文件损坏 → 标记为 `failed`，记录详细原因
   - ✅ 磁盘空间不足 → 中止转码
   - ✅ FFmpeg 执行失败 → 记录前 500 字符错误日志

3. **日志增强**：
   ```
   ❌ 转码失败 #8: /mnt/5252/target/1 (3)-2.mp4
   🔍 源文件损坏或格式不支持，建议检查
   ```

### 配置选项

**config.yaml**
```yaml
ffmpeg:
  strict_check: true  # 启用严格文件检查，跳过损坏文件
```

---

## 查看失败任务

### Web 界面

访问 `http://your-server:9999/tasks?status=failed` 查看所有失败任务

### 数据库查询

```bash
docker exec stm sqlite3 /data/tasks.db \
  "SELECT id, source_path, error_message FROM tasks WHERE status='failed' ORDER BY updated_at DESC LIMIT 10;"
```

---

## 手动处理损坏文件

### 1. 检查文件完整性

```bash
# 快速检查
ffprobe -v error -show_format -show_streams input.mp4

# 完整解码测试（较慢）
ffmpeg -v error -i input.mp4 -f null - 2>&1 | grep -i error
```

### 2. 修复尝试

```bash
# 尝试修复容器
ffmpeg -i broken.mp4 -c copy -y fixed.mp4

# 重新编码（可能丢失部分内容）
ffmpeg -err_detect ignore_err -i broken.mp4 -c:v libx264 -crf 23 -c:a aac repaired.mp4
```

### 3. 删除无法修复的文件

```bash
# 移动到隔离目录
mkdir -p /mnt/corrupted_files
mv /path/to/broken.mp4 /mnt/corrupted_files/
```

---

## 常见问题

### Q1: 为什么文件检查通过了，但转码还是失败？

A: `strict_check` 只检查前 2 秒，文件后半部分可能损坏。可以增加检查时长（修改 `-t 2` 参数）。

### Q2: 所有文件都被标记为损坏？

A: 检查：
1. FFmpeg 版本是否正确
2. 文件权限是否正确
3. 磁盘是否已满

### Q3: 如何手动重试失败任务？

A: 目前需要在数据库中手动更新状态：
```sql
UPDATE tasks SET status='pending', retry_count=0 WHERE id=8;
```

---

## 统计失败原因

```bash
docker exec stm sqlite3 /data/tasks.db <<EOF
SELECT 
  CASE 
    WHEN error_message LIKE '%损坏%' THEN '文件损坏'
    WHEN error_message LIKE '%磁盘空间%' THEN '磁盘不足'
    WHEN error_message LIKE '%Not yet implemented%' THEN '格式不支持'
    ELSE '其他错误'
  END AS error_type,
  COUNT(*) AS count
FROM tasks 
WHERE status='failed' 
GROUP BY error_type 
ORDER BY count DESC;
EOF
```

---

## 更新日志

- **2026-01-10**: 添加 `strict_check` 配置，增强错误日志分类

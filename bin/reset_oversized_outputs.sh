#!/bin/bash
# 重置文件变大的已完成任务，让CPU重新转码

set -e

DB_PATH="/data/tasks.db"
CONFIG_PATH="/opt/stm/configs/config.yaml"

echo "═══════════════════════════════════════════════════════"
echo "  重置历史中文件变大的任务"
echo "═══════════════════════════════════════════════════════"
echo ""

# 获取输出目录配置
echo "📋 查询文件变大的任务..."
TASKS=$(sqlite3 "$DB_PATH" "SELECT id, source_path, output_size, source_size FROM tasks WHERE status='completed' AND output_size > source_size;")

if [ -z "$TASKS" ]; then
    echo "✅ 没有需要重置的任务"
    exit 0
fi

echo "$TASKS" | while IFS='|' read -r id source_path output_size source_size; do
    # 构建输出路径
    # 从source_path推断output_path
    # 假设 /mnt/5252/target/xxx -> /mnt/5252/target_new/xxx.mp4
    
    # 提取相对路径
    if [[ "$source_path" =~ /mnt/5252/target/ ]]; then
        rel_path="${source_path#/mnt/5252/target/}"
        output_path="/mnt/5252/target_new/${rel_path%.*}.mp4"
    else
        # 其他路径对的处理
        continue
    fi
    
    echo ""
    echo "任务 #$id:"
    echo "  源文件: $source_path"
    echo "  输出: $output_path"
    echo "  大小: $(($source_size/1024/1024))MB → $(($output_size/1024/1024))MB (+$(( ($output_size-$source_size)*100/$source_size ))%)"
    
    # 删除输出文件
    if [ -f "$output_path" ]; then
        rm -f "$output_path"
        echo "  ✅ 已删除输出文件"
    else
        echo "  ⚠️  输出文件不存在: $output_path"
    fi
    
    # 更新数据库
    sqlite3 "$DB_PATH" << EOSQL
UPDATE tasks 
SET status = 'pending',
    repair_mode = 'force_cpu',
    progress = 0,
    output_size = 0,
    completed_at = NULL,
    log = '文件变大，重置后由CPU重新转码'
WHERE id = $id;
EOSQL
    
    echo "  ✅ 任务已重置为待处理（强制CPU）"
done

echo ""
echo "═══════════════════════════════════════════════════════"
echo "✅ 重置完成！"
echo ""
echo "这些任务将由CPU编码器重新转码，确保文件变小。"
echo "═══════════════════════════════════════════════════════"

#!/bin/bash
# 卸载 STM systemd 服务

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

# 检查是否以root运行
if [ "$EUID" -ne 0 ]; then 
    echo -e "${RED}错误: 请使用 root 权限运行此脚本${NC}"
    exit 1
fi

echo "======================================"
echo "  STM Systemd 服务卸载"
echo "======================================"

# 停止服务
echo -e "\n${YELLOW}[1/4] 停止服务...${NC}"
if systemctl is-active --quiet stm; then
    systemctl stop stm
    echo "服务已停止"
else
    echo "服务未运行"
fi

# 禁用服务
echo -e "\n${YELLOW}[2/4] 禁用服务...${NC}"
if systemctl is-enabled --quiet stm 2>/dev/null; then
    systemctl disable stm
    echo "服务已禁用"
else
    echo "服务未启用"
fi

# 删除 service 文件
echo -e "\n${YELLOW}[3/4] 删除 service 文件...${NC}"
if [ -f /etc/systemd/system/stm.service ]; then
    rm /etc/systemd/system/stm.service
    systemctl daemon-reload
    echo "Service 文件已删除"
else
    echo "Service 文件不存在"
fi

# 询问是否删除程序文件
echo -e "\n${YELLOW}[4/4] 清理程序文件...${NC}"
read -p "是否删除 /opt/stm 目录? (y/N): " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
    if [ -d /opt/stm ]; then
        # 备份配置
        if [ -f /opt/stm/configs/config.yaml ]; then
            cp /opt/stm/configs/config.yaml /tmp/stm-config-backup.yaml
            echo "配置已备份到: /tmp/stm-config-backup.yaml"
        fi
        
        rm -rf /opt/stm
        echo "/opt/stm 已删除"
    fi
else
    echo "保留 /opt/stm 目录"
fi

# 询问是否删除数据
read -p "是否删除数据目录 /data (包含数据库和日志)? (y/N): " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
    if [ -d /data ]; then
        # 备份数据库
        if [ -f /data/tasks.db ]; then
            cp /data/tasks.db /tmp/stm-tasks-backup.db
            echo "数据库已备份到: /tmp/stm-tasks-backup.db"
        fi
        
        rm -rf /data
        echo "/data 已删除"
    fi
else
    echo "保留 /data 目录"
fi

echo -e "\n${GREEN}卸载完成!${NC}"

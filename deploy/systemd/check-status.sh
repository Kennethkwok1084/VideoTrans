#!/bin/bash
# STM 部署状态检查脚本

# 颜色定义
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

echo -e "${BLUE}======================================"
echo "  STM 部署状态检查"
echo "======================================${NC}"

# 检查服务状态
echo -e "\n${BLUE}[1] 服务状态${NC}"
if systemctl is-active --quiet stm; then
    echo -e "${GREEN}✓ STM 服务正在运行${NC}"
    systemctl status stm --no-pager -l | grep -E "(Active|Main PID|Memory|CPU)" | head -4
else
    echo -e "${RED}✗ STM 服务未运行${NC}"
fi

# 检查开机自启
echo -e "\n${BLUE}[2] 开机自启${NC}"
if systemctl is-enabled --quiet stm 2>/dev/null; then
    echo -e "${GREEN}✓ 已启用开机自启${NC}"
else
    echo -e "${YELLOW}✗ 未启用开机自启${NC}"
fi

# 检查文件
echo -e "\n${BLUE}[3] 安装文件${NC}"
[ -f /opt/stm/bin/stm ] && echo -e "${GREEN}✓ 程序文件存在${NC}" || echo -e "${RED}✗ 程序文件缺失${NC}"
[ -f /opt/stm/configs/config.yaml ] && echo -e "${GREEN}✓ 配置文件存在${NC}" || echo -e "${RED}✗ 配置文件缺失${NC}"
[ -d /opt/stm/internal/web/templates ] && echo -e "${GREEN}✓ 模板文件存在${NC}" || echo -e "${RED}✗ 模板文件缺失${NC}"
[ -f /etc/systemd/system/stm.service ] && echo -e "${GREEN}✓ Service 文件存在${NC}" || echo -e "${RED}✗ Service 文件缺失${NC}"

# 检查数据库
echo -e "\n${BLUE}[4] 数据目录${NC}"
[ -d /data ] && echo -e "${GREEN}✓ 数据目录存在${NC}" || echo -e "${RED}✗ 数据目录缺失${NC}"
[ -f /data/tasks.db ] && echo -e "${GREEN}✓ 数据库文件存在 ($(du -h /data/tasks.db | cut -f1))${NC}" || echo -e "${YELLOW}✗ 数据库文件不存在${NC}"

# 检查 Web 服务
echo -e "\n${BLUE}[5] Web 服务${NC}"
if curl -s http://localhost:8080 > /dev/null 2>&1; then
    echo -e "${GREEN}✓ Web 界面可访问 (http://localhost:8080)${NC}"
else
    echo -e "${RED}✗ Web 界面无法访问${NC}"
fi

# 检查硬件支持
echo -e "\n${BLUE}[6] 硬件加速${NC}"
if command -v nvidia-smi &> /dev/null; then
    GPU_NAME=$(nvidia-smi --query-gpu=name --format=csv,noheader 2>/dev/null | head -1)
    if [ -n "$GPU_NAME" ]; then
        echo -e "${GREEN}✓ NVIDIA GPU: $GPU_NAME${NC}"
    fi
else
    echo -e "${YELLOW}○ NVIDIA GPU 不可用${NC}"
fi

if [ -d "/dev/dri" ] && ls /dev/dri/renderD* &> /dev/null; then
    echo -e "${GREEN}✓ Intel QSV 设备可用${NC}"
else
    echo -e "${YELLOW}○ Intel QSV 不可用${NC}"
fi

# 检查配置
echo -e "\n${BLUE}[7] 运行配置${NC}"
RUN_MODE=$(grep "^run_mode:" /opt/stm/configs/config.yaml | cut -d'"' -f2)
echo -e "  运行模式: ${GREEN}$RUN_MODE${NC}"

WORKERS=$(grep "^  max_workers:" /opt/stm/configs/config.yaml | awk '{print $2}')
echo -e "  Worker 数: ${GREEN}$WORKERS${NC}"

# 检查最近日志
echo -e "\n${BLUE}[8] 最近日志${NC}"
journalctl -u stm -n 5 --no-pager 2>/dev/null | tail -5

# 总结
echo -e "\n${BLUE}======================================"
echo "  快速命令"
echo "======================================${NC}"
echo "  查看状态: systemctl status stm"
echo "  查看日志: journalctl -u stm -f"
echo "  重启服务: systemctl restart stm"
echo "  Web 界面: http://localhost:8080"
echo ""

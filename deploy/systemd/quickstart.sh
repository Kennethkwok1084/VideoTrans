#!/bin/bash
# STM 快速开始脚本 - Systemd 部署

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

clear
echo -e "${BLUE}"
cat << "EOF"
╔═══════════════════════════════════════════════════╗
║                                                   ║
║     STM - Smart Transcode Manager                ║
║     智能视频转码管理系统                          ║
║                                                   ║
║     Systemd 部署 - 硬件加速支持                   ║
║                                                   ║
╚═══════════════════════════════════════════════════╝
EOF
echo -e "${NC}"

# 检查 root 权限
if [ "$EUID" -ne 0 ]; then 
    echo -e "${RED}错误: 此脚本需要 root 权限${NC}"
    echo "请使用: sudo $0"
    exit 1
fi

# 检查必要工具
echo -e "${BLUE}[1/6] 检查系统要求...${NC}"
MISSING_TOOLS=""

if ! command -v go &> /dev/null; then
    MISSING_TOOLS="${MISSING_TOOLS}go "
fi

if ! command -v ffmpeg &> /dev/null; then
    MISSING_TOOLS="${MISSING_TOOLS}ffmpeg "
fi

if ! command -v systemctl &> /dev/null; then
    echo -e "${RED}错误: 此系统不支持 systemd${NC}"
    exit 1
fi

if [ -n "$MISSING_TOOLS" ]; then
    echo -e "${YELLOW}警告: 以下工具未安装: ${MISSING_TOOLS}${NC}"
    echo "是否继续? (y/N)"
    read -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
else
    echo -e "${GREEN}✓ 系统要求检查通过${NC}"
fi

# 硬件检测
echo -e "\n${BLUE}[2/6] 检测硬件加速支持...${NC}"
HARDWARE_CHECK_RESULT=""

# 检测 NVIDIA
if command -v nvidia-smi &> /dev/null; then
    echo -e "${GREEN}✓ 检测到 NVIDIA GPU${NC}"
    nvidia-smi --query-gpu=name --format=csv,noheader | head -n 1
    HAS_NVIDIA=true
else
    echo -e "${YELLOW}✗ 未检测到 NVIDIA GPU${NC}"
    HAS_NVIDIA=false
fi

# 检测 Intel
if [ -d "/dev/dri" ] && ls /dev/dri/renderD* &> /dev/null; then
    echo -e "${GREEN}✓ 检测到 Intel QSV 设备${NC}"
    HAS_INTEL=true
else
    echo -e "${YELLOW}✗ 未检测到 Intel QSV 设备${NC}"
    HAS_INTEL=false
fi

# 运行详细硬件检测脚本
echo ""
echo "是否运行详细硬件检测? (推荐) (Y/n)"
read -n 1 -r
echo
if [[ ! $REPLY =~ ^[Nn]$ ]]; then
    chmod +x deploy/systemd/check-hardware.sh
    ./deploy/systemd/check-hardware.sh
    echo ""
    echo "按 Enter 继续..."
    read
fi

# 构建项目
echo -e "\n${BLUE}[3/6] 构建项目...${NC}"
cd "$(dirname "$0")"
go build -o bin/stm ./cmd/stm
chmod +x bin/stm
echo -e "${GREEN}✓ 构建完成${NC}"

# 配置文件准备
echo -e "\n${BLUE}[4/6] 准备配置文件...${NC}"

# 创建临时配置
TEMP_CONFIG="/tmp/stm-config-temp.yaml"
cp configs/config.yaml "$TEMP_CONFIG"

# 根据检测结果调整配置
if [ "$HAS_NVIDIA" = true ] || [ "$HAS_INTEL" = true ]; then
    echo "检测到硬件加速支持，配置 worker_mapping..."
    
    # 询问配置策略
    echo ""
    echo "请选择编码器配置策略:"
    echo "  1) 优先使用 NVIDIA GPU (如果可用)"
    echo "  2) 优先使用 Intel QSV (如果可用)"
    echo "  3) 混合使用 (NVIDIA + Intel + CPU)"
    echo "  4) 仅使用 CPU (不使用硬件加速)"
    echo ""
    read -p "请选择 [1-4] (默认: 1): " STRATEGY
    STRATEGY=${STRATEGY:-1}
    
    case $STRATEGY in
        1)
            if [ "$HAS_NVIDIA" = true ]; then
                echo "配置为: NVIDIA 优先"
                sed -i 's/run_mode: "docker"/run_mode: "systemd"/' "$TEMP_CONFIG"
                sed -i '/worker_mapping:/,/^[^ ]/ s/# *- "nvidia_high"/- "nvidia_high"/' "$TEMP_CONFIG"
            else
                echo -e "${YELLOW}NVIDIA GPU 不可用，回退到 CPU${NC}"
            fi
            ;;
        2)
            if [ "$HAS_INTEL" = true ]; then
                echo "配置为: Intel QSV 优先"
                sed -i 's/run_mode: "docker"/run_mode: "systemd"/' "$TEMP_CONFIG"
                sed -i '/worker_mapping:/,/^[^ ]/ s/# *- "intel_balanced"/- "intel_balanced"/' "$TEMP_CONFIG"
            else
                echo -e "${YELLOW}Intel QSV 不可用，回退到 CPU${NC}"
            fi
            ;;
        3)
            echo "配置为: 混合模式"
            sed -i 's/run_mode: "docker"/run_mode: "systemd"/' "$TEMP_CONFIG"
            if [ "$HAS_NVIDIA" = true ]; then
                sed -i '/worker_mapping:/,/^[^ ]/ s/# *- "nvidia_high"/- "nvidia_high"/' "$TEMP_CONFIG"
            fi
            if [ "$HAS_INTEL" = true ]; then
                sed -i '/worker_mapping:/,/^[^ ]/ s/# *- "intel_balanced"/- "intel_balanced"/' "$TEMP_CONFIG"
            fi
            ;;
        4)
            echo "配置为: 仅 CPU 模式"
            ;;
        *)
            echo -e "${YELLOW}无效选择，使用默认配置${NC}"
            ;;
    esac
fi

# 询问输入输出路径
echo ""
echo "是否配置输入输出路径? (y/N)"
read -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
    read -p "输入视频目录 (默认: /mnt/pve/media/downloads): " INPUT_PATH
    INPUT_PATH=${INPUT_PATH:-/mnt/pve/media/downloads}
    
    read -p "输出视频目录 (默认: /mnt/pve/media/archive): " OUTPUT_PATH
    OUTPUT_PATH=${OUTPUT_PATH:-/mnt/pve/media/archive}
    
    # 这里需要 yq 或者手动编辑，简化起见提示用户
    echo -e "${YELLOW}请在安装后手动编辑 /opt/stm/configs/config.yaml${NC}"
    echo "设置 path.pairs 为:"
    echo "  - input: $INPUT_PATH"
    echo "    output: $OUTPUT_PATH"
fi

echo -e "${GREEN}✓ 配置准备完成${NC}"

# 安装
echo -e "\n${BLUE}[5/6] 安装 STM...${NC}"
mkdir -p /opt/stm/bin
mkdir -p /opt/stm/configs
mkdir -p /data

cp bin/stm /opt/stm/bin/
cp "$TEMP_CONFIG" /opt/stm/configs/config.yaml

# 安装 service
cp deploy/systemd/stm.service /etc/systemd/system/
systemctl daemon-reload

echo -e "${GREEN}✓ 安装完成${NC}"

# 启动服务
echo -e "\n${BLUE}[6/6] 启动服务...${NC}"
echo "是否立即启动服务? (Y/n)"
read -n 1 -r
echo
if [[ ! $REPLY =~ ^[Nn]$ ]]; then
    systemctl enable stm
    systemctl start stm
    sleep 2
    
    if systemctl is-active --quiet stm; then
        echo -e "${GREEN}✓ 服务启动成功${NC}"
        systemctl status stm --no-pager
    else
        echo -e "${RED}✗ 服务启动失败${NC}"
        journalctl -u stm -n 20
    fi
else
    echo "跳过启动，使用以下命令手动启动:"
    echo "  sudo systemctl start stm"
fi

# 完成
echo ""
echo -e "${GREEN}"
cat << "EOF"
╔═══════════════════════════════════════════════════╗
║                                                   ║
║              🎉 部署完成! 🎉                      ║
║                                                   ║
╚═══════════════════════════════════════════════════╝
EOF
echo -e "${NC}"

echo "后续操作:"
echo ""
echo "1. 查看服务状态:"
echo "   ${GREEN}systemctl status stm${NC}"
echo ""
echo "2. 查看实时日志:"
echo "   ${GREEN}journalctl -u stm -f${NC}"
echo ""
echo "3. 编辑配置文件:"
echo "   ${GREEN}nano /opt/stm/configs/config.yaml${NC}"
echo "   修改后重启: ${GREEN}systemctl restart stm${NC}"
echo ""
echo "4. 访问 Web 界面:"
echo "   ${GREEN}http://localhost:8080${NC}"
echo ""
echo "5. 使用 Makefile 管理:"
echo "   ${GREEN}make help${NC}  - 查看所有可用命令"
echo "   ${GREEN}make status${NC} - 查看状态"
echo "   ${GREEN}make logs${NC} - 查看日志"
echo ""
echo "6. 卸载服务:"
echo "   ${GREEN}make uninstall${NC}"
echo ""

# 显示配置摘要
echo "当前配置摘要:"
echo "  安装目录: /opt/stm"
echo "  数据目录: /data"
echo "  配置文件: /opt/stm/configs/config.yaml"
if [ "$HAS_NVIDIA" = true ]; then
    echo "  NVIDIA GPU: 已启用"
fi
if [ "$HAS_INTEL" = true ]; then
    echo "  Intel QSV: 已启用"
fi
echo ""

echo "完整部署文档: deploy/systemd/README_SYSTEMD.md"
echo ""
echo "祝您使用愉快! 🚀"

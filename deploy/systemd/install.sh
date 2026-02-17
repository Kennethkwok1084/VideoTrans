#!/bin/bash
# STM Systemd 部署安装脚本

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# 检查是否以root运行
if [ "$EUID" -ne 0 ]; then 
    echo -e "${RED}错误: 请使用 root 权限运行此脚本${NC}"
    exit 1
fi

echo "======================================"
echo "  STM Systemd 部署安装脚本"
echo "======================================"

# 1. 构建项目
echo -e "\n${GREEN}[1/7] 构建 Go 项目...${NC}"
cd "$(dirname "$0")/../.."
go build -o bin/stm ./cmd/stm
chmod +x bin/stm
echo "构建完成: $(pwd)/bin/stm"

# 2. 创建安装目录
echo -e "\n${GREEN}[2/7] 创建安装目录...${NC}"
mkdir -p /opt/stm/bin
mkdir -p /opt/stm/configs
mkdir -p /opt/stm/internal/web
mkdir -p /data
echo "目录创建完成"

# 3. 复制文件
echo -e "\n${GREEN}[3/7] 复制程序文件...${NC}"
cp bin/stm /opt/stm/bin/
cp configs/config.yaml /opt/stm/configs/
cp -r internal/web/templates /opt/stm/internal/web/

# 如果存在自定义配置，备份原配置
if [ -f /opt/stm/configs/config.yaml ]; then
    echo "检测到已存在配置文件，创建备份..."
    cp /opt/stm/configs/config.yaml /opt/stm/configs/config.yaml.backup.$(date +%Y%m%d_%H%M%S)
fi

echo "文件复制完成"

# 4. 安装 systemd service
echo -e "\n${GREEN}[4/7] 安装 systemd service...${NC}"
cp deploy/systemd/stm.service /etc/systemd/system/
systemctl daemon-reload
echo "Service 安装完成"

# 5. 检查硬件支持
echo -e "\n${GREEN}[5/7] 检查硬件加速支持...${NC}"

# 检查 NVIDIA GPU
if command -v nvidia-smi &> /dev/null; then
    echo -e "${GREEN}✓ NVIDIA GPU 已检测到:${NC}"
    nvidia-smi --query-gpu=name,driver_version --format=csv,noheader
    echo -e "${YELLOW}  提示: 确保已安装 nvidia-container-toolkit 或 nvidia-docker2${NC}"
else
    echo -e "${YELLOW}✗ 未检测到 NVIDIA GPU 或驱动${NC}"
fi

# 检查 Intel QSV
if [ -d "/dev/dri" ] && ls /dev/dri/renderD* &> /dev/null; then
    echo -e "${GREEN}✓ Intel QSV 设备已检测到:${NC}"
    ls -l /dev/dri/renderD*
    
    # 检查是否安装了必要的库
    if ldconfig -p | grep -q libva; then
        echo -e "${GREEN}  ✓ libva 已安装${NC}"
    else
        echo -e "${YELLOW}  ✗ libva 未安装，请运行: apt install libva2 libva-drm2 vainfo${NC}"
    fi
    
    if ldconfig -p | grep -q libmfx; then
        echo -e "${GREEN}  ✓ Intel Media SDK 已安装${NC}"
    else
        echo -e "${YELLOW}  ✗ Intel Media SDK 未安装，请运行: apt install intel-media-va-driver-non-free${NC}"
    fi
else
    echo -e "${YELLOW}✗ 未检测到 Intel QSV 设备${NC}"
fi

# 检查 FFmpeg 是否支持硬件编码
echo -e "\n检查 FFmpeg 硬件编码支持..."
if command -v ffmpeg &> /dev/null; then
    echo -e "${GREEN}✓ FFmpeg 已安装${NC}"
    
    if ffmpeg -hide_banner -encoders | grep -q h264_nvenc; then
        echo -e "${GREEN}  ✓ h264_nvenc (NVIDIA) 支持${NC}"
    else
        echo -e "${YELLOW}  ✗ h264_nvenc 不支持，需要使用支持 NVENC 的 FFmpeg 版本${NC}"
    fi
    
    if ffmpeg -hide_banner -encoders | grep -q h264_qsv; then
        echo -e "${GREEN}  ✓ h264_qsv (Intel QSV) 支持${NC}"
    else
        echo -e "${YELLOW}  ✗ h264_qsv 不支持，需要使用支持 QSV 的 FFmpeg 版本${NC}"
    fi
else
    echo -e "${RED}✗ FFmpeg 未安装，请先安装 FFmpeg${NC}"
fi

# 6. 配置文件提示
echo -e "\n${GREEN}[6/7] 配置文件检查...${NC}"
echo "配置文件位置: /opt/stm/configs/config.yaml"
echo -e "${YELLOW}请编辑配置文件，确保:${NC}"
echo "  1. run_mode 设置为 'systemd'"
echo "  2. 启用 encoder_profiles 中的硬件编码配置"
echo "  3. 设置 worker_mapping 以分配硬件编码器"
echo "  4. 配置正确的输入输出路径"
echo ""
echo "示例编辑命令: nano /opt/stm/configs/config.yaml"

# 7. 完成提示
echo -e "\n${GREEN}[7/7] 安装完成!${NC}"
echo ""
echo "======================================"
echo "  后续操作"
echo "======================================"
echo ""
echo "1. 编辑配置文件:"
echo "   nano /opt/stm/configs/config.yaml"
echo ""
echo "2. 启动服务:"
echo "   systemctl start stm"
echo ""
echo "3. 启用开机自启:"
echo "   systemctl enable stm"
echo ""
echo "4. 查看服务状态:"
echo "   systemctl status stm"
echo ""
echo "5. 查看日志:"
echo "   journalctl -u stm -f"
echo ""
echo "6. 访问 Web 界面:"
echo "   http://localhost:8080"
echo ""
echo "======================================"

#!/bin/bash
# STM 硬件加速环境检测和配置脚本

set -e

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo "======================================"
echo "  STM 硬件加速环境检测"
echo "======================================"

# 检测函数
check_nvidia() {
    echo -e "\n${BLUE}=== NVIDIA GPU 检测 ===${NC}"
    
    # 检查 nvidia-smi
    if command -v nvidia-smi &> /dev/null; then
        echo -e "${GREEN}✓ nvidia-smi 已安装${NC}"
        echo ""
        nvidia-smi --query-gpu=index,name,driver_version,memory.total --format=csv,noheader,nounits | while read line; do
            echo "  GPU $line"
        done
        
        # 检查 NVENC 支持
        echo ""
        echo -e "${BLUE}检查 NVENC 编码能力...${NC}"
        nvidia-smi --query-gpu=encoder.stats.sessionCount,encoder.stats.averageFps --format=csv,noheader 2>/dev/null || echo "  无法查询编码器状态（正常，仅运行时有数据）"
        
        NVIDIA_AVAILABLE=true
    else
        echo -e "${RED}✗ nvidia-smi 未找到${NC}"
        echo "  请安装 NVIDIA 驱动: https://www.nvidia.com/Download/index.aspx"
        NVIDIA_AVAILABLE=false
    fi
    
    # 检查 FFmpeg NVENC 支持
    if command -v ffmpeg &> /dev/null; then
        if ffmpeg -hide_banner -encoders 2>&1 | grep -q "h264_nvenc"; then
            echo -e "${GREEN}✓ FFmpeg 支持 h264_nvenc${NC}"
        else
            echo -e "${YELLOW}✗ FFmpeg 不支持 h264_nvenc${NC}"
            echo "  需要重新编译 FFmpeg 或安装支持 NVENC 的版本"
            NVIDIA_AVAILABLE=false
        fi
        
        if ffmpeg -hide_banner -encoders 2>&1 | grep -q "hevc_nvenc"; then
            echo -e "${GREEN}✓ FFmpeg 支持 hevc_nvenc${NC}"
        fi
    fi
    
    # 测试 NVENC
    if [ "$NVIDIA_AVAILABLE" = true ]; then
        echo ""
        echo -e "${BLUE}测试 NVENC 编码...${NC}"
        if timeout 10 ffmpeg -f lavfi -i testsrc=duration=1:size=320x240:rate=30 -c:v h264_nvenc -preset fast -f null - &>/dev/null; then
            echo -e "${GREEN}✓ NVENC 编码测试成功${NC}"
        else
            echo -e "${YELLOW}✗ NVENC 编码测试失败${NC}"
            NVIDIA_AVAILABLE=false
        fi
    fi
    
    echo ""
    if [ "$NVIDIA_AVAILABLE" = true ]; then
        echo -e "${GREEN}✓✓✓ NVIDIA 硬件加速已就绪 ✓✓✓${NC}"
    else
        echo -e "${YELLOW}⚠ NVIDIA 硬件加速不可用${NC}"
    fi
}

check_intel() {
    echo -e "\n${BLUE}=== Intel QSV 检测 ===${NC}"
    
    # 检查 /dev/dri
    if [ -d "/dev/dri" ]; then
        echo -e "${GREEN}✓ /dev/dri 目录存在${NC}"
        
        if ls /dev/dri/renderD* &> /dev/null; then
            echo -e "${GREEN}✓ 检测到 render 节点:${NC}"
            ls -l /dev/dri/renderD* | awk '{print "  " $0}'
            INTEL_DEVICE=true
        else
            echo -e "${YELLOW}✗ 未找到 renderD* 设备${NC}"
            INTEL_DEVICE=false
        fi
        
        if ls /dev/dri/card* &> /dev/null; then
            echo -e "${GREEN}✓ 检测到显卡节点:${NC}"
            ls -l /dev/dri/card* | awk '{print "  " $0}'
        fi
    else
        echo -e "${RED}✗ /dev/dri 目录不存在${NC}"
        INTEL_DEVICE=false
    fi
    
    # 检查 VA-API
    echo ""
    echo -e "${BLUE}检查 VA-API 库...${NC}"
    if ldconfig -p | grep -q libva; then
        echo -e "${GREEN}✓ libva 已安装${NC}"
        ldconfig -p | grep libva | head -n 3 | awk '{print "  " $0}'
        
        if command -v vainfo &> /dev/null; then
            echo -e "${GREEN}✓ vainfo 已安装${NC}"
            echo ""
            echo -e "${BLUE}VA-API 信息:${NC}"
            vainfo 2>&1 | grep -E "(Driver|VAProfile)" | head -n 10 | awk '{print "  " $0}'
        else
            echo -e "${YELLOW}✗ vainfo 未安装${NC}"
            echo "  安装: apt install vainfo"
        fi
    else
        echo -e "${YELLOW}✗ libva 未安装${NC}"
        echo "  安装: apt install libva2 libva-drm2"
        INTEL_DEVICE=false
    fi
    
    # 检查 Intel Media Driver  
    echo ""
    echo -e "${BLUE}检查 Intel Media Driver...${NC}"
    if ldconfig -p | grep -q "iHD_drv_video.so"; then
        echo -e "${GREEN}✓ iHD driver (新版) 已安装${NC}"
    elif ldconfig -p | grep -q "i965_drv_video.so"; then
        echo -e "${GREEN}✓ i965 driver (旧版) 已安装${NC}"
    else
        echo -e "${YELLOW}✗ Intel Media Driver 未安装${NC}"
        echo "  新硬件: apt install intel-media-va-driver-non-free"
        echo "  旧硬件: apt install i965-va-driver"
        INTEL_DEVICE=false
    fi
    
    # 检查 FFmpeg QSV 支持
    echo ""
    echo -e "${BLUE}检查 FFmpeg QSV 支持...${NC}"
    if command -v ffmpeg &> /dev/null; then
        if ffmpeg -hide_banner -encoders 2>&1 | grep -q "h264_qsv"; then
            echo -e "${GREEN}✓ FFmpeg 支持 h264_qsv${NC}"
        else
            echo -e "${YELLOW}✗ FFmpeg 不支持 h264_qsv${NC}"
            echo "  需要重新编译 FFmpeg 并启用 --enable-libmfx"
            INTEL_DEVICE=false
        fi
        
        if ffmpeg -hide_banner -encoders 2>&1 | grep -q "hevc_qsv"; then
            echo -e "${GREEN}✓ FFmpeg 支持 hevc_qsv${NC}"
        fi
    fi
    
    # 测试 QSV
    if [ "$INTEL_DEVICE" = true ] && command -v ffmpeg &> /dev/null; then
        echo ""
        echo -e "${BLUE}测试 QSV 编码...${NC}"
        if timeout 10 ffmpeg -init_hw_device qsv=hw -filter_hw_device hw -f lavfi -i testsrc=duration=1:size=320x240:rate=30 -vf hwupload=extra_hw_frames=64,format=qsv -c:v h264_qsv -preset fast -f null - &>/dev/null; then
            echo -e "${GREEN}✓ QSV 编码测试成功${NC}"
        else
            echo -e "${YELLOW}✗ QSV 编码测试失败${NC}"
            echo "  尝试不使用 hwupload 的简单测试..."
            if timeout 10 ffmpeg -f lavfi -i testsrc=duration=1:size=320x240:rate=30 -c:v h264_qsv -preset fast -f null - &>/dev/null; then
                echo -e "${GREEN}✓ 简单 QSV 编码测试成功${NC}"
            else
                echo -e "${YELLOW}✗ 简单 QSV 编码测试也失败${NC}"
                INTEL_DEVICE=false
            fi
        fi
    fi
    
    echo ""
    if [ "$INTEL_DEVICE" = true ]; then
        echo -e "${GREEN}✓✓✓ Intel QSV 硬件加速已就绪 ✓✓✓${NC}"
    else
        echo -e "${YELLOW}⚠ Intel QSV 硬件加速不可用${NC}"
    fi
}

check_system_info() {
    echo -e "\n${BLUE}=== 系统信息 ===${NC}"
    echo "  OS: $(uname -s) $(uname -r)"
    echo "  CPU: $(lscpu | grep "Model name" | cut -d: -f2 | xargs)"
    echo "  内存: $(free -h | awk '/Mem:/ {print $2}')"
    echo ""
    
    # 检查 FFmpeg 版本
    if command -v ffmpeg &> /dev/null; then
        echo "  FFmpeg: $(ffmpeg -version | head -n1)"
    else
        echo -e "  ${YELLOW}FFmpeg: 未安装${NC}"
    fi
}

check_permissions() {
    echo -e "\n${BLUE}=== 权限检查 ===${NC}"
    
    # 检查当前用户组
    echo "  当前用户: $(whoami)"
    echo "  用户组: $(groups)"
    
    # 检查是否在 video 和 render 组
    if groups | grep -q "video"; then
        echo -e "  ${GREEN}✓ 在 video 组${NC}"
    else
        echo -e "  ${YELLOW}✗ 不在 video 组${NC}"
        echo "    添加到组: sudo usermod -aG video $(whoami)"
    fi
    
    if groups | grep -q "render"; then
        echo -e "  ${GREEN}✓ 在 render 组${NC}"
    else
        echo -e "  ${YELLOW}✗ 不在 render 组${NC}"
        echo "    添加到组: sudo usermod -aG render $(whoami)"
    fi
    
    # 检查 /dev/dri 权限
    if [ -d "/dev/dri" ]; then
        echo ""
        echo "  /dev/dri 权限:"
        ls -l /dev/dri/ | awk '{print "    " $0}'
    fi
}

# 主逻辑
check_system_info
check_nvidia
check_intel
check_permissions

# 总结
echo ""
echo "======================================"
echo "  检测总结"
echo "======================================"
echo ""

if [ "$NVIDIA_AVAILABLE" = true ]; then
    echo -e "${GREEN}✓ NVIDIA 硬件加速可用${NC}"
else
    echo -e "${YELLOW}✗ NVIDIA 硬件加速不可用${NC}"
fi

if [ "$INTEL_DEVICE" = true ]; then
    echo -e "${GREEN}✓ Intel QSV 硬件加速可用${NC}"
else
    echo -e "${YELLOW}✗ Intel QSV 硬件加速不可用${NC}"
fi

echo ""
echo "======================================"
echo "  建议操作"
echo "======================================"
echo ""

# NVIDIA 建议
if [ "$NVIDIA_AVAILABLE" != true ]; then
    echo -e "${YELLOW}NVIDIA 配置建议:${NC}"
    echo "1. 安装 NVIDIA 驱动："
    echo "   Ubuntu: sudo apt install nvidia-driver-525"
    echo "   或访问: https://www.nvidia.com/Download/index.aspx"
    echo ""
    echo "2. 确保 FFmpeg 支持 NVENC："
    echo "   编译选项: --enable-cuda-nvcc --enable-cuvid --enable-nvenc"
    echo "   或使用: apt install ffmpeg (Ubuntu 22.04+)"
    echo ""
fi

# Intel 建议
if [ "$INTEL_DEVICE" != true ]; then
    echo -e "${YELLOW}Intel QSV 配置建议:${NC}"
    echo "1. 安装 VA-API 相关库："
    echo "   sudo apt install vainfo libva2 libva-drm2"
    echo ""
    echo "2. 安装 Intel Media Driver："
    echo "   新硬件 (>=Broadwell): sudo apt install intel-media-va-driver-non-free"
    echo "   旧硬件 (<Broadwell): sudo apt install i965-va-driver"
    echo ""
    echo "3. 确保 FFmpeg 支持 QSV："
    echo "   编译选项: --enable-libmfx 或 --enable-libvpl"
    echo "   或从源码编译: https://trac.ffmpeg.org/wiki/Hardware/QuickSync"
    echo ""
    echo "4. 添加用户到正确的组："
    echo "   sudo usermod -aG video,render $(whoami)"
    echo "   然后重新登录"
    echo ""
fi

# 通用建议
echo -e "${BLUE}通用建议:${NC}"
echo "1. 编辑配置文件 /opt/stm/configs/config.yaml"
echo "2. 设置 run_mode: \"systemd\""
echo "3. 根据可用硬件启用对应的 encoder_profiles"
echo "4. 配置 worker_mapping 分配编码器"
echo ""

echo "======================================"

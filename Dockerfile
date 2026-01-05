# ============ Stage 1: 构建 Go 二进制 ============
FROM golang:1.24-alpine AS builder

WORKDIR /build

# 配置国内镜像源
RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories

# 配置 Go 国内代理
ENV GOPROXY=https://goproxy.cn,direct
ENV GO111MODULE=on

# 安装编译依赖
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
FROM jrottenberg/ffmpeg:6.1-alpine

# 配置国内镜像源
RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories

# 创建应用目录
WORKDIR /app

# 从构建阶段复制二进制文件
COPY --from=builder /build/stm /usr/local/bin/stm

# 复制默认配置
COPY configs/config.yaml /app/config.yaml

# 复制HTML模板文件
COPY --from=builder /build/internal/web/templates /app/templates

# 创建数据目录
RUN mkdir -p /data /input /output

# 暴露 Web 端口
EXPOSE 8080

# 健康检查
HEALTHCHECK --interval=30s --timeout=3s \
  CMD wget --quiet --tries=1 --spider http://localhost:8080/api/health || exit 1

# 启动程序
ENTRYPOINT ["/usr/local/bin/stm"]
CMD ["--config", "/app/config.yaml"]

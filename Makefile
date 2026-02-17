# Makefile for STM - Smart Transcode Manager

.PHONY: help build install uninstall start stop restart status logs check-hardware clean test

# 默认目标
.DEFAULT_GOAL := help

# 配置
BINARY_NAME=stm
INSTALL_DIR=/opt/stm
CONFIG_DIR=$(INSTALL_DIR)/configs
DATA_DIR=/data
SERVICE_NAME=stm.service

help: ## 显示帮助信息
	@echo "STM - Smart Transcode Manager"
	@echo ""
	@echo "可用命令:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'
	@echo ""

build: ## 构建项目
	@echo "构建 $(BINARY_NAME)..."
	go build -ldflags="-s -w" -o bin/$(BINARY_NAME) ./cmd/stm
	@echo "构建完成: bin/$(BINARY_NAME)"

build-debug: ## 构建调试版本
	@echo "构建调试版本..."
	go build -gcflags="all=-N -l" -o bin/$(BINARY_NAME) ./cmd/stm
	@echo "调试版本构建完成"

test: ## 运行测试
	@echo "运行测试..."
	go test -v ./...

check-hardware: ## 检查硬件加速支持
	@echo "检查硬件加速环境..."
	@chmod +x deploy/systemd/check-hardware.sh
	sudo deploy/systemd/check-hardware.sh

install: build ## 安装并部署 systemd 服务 (需要 root)
	@echo "安装 STM systemd 服务..."
	@chmod +x deploy/systemd/install.sh
	sudo deploy/systemd/install.sh

uninstall: ## 卸载 systemd 服务 (需要 root)
	@echo "卸载 STM systemd 服务..."
	@chmod +x deploy/systemd/uninstall.sh
	sudo deploy/systemd/uninstall.sh

update: build ## 更新已安装的程序 (需要 root)
	@echo "更新程序..."
	sudo systemctl stop $(SERVICE_NAME) || true
	sudo cp bin/$(BINARY_NAME) $(INSTALL_DIR)/bin/
	sudo systemctl start $(SERVICE_NAME)
	@echo "更新完成"

update-config: ## 更新配置文件 (需要 root)
	@echo "备份并更新配置..."
	sudo cp $(CONFIG_DIR)/config.yaml $(CONFIG_DIR)/config.yaml.backup.$$(date +%Y%m%d_%H%M%S)
	sudo cp configs/config.yaml $(CONFIG_DIR)/
	@echo "配置已更新，请检查并重启服务: make restart"

start: ## 启动服务
	@echo "启动 STM 服务..."
	sudo systemctl start $(SERVICE_NAME)
	@sleep 1
	@make status

stop: ## 停止服务
	@echo "停止 STM 服务..."
	sudo systemctl stop $(SERVICE_NAME)

restart: ## 重启服务
	@echo "重启 STM 服务..."
	sudo systemctl restart $(SERVICE_NAME)
	@sleep 1
	@make status

status: ## 查看服务状态
	@sudo systemctl status $(SERVICE_NAME) --no-pager || true

enable: ## 启用开机自启
	@echo "启用开机自启动..."
	sudo systemctl enable $(SERVICE_NAME)
	@echo "已启用"

disable: ## 禁用开机自启
	@echo "禁用开机自启动..."
	sudo systemctl disable $(SERVICE_NAME)
	@echo "已禁用"

logs: ## 查看实时日志
	sudo journalctl -u $(SERVICE_NAME) -f

logs-recent: ## 查看最近100条日志
	sudo journalctl -u $(SERVICE_NAME) -n 100

logs-app: ## 查看应用日志文件
	sudo tail -f $(DATA_DIR)/stm.log

clean: ## 清理编译产物
	@echo "清理编译产物..."
	rm -f bin/$(BINARY_NAME)
	go clean
	@echo "清理完成"

dev: build ## 在开发模式下运行 (使用本地配置)
	@echo "开发模式运行..."
	./bin/$(BINARY_NAME) -config configs/config.yaml

docker-build: ## 构建 Docker 镜像
	@echo "构建 Docker 镜像..."
	docker-compose build

docker-up: ## 启动 Docker 容器
	@echo "启动 Docker 容器..."
	docker-compose up -d

docker-down: ## 停止 Docker 容器
	@echo "停止 Docker 容器..."
	docker-compose down

docker-logs: ## 查看 Docker 日志
	docker-compose logs -f

backup-db: ## 备份数据库
	@echo "备份数据库..."
	sudo cp $(DATA_DIR)/tasks.db /tmp/stm-tasks-backup-$$(date +%Y%m%d_%H%M%S).db
	@echo "备份完成: /tmp/stm-tasks-backup-$$(date +%Y%m%d_%H%M%S).db"

restore-db: ## 恢复数据库 (需指定 DB_FILE=path)
ifndef DB_FILE
	@echo "错误: 请指定数据库文件，如: make restore-db DB_FILE=/tmp/backup.db"
	@exit 1
endif
	@echo "恢复数据库..."
	sudo systemctl stop $(SERVICE_NAME) || true
	sudo cp $(DB_FILE) $(DATA_DIR)/tasks.db
	sudo systemctl start $(SERVICE_NAME)
	@echo "恢复完成"

tail-errors: ## 查看错误日志
	sudo journalctl -u $(SERVICE_NAME) -p err -n 50

check-config: ## 验证配置文件
	@echo "验证配置文件..."
	@./bin/$(BINARY_NAME) -config configs/config.yaml -validate 2>&1 || echo "需要实现 -validate 参数"

nvidia-info: ## 显示 NVIDIA GPU 信息
	@command -v nvidia-smi >/dev/null 2>&1 && nvidia-smi || echo "nvidia-smi 未安装"

intel-info: ## 显示 Intel GPU 信息
	@command -v vainfo >/dev/null 2>&1 && vainfo || echo "vainfo 未安装"
	@command -v intel_gpu_top >/dev/null 2>&1 && echo "可使用 intel_gpu_top 监控" || echo "intel_gpu_top 未安装"

monitor: ## 监控服务运行状态
	@watch -n 2 'systemctl status $(SERVICE_NAME) --no-pager; echo ""; echo "=== Recent Logs ==="; journalctl -u $(SERVICE_NAME) -n 5 --no-pager'

web: ## 打开 Web 界面
	@echo "打开 Web 界面..."
	@command -v xdg-open >/dev/null 2>&1 && xdg-open http://localhost:8080 || echo "请访问: http://localhost:8080"

info: ## 显示安装信息
	@echo "STM 安装信息:"
	@echo "  安装目录: $(INSTALL_DIR)"
	@echo "  配置文件: $(CONFIG_DIR)/config.yaml"
	@echo "  数据目录: $(DATA_DIR)"
	@echo "  Service: /etc/systemd/system/$(SERVICE_NAME)"
	@echo "  Web 界面: http://localhost:8080"
	@echo ""
	@echo "服务状态:"
	@systemctl is-active $(SERVICE_NAME) >/dev/null 2>&1 && echo "  运行中" || echo "  未运行"
	@systemctl is-enabled $(SERVICE_NAME) >/dev/null 2>&1 && echo "  已启用开机自启" || echo "  未启用开机自启"

quick-deploy: ## 快速部署 (检查硬件 -> 构建 -> 安装 -> 启动)
	@echo "===== 快速部署 ====="
	@make check-hardware
	@echo ""
	@echo "按 Enter 继续部署，或 Ctrl+C 取消..."
	@read _
	@make install
	@make enable
	@make start
	@echo ""
	@echo "===== 部署完成 ====="
	@make info

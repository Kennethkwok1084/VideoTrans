package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/stm/video-transcoder/internal/app"
	"github.com/stm/video-transcoder/internal/config"
	"github.com/stm/video-transcoder/internal/database"
)

func main() {
	// 解析命令行参数
	configPath := flag.String("config", "configs/config.yaml", "配置文件路径")
	flag.Parse()

	log.Println("====================================")
	log.Println("  STM - 视频自动化转码中心  v1.0")
	log.Println("====================================")

	// 加载配置
	log.Println("[Main] 加载配置文件...")
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("[Main] 加载配置失败: %v", err)
	}
	log.Printf("[Main] 配置加载成功: input=%s, output=%s", cfg.Path.Input, cfg.Path.Output)

	// 初始化数据库
	log.Println("[Main] 初始化数据库...")
	db, err := database.Init(cfg.Path.Database)
	if err != nil {
		log.Fatalf("[Main] 初始化数据库失败: %v", err)
	}
	defer db.Close()
	log.Println("[Main] 数据库初始化成功")

	// 数据库配置覆盖 YAML（数据库为主，YAML 为启动默认）
	if kv, err := db.GetAppConfig(); err != nil {
		log.Printf("[Main] 读取数据库配置失败，继续使用YAML: %v", err)
	} else if len(kv) > 0 {
		if err := cfg.ApplyRuntimeKV(kv); err != nil {
			log.Printf("[Main] 应用数据库配置失败，继续使用YAML: %v", err)
		} else {
			log.Printf("[Main] 已应用数据库配置覆盖（%d 项）", len(kv))
		}
	} else {
		if err := db.UpsertAppConfig(cfg.RuntimeKV()); err != nil {
			log.Printf("[Main] 初始化数据库配置失败（不影响运行）: %v", err)
		} else {
			log.Printf("[Main] 已将YAML配置初始化到数据库")
		}
	}

	if count, err := db.ResetProcessingTasksToPending(); err != nil {
		log.Printf("[Main] 恢复未完成任务失败: %v", err)
	} else if count > 0 {
		log.Printf("[Main] 已恢复 %d 个未完成任务为待处理", count)
	}

	// 创建应用程序 Supervisor
	application := app.New(cfg, db)

	// 创建上下文用于优雅关闭
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 设置信号处理
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)

	// 错误通道
	errChan := make(chan error, 1)

	// 启动应用程序
	go func() {
		errChan <- application.Run(ctx)
	}()

	log.Println("[Main] 所有服务已启动")
	log.Println("====================================")

	// 等待停止信号或错误
	select {
	case sig := <-sigChan:
		log.Printf("\n[Main] 收到关闭信号 (%v)，开始优雅关闭...", sig)
	case err := <-errChan:
		if err != nil {
			log.Printf("\n[Main] 应用程序错误: %v", err)
		}
		// Run 已经退出，无需再等待
		log.Println("[Main] 程序已退出")
		return
	}

	// 1. 取消上下文，通知所有组件停止
	cancel()

	// 2. 等待 application.Run() 返回（包含 worker drain）
	log.Println("[Main] 等待应用程序停止（最多30秒）...")

	select {
	case err := <-errChan:
		if err != nil {
			log.Printf("[Main] 应用程序关闭出错: %v", err)
		} else {
			log.Println("[Main] 应用程序已安全停止")
		}
	case <-time.After(30 * time.Second):
		log.Println("[Main] ⚠️  等待超时，强制退出")
	}

	// 3. 额外关闭 Web 服务器（如果 Run 中未完成）
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := application.Shutdown(shutdownCtx); err != nil {
		log.Printf("[Main] Web 服务器关闭出错: %v", err)
	}

	log.Println("[Main] 程序已退出")
}

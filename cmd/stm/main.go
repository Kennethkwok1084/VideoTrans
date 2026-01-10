package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/stm/video-transcoder/internal/cleaner"
	"github.com/stm/video-transcoder/internal/config"
	"github.com/stm/video-transcoder/internal/database"
	"github.com/stm/video-transcoder/internal/scanner"
	"github.com/stm/video-transcoder/internal/web"
	"github.com/stm/video-transcoder/internal/worker"
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
	if count, err := db.ResetProcessingTasksToPending(); err != nil {
		log.Printf("[Main] 恢复未完成任务失败: %v", err)
	} else if count > 0 {
		log.Printf("[Main] 已恢复 %d 个未完成任务为待处理", count)
	}

	// 创建各模块实例
	scan := scanner.New(cfg, db)
	work := worker.New(cfg, db)
	clean := cleaner.New(cfg, db)
	webServer := web.New(cfg, db, scan, work, clean)

	// 创建上下文用于优雅关闭
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 启动各模块的Goroutine
	log.Println("[Main] 启动后台服务...")

	// 启动扫描器
	go scan.RunPeriodically(ctx)

	// 启动Worker
	go work.Run(ctx)

	// 启动清理模块
	go clean.Run(ctx)

	// 设置信号处理
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)

	// 在单独的Goroutine中启动Web服务器
	go func() {
		if err := webServer.Start(":8080"); err != nil {
			log.Fatalf("[Main] Web服务器启动失败: %v", err)
		}
	}()

	log.Println("[Main] 所有服务已启动")
	log.Println("====================================")

	// 等待停止信号
	<-sigChan
	log.Println("\n[Main] 收到关闭信号，开始优雅关闭...")

	// 1. 取消上下文，通知所有Goroutine停止
	cancel()

	// 2. 关闭Web服务器（如果需要可以添加Shutdown方法）
	log.Println("[Main] 正在关闭Web服务器...")

	// 3. 等待 Worker 完成当前任务
	log.Println("[Main] 等待Worker完成当前任务...")
	// Worker.Run() 内部会通过 ctx.Done() 收到信号，
	// 并在当前任务完成后退出，wg.Wait() 会等待所有worker goroutine结束

	// 给一些时间让各模块优雅退出
	log.Println("[Main] 等待后台服务停止（最多10秒）...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	// 这里可以添加具体的等待逻辑
	// 例如: work.Wait(), scan.Wait() 等
	<-shutdownCtx.Done()

	log.Println("[Main] 服务已安全关闭")
}

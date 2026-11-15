// Command server 启动 HTTP 服务、WebSocket 推送和两个后台 worker。
//
// @title           Forum Backend API
// @version         1.0
// @description     社区论坛后端学习项目，统一响应格式为 { code, message, data }；失败响应额外带 error_code 与 request_id
// @host            localhost:8080
// @BasePath        /api
// @securityDefinitions.apikey Bearer
// @in              header
// @name            Authorization
//
//go:generate swag init -g cmd/server/main.go -o api/swagger --parseInternal
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"forum-backend/internal/config"
	"forum-backend/internal/notification"
	"forum-backend/internal/realtime"
	"forum-backend/internal/router"
	"forum-backend/pkg/jwt"
)

// main 的职责只有装配和生命周期管理，业务逻辑一律不写在这里。
// 顺序是：先建资源（配置、DB、Redis、JWT、Hub），再建路由，
// 再起后台任务，最后跑 HTTP 服务并等退出信号收尾。
func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	db, err := config.InitDB(&cfg.MySQL)
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err == nil {
		defer sqlDB.Close() // 进程退出时释放连接池
	}

	// 本机开发默认开着，改完模型重启就建表；生产关掉，改表走独立的 migrate 命令。
	// 关掉时这里只打一行日志，避免运维以为表已经建好了却查不到表。
	if cfg.MySQL.AutoMigrate {
		if err := config.Migrate(db); err != nil {
			log.Fatalf("自动迁移失败: %v", err)
		}
	} else {
		log.Printf("mysql.auto_migrate=false，已跳过自动建表；部署前请先执行 migrate 命令")
	}

	rdb, err := config.InitRedis(&cfg.Redis)
	if err != nil {
		log.Fatalf("连接 Redis 失败: %v", err)
	}
	defer rdb.Close()

	jwtManager, err := jwt.NewManager(cfg.JWT.Secret, cfg.JWT.Expire)
	if err != nil {
		log.Fatalf("JWT 配置错误: %v", err)
	}

	// Hub 必须全进程唯一：HTTP 侧推送和 WS 侧注册都指向同一个实例，
	// 各建一个的话消息永远找不到连接。
	hub := realtime.NewHub()

	// 所有外部依赖通过 Deps 显式注入，router 内部不再读全局配置。
	r, err := router.New(router.Deps{
		DB:             db,
		JWT:            jwtManager,
		Redis:          rdb,
		Hub:            hub,
		WSOrigins:      cfg.WS.AllowedOrigins,
		RateLimit:      cfg.RateLimit,
		TrustedProxies: cfg.Server.TrustedProxies,
		CORSOrigins:    cfg.CORS.AllowedOrigins,
	})
	if err != nil {
		log.Fatalf("注册路由失败: %v", err)
	}

	// 一个信号源同时管 HTTP server 与两个后台 worker。
	// NotifyContext 收到 Ctrl+C 或 SIGTERM 时取消 ctx，三处都会读到取消信号。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// relay 负责 outbox -> Redis Stream，notifier 负责 Stream -> 站内通知。
	// 两者组成事件链路的上下游，必须跟着进程一起起停。
	relay := notification.NewRelay(db, rdb)
	notifier := notification.NewNotificationWorker(db, rdb, hub)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		relay.Run(ctx)
	}()
	go func() {
		defer wg.Done()
		notifier.Run(ctx)
	}()

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	srv := &http.Server{Addr: addr, Handler: r}

	// 单独的 goroutine 监听退出信号，执行优雅关闭。
	go func() {
		<-ctx.Done()

		// WebSocket 连接握手后就被 hijack 了，不算 http.Server 管的活跃请求，
		// Shutdown 不会关它们，所以必须自己先全部断开，否则这些 goroutine 一直挂着。
		hub.CloseAll()

		// 给在途 HTTP 请求 5 秒收尾时间；超时就强制返回，不再等。
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown server: %v", err)
		}
	}()

	log.Printf("server start at %s", addr)
	// Shutdown 会让 ListenAndServe 返回 ErrServerClosed，那是正常退出，不是故障。
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("启动服务失败: %v", err)
	}

	// 等两个 worker 收完手，再让 main 返回、defer 关连接池。
	wg.Wait()
}

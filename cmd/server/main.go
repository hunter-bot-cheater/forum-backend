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
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"forum-backend/internal/config"
	"forum-backend/internal/message"
	"forum-backend/internal/notification"
	"forum-backend/internal/realtime"
	"forum-backend/internal/router"
	"forum-backend/internal/user"
	"forum-backend/pkg/jwt"
	"forum-backend/pkg/logging"
)

// main 的职责只有装配和生命周期管理，业务逻辑一律不写在这里。
// 顺序是：先建资源（日志、配置、DB、Redis、JWT、Hub），再建路由，
// 再起后台任务，最后跑 HTTP 服务并等退出信号收尾。
func main() {
	// 日志必须最先初始化：后面每一步失败都要靠它记下原因。
	// 这一步之前出错的，只能用标准错误输出，因为此刻还没有 logger。
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}

	_, closeLog, err := logging.Setup(logging.Config{
		Level:   cfg.Log.Level,
		Format:  cfg.Log.Format,
		Console: cfg.Log.Console,
		File: logging.FileConfig{
			Path:       cfg.Log.File.Path,
			MaxSizeMB:  cfg.Log.File.MaxSizeMB,
			MaxBackups: cfg.Log.File.MaxBackups,
			MaxAgeDays: cfg.Log.File.MaxAgeDays,
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化日志失败: %v\n", err)
		os.Exit(1)
	}
	defer closeLog()

	db, err := config.InitDB(&cfg.MySQL)
	if err != nil {
		exitOnError("连接数据库失败", err)
	}
	sqlDB, err := db.DB()
	if err == nil {
		defer sqlDB.Close() // 进程退出时释放连接池
	}

	// 本机开发默认开着，改完模型重启就建表；生产关掉，改表走独立的 migrate 命令。
	// 关掉时这里只打一行日志，避免运维以为表已经建好了却查不到表。
	if cfg.MySQL.AutoMigrate {
		if err := config.Migrate(db); err != nil {
			exitOnError("自动迁移失败", err)
		}
	} else {
		slog.Warn("mysql.auto_migrate=false，已跳过自动建表；部署前请先执行 migrate 命令")
	}

	rdb, err := config.InitRedis(&cfg.Redis)
	if err != nil {
		exitOnError("连接 Redis 失败", err)
	}
	defer rdb.Close()

	jwtManager, err := jwt.NewManager(cfg.JWT.Secret, cfg.JWT.Expire)
	if err != nil {
		exitOnError("JWT 配置错误", err)
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
		Metrics:        cfg.Metrics,
	})
	if err != nil {
		exitOnError("注册路由失败", err)
	}

	// 一个信号源同时管 HTTP server 与两个后台 worker。
	// NotifyContext 收到 Ctrl+C 或 SIGTERM 时取消 ctx，三处都会读到取消信号。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// relay 负责 outbox -> Redis Stream，notifier 负责 Stream -> 站内通知，
	// messagePusher 负责 Stream -> 私信实时推送（独立的消费组，见 message.PushWorker）。
	// 三者是事件链路的上下游，必须跟着进程一起起停。
	relay := notification.NewRelay(db, rdb)
	notifier := notification.NewNotificationWorker(db, rdb, hub)
	// user service 在这里单独建一份：router 内部那份不对外暴露，
	// 而推送只需要它「按 id 取展示信息」的能力，本身无状态，共用同一个 db 即可。
	messagePusher := message.NewPushWorker(db, rdb, hub, user.NewUserService(db))

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		relay.Run(ctx)
	}()
	go func() {
		defer wg.Done()
		notifier.Run(ctx)
	}()
	go func() {
		defer wg.Done()
		messagePusher.Run(ctx)
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
			slog.Error("shutdown server", "error", err)
		}
	}()

	slog.Info("server start", "addr", addr, "log_format", cfg.Log.Format, "log_file", cfg.Log.File.Path)
	// Shutdown 会让 ListenAndServe 返回 ErrServerClosed，那是正常退出，不是故障。
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		exitOnError("启动服务失败", err)
	}

	// 等两个 worker 收完手，再让 main 返回、defer 关连接池。
	wg.Wait()
}

// exitOnError 记一条 error 日志后退出进程。
//
// 启动阶段的失败没有恢复路径，能做的只是留下可查的记录并让托管方重启。
// 不用 log.Fatal：日志已经统一走 slog，混用两套格式会让采集端拿到非 JSON 行；
// 也正因为直接 os.Exit，前面的 defer（关日志文件、关连接池）不会执行——
// 进程结束时操作系统会回收句柄，日志 writer 没有缓冲，不会丢最后一条。
func exitOnError(msg string, err error) {
	slog.Error(msg, "error", err)
	os.Exit(1)
}

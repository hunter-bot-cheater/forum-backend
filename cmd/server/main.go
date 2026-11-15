//go:generate swag init -g cmd/server/main.go -o api/swagger --parseInternal

// @title           Forum Backend API
// @version         1.0
// @description     社区论坛后端学习项目，统一响应格式为 { code, message, data }；失败响应额外带 error_code 与 request_id
// @host            localhost:8080
// @BasePath        /api
// @securityDefinitions.apikey Bearer
// @in              header
// @name            Authorization

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

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	// 数据库也在这里创建，错误就地处理
	db, err := config.InitDB(&cfg.MySQL)
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err == nil {
		defer sqlDB.Close() // 进程退出时释放连接池
	}

	rdb, err := config.InitRedis(&cfg.Redis)
	if err != nil {
		log.Fatalf("连接 Redis 失败: %v", err)
	}
	defer rdb.Close() // 进程退出时释放连接池

	jwtManager, err := jwt.NewManager(cfg.JWT.Secret, cfg.JWT.Expire)
	if err != nil {
		log.Fatalf("JWT 配置错误: %v", err)
	}
	hub := realtime.NewHub()

	r, err := router.New(router.Deps{
		DB:             db,
		JWT:            jwtManager,
		Redis:          rdb,
		Hub:            hub,
		WSOrigins:      cfg.WS.AllowedOrigins,
		RateLimit:      cfg.RateLimit,
		TrustedProxies: cfg.Server.TrustedProxies,
	})
	if err != nil {
		log.Fatalf("注册路由失败: %v", err)
	}

	// 一个信号源同时管 HTTP server 与两个后台 worker。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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

	// 端口从配置读，不要写死 :8080
	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	srv := &http.Server{Addr: addr, Handler: r}

	go func() {
		<-ctx.Done() // 收到关闭信号
		// WebSocket 连接在握手后就被 hijack 了，不算 http.Server 管的活跃请求，
		// Shutdown 不会关它们。必须自己先断，否则这些 goroutine 一直挂着。
		hub.CloseAll()                                                                  //主动掐断所有 WebSocket 连接                                                              //监听http关闭信号
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second) //1.立刻关闭底层的监听端口（不再接受新请求）
		//2.等待所有正在处理的请求执行完毕
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil { //优雅关闭 HTTP 服务
			log.Printf("shutdown server: %v", err)
		}
	}()
	log.Printf("server start at %s", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("启动服务失败: %v", err)
	}

	// 等两个 worker 收完手，再让 main 返回、defer 关连接池。
	wg.Wait()
}

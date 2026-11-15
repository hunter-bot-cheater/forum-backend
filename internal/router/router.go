// Package router 路由注册：把 URL 绑定到 handler 和 middleware。
package router

import (
	"errors"
	"fmt"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"gorm.io/gorm"

	_ "forum-backend/api/swagger"
	"forum-backend/internal/article"
	"forum-backend/internal/comment"
	"forum-backend/internal/config"
	"forum-backend/internal/floor"
	"forum-backend/internal/hot"
	"forum-backend/internal/like"
	"forum-backend/internal/middleware"
	"forum-backend/internal/notification"
	"forum-backend/internal/realtime"
	"forum-backend/internal/user"
	"forum-backend/internal/ws"
	"forum-backend/pkg/jwt"
)

// Deps 是 router.New 需要的全部外部依赖。
// 用结构体传依赖，指针参数写反顺序时编译器发现不了。
type Deps struct {
	DB             *gorm.DB
	JWT            *jwt.Manager
	Redis          *redis.Client
	Hub            *realtime.Hub
	WSOrigins      []string
	RateLimit      config.RateLimitConfig
	TrustedProxies []string
}

// New 注册全部路由。
// db、jwtManager、rdb 都由 main 创建后传入，router 只负责组装。
func New(deps Deps) (*gin.Engine, error) {
	if deps.DB == nil {
		return nil, errors.New("router: database is required")
	}
	if deps.JWT == nil {
		return nil, errors.New("router: JWT manager is required")
	}
	if deps.Redis == nil {
		return nil, errors.New("router: Redis client is required")
	}
	if deps.Hub == nil {
		return nil, errors.New("router: realtime hub is required")
	}

	// gin.Default 自带的 404/405/panic 都返回纯文本，前端统一拆包会失败。
	// 换成 gin.New 加自定义兜底，所有失败路径输出统一 JSON。
	r := gin.New()
	installFallbacks(r)

	// API 文档只在调试模式暴露。生产用 GIN_MODE=release 关闭。
	if gin.IsDebugging() {
		r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler, ginSwagger.PersistAuthorization(true)))
	}

	// gin 默认信任所有代理，会让 X-Forwarded-For 伪造绕过按 IP 限流。
	// 配置留空时退回 RemoteAddr；部署在反代后面才填那一段代理 IP。
	if err := r.SetTrustedProxies(deps.TrustedProxies); err != nil {
		return nil, fmt.Errorf("router: set trusted proxies: %w", err)
	}
	userSvc := user.NewUserService(deps.DB)
	userHandler := user.NewUserHandler(userSvc, deps.JWT)

	articleSvc := article.NewArticleService(deps.DB, deps.Redis)
	commentSvc := comment.NewCommentService(deps.DB)
	articleHandler := article.NewArticleHandler(articleSvc)
	commentHandler := comment.NewCommentHandler(commentSvc)

	// 共用一个 redis.Client。
	likeSvc := like.NewLikeService(deps.DB, deps.Redis, articleSvc)
	hotSvc := hot.NewHotService(deps.DB, deps.Redis)
	likeHandler := like.NewLikeHandler(likeSvc, hotSvc)

	notificationSvc := notification.NewNotificationService(deps.DB)
	notificationHandler := notification.NewNotificationHandler(notificationSvc)
	wsHandler := ws.NewWSHandler(deps.Hub, deps.JWT, deps.WSOrigins)

	// 抢楼活动共用一个 redis.Client：发号器就在 Redis 里。
	floorSvc := floor.NewService(deps.DB, deps.Redis)
	floorHandler := floor.NewHandler(floorSvc)

	rateLimit, err := middleware.RateLimit(deps.Redis, middleware.RateLimitConfig{
		Capacity:  deps.RateLimit.Capacity,
		Rate:      deps.RateLimit.Rate,
		KeyPrefix: deps.RateLimit.KeyPrefix,
	})
	if err != nil {
		return nil, err
	}

	api := r.Group("/api")
	api.Use(limitBody(1 << 20))
	api.Use(rateLimit)
	{
		api.POST("/register", userHandler.Register)
		api.POST("/login", userHandler.Login)

		// 读接口公开，不挂 JWTAuth。
		api.GET("/articles", articleHandler.List)
		api.GET("/articles/:id", articleHandler.Detail)
		api.GET("/articles/:id/comments", commentHandler.List)
		// 静态段 hot 与通配段 :id 同层，0.7 节实测静态优先。
		api.GET("/articles/hot", likeHandler.Hot)
		// 活动详情公开：中奖名单本来就是要给人看的。
		api.GET("/floor-events/:id", floorHandler.Detail)

		// WebSocket 握手带不了自定义头，token 由 handler 自己从查询参数读，
		// 所以这两条放在公开组里，不能挂 JWTAuth。
		api.GET("/ws/notifications", wsHandler.Notifications)
		api.GET("/ws/stats", wsHandler.Stats)
		auth := api.Group("", middleware.JWTAuth(deps.JWT))
		{
			auth.POST("/articles", articleHandler.Create)
			auth.DELETE("/articles/:id", articleHandler.Delete)
			auth.POST("/articles/:id/comments", commentHandler.Create)
			auth.DELETE("/comments/:id", commentHandler.Delete)
			auth.PUT("/articles/:id/like", likeHandler.Like)
			auth.DELETE("/articles/:id/like", likeHandler.Unlike)
			auth.GET("/notifications", notificationHandler.List)
			auth.PUT("/notifications/read-all", notificationHandler.ReadAll)
			auth.POST("/floor-events", floorHandler.Create)
			auth.POST("/floor-events/:id/grab", floorHandler.Grab)
			auth.GET("/floor-events/:id/mine", floorHandler.Mine)
		}
	}

	return r, nil
}

// Package router 路由注册：把 URL 绑定到 handler 和 middleware。
package router

import (
	"context"
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
	"forum-backend/internal/follow"
	"forum-backend/internal/hot"
	"forum-backend/internal/like"
	"forum-backend/internal/message"
	"forum-backend/internal/metrics"
	"forum-backend/internal/middleware"
	"forum-backend/internal/notification"
	"forum-backend/internal/realtime"
	"forum-backend/internal/user"
	"forum-backend/internal/ws"
	"forum-backend/pkg/jwt"
)

// Deps 是 router.New 需要的全部外部依赖。
// 用结构体传依赖而不是一长串参数，指针参数写反顺序时编译器发现不了，会静默传错。
//
// 字段分三类：
//   - 连接类：DB、Redis（数据库与缓存客户端）
//   - 组件类：JWT（签发/校验 token）、Hub（WebSocket 在线连接表）
//   - 配置类：WSOrigins、RateLimit、TrustedProxies、CORSOrigins、Metrics（从 config 透传进来）
type Deps struct {
	DB             *gorm.DB
	JWT            *jwt.Manager
	Redis          *redis.Client
	Hub            *realtime.Hub
	WSOrigins      []string
	RateLimit      config.RateLimitConfig
	TrustedProxies []string
	CORSOrigins    []string
	Metrics        config.MetricsConfig
}

// New 组装出完整的 HTTP 服务：创建 gin 引擎 → 装全局中间件 → 建各业务对象 → 注册路由。
// 数据库、Redis、JWT 这些外部依赖由 main 创建好再传进来，router 只管接线，不负责连接。
func New(deps Deps) (*gin.Engine, error) {
	// 装配期一次性校验必需的依赖。这里挡掉比运行期每次请求都判断划算：
	// 少传一个依赖，服务直接起不来，而不是某个接口在半夜才报 nil panic。
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

	// gin.Default 自带的 404/405/panic 处理都返回纯文本，前端统一拆包会失败。
	// 换成 gin.New 加自定义兜底，所有失败路径都输出统一 JSON。
	r := gin.New()
	installFallbacks(r)

	// 留空表示同域部署（前端和接口同一个域名，走反代），完全不装中间件，行为与改动前一致。
	// 只有前后端分属不同域名时才需要配这个。
	if len(deps.CORSOrigins) > 0 {
		cors, err := middleware.CORS(deps.CORSOrigins)
		if err != nil {
			return nil, err
		}
		r.Use(cors)
	}

	// API 文档只在调试模式暴露。生产用 GIN_MODE=release 关闭。
	if gin.IsDebugging() {
		r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler, ginSwagger.PersistAuthorization(true)))
	}

	// gin 默认信任所有代理，会让客户端伪造 X-Forwarded-For 绕过按 IP 限流。
	// 配置留空时退回 RemoteAddr（直连 IP）；部署在反代后面才填那一段代理 IP。
	if err := r.SetTrustedProxies(deps.TrustedProxies); err != nil {
		return nil, fmt.Errorf("router: set trusted proxies: %w", err)
	}

	// 探活接口挂在根路径、不进 /api 分组，因此既不限流也不过 body 限制：
	// 它们会被 Nginx、监控和穿透工具周期性调用，不该占用业务接口的令牌桶。
	registerHealth(r, []healthCheck{
		{
			name: "mysql",
			check: func(ctx context.Context) error {
				// gorm.Open 是懒连接，真正能反映可用性的是底层 *sql.DB 的 Ping。
				sqlDB, err := deps.DB.DB()
				if err != nil {
					return err
				}
				return sqlDB.PingContext(ctx)
			},
		},
		{
			name: "redis",
			check: func(ctx context.Context) error {
				return deps.Redis.Ping(ctx).Err()
			},
		},
	})

	// 指标端点与两个在线连接数 gauge。
	// 用 BindGauge 而不是在 metrics 包里写死：Hub 是每个进程一个实例，
	// 只有装配到它的人才知道去哪里读在线数；抓取时才求值，热路径上不用埋钩子。
	registerMetrics(r, deps.Metrics)
	metrics.BindGauge("ws_online_users", "当前在线的 WebSocket 用户数", func() float64 {
		return float64(deps.Hub.OnlineUsers())
	})
	metrics.BindGauge("ws_online_connections", "当前 WebSocket 连接数（同一用户多标签页算多个）", func() float64 {
		return float64(deps.Hub.OnlineConns())
	})

	// 下面这一段是「接线」：把 service 和 handler 一层层 new 出来。
	// 每个模块的形状都一样：service 持有 data 层依赖 → handler 持有 service。
	// handler 只做参数解析与响应组装，业务逻辑全在 service 里。
	userSvc := user.NewUserService(deps.DB)
	// 关注模块要用 user.Briefs 拿展示信息，所以先建 userSvc 再建它。
	followSvc := follow.NewService(deps.DB, deps.Redis, userSvc)
	// 个人主页要显示关注数与「我是否关注了 TA」，把关注模块当接口注进去。
	// user 包不 import follow，依赖方向是 follow -> user，不会成环。
	userHandler := user.NewUserHandler(userSvc, deps.JWT, followSvc)
	followHandler := follow.NewHandler(followSvc)

	articleSvc := article.NewArticleService(deps.DB, deps.Redis)
	commentSvc := comment.NewCommentService(deps.DB)
	articleHandler := article.NewArticleHandler(articleSvc)
	commentHandler := comment.NewCommentHandler(commentSvc)

	// 点赞会触发文章缓存失效并写热榜，所以 like 要拿到 articleSvc 和 hotSvc。
	// 所有模块共用一个 redis.Client，它内部自带连接池，new 多个反而浪费。
	likeSvc := like.NewLikeService(deps.DB, deps.Redis, articleSvc)
	hotSvc := hot.NewHotService(deps.DB, deps.Redis)
	likeHandler := like.NewLikeHandler(likeSvc, hotSvc)

	notificationSvc := notification.NewNotificationService(deps.DB)
	notificationHandler := notification.NewNotificationHandler(notificationSvc)
	wsHandler := ws.NewWSHandler(deps.Hub, deps.JWT, deps.WSOrigins)

	// 私信的展示信息同样走 user.Briefs，所以只要 userSvc，不需要 follow。
	messageSvc := message.NewService(deps.DB, userSvc)
	messageHandler := message.NewHandler(messageSvc)

	// 抢楼活动的发号器（下一个空楼层）就存在 Redis 里，所以要传 redis 客户端。
	floorSvc := floor.NewService(deps.DB, deps.Redis)
	floorHandler := floor.NewHandler(floorSvc)

	// 限流器构造失败说明配置填错，跟上面的 nil 校验一样在装配期直接返回。
	rateLimit, err := middleware.RateLimit(deps.Redis, middleware.RateLimitConfig{
		Capacity:  deps.RateLimit.Capacity,
		Rate:      deps.RateLimit.Rate,
		KeyPrefix: deps.RateLimit.KeyPrefix,
	})
	if err != nil {
		return nil, err
	}

	// /api 组上挂两个所有接口都要过的中间件：
	// limitBody 限制请求体大小（防止超大 body 打爆内存），rateLimit 按 IP 限流。
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
		// 用户主页公开：看别人的资料不需要登录。
		// 静态段 me 与通配段 :id 同层，靠 gin 的静态优先规则区分。
		// 单独挂 OptionalJWTAuth 而不是整组挂：这两条要在公开可看的前提下
		// 多算一个「当前访问者是谁」，带 token 就算 is_following，不带就当游客。
		optionalAuth := middleware.OptionalJWTAuth(deps.JWT)
		api.GET("/users/:id", optionalAuth, userHandler.Detail)
		api.GET("/users/:id/following", optionalAuth, followHandler.Following)
		api.GET("/users/:id/followers", optionalAuth, followHandler.Followers)
		// 活动详情公开：中奖名单本来就是要给人看的。
		api.GET("/floor-events/:id", floorHandler.Detail)

		// WebSocket 握手带不了自定义头，token 由 handler 自己从查询参数读，
		// 所以这两条放在公开组里，不能挂 JWTAuth。
		api.GET("/ws/notifications", wsHandler.Notifications)
		api.GET("/ws/stats", wsHandler.Stats)

		// 下面这个子组路径是空的（""），表示沿用 /api 前缀，只是多挂一个登录校验。
		// 挂在这个组上的接口，handler 里一定能取到 userID；上面公开组的接口则取不到。
		auth := api.Group("", middleware.JWTAuth(deps.JWT))
		{
			// 自己看自己走 me，与看别人走 :id 是两条独立路径：
			// 以后个人中心要带「我的未读数」「我是否关注了 TA」这类只有登录态才有的字段，
			// 混用一个接口会让公开路径也得处理登录与否两种分支。
			auth.GET("/users/me", userHandler.Me)
			auth.PUT("/users/me", userHandler.UpdateMe)

			// 关注与取关都挂在登录组里：只有登录用户能建立关系。
			// 用 PUT + DELETE 而不是 POST：这是一份可以被反复覆盖的「关系状态」，
			// 重复 PUT 结果一致，符合幂等语义。
			auth.PUT("/users/:id/follow", followHandler.Follow)
			auth.DELETE("/users/:id/follow", followHandler.Unfollow)

			auth.POST("/articles", articleHandler.Create)
			auth.DELETE("/articles/:id", articleHandler.Delete)
			auth.POST("/articles/:id/comments", commentHandler.Create)
			auth.DELETE("/comments/:id", commentHandler.Delete)
			auth.PUT("/articles/:id/like", likeHandler.Like)
			auth.DELETE("/articles/:id/like", likeHandler.Unlike)
			auth.GET("/notifications", notificationHandler.List)
			auth.PUT("/notifications/read-all", notificationHandler.ReadAll)

			// 私信全部需要登录，没有公开可读的部分。
			// 路径用 with/{user_id} 而不是 {conversation_id}：前端从用户主页点「私信」
			// 时手里只有对方 id，还没有会话记录。让服务端按 pair_key 找会话，
			// 前端就不需要先调一次「创建会话」再跳转。
			auth.GET("/conversations", messageHandler.Conversations)
			// 静态段 unread 与 with/ 前缀不同层，不会和下面的通配段抢匹配。
			auth.GET("/conversations/unread", messageHandler.Unread)
			auth.GET("/conversations/with/:user_id/messages", messageHandler.Messages)
			auth.POST("/conversations/with/:user_id/messages", messageHandler.Send)
			auth.PUT("/conversations/with/:user_id/read", messageHandler.MarkRead)
			auth.POST("/floor-events", floorHandler.Create)
			auth.POST("/floor-events/:id/grab", floorHandler.Grab)
			auth.GET("/floor-events/:id/mine", floorHandler.Mine)
		}
	}

	return r, nil
}

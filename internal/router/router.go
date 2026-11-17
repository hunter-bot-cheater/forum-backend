// Package router 路由注册：把 URL 绑定到 handler 和 middleware。
package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"gorm.io/gorm"

	_ "forum-backend/api/swagger"
	"forum-backend/internal/article"
	"forum-backend/internal/ban"
	"forum-backend/internal/comment"
	"forum-backend/internal/config"
	"forum-backend/internal/floor"
	"forum-backend/internal/follow"
	"forum-backend/internal/hot"
	"forum-backend/internal/like"
	"forum-backend/internal/llm"
	"forum-backend/internal/message"
	"forum-backend/internal/metrics"
	"forum-backend/internal/middleware"
	"forum-backend/internal/notification"
	"forum-backend/internal/realtime"
	"forum-backend/internal/report"
	"forum-backend/internal/session"
	"forum-backend/internal/upload"
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
	// Admin 是管理类接口的白名单，为空表示没人能调（安全默认）。
	Admin config.AdminConfig
	// LLM 是自动生成标签的大模型配置。Enabled 或 APIKey 缺一个就不启用，
	// 由 router 决定是否给 article 服务注入 tagger。
	LLM config.LLMConfig
	// Upload 是用户上传图片的落盘目录与压缩参数。
	Upload config.UploadConfig
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

	// 管理员白名单为空时，创建活动接口对所有人都是 403。
	// 装配期提示一次，比等运营点了「创建」才看见 403 好排查：
	// 那时第一反应通常是怀疑登录态或前端，而不是配置里漏了一行。
	if len(deps.Admin.UserIDs) == 0 {
		slog.Warn("router: admin.user_ids is empty, POST /api/floor-events is disabled for everyone")
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
	// refresh token 的登记表在 Redis 里，签发、刷新、登出三个入口共用这一个 service。
	// 放在 userSvc 旁边是因为它只服务登录态，别的模块不需要。
	sessionSvc := session.NewService(deps.JWT, deps.Redis)
	// 关注模块要用 user.Briefs 拿展示信息，所以先建 userSvc 再建它。
	followSvc := follow.NewService(deps.DB, deps.Redis, userSvc)
	// 个人主页要显示关注数与「我是否关注了 TA」，把关注模块当接口注进去。
	// user 包不 import follow，依赖方向是 follow -> user，不会成环。
	// 登录失败锁定按用户名计数，与按 IP 的限流互补：
	// 限流挡的是「一台机器狂打」，换 IP 就绕过；锁定挡的是「盯着一个账号猜」。
	loginGuard := user.NewLoginGuard(deps.Redis)
	// 把白名单判定同时交给 /users/me 和 RequireAdmin：
	// 前端靠 is_admin 决定菜单里出不出现管理后台，判定源必须是同一个，
	// 否则会出现「菜单里有入口、点进去 403」。
	userHandler := user.NewUserHandler(userSvc, sessionSvc, loginGuard, followSvc,
		middleware.AdminChecker(deps.Admin.UserIDs))
	followHandler := follow.NewHandler(followSvc)

	// 自动标签是可选的：开关打开且填了密钥才注入，否则传 nil，
	// 文章服务在「没填标签」时就不去调模型，发布请求也不会多一次网络等待。
	// 这里用 var 声明接口而不是 *llm.Client：后者为 nil 时装进接口是非 nil 接口，
	// article 里那句 `s.tagger == nil` 会失效，结果是没密钥也去发一次 401 请求。
	var tagger article.TagSuggester
	if deps.LLM.Enabled && deps.LLM.APIKey != "" {
		tagger = llm.NewClient(llm.Options{
			BaseURL: deps.LLM.BaseURL,
			APIKey:  deps.LLM.APIKey,
			Model:   deps.LLM.Model,
			Timeout: time.Duration(deps.LLM.TimeoutSeconds) * time.Second,
		})
	}
	articleSvc := article.NewArticleService(deps.DB, deps.Redis, tagger)

	// 点赞会触发文章缓存失效并写热榜，所以 like 要拿到 articleSvc。
	// 反过来 article 的详情要显示「我点过没」，需要 likeSvc.HasLiked；
	// 但 article 不能直接 import like（like 已经 import article，反过来就成环），
	// 所以这里以 LikedChecker 接口的形式注入，article 包只认那个接口。
	likeSvc := like.NewLikeService(deps.DB, deps.Redis, articleSvc)
	hotSvc := hot.NewHotService(deps.DB, deps.Redis)

	articleHandler := article.NewArticleHandler(articleSvc, likeSvc)
	commentSvc := comment.NewCommentService(deps.DB)
	commentHandler := comment.NewCommentHandler(commentSvc)

	// 所有模块共用一个 redis.Client，它内部自带连接池，new 多个反而浪费。
	likeHandler := like.NewLikeHandler(likeSvc, hotSvc)

	// 通知列表要显示「谁赞了你」，actor 资料同样走 user.Briefs。
	notificationSvc := notification.NewNotificationService(deps.DB, userSvc)
	notificationHandler := notification.NewNotificationHandler(notificationSvc)
	wsHandler := ws.NewWSHandler(deps.Hub, deps.JWT, deps.WSOrigins)

	// 私信的展示信息同样走 user.Briefs，所以只要 userSvc，不需要 follow。
	messageSvc := message.NewService(deps.DB, userSvc)
	messageHandler := message.NewHandler(messageSvc)

	// 封禁状态按主键读 users 表的一列，不做缓存：解封后必须立刻生效，
	// 缓存的收益抵不上「已解封但缓存还没过期」带来的困扰。
	banStore := ban.NewStore(deps.DB)
	// 举报处置要下架内容并封人：下架复用 article / comment 已有的 Delete，
	// 封禁走 banStore，管理端列表补作者资料走 userSvc。
	reportSvc := report.NewService(deps.DB, banStore, articleSvc, commentSvc, userSvc)
	reportHandler := report.NewHandler(reportSvc)

	// 抢楼活动的发号器（下一个空楼层）就存在 Redis 里，所以要传 redis 客户端。
	floorSvc := floor.NewService(deps.DB, deps.Redis)
	floorHandler := floor.NewHandler(floorSvc)

	// 上传模块在这里构造，原因是它要建落盘目录：
	// 磁盘没挂上、权限不对应该在启动时报错，而不是等第一个用户上传时吃一个 500。
	uploadSvc, err := upload.NewService(upload.Config{
		Dir:          deps.Upload.Dir,
		URLPrefix:    deps.Upload.URLPrefix,
		MaxBytes:     deps.Upload.MaxBytes,
		MaxDimension: deps.Upload.MaxDimension,
		JPEGQuality:  deps.Upload.JPEGQuality,
		MaxPixels:    deps.Upload.MaxPixels,
	})
	if err != nil {
		return nil, err
	}
	uploadHandler := upload.NewHandler(uploadSvc)

	// 限流器构造失败说明配置填错，跟上面的 nil 校验一样在装配期直接返回。
	rateLimit, err := middleware.RateLimit(deps.Redis, middleware.RateLimitConfig{
		Capacity:  deps.RateLimit.Capacity,
		Rate:      deps.RateLimit.Rate,
		KeyPrefix: deps.RateLimit.KeyPrefix,
	})
	if err != nil {
		return nil, err
	}

	// 登录与注册用一个更严的桶：它们的正常用户一辈子只调几次，
	// 而撞库脚本一秒能打几十次。跟全局桶叠着用（先全局、再这个），
	// 前缀拼在环境前缀后面，多环境共用一个 Redis 时额度不会互相占用。
	// 容量 5、每秒补 0.25 个：突发 5 次，之后每 4 秒才多一次机会。
	credentialLimit, err := middleware.RateLimit(deps.Redis, middleware.RateLimitConfig{
		Capacity:  5,
		Rate:      0.25,
		KeyPrefix: deps.RateLimit.KeyPrefix + "credential:",
	})
	if err != nil {
		return nil, err
	}

	// 上传单独一个桶：每张图都要解码、缩放、编码、写盘，比普通接口重两个数量级。
	// 正常用户换头像一天也换不了几次，容量 5、每 10 秒补一个足够，
	// 而把它挂在全局桶里，刷图的人会把发帖、点赞的额度一起吃掉。
	uploadLimit, err := middleware.RateLimit(deps.Redis, middleware.RateLimitConfig{
		Capacity:  5,
		Rate:      0.1,
		KeyPrefix: deps.RateLimit.KeyPrefix + "upload:",
	})
	if err != nil {
		return nil, err
	}

	// 上传的图片由后端直接吐，本机开发够用。
	// 生产是 Nginx 读同一个目录（见 deploy/prod/locations.conf），请求根本不会到这里，
	// 但仍然注册：单机部署、或者 Nginx 漏配 /uploads 时，图还能显示出来。
	r.Static(uploadSvc.URLPrefix(), deps.Upload.Dir)

	// /api 组上挂两个所有接口都要过的中间件：
	// limitBody 限制请求体大小（防止超大 body 打爆内存），rateLimit 按 IP 限流。
	api := r.Group("/api")
	api.Use(limitBody(1 << 20))
	api.Use(rateLimit)

	// 上传单独建组，不挂在 api 组下面：那组的 limitBody 是 1 MiB，
	// 一张手机照片天然超它；把 api 组放宽等于所有接口一起放宽五倍，
	// 白白给了解析大 body 的机会。这里只给这一条路径放行。
	uploads := r.Group("/api/uploads")
	uploads.Use(limitBody(uploadHandler.BodyLimit()))
	uploads.Use(rateLimit)
	uploads.Use(middleware.JWTAuth(deps.JWT))
	// 封禁限制的是「制造内容」，上传头像是个人资料的一部分，
	// 但头像是公开可见的展示位，被封的人不该还能往上放图。
	uploads.Use(middleware.RequireActive(banStore))
	uploads.Use(uploadLimit)
	{
		uploads.POST("/avatar", uploadHandler.Avatar)
	}
	{
		api.POST("/register", credentialLimit, userHandler.Register)
		api.POST("/login", credentialLimit, userHandler.Login)
		// 刷新与登出都不挂 JWTAuth：前者进来时 access 通常已经过期，
		// 后者的 access 也可能刚好过期，卡住会让用户退不出去。
		// 两者的身份完全由 body 里的 refresh 证明。
		api.POST("/auth/refresh", userHandler.Refresh)
		api.POST("/auth/logout", userHandler.Logout)

		// 读接口公开，不挂 JWTAuth。
		// 文章详情挂 optionalAuth：公开可看，但登录用户要多算一个「我点过赞没」，
		// 详情接口据此回填 liked。没带 token 或 token 过期时降级成游客，照常能看。
		optionalAuth := middleware.OptionalJWTAuth(deps.JWT)
		api.GET("/articles", articleHandler.List)
		// 标签云公开：它是列表页的筛选入口，游客也应该看得到有哪些标签。
		// 放在 /articles 之后、/articles/:id 之前只是阅读顺序，gin 按静态段优先匹配，不受影响。
		api.GET("/tags", articleHandler.ListTags)
		api.GET("/articles/:id", optionalAuth, articleHandler.Detail)
		api.GET("/articles/:id/comments", commentHandler.List)
		// 评论定位同样公开：通知点进来时游客也可能落在详情页（比如链接被分享），
		// 定位失败最多是没滚动到那一条，不该让整页报错。
		api.GET("/comments/:id/locate", commentHandler.Locate)
		// 静态段 hot 与通配段 :id 同层，0.7 节实测静态优先。
		// 挂 optionalAuth 是为了回填每条的 liked：登录用户看到的爱心才是红的，
		// 没带 token 时降级成游客，榜单照常返回。
		api.GET("/articles/hot", optionalAuth, likeHandler.Hot)
		// 用户主页公开：看别人的资料不需要登录。
		// 静态段 me 与通配段 :id 同层，靠 gin 的静态优先规则区分。
		// 同样挂 OptionalJWTAuth：要在公开可看的前提下多算一个「当前访问者是谁」，
		// 带 token 就算 is_following，不带就当游客。
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
			// ——读接口—— 封禁只限制「制造内容」，不限制看内容：
			// 把人也一并挡在外面，等于让他连自己以前发的东西都翻不到。
			//
			// 自己看自己走 me，与看别人走 :id 是两条独立路径：
			// 个人中心要带「我的未读数」这类只有登录态才有的字段，
			// 混用一个接口会让公开路径也得处理登录与否两种分支。
			auth.GET("/users/me", userHandler.Me)
			auth.GET("/notifications", notificationHandler.List)
			auth.GET("/notifications/summary", notificationHandler.Summary)

			// 私信全部需要登录，没有公开可读的部分。
			// 路径用 with/{user_id} 而不是 {conversation_id}：前端从用户主页点「私信」
			// 时手里只有对方 id，还没有会话记录。让服务端按 pair_key 找会话，
			// 前端就不需要先调一次「创建会话」再跳转。
			auth.GET("/conversations", messageHandler.Conversations)
			// 静态段 unread 与 with/ 前缀不同层，不会和下面的通配段抢匹配。
			auth.GET("/conversations/unread", messageHandler.Unread)
			auth.GET("/conversations/with/:user_id/messages", messageHandler.Messages)
			auth.GET("/floor-events/:id/mine", floorHandler.Mine)

			// 三个「标记已读」虽然也写库，但改的是自己名下的已读标记，
			// 不产出任何别人能看到的内容，所以留在读组：
			// 封禁期间连红点都清不掉，只会让人以为通知也坏了。
			auth.PUT("/notifications/read-all", notificationHandler.ReadAll)
			// 单条已读：路径比 read-all 多一段，静态段与通配段同层由 gin 的静态优先规则区分。
			auth.PUT("/notifications/:id/read", notificationHandler.MarkRead)
			auth.PUT("/conversations/with/:user_id/read", messageHandler.MarkRead)

			// ——写接口—— 统一挂在封禁组上：被限制发言的人发不了文章、评论、私信，
			// 也点不了赞、关注不了人、改不了资料。
			//
			// 单独建一个子组而不是逐条路由加中间件：漏挂一条就是「被封了还能发」，
			// 而漏挂很难在测试里发现。分组之后，新加的写路由只要写进这个块就自动带上检查。
			write := auth.Group("", middleware.RequireActive(banStore))
			{
				write.PUT("/users/me", userHandler.UpdateMe)

				// 关注与取关是写操作：只有登录且未被封禁的用户能建立关系。
				// 用 PUT + DELETE 而不是 POST：这是一份可以被反复覆盖的「关系状态」，
				// 重复 PUT 结果一致，符合幂等语义。
				write.PUT("/users/:id/follow", followHandler.Follow)
				write.DELETE("/users/:id/follow", followHandler.Unfollow)

				write.POST("/articles", articleHandler.Create)
				// 标签建议放在发布之前调：写作页边写边问，把模型冷启动挪出发布链路。
				// 路径是静态段，与下面的 /articles/:id 不冲突（gin 静态优先）。
				// 它每次都要花一次模型额度，所以也在写组里：被封禁的账号不该继续消耗额度。
				write.POST("/articles/tag-suggest", articleHandler.SuggestTag)
				// 编辑用 PUT 而不是 PATCH：标题与正文都是必填的全量字段，
				// 没有「只改一项」的语义，完整的资源替换更好理解。
				write.PUT("/articles/:id", articleHandler.Update)
				write.DELETE("/articles/:id", articleHandler.Delete)
				write.POST("/articles/:id/comments", commentHandler.Create)
				write.DELETE("/comments/:id", commentHandler.Delete)
				write.PUT("/articles/:id/like", likeHandler.Like)
				write.DELETE("/articles/:id/like", likeHandler.Unlike)

				write.POST("/conversations/with/:user_id/messages", messageHandler.Send)

				// 举报也算写操作：被封禁期间这个账号不再产生任何新记录，
				// 管理端不必区分「哪种写要拦、哪种不拦」。
				write.POST("/reports", reportHandler.Submit)

				// 创建活动是运营动作，放在下面的 admin 组；抢楼是普通用户行为，
				// 跟着写组走：被封禁期间同样不能参与。
				write.POST("/floor-events/:id/grab", floorHandler.Grab)
			}

			// 管理端接口单独成组：管理员自己不该被封禁拦住——
			// 处置动作是他维持秩序的手段，被封了就没法撤销误判。
			// 所以这一组只挂 RequireAdmin，不进上面的 write 组；
			// 路径前缀也不统一到这里，运营类接口（/floor-events）与管理后台（/admin）各用各的路径。
			admin := auth.Group("", middleware.RequireAdmin(deps.Admin.UserIDs))
			{
				// 创建活动是运营动作：一个假活动会让抢楼模块白白占住 Redis 发号器
				// 和一批奖品名额，还会把首页塞满无效入口。
				// 白名单为空时这条路由对所有人都是 403，装配期会打一条 warn 提示。
				admin.POST("/floor-events", floorHandler.Create)

				adminAPI := admin.Group("/admin")
				{
					adminAPI.GET("/reports", reportHandler.List)
					adminAPI.PUT("/reports/:id", reportHandler.Handle)

					// 标签改名与合并只给管理员：一次操作会改掉成千上万篇文章的标签，
					// 普通用户误点一下就得靠对账式的数据修复才能还原。
					adminAPI.GET("/tags", articleHandler.ListTagsForAdmin)
					adminAPI.PUT("/tags/:id", articleHandler.RenameTag)
				}
			}
		}
	}

	return r, nil
}

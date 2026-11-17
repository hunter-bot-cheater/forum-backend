# 社区论坛后端学习项目

循序渐进的 Go 后端学习项目。学习目标是从可运行的注册登录服务逐步扩展到具备测试、配置、安全、部署和运维基础的论坛 API。每个阶段按步骤实现、运行、验收。代码示例会标明属于完整文件、增量修改或概念伪代码。没有实现并验证的功能不算项目已具备。

## 当前代码状态

- 已有：Gin 启动入口、Viper 配置读取、MySQL 连接与 GORM 自动迁移、Redis 连接初始化、用户模型、注册与登录业务逻辑及 HTTP handler、JWT Manager、中间件、路由接线。
- 文章与评论链路：service 方法、`internal/handler/dto.go` 的响应结构体、`article.go`、`comment.go`、`errors.go`、`paging.go` 均已落盘，`internal/router/router.go` 已注册列表、详情、评论列表等读路由与 JWT 保护的写路由。文章与评论的四个写接口都已在仓库源码实现，service 与 handler 的方法名统一成动词式（`Create` / `List` / `Detail` / `Delete`），对照表见 03 章 1.2。判断完成度用「路由已注册且验收通过」，别用「文件已存在」。
- `main.go` 已调用 `config.InitRedis`，`router.New` 的签名是 `New(db *gorm.DB, jwtManager *jwt.Manager, rdb *redis.Client)`。Redis 是启动硬依赖，连不上会直接退出。三层装配顺序见 03 章 7.2。仓库里没有 `/ping` 路由。
- 缓存已落地：详情接口的 Cache Aside 读路径（03 章 7.3）、写路径缓存失效（`Create` 与 `Delete` 在数据库写入或事务提交成功后调用 `InvalidateDetailCache`）、`InitRedis` 在 `Ping` 失败分支调用 `rdb.Close()`。旧版 README、00 章与 03 章 7.6 把这三项写成「待实现」或「还缺」，2026-11-07 核对源码后已更正。
- 已落盘：四个写接口（03 章 6.6、6.7）、缓存工程化加固（TTL 抖动、空值缓存、singleflight 请求合并、降级超时与连接池、指标计数，见 `internal/article/cache.go` 与 03a 章，路径已从旧的 `internal/service` 迁到 `internal/article`）。未完成：第 3 周点赞、热榜、限流的源码，第 3 周源码由你按 04 章十步跟做逐步落盘，状态以工作树为准，落盘并验收后才算具备；生产部署所需的迁移已提供独立的 `migrate` 命令（`cmd/migrate`，见「生产配置与迁移」），可观测性已提供结构化日志（`pkg/logging`，见「日志」）、`/metrics` 指标端点（见「指标」）与配套的抓取与告警样例（`deploy/prometheus.yml`、`deploy/alerts.yml`，见「监控告警」），服务管理与备份流程仍未做。文档里的设计要经过实现与验收才算完成。
- `docs/03a-第2周进阶-Redis缓存工程化加固.md` 是缓存加固的独立进阶专题，接在 03 章第 7 节之后，不占用周次；第 3 周仍是 04 章，第 4 周仍是 05 章。它同时修正了前面章节的进度快照。它记录的 `gorm.ErrRecordNotFound` 映射缺陷现已在源码修好，查不存在的文章与删后 GET 详情都返回 404。
- `docs/04a-第3周进阶-异步消息与站内通知.md` 与 `docs/04b-第3周进阶-Swagger-API文档与契约.md` 是第 3 周之后的两个进阶专题，同样不占周次。前者用 Transactional Outbox 加 Redis Stream 做点赞、评论后的站内通知，后者用 swaggo 从 handler 注解生成 Swagger 2.0 文档与可交互 UI。两章只给可落盘代码与验收步骤，源码状态以工作树为准。
- `docs/04c-第3周进阶-WebSocket实时通知.md`、`docs/04d-第3周进阶-敏感词过滤与前缀树.md`、`docs/04e-第3周进阶-全文搜索与倒排索引.md` 是同一系列的三个进阶专题，接在 04b 之后，同样不占周次。04c 把异步通知升级成服务端主动推送，含连接登记、心跳、慢客户端剔除与关停处理；04d 自己实现前缀树做敏感词替换，附 benchmark 实测与朴素做法的对比结论；04e 用第二个消费组把文章同步进 Meilisearch，实现关键词搜索与相关性排序。三章都只给可落盘代码，源码状态以工作树为准。
- 04a 章会改动 `cmd/server/main.go`，加入后台 worker 装配、`signal.NotifyContext` 与 `WaitGroup` 优雅关闭；04b 章会新增 `api/swagger/` 生成目录，别让它落进 `docs/`。04 章写的「main.go 零改动」只对那十步成立。04c 章把 `router.New` 的签名从三个位置参数改成 `New(Deps)`，`main.go` 的调用点与 `NewNotificationWorker` 的构造都要跟着改，04d、04e 依次往 `Deps` 里加 `Filter` 与 `Search`，所以这三章必须按顺序做。
- 对外响应结构体集中在 `internal/handler/dto.go`，与 handler 同包。注册与登录不再直接序列化 `model.User`，响应 JSON 形状与改动前一致。取舍依据见 02 章 8.0。
- `go.mod` 声明 `go 1.26.0`，本机实测 `go1.27.3`。先运行 `go version` 确认本机版本满足要求。旧版 README 写的 1.25.0 已作废。
- `go build ./...` 与 `go test ./...` 当前可通过，测试文件以工作树里的 `*_test.go` 为准（03a 章有 `internal/service/article_cache_test.go`，04 章 4.10 会新增 `internal/handler/dto_test.go`）。仓库里多处源码尚未 `gofmt`，用 `gofmt -l ./cmd ./internal ./pkg` 查差异，落地后对本次改过的文件跑 `gofmt -w`。
- 测试地基已落地：`internal/testutil` 提供真实 MySQL（`TEST_MYSQL_DSN` 开关）与 miniredis 两套夹具，`internal/comment` 已作为示范补齐 service 与 handler 测试，包覆盖率从 0% 到 81%（总覆盖率 21.7% → 24.4%）。CI 增加了 MySQL service 与 `-race`。用法见「测试」一节。
- 前端也有用例了：vitest + `@vue/test-utils`（jsdom），`cd web && npm test`，4 个文件 45 条。锁的是管理后台的权限分支与终态不可操作、用户 store 的加载态与登出顺序、热榜卡片的条数与截断条件，不是排版。详见「测试」一节的「前端」小节。
- 关注数已进 Redis 缓存：key 是 `user:follow:stats:v1:{user_id}`，TTL 5 分钟（叠加 0~1 分钟随机抖动防雪崩），关注/取关时删掉双方（关注者、被关注者）的 key。未命中时按用户 ID 做 singleflight 合并回源，热点主页同时过期不会把两次 COUNT 打成一片。读不到或 Redis 故障时直接回源 MySQL，接口不报错。公共读写工具在 `internal/cache`，Redis 单次操作限时 100ms。
- Redis 故障时的延迟预算是分层的，任何一层都不允许退回到客户端的 500ms：连接池 `PoolTimeout` 100ms（拿不到连接就降级）、限流 50ms（它在链路最前面，站住就是整站变慢）、缓存读写 100ms（`cache.WithTimeout`，文章详情和关注数共用）。`MaxRetries` 关闭（-1）：所有 Redis 用途都可降级，重试三次只会把一次故障放大成三倍延迟。实测 `CLIENT PAUSE 3000` 期间，主页 0.62s → 0.25s，文章详情 1.55s → 0.35s，正常路径均为 2~3ms。
- 私信已落地，一对一会话：`conversations` 用归一化的 `pair_key`（小 id 在前）保证两个人只有一条记录，未读数是会话行上的两列计数，与消息写入同事务。接口全部按「对方 user_id」寻址（`/conversations/with/{user_id}/...`），所以从某人主页点私信可以直接进聊天页，不需要先创建会话；消息列表响应里带 `peer`，首屏一个请求就能画出顶栏和消息。
- 私信记录是持久化的，两人可以反复、跨设备继续聊：消息落 `messages` 表，会话摘要（最后一条、未读数）落 `conversations` 表，两者在同一个事务里提交。重启服务、关浏览器、换设备都不影响历史，`TestMessagesPersistAfterReconnect` 会用一条全新的数据库连接把同一批消息读回来验证这一点（不是「看起来还在」，是换连接重读）。
- 消息列表用游标分页而不是页码：`GET /conversations/with/{user_id}/messages?before_id=<上一页的 next_before_id>&limit=20`。聊天记录只会往后追加，页码会随着新消息漂移，往上翻历史时会重复或漏条；`id` 稳定，用「本页最早一条的 id」当锚点不会漂。响应带 `has_more` 和 `next_before_id`，不需要额外的 COUNT。
- 发送接口支持幂等键：请求体里的可选字段 `client_msg_id`（客户端生成 UUID），同一个 key 重复提交只落一条，返回第一次那条消息，未读数也只加一次。撞唯一索引之后补查必须用 `FOR UPDATE` 当前读（RR 下快照读看不到并发事务刚提交的行），否则并发同键会有一个请求报 500。
- 私信已接实时推送：`Send` 在消息所在的同一个事务里写一条 `message.created` 事件，relay 搬进 Redis Stream，`message.PushWorker` 消费后推给收件人的在线连接（`/api/ws/notifications`）。推送帧是 `{"type":"message.created","data":{"message":{...},"sender":{...},"unread":3}}`，其中 `message` 与 REST 的 `messageResp` 同形，前端一套渲染逻辑能吃两种来源；`unread` 是服务端算好的该会话未读数，前端直接拿来更新红点，不用自己 +1。
- 推送的消费组按实例切分（`forum:message:v1:<主机名>`），不是所有实例共用一个组：消费组的语义是「一条消息只投给组内一个消费者」，共用组会让同一条私信只到一台机器，其余机器上在线的收件人永远等不到。组用 `$` 创建，只收新事件，避免每次部署把整个 Stream 重放一遍、把旧消息推给在线用户。代价是实例更替会在 Redis 里留下空组，需要用 `XGROUP DESTROY` 清理；单实例部署不存在这个问题。
- 私信不生成站内通知：`notification` 里给 `message.created` 注册了一个返回 nil 的 resolver，让它走正常路径直接 ACK。不注册会打一条「no notification resolver」告警（每条私信一行），注册成真通知则会让消息中心多出一份红点。
- 推送是尽力而为：收件人不在线、连接刚好断开都不影响消息已经落库，他下次打开聊天页会从 MySQL 拉到完整历史。重投可能让前端收到同一帧两次，前端按 `message.id` 去重即可（`client_msg_id` 也能用来对齐乐观渲染的气泡）。
- 私信前端已落地：`web/src/views/message/index.vue`，两个路由共用一个组件——`/messages` 只看会话列表，`/messages/:userId` 直接进入与某人的聊天页（从没聊过也能进，后端会返回空的 `list` 加对方的 `peer`）。界面不自己搭对话骨架，用开源库 `vue-element-plus-x` 的三个组件：`Conversations`（会话列表，选中态/hover/滚动/空态插槽）、`BubbleList`（气泡流，贴底跟随、上滑加载更早消息、回到最新按钮）、`XSender`（输入框，Enter 发送、字数上限、清空）。视觉按抖音私信调：左栏 320px 白底、灰色圆角搜索条、未读红点压头像右上角内侧、自己蓝色 `#1c7dff` 实心气泡、对方 `#f2f3f5` 灰气泡、圆角 18px、时间分隔整行居中。
- 上翻历史用 `before_id` 游标，滚动位置由 `BubbleList` 在插入旧消息后自行补正——早期是自己记 `scrollHeight` 差值算的，换成组件后这段逻辑删掉了。行样式走组件暴露的 `itemsStyle` / `itemsHoverStyle` / `itemsActiveStyle`，不 `:deep` 覆盖内部类名（内部结构会随版本变）。气泡配色必须带 `.elx-bubble__content-wrapper` 这一层选择器，组件默认规则挂在它上面，只写 `.elx-bubble__content` 特异性不够、自己的气泡仍是灰的（实测踩过）。
- 前端发送是乐观渲染：点发送先画一条半透明气泡并生成 `client_msg_id`，服务端确认后替换成真实消息，失败则标红并提供「重发」——重发复用同一个幂等键，即使上一次其实已经落库也只会返回同一条。连按 Enter 不会被拦截，`sending` 是计数而不是布尔。
- 前端的 WebSocket 是全局单例（`web/src/utils/ws.ts`）：`message.created` 一帧同时喂给聊天页（追加气泡）、会话列表（换摘要、顶到最前）和顶栏角标（更新未读数），各订阅各的，页面卸载时退订。断线按 1→2→4…30 秒退避重连并叠 0~1 秒随机抖动，避免后端重启后所有客户端在同一秒涌回来；重连成功后自动补拉断线期间漏掉的消息。
- 前端不再只存 token 和用户名：布局挂载时会调一次 `GET /users/me` 拿昵称头像，刷新页面后顶栏也能正确显示。登出会断开 WebSocket 并清空会话列表，否则下一个登录的人会先看到上一个人的私信。
- 前端视觉是一套统一基座：色板、圆角、阴影、三级文字都定义在 `web/src/styles/index.css` 的 `--fs-*` 变量里，并把 Element Plus 的 `--el-color-primary` 等主题变量接到同一支蓝上。布局是「顶栏 + 内容区」：品牌/导航/私信/通知角标/用户菜单在顶栏，页面自己决定单栏还是两栏（文章列表是「卡片流 + 右侧栏热榜」，详情页是 720px 单栏）。页面状态进 Pinia（`article`、`hot` 两个 store），列表分页与榜单缓存不放在组件里：从详情页返回能保留页码，右侧栏与热榜页共用同一份榜单缓存。
- 本地调试 WebSocket 时 `ws.allowed_origins` 要同时写 `127.0.0.1:5173` 和 `localhost:5173`：浏览器把它们当成两个不同的源，后端比对的是 `Origin` 的 `host:port`，漏一个的表现是页面里 WebSocket 握手 403、私信收不到实时消息。

- 消息中心已落地（`web/src/views/notification/index.vue`）：顶部四个分类胶囊「全部 / 赞 / 评论 / 关注」，数字来自 `GET /notifications/summary` 的一次 `GROUP BY type`（分类映射写在 `notification.kindEventTypes`，空分类表示不过滤）。已读只是把红点去掉，历史通知一直留在列表里可翻页、可点进文章，和抖音的消息中心一致；`PUT /notifications/read-all` 只改 `is_read`，不删记录。
- 通知列表带上了「谁」：`GET /notifications` 的每条现在是 `{...通知字段, actor:{id,username,nickname,avatar_url}}`，actor 由 `user.Briefs` 一次批量查出，与关注列表、私信会话共用同一份展示口径。触发者账号注销时 `actor` 整个字段省略（`omitempty`），前端显示「有人」，不会出现空白名字。
- 分类过滤的 `total` / `unread_count` / `list` 三条查询共用同一套过滤条件，否则切 tab 会出现「红点 2 条、列表 1 条」这种对不上的情况。前端按请求序号丢弃迟到响应：连续点 tab 时慢的那次回来若属于旧分类，直接扔掉。
- 同一种事件类型有两种文案：`comment.created` 的 `target_type` 是 `article` 时显示「评论了你的文章」，是 `comment` 时显示「回复了你的评论」，只看 `type` 会把回复写成评论。
- 评论支持回复：`comments` 表加了两列，`parent_id`（直接回复的那条，顶级为 0）与 `root_id`（所属根评论，顶级自己为 0），回复的回复统一挂回根评论，结构固定两层。列表只对根评论分页，回复用一次 `WHERE root_id IN (...)` 带回并按根评论归组，每条回复附带 `reply_to`（被回复者昵称），前端可以直接渲染「回复 @某人」。
- 回复的收件人换成被回复者：`comment.created` 事件里，顶级评论的 `Target` 是文章（照旧通知文章作者），回复的 `Target` 是被回复的那条评论，`resolveCommentCreated` 据此分流。已知取舍是回复不再顺带通知文章作者——热门文章会因此刷屏，要做也应按用户设置来，不在 resolver 里硬编码。
- 删根评论会连它下面的回复一起删（同事务），删回复只删那一条。不做级联的话，列表里会出现点不进父级、又找不到归属的孤儿回复。
- 单条已读：`PUT /notifications/:id/read`，点开哪条就清哪条的红点（和抖音一致），`affected` 为 0 表示本来就已读；`read-all` 才是整页清零。归属判断在 `WHERE id = ? AND user_id = ?`，别人的通知返回 404。
- 关注会通知对方：`follow` 行的插入与 `follow.created` 事件写在同一个事务里，进程挂在中间不会出现「关注成功但对方收不到」。重复关注（`RowsAffected = 0`）不写第二条事件，连点两下不会让对方多一个红点；取关也不撤回已发的通知，那条记录的是「当时有人关注了你」。关注类通知没有文章可跳（`article_id` 为 0），点开落到触发者主页。
- 回复配额按根评论算：每页每条根评论最多带 200 条回复（`ROW_NUMBER() OVER (PARTITION BY root_id)`，MySQL 8），不是整页共用一个上限——共用时一条热帖会把配额吃光，后面的根评论回复为空而 `reply_count` 照常显示几百。
- 文章列表支持关键词搜索：`GET /articles?keyword=xxx` 同时匹配标题与正文，可与 `author_id` 叠加，两个条件必须同时作用于 `Count` 与 `Find`（只给一边加会出现「总数 N 篇、翻页只有 M 条」）。关键词里的 `%`、`_`、`\` 先转义再拼进 `LIKE`，不转义时搜一个 `%` 会返回全站；空白关键词按「没搜」处理；超过 50 个字返回 400 而不是截断，截断会让结果与输入对不上。搜索走 `LIKE '%kw%'` 用不上索引，数据量上去后换 MySQL 全文索引或 04e 章的 Meilisearch，当前站内量级下全表扫在毫秒级，先不引入第二个组件。前端把搜索词放在地址栏 `?q=`，进入页面、前进后退、粘链接都从同一个入口还原，页面里的提交动作只负责改地址栏。
- 浏览数已落地：`articles.view_count`，只在详情接口被请求时 +1（内部调用如编辑后回读不算）。同一访问者 30 分钟内重复打开只算一次，去重靠 Redis `SETNX article:view:v1:{id}:{viewer}`，登录用户按 `u:<user_id>`、游客按 `ip:<客户端 IP>`，两种前缀不共用——否则 4 号用户会和「IP 末尾是 4」的游客互相占掉对方的标记。自增走一条 `UPDATE view_count = view_count + 1`，用 `UpdateColumn` 而不是 `Update`：后者会顺带写 `updated_at`，文章被看一眼「更新时间」就跳到当前。Redis 故障时降级为照常计数（宁可多算，不要在抖动期间一条都不记），降级方向与详情缓存相反。
- 详情响应里的浏览数用库里最新的值覆盖，不用详情缓存里那份快照。缓存 TTL 是 10~12 分钟，期间数字一直在涨，直接展示会越看越旧；更糟的是同一读者刷新两次，第二次命中缓存反而看到比第一次小的数字。代价是详情接口多一次主键单列查询（不走 JOIN、不 COUNT 点赞、不搬正文），换来的是准确且单调不减。缓存值只在这次查询失败时兜底。因为这个字段，详情缓存的 key 版本从 `v1` 升到了 `v2`，旧缓存无人读取、自然过期。
- 浏览数的展示在列表卡片与详情页的 meta 行（`N 阅读`），四位以内原样显示、上万后压成 `1.2 万`（`utils/format.ts` 的 `formatCount`）：卡片底部那一行很窄，五位数会把作者名挤掉。数字不适合当审计依据（去重窗口 + 降级都会让它偏离真实访问量），要精确统计得从 access log 数。
- 热榜分数是「点赞 × 3 + 浏览」，口径只有一份，在 `internal/hot/score.go` 的 `ScoreOf`（重建、点赞写入、浏览增量、对账四处共用）。之前只按点赞排：一篇被读了几百次、没人点赞的文章永远进不了候选集，而这类「有用但不让人激动」的内容恰恰是论坛里最常见的好文章。3:1 来自信号成本——点赞要先读完再点一下，一次赞背后至少三次访问，浏览可能只是一眼扫过。浏览只推进已经在榜的文章（`ZADD XX INCR`），不让每篇被打开过的文章都进 ZSET，否则这个集合几年后会变成全站文章的镜像；新文章进榜靠每小时的全量重建，或者等它的第一个赞（点赞路径算得出绝对分数，写绝对值，Redis 被清空后也能靠下一次点赞自己恢复）。没有时间衰减，当前是累计热度，一篇半年前的文章只要浏览量足够大就会一直在前面；要做近期热度得连重建的 SQL 一起改成带时间窗口的。
- 榜单条目的展示拆成三个数：`score` 是排名依据，`like_count` 与 `view_count` 是它的两个来源。ZSET 里只存合成分数，反推不出点赞数，所以读榜时多查一次 `article_likes` 的 `GROUP BY`（`fillLikeCounts`，走 `uk_article_user` 最左前缀）；这一步不能挪进 `loadByIDs`，否则降级路径的进程内缓存会被这次查询废掉。
- 标签已落地：`tags`（`name` 唯一，varchar(32)）加 `article_tags`（复合主键 `article_id + tag_id`，两个方向都是 RESTRICT 外键）。一篇文章最多 3 个标签，服务端做去空白、按不区分大小写去重（保留第一次出现的写法），超过上限返回 400 而不是静默截断——截断会让用户以为标签都存上了。发布与编辑都是「整体替换」：先删旧关联再写新的，编辑时标签变少才能真的减掉。
- 删文章要在同一个事务里先清 `article_tags`：外键是 RESTRICT，漏了这一步 MySQL 会直接拒绝删除，作者只看到「删除失败」而猜不到原因。`TestDeleteWithTagsRemovesLinks` 守着它，删掉那一行会真的报 `violates foreign key constraint`。
- 按标签筛选用子查询（`id IN (SELECT article_id FROM article_tags JOIN tags ...)`）而不是 JOIN 主表：列表的 `Count` 与 `Find` 共用同一个 scope，JOIN 会让一篇文章按标签数重复出现、总数也跟着翻倍，前端算出来的页数比实际多。`GET /articles?tag=后端` 可与 `keyword`、`author_id` 叠加。
- 标签跟着文章进出详情缓存：`model.Article` 加了 `Tags []string`（`gorm:"-"`，不落 `articles` 表），`queryDetail` 回源时填好，命中缓存也能拿到。因此详情缓存 key 从 `v2` 升到 `v3`；编辑标签走 `Update`，成功后照常删缓存。列表侧不做缓存，一次 JOIN 批量取回本页所有文章的标签，不逐篇查。
- `GET /api/tags` 返回按文章数倒序的前 20 个标签（平手按 `tags.id` 稳定排序），公开接口。列表页的标签云与写作页的下拉候选都已撤掉（标签一多会撑成好几排，下拉还会挡住正文），这个接口暂时没有前端调用方，留着是为了以后的「全部标签」页。
- 标签管理只有管理员能用，两个接口：`GET /api/admin/tags`（分页、可按名字搜、带 id、**含没人使用的空标签**——那正是最该清理的一批）与 `PUT /api/admin/tags/:id`（body 只收 `name`）。改成一个已经存在的名字时自动合并，不额外要一个 `merge` 开关：管理员想把「golang」并进「Go」，他要做的本来就是「把 golang 改名成 Go」，此时「Go」必然已存在；报「名字已存在」会让他无路可走（外键 RESTRICT 也不允许先删掉目标）。合并在一个事务里做三步：先删掉「同时挂着两个标签」的关联（不删会撞 `(article_id, tag_id)` 复合主键，整个事务失败）、整条 `UPDATE` 改指向、最后删源标签行。返回里的 `merged` 与 `moved_articles` 告诉管理员这次动了多少篇文章。
- 改名是唯一一种「不碰 `articles` 表却必须让文章详情缓存失效」的操作：缓存键是文章 id，而标签名存在缓存内容里。受影响文章在事务内先查出来（合并之后它们的 `tag_id` 都变了，事后再按源标签查一篇都查不到），提交后用 pipeline `UNLINK` 分批删，不用 `DEL` 是在主线程同步释放内存，几千个 key 会卡住 Redis 影响全站。清理失败只记日志不返回错误：库已经改好了，让一次 Redis 抖动把接口变成 500，管理员会以为没成功又点一次。暂不提供「删除标签」：删掉等于一次性抹掉几百上千篇文章的分类，而且没有撤销手段，需要清理时并到一个专门标签上更好收场。
- 前端两处入口：文章卡片标题右侧的标签（紧跟标题，点击进 `?tag=`）、详情页 meta 下方标签（同样进筛选）。筛选态和搜索词一样放在地址栏（`?tag=`），进入页面、前进后退、粘链接都从 query 还原；换标签回第 1 页，清标签保留搜索词。列表页只在真的筛着的时候显示一条「标签 xxx ×」的提示，点掉即取消——标签云没了之后，没有这条提示就没人知道列表为什么变短了。
- 写作页的标签是裸输入框：回车或中英文逗号确认，胶囊右侧 `×` 删除，限 3 个并显示 `n/3`。输入法组词中的回车不算确认（`isComposing`），否则中文用户永远打不出词。
- **没填标签会在后台自动补一个**：发布时 `tags` 为空 → 只往 `article:tags:v1` 投一条事件就返回，`TagWorker` 消费后调大模型（`internal/llm`，OpenAI 兼容的 `/chat/completions`）要一个 2~6 字的分类，返回后同样过 `normalizeTags` 清洗再落库。发布链路因此不再等模型：之前最慢的一次要 8 秒，现在响应时间与「填了标签」一样。`XADD` 带 `MaxLen: 1000, Approx`，Stream 不会自己回收已 ACK 的消息，不设上限就是常驻内存的泄漏。开关与密钥在 `config.yaml` 的 `llm` 段（`enabled` + `api_key` 都成立才生效）。任何失败（超时、401、内容审核拒绝）只记一条 warn 且不重试——重试会让消息永远挂在 PEL 里每轮失败一次，而标签是附加信息，不该让「起不出名字」变成「发不出去」。worker 写库前在同一个事务里再数一次标签，非空就放弃：模型思考的这 0.5~8 秒里，作者可能已经手动补了标签，不二次确认会覆盖用户的选择（`TestTagWorkerDoesNotOverwrite` 守着，删掉那段判断用例真失败）。编辑接口**不做**这件事：编辑时把标签全删是用户的明确动作，自动补一个等于删不掉。
- 写作页会在你写的时候就问一次标签：标题非空且正文 ≥20 字时防抖 1.2 秒调 `POST /articles/tag-suggest`，把模型的冷启动挪出发布链路。接口只返回建议、不落库，采用与否由作者点「采用」决定（走的是与手输同一套去重和 3 个上限）。单用户每小时 20 次配额（Redis `INCR` + 首次设过期），标题正文都空时直接返回空串不烧额度——那时模型只会随手编一个词。
- 举报与处置：`POST /api/reports` 举报文章或评论，`report` 表用 `target_type + target_id` 表达多态目标（文章和评论分属两张表，一个列没法同时指向），并冗余 `target_user_id` 与 `article_id` 供处置时直接用。同一个人对同一条内容只保留一条 `pending` 举报，处置完可以再报（内容可能被恢复后重新违规）；不能举报自己的内容。管理端两条接口挂在 `admin` 组：`GET /admin/reports`（默认只列 `pending`，一次批量补齐举报人、作者、文章标题与评论摘要，三张表各查一次而不是 2N+1 次）、`PUT /admin/reports/:id`（`dismiss` 驳回 / `remove` 下架 / `ban` 下架并封禁）。
- 处置的并发语义：先条件更新抢占 `WHERE id = ? AND status = 'pending'`，抢到才执行副作用，副作用失败再把状态退回 `pending`。反过来的顺序（先下架再改状态）在两位管理员同时点「驳回」与「封禁」时会自相矛盾——记录写着驳回，内容却已经被删、人也已经被封。`Handle` 里特意不先判断读出来的 `status`：那次判断挡不住并发，留着还会让「重复处置」的用例以为覆盖了条件更新，其实压的是预检（变异测试确认过）。
- 下架复用 `article` / `comment` 已有的 `Delete`，以举报记录里的作者 id 调用：那边用 `WHERE id = ? AND user_id = ?` 一次判断归属并软删，缓存失效、清点赞集合、移出热榜也跟着走，举报模块不必再实现一遍。内容已被作者自删或另一位管理员下架时删不到行，`ErrNotFound` 在这里被忽略——管理员要的结果已经达成。
- 封禁落在 `users.banned_until`（NULL 表示没封）与只给管理员看的 `ban_reason`。解封不需要定时任务：每次判断拿当前时间比一次，到期自然失效。拦截挂在写接口组上（`middleware.RequireActive`），读接口不拦——封禁的目的是不让人继续制造内容，不是让他连自己发过的东西都看不了。管理端接口单独成组、不挂这个中间件：管理员被封了就没法撤销误判。返回 403 + `error_code: banned`，文案里带解除时间，因为用户最想知道的就是「什么时候能再发」。查封禁状态不走缓存：封禁极低频，缓存的收益抵不上「已解封但缓存还没过期」的一致性成本，而按主键取一列是微秒级。
- 管理后台前端在 `/admin`（`web/src/views/admin/index.vue`），两个面板：举报处置按状态筛选（待处理 / 已驳回 / 已下架 / 已封禁），每条把举报人、作者、文章标题、评论摘要与补充说明一次摆全，避免管理员为了一条举报连开三个页面；标签整理可以按名字搜、行内改名，重名自动合并并回报「改挂了多少篇文章」。入口在用户菜单里，是否显示由 `GET /users/me` 新增的 `is_admin` 决定——这个字段的主语是「当前查看者」，所以只挂在 `/users/me` 上，公开主页的 `profileResp` 故意不带（`TestPublicProfileHasNoAdminFlag` 守着），否则看别人主页时会被读成「这个人是管理员」。判定函数 `middleware.AdminChecker` 与路由上的 `RequireAdmin` 共用同一份名单，不会出现「菜单里有入口、点进去 403」。前端这层只是入口开关：真正的边界在后端，把本地状态改成 true 也调不动任何管理接口。封禁天数用输入框而不是固定档位（1~3650 与后端一致，3650 约等于十年而不是「永久」），下架与封禁都要二次确认，驳回不要——驳回不处罚任何人，误点只是这条举报回到待处理。

## 计划实现的功能

- 用户注册和登录：密码使用 bcrypt 哈希保存，登录后签发 JWT。
- 文章：发布、分页列表、详情和作者删除。
- 评论：发表评论、查看文章评论和作者删除。
- Redis 练习：文章缓存、点赞集合、热榜 ZSET 和接口限流。

以上是最初的学习目标。文章与评论的读写接口、缓存链路已在源码落盘，第 3 周及其进阶专题按文档逐步落盘，完成度以各章进度快照和工作树为准。

## 技术栈

Go、Gin、GORM、MySQL、Viper、bcrypt、JWT 属于当前代码或第 1 周练习涉及的技术。go-redis 依赖已在 `go.mod` 中，客户端初始化与三参数装配已在第 2 周完成，缓存工程化加固见 03a 章。第 3 周进阶专题另外用到 Redis Stream、swaggo/swag 与 gin-swagger，落地状态以工作树为准。具体版本以 `go.mod` 为准。

## 目录

```text
cmd/server/        程序入口
cmd/migrate/       独立迁移命令（生产环境关掉自动迁移时用）
cmd/reconcile/     缓存与榜单对账
cmd/loadtest/      压测客户端
config/            YAML 配置
internal/config/   配置结构、MySQL/Redis 初始化、迁移、GORM 日志适配
internal/model/    GORM 数据模型
internal/user/     用户：注册登录、JWT 签发、资料与主页、Brief 批量取展示信息
internal/follow/   关注关系：关注/取关（幂等）、关注与粉丝列表、计数缓存
internal/message/  私信：一对一会话、发送、会话列表、消息分页、已读与未读角标
internal/article/  文章：发布、分页列表、详情（带缓存）、删除
internal/comment/  评论：发表、列表、删除
internal/like/     点赞（Redis SET + 计数）
internal/hot/      热榜（ZSET 与定时重算）
internal/floor/    抢楼活动与发号器
internal/notification/ 站内通知：outbox 消费、落库、WebSocket 推送
internal/realtime/     WebSocket 连接表 Hub
internal/ws/           WebSocket 升级入口与心跳
internal/event/    领域事件类型、outbox 写入、Stream 消费循环骨架（Consumer）
internal/cache/    Redis JSON 读写的公共小工具（读/写/删 + 降级约定）
internal/keys/     跨模块共用的 Redis key
internal/httputil/ handler 侧的公共逻辑：当前用户、错误映射、分页参数解析
internal/paging/   分页参数校验与偏移量计算（handler 与 service 共用一份上界）
internal/textutil/ 长度上限判断：区分按字符（varchar 列）与按字节（text 列）
internal/domain/   领域错误哨兵
internal/middleware/ 中间件（JWT、限流、访问日志、CORS、Recovery）
internal/metrics/  Prometheus 指标定义与抓取 handler
internal/router/   路由组装
internal/testutil/ 测试夹具：真实 MySQL 与 miniredis
pkg/               JWT、统一响应、结构化日志
docs/              分阶段学习文档（docs/10 是上线部署）
deploy/            部署样例：裸机 nginx、Prometheus 规则、容器镜像与生产叠加层
api/swagger/       Swagger 生成物（04b 章）
```

## 开始学习

1. 读 [`docs/00-完整项目实施总图.md`](docs/00-完整项目实施总图.md)，了解验收合同、讲解模板和全局勘误索引。
2. 读 [`docs/01-项目介绍与技术栈.md`](docs/01-项目介绍与技术栈.md)，确认项目边界与代码状态。
3. 按 `docs/02` 到 `docs/05` 完成各阶段练习；`docs/03a` 是第 2 周之后的缓存加固专题，建议做完 03 章第 7 节就接上，再进 04 章；做完 04 章十步之后依次接 `docs/04a`（异步消息与通知）、`docs/04b`（Swagger API 文档）、`docs/04c`（WebSocket 实时通知）、`docs/04d`（敏感词过滤）、`docs/04e`（全文搜索），最后进 05 章。复制示例前先核对函数签名、依赖和现有实现，冲突时以仓库源码为准。
4. 完成某个阶段后用文档里的验收步骤验证，再更新自己的项目说明。
5. `docs/06` 是简历表达练习模板，只保留自己真正实现、验证并能解释的内容。

每个功能点的讲解顺序统一为：目标与规则、最小概念、完整可运行代码、逐行拆解表、设计决策、常见错误对照、验证命令、参考来源。结构说明与英文权威链接见 00 章。

## 本机运行

在仓库根目录执行：

```powershell
go version
go mod download
go test ./...
go run ./cmd/server
```

默认从项目根目录运行时，配置读取 `config/config.yaml`。启动链路会校验 JWT 配置、连接 MySQL 并执行自动迁移，所以要先创建配置中的数据库并确认 MySQL 正在运行。连不上数据库时进程会退出，先修依赖再试 HTTP 请求。

## 测试

测试分两类，靠环境变量 `TEST_MYSQL_DSN` 开关：

| 类型 | 依赖 | 跑法 | 覆盖内容 |
|---|---|---|---|
| 纯单测 | 无 | `go test ./...` 默认就会跑 | 配置解析、中间件、纯函数、指标、日志轮转 |
| 集成测试 | MySQL + 进程内 Redis | 设好 `TEST_MYSQL_DSN` 后同样一条命令 | service 的 SQL、事务、外键、事件写入 |

不设 `TEST_MYSQL_DSN` 时集成测试走 `t.Skip`，没装 MySQL 的机器照样能跑单测；CI 里配了 MySQL service 并注入这个变量，所以不会出现「本地跳过、CI 假绿」。

```bash
# bash / macOS / CI
export TEST_MYSQL_DSN="root:你的密码@tcp(127.0.0.1:3306)/forum_test?parseTime=true&loc=Local&charset=utf8mb4"
go test -p 1 -count=1 ./...
```

```powershell
# PowerShell
$env:TEST_MYSQL_DSN="root:你的密码@tcp(127.0.0.1:3306)/forum_test?parseTime=true&loc=Local&charset=utf8mb4"
go test -p 1 -count=1 ./...
```

`-p 1` 是必须的：所有包共用同一个 `forum_test` 库，而每个用例开头都会 TRUNCATE 全部表。并行跑包时 A 包刚插进去的数据会被 B 包的清表抹掉，表现是随机几个用例挂掉、重跑又绿。这种红比不跑更浪费时间，所以 Makefile 和 CI 都加了 `-p 1`。

也可以 `make test-integration`（先 export 上面那个变量）或 `make test-cover`。

三个刻意的选择：

- **测试库必须和开发库分开**。`internal/testutil` 每个用例开始前会 `TRUNCATE` 所有表，指错库就是把开发数据清空，所以它拒绝不带库名的 DSN，也请把库名固定成 `forum_test`。
- **用真实 MySQL 而不是 sqlite 内存库**。项目的 SQL 用到 `LEFT()` 截摘要、`RESTRICT` 外键、条件 `UPDATE` 抢名额，sqlite 要么不支持要么语义不同，用它跑出来的绿是假绿。
- **Redis 用 miniredis**。进程内起一个、每个用例一份，既不用额外装服务，也不会把本地开发的热榜与限流桶清掉。

新增表时要把表名补进 `internal/testutil/db.go` 的 `testTables`，漏一张就会让脏数据串到下一个用例，典型症状是「单独跑能过、整包一起跑就挂」。

看覆盖率：

```bash
go test -cover ./...                                                      # 各包一行
go test -coverprofile=c.out ./... && go tool cover -func=c.out | tail -1   # 只看总计
```

### 前端

前端用例用 vitest + `@vue/test-utils`，跑在 jsdom 里，不需要后端、MySQL 或 Redis：

```bash
cd web
npm test        # vitest run
```

目前 4 个文件 45 条，覆盖的是「写错了不会报错」的那类判断，不是排版：

| 文件 | 锁住的行为 |
|---|---|
| `src/views/admin/index.spec.ts` | 普通用户看不到面板且不发任何管理请求；资料没拉到时只有骨架屏；拉失败时给重试而不是说没权限；终态举报不出按钮；内容已删时禁用「下架」；切筛选回第 1 页；标签同名不请求、清空名不发请求、重名合并的提示带影响条数 |
| `src/store/modules/user.spec.ts` | 登录后顶栏立刻有名字、双 token 都落盘；`is_admin` 只认字段本身（不看 id/用户名）；`fetchMe` 失败也要置 `profileLoaded`，否则页面永远停在骨架屏；登出先撤销 refresh 再清本地 |
| `src/components/HotRankCard.spec.ts` | 默认 5 条、`limit` 可改；条目等于 `limit` 时不给「查看完整榜单」；前三名高亮；空态与骨架屏的优先级（有旧数据刷新时不塌成骨架） |
| `src/utils/format.spec.ts` | 计数、相对时间、首字的纯函数边界 |

两个本机限制，都不是项目本身的问题：

- 直接 `npm test` 可以跑；但 `npm i` 这类安装命令要先 `NODE_OPTIONS= npm i`，注入的 node shim 会把安装进程拖崩（堆才用几十 MB 就 `EnsureCurrentCapacity`）。
- `vitest.config.ts` 固定 `pool: 'forks'` + `singleFork`。默认的 worker_threads 池在本机被子线程清理逻辑杀掉，报 `Worker exited unexpectedly`，与用例无关。

断言别写在假实现的回调里：被测函数自己包着 `try/catch` 时（例如登出），断言抛出的错误会被当成业务失败吞掉，用例照样绿。先记录当时的状态，出了函数再断言。


## 代码检查与 CI

### 本地

```bash
make lint     # gofmt 检查 + golangci-lint
```

需要先装 golangci-lint v2（配置按 v2 格式写）：

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
```

规则在 `.golangci.yml`，只开了确实能指出问题的十几项，分三组：

| 组 | 检查项 | 抓什么 |
|---|---|---|
| 基础正确性 | errcheck / govet / ineffassign / staticcheck / unused | 没处理的 error、死赋值、可疑写法 |
| 错误处理 | errorlint / nilerr / bodyclose / sqlclosecheck / rowserrcheck | `==` 比错误、吞错误、连接没关 |
| 代码质量 | gocritic / unconvert / unparam / misspell / whitespace | 多余转换、永远同一个值的参数、拼写与空行 |

刻意没收进来的：`gosec`（这个项目里 HTTP 参数都过了绑定与白名单，它报的多是「用户输入直接进 SQL」的误判，要看 20 条里找 1 条真问题，反而没人看）、`revive`/`stylecheck` 的命名类规则（与项目既有的 `articleSvc`、`userID` 命名冲突）。

原则是：一条规则如果长期靠 `//nolint` 压制，它就是在制造噪音，要么改代码要么关掉。当前代码里只有 4 处 `nolint`，每处都写了为什么（SQL 反引号不能用 `%#q`、websocket 的 resp.Body 被 Conn 接管等）。

### CI

`.github/workflows/ci.yml` 四个 job 并行跑：

| job | 内容 | 失败意味着 |
|---|---|---|
| `lint` | gofmt + golangci-lint | 代码风格或静态检查有问题 |
| `swagger` | 重新生成接口文档并比对 | 改了注解但忘了 `make swagger` |
| `test` | MySQL service + `-race` + 覆盖率门槛 | 功能坏了，或覆盖率跌破 45% |
| `web` | `npm ci` + `vue-tsc` + `vite build` | 前端类型或构建错误 |

覆盖率门槛设在 45%（当前约 53%），只防倒退，不追数字。加新功能一行测试都不写时，这一步会先把人拦下来。

前端目前只做类型检查与构建，没有接 ESLint：`vue-tsc` 已经能拦住绝大多数错误，而引入 ESLint 需要先给现有 20 多个组件统一风格，会顺带改掉一批无关文件。要做的话应该单独开一轮，不混在功能提交里。

## 探活接口

两个接口都在根路径，不挂鉴权、不进限流、不写访问日志，供 Nginx、监控和穿透工具调用：

```powershell
curl.exe -i http://localhost:8080/healthz   # 进程活着就 200，不查依赖
curl.exe -i http://localhost:8080/readyz    # 探测 MySQL 与 Redis，任一失败返回 503
```

`/healthz` 只回答「进程还能不能处理请求」，故意不查数据库：如果它也查，MySQL 抖动一下就会被外部工具判定进程已死并重启，故障反而放大。`/readyz` 返回 `{"status":"fail","checks":{"mysql":"fail","redis":"ok"}}` 时表示暂时接不了流量，进程不用重启，等依赖恢复即可。具体错误只写在服务端日志里，不回给调用方，避免泄露连接地址。

## 生产配置与迁移

`config/config.production.yaml` 是生产模板（不含任何真实凭据）。与本机配置的差异只有五处：`mysql.auto_migrate` 关掉并放大慢查询阈值、限流放大、`trusted_proxies` 指向本机 Nginx、`ws.allowed_origins` 换成真实域名、日志落盘。密码与密钥一律用环境变量给，不写进文件：

```bash
export MYSQL_PASSWORD=...
export REDIS_PASSWORD=...
export JWT_SECRET=$(openssl rand -hex 32)

CONFIG_PATH=/etc/forum/config.yaml GIN_MODE=release ./forum-migrate   # 先建表/补列
CONFIG_PATH=/etc/forum/config.yaml GIN_MODE=release ./forum-server    # 再启动服务
```

`mysql.auto_migrate` 默认 `true`，本机改完模型重启就建表。生产模板里是 `false`，改表必须显式跑 `./forum-migrate`（本机用 `make migrate`）：它跟启动解耦，部署顺序才是确定的；多个实例同时启动各跑一次迁移会互相等锁，甚至建到一半失败。跳过自动迁移时启动日志会明确打一行，避免运维以为表已经建好。

`make build` 同时产出 `bin/forum-server` 与 `bin/forum-migrate`；本机是 Windows 时用 `make build-release` 交叉编译 Linux 版（`-trimpath -ldflags="-s -w"`），再传到服务器。

## 日志

日志统一走 `log/slog`（`pkg/logging` 负责组装），默认 JSON 打控制台，字段固定为 `time / level / msg` 加业务字段，请求链路上的每条日志还会自动带 `request_id`。

```json
{"time":"2026-11-15T22:31:04.512+08:00","level":"INFO","msg":"http request","method":"GET","path":"/api/articles/12","status":200,"latency_ms":3.4,"size":312,"client_ip":"127.0.0.1","route":"/api/articles/:id","request_id":"9f2c..."}
```

三类日志的分工：

| 来源 | 内容 | 位置 |
|---|---|---|
| 访问日志 | 方法、路径、路由模板、状态码、耗时、响应大小 | `internal/middleware/accesslog.go` |
| 慢查询 | 超过 `mysql.slow_query_ms`（默认 200ms）的 SQL 与耗时 | `internal/config/gormlog.go` |
| 业务错误 | 操作名、路由、错误原文；panic 额外带堆栈 | `httputil.LogRequestError`、`handlePanic` |

级别按状态码决定：2xx/3xx 记 `INFO`，4xx 记 `WARN`，5xx 记 `ERROR`，可以直接按级别配告警。`/healthz`、`/readyz`、`/metrics` 不记访问日志，否则探测与抓取的频率会淹掉业务日志。

要落盘（裸机、内网穿透这类没有 journald 的场景）就配 `log.file.path`，写满 `max_size_mb` 切分成 `app.log.1`、`.2`，按 `max_backups` 与 `max_age_days` 控制总量：

```yaml
log:
  level: info          # debug / info / warn / error
  format: json         # 本机排查可改 text
  console: true
  file:
    path: /var/log/forum/app.log
    max_size_mb: 50    # 50MB × 8 份 ≈ 400MB 上限
    max_backups: 7
    max_age_days: 30
```

注意两点：日志文件同时只能被一个进程写，所以 `forum-server` 落盘、`forum-migrate`/`forum-reconcile` 只打控制台；`forum-reconcile` 的报告要占 stdout 给脚本消费，它的日志走 stderr，别混着看。

```powershell
# 提交前排查：只看错误，且只看某个 request_id
Get-Content logs\app.log | Select-String '"level":"ERROR"'
```

## 指标

`GET /metrics` 用 `prometheus/client_golang` 采集，Prometheus 文本格式（0.0.4），支持 gzip 与 OpenMetrics 协商。挂根路径、不在 `/api` 下，所以不限流、不占业务令牌桶；`metrics.enabled: false` 时完全不注册这条路由（返回统一 404，而不是空指标）。

指标分两层：

| 来源 | 指标 | 用途 |
|---|---|---|
| `internal/metrics/builtin.go` | `http_requests_total{method,route,status}` | 路由与状态码维度的流量，`route` 是模板（`/api/articles/:id`），不会随文章数发散 |
| | `http_request_duration_seconds{method,route}` | 耗时直方图，5ms~5s 十个桶，p95/p99 在 Prometheus 侧用 `histogram_quantile` 算 |
| | `http_panics_total`、`http_rate_limited_total`、`rate_limit_errors_total` | 三个「非 0 就该看一眼」的计数器 |
| | `gorm_slow_queries_total`、`gorm_query_errors_total` | SQL 层信号，慢查询阈值来自 `mysql.slow_query_ms` |
| | `article_cache_events_total{event}` | 缓存事件（hit/miss/load/share/degrade/write），命中率 = hit /(hit+miss) |
| | `follow_stats_cache_events_total{event}` | 关注数缓存事件（hit/miss/degrade/invalidate/share），`invalidate` 应当与关注/取关次数成正比，`share` 不为零说明 singleflight 正在合并并发回源 |
| | `ws_online_users`、`ws_online_connections` | 抓取时向 Hub 现读，不在连接建立/断开的热路径上埋点 |
| 库自带采集器 | `go_*`、`process_*` | goroutine 数、堆内存、GC、CPU、常驻内存、fd 数等，字段名和社区 Grafana 看板一致 |

想算 p95 就写 `histogram_quantile(0.95, sum by (le, route) (rate(http_request_duration_seconds_bucket[5m])))`。

生产上默认只允许本机或内网抓取（`deploy/nginx.conf.example` 里 `/metrics` 配了 `allow`/`deny`）。跨网段抓取时给 `metrics.token` 一个随机值，抓取请求带上 `Authorization: Bearer <token>`：指标里有路由清单和流量规模，裸奔在公网等于把接口地图送人。token 用环境变量给，别写进文件：

```bash
METRICS_TOKEN=xxxxxxxx ./forum-server-linux
curl -H "Authorization: Bearer xxxxxxxx" http://127.0.0.1:8080/metrics
```

指标只是原料，「要不要半夜叫醒人」得靠告警规则判断，见下一节。

## 监控告警（样例）

`deploy/prometheus.yml` 是抓取配置，`deploy/alerts.yml` 是告警规则，两个文件配套使用：

```bash
# 服务器上
cp deploy/prometheus.yml /etc/prometheus/prometheus.yml
cp deploy/alerts.yml     /etc/prometheus/alerts.yml
promtool check config /etc/prometheus/prometheus.yml   # 语法 + 规则一起校验
systemctl reload prometheus                            # 抓取与规则都支持热加载
```

抓取配置里只有两处要改：`targets` 换成后端实际地址，`credentials_file` 的路径按需调整（后端 `metrics.token` 为空就把整段 `authorization` 删掉，否则会带一个没人校验的头）。

12 条规则按「出了什么问题」分组，全部只用 `/metrics` 已有的指标，不需要额外装 exporter：

| 分组 | 规则 | 触发条件 | 级别 |
|---|---|---|---|
| 可用性 | `ForumDown` | 抓取失败持续 1 分钟 | critical |
| | `ForumNotReady` | `/readyz` 非 2xx 持续 2 分钟（需 blackbox exporter，默认注释掉） | critical |
| 错误 | `ForumHighErrorRatio` | 5xx 占比 > 5%，且流量 > 0.2 req/s，持续 5 分钟 | critical |
| | `ForumPanics` | 10 分钟内出现 panic | critical |
| | `ForumRateLimitFailing` | 限流器持续故障（fail-open 裸奔） | warning |
| | `ForumGormErrors` | SQL 错误 > 0.1 次/秒，持续 5 分钟 | warning |
| 延迟 | `ForumHighLatencyP95` | p95 > 500ms 持续 10 分钟 | warning |
| | `ForumSlowQuerySpike` | 慢查询 > 0.2 次/秒，持续 5 分钟 | warning |
| 缓存 | `ForumCacheHitRateLow` | 命中率 < 60% 持续 15 分钟 | warning |
| | `ForumCacheDegraded` | 出现降级（Redis 出错回源） | warning |
| 容量 | `ForumGoroutineHigh` | goroutine > 1000 持续 15 分钟 | warning |
| | `ForumMemoryHigh` | 常驻内存 > 512MiB 持续 15 分钟 | warning |

几个刻意的处理：

- **低流量闸门**。`ForumHighErrorRatio` 与 `ForumCacheHitRateLow` 后面都跟了 `and on() (...)`，要求 QPS 超过阈值才算。否则刚发版时 2 个请求里 1 个 500，占比 50%，半夜就会响。
- **`ForumPanics` 用 `increase` 不用 `rate`**。次数少的时候 `rate` 会算成 `0.0017` 这种值，看了一眼以为是噪声；`increase` 直接给出「发生过 1 次」。
- **`ForumDown` 用 `up`**。这是 Prometheus 自己生成的合成指标，抓不到就是 0，不依赖后端任何代码。
- **`ForumNotReady` 需要黑盒探测**。`/metrics` 能抓到只证明「进程能响应 HTTP」，看不见 MySQL / Redis。要把 `/readyz` 的 503 变成指标，得跑一个 blackbox_exporter，`prometheus.yml` 里那段注释掉的 job 就是配置，取消注释后规则才生效（不配时规则永远无数据，不误报也不报警）。

阈值是按小规模单机部署挑的起点，真正的数字要拿 `docs/05a` 的压测结论来定。定太低天天响、定太高真出事不响，两种都等于没有告警。

样例没有覆盖的部分：Alertmanager 的分组路由与静默（现在只配了 `targets`，告警会全部轰到默认 receiver）、Grafana 看板 JSON、以及 Prometheus 自身挂掉时的外部兜底（自监控告警发不出去，得靠另一个监控系统看它）。

## 服务器部署（样例）

`deploy/nginx.conf.example` 是单机同域反代的样例：Nginx 托管 `web/dist`，把 `/api` 转发到本机 8080，并单独处理 `/api/ws/` 的 WebSocket 升级。文件头部的注释列了必改项，包括 `server_name`、`root`、证书路径和后端 `server.trusted_proxies`。`/healthz`、`/readyz`、`/metrics` 也单独代理了，否则会落到 SPA 回退规则上、返回 200 但内容是一张网页。

```bash
cd web && npm run build                 # 前端产物在 web/dist
make build-release                      # 交叉编译 bin/forum-server-linux 与 bin/forum-migrate-linux
GIN_MODE=release ./forum-server-linux    # 生产模式：关闭 Swagger，跳过自动迁移
```

完整的上线步骤（阿里云香港 ECS 选型、安全组、Docker 安装、拉代码、环境变量、域名解析、HTTPS 与自动续期、备份与排错）见 [`docs/10-部署上线-阿里云香港ECS与域名.md`](docs/10-部署上线-阿里云香港ECS与域名.md)，配套文件在 `deploy/prod/`（生产 compose 叠加层、两份 Nginx 配置、共用 locations、MySQL 小内存参数）。

同域部署时 `VITE_API_BASE_URL` 保持默认 `/api`，后端的 `cors.allowed_origins` 留空即可。前后端拆到不同域名时，才需要放行完整 Origin 并把 `VITE_API_BASE_URL` 改成完整地址。样例只覆盖 Nginx 这一层，进程守护、证书申请与数据库备份仍需另外补；告警规则见上一节的 `deploy/alerts.yml`。

## 容器化部署（可选）

不想在服务器上装 Go、Node、MySQL、Redis 时，用 `docker-compose.yml` 一次起齐五个部分：`mysql`、`redis`、`migrate`（一次性建表）、`api`、`web`（nginx 托管静态文件并反代 `/api`）。

```bash
cp .env.example .env                  # 填数据库密码、Redis 密码、JWT_SECRET
docker compose build
docker compose run --rm migrate       # 首次部署建表，之后改表结构再跑一次
docker compose up -d                  # 打开 http://127.0.0.1:8088
```

几个刻意的选择：

- **迁移是独立的一次性任务**，不写进 api 的启动脚本。多副本同时启动各跑一遍 `AutoMigrate` 会互相等锁，表可能建到一半失败。`api` 依赖 `migrate` 的 `service_completed_successfully`，顺序由 compose 保证。
- **api 不映射宿主机端口**，只有 `web` 容器能通过内部网络访问 8080。少一个能被公网扫到的入口。
- **子网写死成 `172.28.0.0/16`**，与 `config/config.docker.yaml` 的 `server.trusted_proxies` 对应。让 docker 随机选网段的话，后端只能看到 nginx 一个 IP，按 IP 限流会退化成全局桶。
- **镜像里不放密钥**。`config/config.docker.yaml` 的 `password`、`jwt.secret` 全留空，由 compose 从 `.env` 注入环境变量覆盖（viper 的 `MYSQL_PASSWORD` / `REDIS_PASSWORD` / `JWT_SECRET`）。
- **Redis 开了 `appendonly`**。refresh token 的登记表在 Redis 里，进程重启丢持久化会让所有人被迫重新登录；缓存丢了无所谓，登录态丢了很刺眼。

镜像在本机没有验证过（这台机器没装 Docker），首次在服务器上跑请留意构建日志。

服务器是 2 核 2G 时别在上面构建：`bash scripts/pack-linux.sh --tar` 会在本机交叉编译出 Linux 产物与前端 `dist`，打成一个几十 MB 的包，传上去之后服务器只做 `COPY`。完整命令（含 scp 与回退）见部署文档 13.5 节。

打包前必须先提交：镜像标签是 `月日-时分-commit短哈希`（如 `1118-0228-7d2ac1f`），工作区有未提交改动时脚本会直接拒绝，避免线上镜像的标签对不回源码。仅验证流程时可加 `--allow-dirty`。

## 图片上传与省流量

头像只走上传一条路：`POST /api/uploads/avatar`（multipart 的 `file` 字段，需登录）。

不提供「填图片地址」的入口是刻意的：贴外链看着灵活，但大多数人手边没有可外链的图床，填进去的多半是防盗链地址（带 Referer 直接 403），最后都变成「头像怎么没变」的困惑。后端的 `avatar_url` 仍然接受 http/https 绝对地址，那是为了老数据还能正常保存——前端不再给填的入口。

服务端收下图片后会**重编码**再落盘，不是原样存：

| 处理 | 默认值 | 为什么 |
|---|---|---|
| 按 EXIF 方向摆正 | — | 手机横拍的照片像素是躺着的，方向只写在 EXIF 里。重编码会丢这个标签，不摆正的话用户再也没法转回来 |
| 缩到最长边 | 256 px | 页面上最大的头像是个人主页的 76 px，三倍屏 228 px。原图存下来，每次浏览都要多传几十倍没人会看的像素 |
| 重编码 | JPEG q82 / 保留 PNG | 一张 267 KB 的 860 px 照片压完是 **16 KB**，约 16 倍 |
| 文件名 | 内容的 sha256 前 16 位 | 同一张图重复上传只有一个文件；内容一变文件名就变，URL 天然带版本 |

透明处理按面积判断：透明像素少于 1% 的图（截图、修图软件导出常见）先合成到白底再压 JPEG，真正大面积透明的（抠图头像、logo）保留 PNG。不这么分的话，要么多付三四倍体积，要么把透明区域压成黑块。

省流量靠三件事叠加，缺一件效果就掉一半：

1. **上传即压缩** 把单张图从几百 KB 降到十几 KB；
2. **URL 带内容哈希** 让「缓存一年」是安全的——图变了地址就变，不会有人看到旧头像；
3. **Nginx 直接吐文件** 并加 `Cache-Control: public, max-age=31536000, immutable`（见 `deploy/prod/locations.conf`），回访用户一个字节都不再请求。

第 2 条是关键：没有内容哈希就只能用短缓存或主动刷新，前者省不下流量，后者要写一套清理逻辑还会漏。文件名现在是内容哈希，所以这两件事都不需要。

参数在 `config.yaml` 的 `upload` 段：`max_bytes`（原始文件上限，默认 5 MB）、`max_dimension`、`jpeg_quality`、`max_pixels`（解码前的像素上限，挡解压炸弹）、`dir`（落盘目录，容器里必须挂数据卷）。限流单独一个桶（突发 5 次、每 10 秒补 1 次），不占发帖点赞的额度。

上传目录是**用户数据**，不在代码里：`uploads/` 已加进 `.gitignore`，部署时要挂出来并纳入备份（见部署文档第 13 节）。

## 备份与恢复

```bash
make backup                  # 等价于 bash scripts/backup.sh，产物在 backups/
KEEP_DAYS=30 make backup     # 改保留天数
BACKUP_MODE=docker make backup   # 从 compose 的 mysql 容器里导
```

脚本用 `mysqldump --single-transaction` 开一致性快照，不锁表，备份期间用户发布文章不会被挡住。落盘后做两道校验：gzip 能解开且文件不小于 1KB，解压后能搜到 `CREATE TABLE`。第二道是为了抓「连上了但连错库」这类问题，那种 dump 只有头部注释，`gzip -t` 照样通过。只有备份成功后才会清理过期文件，避免备份天天失败还把旧档删光。

恢复：

```bash
gunzip -c backups/forum-20261117-001045.sql.gz | mysql -uroot -p forum
```

Redis 不在备份范围内。除了 refresh token 登记表，里面全是可重建的缓存、计数与限流桶；丢登记表的代价是所有人重新登录一次，比丢文章轻。要连 Redis 一起备，把 `redis-data` 卷定期打包即可。

## 注册和登录接口（当前可用）

请求前先按「本机运行」配置并启动服务。注册密码至少 8 字节，示例使用 `12345678`：

```powershell
curl.exe -X POST http://localhost:8080/api/register `
  -H "Content-Type: application/json" `
  -d '{\"username\":\"admin\",\"password\":\"12345678\"}'

curl.exe -X POST http://localhost:8080/api/login `
  -H "Content-Type: application/json" `
  -d '{\"username\":\"admin\",\"password\":\"12345678\"}'
```

登录成功后从响应中复制 `token`。受保护接口的请求头格式为 `Authorization: Bearer <token>`。当前受保护路由组里还没有业务接口，所以拿到 token 只能证明签发成功，端到端鉴权要等第 2 周挂上文章接口后验证。

文章接口要等第 2 周实现后才可调用，例如：

```powershell
curl.exe -X POST http://localhost:8080/api/articles `
  -H "Authorization: Bearer <token>" `
  -H "Content-Type: application/json" `
  -d '{\"title\":\"你好\",\"content\":\"第一篇\"}'
```

当前 `pkg/response.OK` 固定返回 HTTP 200。00 章 API 合同表里的 201 与 204 属于目标状态，落地前要先扩展响应包，详见 00 章。

`POST /api/articles` 与 `DELETE /api/articles/:id` 的可复制实现见 03 章 6.6，含逐行注释与带伪造 `user_id` 的验收脚本。`POST /api/articles/:id/comments` 与 `DELETE /api/comments/:id` 的完整 service、handler 代码与验收脚本见 03 章 6.7。两套接口都已落盘到仓库源码，命名口径见 03 章 1.2。

## 安全与配置

`config/config.yaml` 不在 Git 里（`.gitignore` 已忽略），仓库只跟踪 `config/config.example.yaml` 与 `config/config.production.yaml` 两个模板，两者都不含真实凭据。本机配置里的 MySQL 密码、JWT secret、`llm.api_key` 都属于机密，只能写在这份未跟踪的文件里，或者用环境变量给。管理类接口走 `admin.user_ids` 白名单（创建抢楼活动的 `POST /api/floor-events`，举报处置的 `GET /api/admin/reports` 与 `PUT /api/admin/reports/:id`、标签改名合并的 `GET /api/admin/tags` 与 `PUT /api/admin/tags/:id`），填的是用户自增 id；同一份名单还决定 `/users/me` 返回的 `is_admin`，前端靠它决定菜单里出不出现管理入口；留空表示没人能调，这是刻意的安全默认。加载器支持 `CONFIG_PATH`、`JWT_SECRET`、`MYSQL_PASSWORD`、`REDIS_PASSWORD`、`LLM_API_KEY` 等环境变量（viper 把 `llm.api_key` 映射成 `LLM_API_KEY`）。提交前再确认一遍没有真实凭据。PowerShell 接口示例使用 `curl.exe`，避免命中 `curl` 别名。Windows 下把内联 JSON 传给原生程序时，单引号里的双引号会被命令行解析吃掉，所以本页写成 `\"`，实测能保住完整 JSON；也可以把 JSON 存成文件后用 `-d @body.json`。Git Bash 里不需要转义，02 章的 bash 示例保持单引号写法。

## 学习路线

0. [`docs/00-完整项目实施总图.md`](docs/00-完整项目实施总图.md)：目标范围、业务规则、讲解模板、全局勘误、各阶段验收和上线准备门槛
1. `docs/01-项目介绍与技术栈.md`：项目介绍和当前状态
2. `docs/02-第1周-地基-登录注册与数据库.md`：配置、数据库、用户和鉴权
3. `docs/03-第2周-核心业务-文章评论与Redis缓存.md`：文章 CRUD 逐行讲解、评论和基础缓存
3a. [`docs/03a-第2周进阶-Redis缓存工程化加固.md`](docs/03a-第2周进阶-Redis缓存工程化加固.md)：缓存雪崩、穿透、击穿的设计推演与完整实现，含 TTL 抖动、空值缓存、请求合并、降级与验收
4. `docs/04-第3周-亮点功能-点赞热榜与限流.md`：点赞、热榜、限流的十步跟做实现，骨架先行，每个功能分两到三版逐步改进；第 10 步把点赞数接进文章详情并闭合写路径缓存失效
4a. [`docs/04a-第3周进阶-异步消息与站内通知.md`](docs/04a-第3周进阶-异步消息与站内通知.md)：Transactional Outbox、Redis Stream 消费组、幂等通知、worker 优雅关闭
4b. [`docs/04b-第3周进阶-Swagger-API文档与契约.md`](docs/04b-第3周进阶-Swagger-API文档与契约.md)：swaggo 注解、Swagger UI、契约防漂移
4c. [`docs/04c-第3周进阶-WebSocket实时通知.md`](docs/04c-第3周进阶-WebSocket实时通知.md)：连接登记、读写分离、心跳超时、慢客户端剔除、在线统计
4d. [`docs/04d-第3周进阶-敏感词过滤与前缀树.md`](docs/04d-第3周进阶-敏感词过滤与前缀树.md)：前缀树构建与最长匹配、等长替换、词库加载、性能实测
4e. [`docs/04e-第3周进阶-全文搜索与倒排索引.md`](docs/04e-第3周进阶-全文搜索与倒排索引.md)：倒排索引、第二个消费组、异步任务等待、瘦索引回查与顺序还原
5. `docs/05-第4周-打磨-测试与面试准备.md`：测试与上线准备
6. `docs/06-简历包装与面试讲稿.md`：完成并验证后再整理简历
7. [`docs/07-核心链路学习笔记与面试问答.md`](docs/07-核心链路学习笔记与面试问答.md)：从 main 到 MySQL 与 Redis 的纵向串讲，表结构与索引实测、缓存与消息一致性、按主题整理的面试问答

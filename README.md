# 社区论坛后端学习项目

循序渐进的 Go 后端学习项目。学习目标是从可运行的注册登录服务逐步扩展到具备测试、配置、安全、部署和运维基础的论坛 API。每个阶段按步骤实现、运行、验收。代码示例会标明属于完整文件、增量修改或概念伪代码。没有实现并验证的功能不算项目已具备。

## 当前代码状态

- 已有：Gin 启动入口、Viper 配置读取、MySQL 连接与 GORM 自动迁移、Redis 连接初始化、用户模型、注册与登录业务逻辑及 HTTP handler、JWT Manager、中间件、路由接线。
- 文章与评论链路：service 方法、`internal/handler/dto.go` 的响应结构体、`article.go`、`comment.go`、`errors.go`、`paging.go` 均已落盘，`internal/router/router.go` 已注册列表、详情、评论列表等读路由与 JWT 保护的写路由。文章与评论的四个写接口都已在仓库源码实现，service 与 handler 的方法名统一成动词式（`Create` / `List` / `Detail` / `Delete`），对照表见 03 章 1.2。判断完成度用「路由已注册且验收通过」，别用「文件已存在」。
- `main.go` 已调用 `config.InitRedis`，`router.New` 的签名是 `New(db *gorm.DB, jwtManager *jwt.Manager, rdb *redis.Client)`。Redis 是启动硬依赖，连不上会直接退出。三层装配顺序见 03 章 7.2。仓库里没有 `/ping` 路由。
- 缓存已落地：详情接口的 Cache Aside 读路径（03 章 7.3）、写路径缓存失效（`Create` 与 `Delete` 在数据库写入或事务提交成功后调用 `InvalidateDetailCache`）、`InitRedis` 在 `Ping` 失败分支调用 `rdb.Close()`。旧版 README、00 章与 03 章 7.6 把这三项写成「待实现」或「还缺」，2026-11-07 核对源码后已更正。
- 已落盘：四个写接口（03 章 6.6、6.7）、缓存工程化加固（TTL 抖动、空值缓存、singleflight 请求合并、降级超时与连接池、原子计数器，见 `internal/service/article_cache.go` 与 03a 章）。未完成：第 3 周点赞、热榜、限流的源码，第 3 周源码由你按 04 章十步跟做逐步落盘，状态以工作树为准，落盘并验收后才算具备；生产部署所需的迁移、服务管理、监控与备份流程也未做。文档里的设计要经过实现与验收才算完成。
- `docs/03a-第2周进阶-Redis缓存工程化加固.md` 是缓存加固的独立进阶专题，接在 03 章第 7 节之后，不占用周次；第 3 周仍是 04 章，第 4 周仍是 05 章。它同时修正了前面章节的进度快照。它记录的 `gorm.ErrRecordNotFound` 映射缺陷现已在源码修好，查不存在的文章与删后 GET 详情都返回 404。
- `docs/04a-第3周进阶-异步消息与站内通知.md` 与 `docs/04b-第3周进阶-Swagger-API文档与契约.md` 是第 3 周之后的两个进阶专题，同样不占周次。前者用 Transactional Outbox 加 Redis Stream 做点赞、评论后的站内通知，后者用 swaggo 从 handler 注解生成 Swagger 2.0 文档与可交互 UI。两章只给可落盘代码与验收步骤，源码状态以工作树为准。
- `docs/04c-第3周进阶-WebSocket实时通知.md`、`docs/04d-第3周进阶-敏感词过滤与前缀树.md`、`docs/04e-第3周进阶-全文搜索与倒排索引.md` 是同一系列的三个进阶专题，接在 04b 之后，同样不占周次。04c 把异步通知升级成服务端主动推送，含连接登记、心跳、慢客户端剔除与关停处理；04d 自己实现前缀树做敏感词替换，附 benchmark 实测与朴素做法的对比结论；04e 用第二个消费组把文章同步进 Meilisearch，实现关键词搜索与相关性排序。三章都只给可落盘代码，源码状态以工作树为准。
- 04a 章会改动 `cmd/server/main.go`，加入后台 worker 装配、`signal.NotifyContext` 与 `WaitGroup` 优雅关闭；04b 章会新增 `api/swagger/` 生成目录，别让它落进 `docs/`。04 章写的「main.go 零改动」只对那十步成立。04c 章把 `router.New` 的签名从三个位置参数改成 `New(Deps)`，`main.go` 的调用点与 `NewNotificationWorker` 的构造都要跟着改，04d、04e 依次往 `Deps` 里加 `Filter` 与 `Search`，所以这三章必须按顺序做。
- 对外响应结构体集中在 `internal/handler/dto.go`，与 handler 同包。注册与登录不再直接序列化 `model.User`，响应 JSON 形状与改动前一致。取舍依据见 02 章 8.0。
- `go.mod` 声明 `go 1.26.0`，本机实测 `go1.27.3`。先运行 `go version` 确认本机版本满足要求。旧版 README 写的 1.25.0 已作废。
- `go build ./...` 与 `go test ./...` 当前可通过，测试文件以工作树里的 `*_test.go` 为准（03a 章有 `internal/service/article_cache_test.go`，04 章 4.10 会新增 `internal/handler/dto_test.go`）。仓库里多处源码尚未 `gofmt`，用 `gofmt -l ./cmd ./internal ./pkg` 查差异，落地后对本次改过的文件跑 `gofmt -w`。

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
cmd/server/       程序入口
config/           YAML 配置
internal/config/   配置结构和数据库、Redis 初始化
internal/model/   GORM 数据模型
internal/handler/ HTTP 请求处理（当前有用户注册与登录）
internal/service/ 业务逻辑（当前有用户注册登录与文章基础代码，直接使用 GORM）
internal/repository/ 目标数据访问层（当前 user.go 仅为空壳）
internal/middleware/ HTTP 中间件（JWT 鉴权）
internal/router/  路由组装
pkg/              JWT、统一响应等公共代码
docs/             分阶段学习文档
api/swagger/     Swagger 生成物（04b 章，按文档生成）
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

`config/config.yaml` 是被 Git 跟踪的示例配置，里面已有一份占位密码和示例 JWT secret。不可把真实数据库密码或生产密钥写进去。`.gitignore` 忽略 `.env` 与 `config/config.local.yaml`，不会忽略默认配置。当前加载器支持 `CONFIG_PATH`、`JWT_SECRET` 和 `MYSQL_PASSWORD` 环境变量，启动时用环境变量或未跟踪的本地配置提供真实值。提交前再确认一遍没有真实凭据。PowerShell 接口示例使用 `curl.exe`，避免命中 `curl` 别名。Windows 下把内联 JSON 传给原生程序时，单引号里的双引号会被命令行解析吃掉，所以本页写成 `\"`，实测能保住完整 JSON；也可以把 JSON 存成文件后用 `-d @body.json`。Git Bash 里不需要转义，02 章的 bash 示例保持单引号写法。

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

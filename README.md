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
- 测试地基已落地：`internal/testutil` 提供真实 MySQL（`TEST_MYSQL_DSN` 开关）与 miniredis 两套夹具，`internal/comment` 已作为示范补齐 service 与 handler 测试，包覆盖率从 0% 到 81%（总覆盖率 21.7% → 24.4%）。CI 增加了 MySQL service 与 `-race`。其余依赖数据库的包仍待补，用法见「测试」一节。

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
go test -count=1 ./...
```

```powershell
# PowerShell
$env:TEST_MYSQL_DSN="root:你的密码@tcp(127.0.0.1:3306)/forum_test?parseTime=true&loc=Local&charset=utf8mb4"
go test -count=1 ./...
```

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

同域部署时 `VITE_API_BASE_URL` 保持默认 `/api`，后端的 `cors.allowed_origins` 留空即可。前后端拆到不同域名时，才需要放行完整 Origin 并把 `VITE_API_BASE_URL` 改成完整地址。样例只覆盖 Nginx 这一层，进程守护、证书申请与数据库备份仍需另外补；告警规则见上一节的 `deploy/alerts.yml`。

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

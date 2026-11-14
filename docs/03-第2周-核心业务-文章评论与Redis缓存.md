# 03 · 第 2 周：文章 CRUD、评论与 Redis 缓存

> 本章目标：把「发布、列表、详情、删除」四个动作从 HTTP 请求一路追到 SQL，能讲清每一行为什么这样写。随后补上评论和 Redis 缓存。
>
> 本章每个 Go 代码块都标了类型。**完整文件**可以直接保存，**增量修改**要按说明替换指定函数，**概念伪代码**只解释思路，不能直接编译。
>
> 阅读顺序：第 0 节建立心智模型，第 1 节对齐仓库现状，第 2 到 5 节逐行学 CRUD，第 7 节之后才碰缓存。先把 MySQL 版本跑通，再加 Redis。

---

## 0. 先建立心智模型

一个请求完整走过这些层：

```text
GET /api/articles?page=1
  → router.New 注册的路由
  → middleware（列表公开，写接口需要 JWTAuth）
  → handler.List（解析 query、校验范围、调用 service、组装 JSON）
  → service.List（业务规则、分页计算、包装错误）
  → GORM（生成 SQL）
  → MySQL
```

CRUD 与 HTTP、SQL 的对应关系：

| 动作 | HTTP | 数据库动作 | 本章函数 | 当前成功状态 |
| --- | --- | --- | --- | --- |
| C | `POST /api/articles` | `INSERT` | `Create` | 200 |
| R | `GET /api/articles` | `SELECT count(*)` 与 `SELECT ... LIMIT/OFFSET` | `List` | 200 |
| R | `GET /api/articles/:id` | `SELECT ... WHERE id = ?` | `Detail` | 200 |
| D | `DELETE /api/articles/:id` | `DELETE ... WHERE id = ? AND user_id = ?` | `Delete` | 200 |
| 评论 C | `POST /api/articles/:id/comments` | `INSERT` | `CommentService.Create` | 200 |

**状态码口径（全教程统一）**：`docs/00` 的 API 合同表写的是目标状态，创建用 201、删除用 204。当前 `pkg/response.OK` 固定发 HTTP 200。要落地目标合同，先给 `pkg/response` 增加 `Created` 与 `NoContent`，再同步改 handler、测试和验收脚本。在扩展响应包之前，所有验收按 200 判定。旧版文档里 00 章写 201、02 章写 200、03 章 Postman 又期望 201，三处冲突已按这条规则统一。

### 为什么要分层

| 层 | 认识什么 | 不认识什么 |
| --- | --- | --- |
| handler | query、JSON、HTTP 状态码、`gin.Context` | SQL、GORM、表结构 |
| service | 业务规则、错误类型、`context.Context` | `gin.Context`、HTTP 状态码 |
| repository（目标） | SQL、Redis 命令 | 业务规则 |

分层带来三个可验证的好处。service 可以脱离 HTTP 做单元测试。改数据库字段时业务规则不用动。改响应 JSON 时 SQL 不用动。代价是文件数量变多，对一个小接口看起来绕。学习阶段值得付这个成本，因为面试要问的正是「为什么这样切」。

`ctx` 从 `c.Request.Context()` 一路传到 `WithContext(ctx)`，用户取消请求时数据库查询跟着取消。Go 官方建议把 `context.Context` 作为函数第一个参数，并沿调用链显式传递 [来源: https://go.dev/wiki/CodeReviewComments]。

---

## 1. 开工前：把仓库现状对齐

本表是 2026-11-07 的仓库快照。跟着文档往下写，文件会变，进入每一步前重新跑一次基线命令核对，别把快照当成永久事实。

**读法先说清**：第 6 节按「无 Redis 的 MySQL 版本」当演进起点讲解，`router.New` 是两参数，handler 是 501 占位。仓库当前源码已经走到 7.2，`main.go` 调了 `InitRedis`，`router.New` 已是三参数，读接口与四个写接口都已填了实现。6.1 的骨架属于「已经走过的那一步」，不要照着它把当前源码改回去。看到文档里写「当前只有注册登录」「仍是 501」时，按对应小节的时间点理解；6.6 与 6.7 之后的快照才是现状。03a 章的加固代码也已落盘，`Detail` 以 `internal/service/article.go` 当前版本为准。

| 事实 | 证据 | 对本周的影响 |
| --- | --- | --- |
| `go.mod` 声明 `go 1.26.0`，本机为 `go1.27.3` | `go.mod`、`go version` | 旧文档写的 1.25.0 作废 |
| `internal/service/article.go` 含 `Create`、`List`、`Detail`、`Delete`，都带 `ctx`；结构体已有 `rdb *redis.Client` | 源码 | 7.2 第 3 步与 7.3 的缓存版 `Detail` 已落地 |
| `internal/service/comment.go` 含 `CommentService`、`NewCommentService`、`List`、`Create`、`Delete` | 源码 | 6.7 已落盘完整文件，5.1 只作最小片段对照，别新建覆盖 |
| `internal/handler/dto.go` 已含用户、文章、评论响应结构体及列表 DTO | 源码 | 3.8、6.3 给规范版对照，别重复粘贴同名类型 |
| `internal/handler/` 已有 `article.go`、`comment.go`、`errors.go`、`paging.go`；`router.go` 已注册文章与评论路由 | 源码 | 6.1 骨架属于已走过的起点；四个写接口都已实现，见 6.6 与 6.7。方法名统一口径见 1.2 |
| `main.go` 已调用 `config.InitRedis`，`router.New` 已是 `(db, jwtManager, rdb)` | 源码 | 7.2 已装配。启动前必须先起 Redis，否则 `log.Fatalf` 直接退出 |
| `internal/config/redis.go` 的 `Ping` 失败分支已调用 `rdb.Close()` | 源码 | 旧版写「仍缺」，2026-11-07 核对后更正；7.2 末尾保留这段代码作教学示例 |
| 仓库里还有 7 个源码文件尚未 `gofmt`（2026-11-07 复核） | `gofmt -l ./cmd ./internal ./pkg` | 本轮改过的 6 个文件已格式化；其余属于你自己的历史格式差异，只按需 `gofmt -w`，别整库格式化淹没 diff |
| `model.Article` 没有 `status` 字段，旧版 `applyArticleFilter` 已从源码删除 | 源码 | 见 1.1 |
| `go.mod` 已含 `go-redis v9.23.0`，缺 `golang.org/x/sync` 直接依赖 | `go.mod` | 03a 章步骤 3 才会 `go get`，本章 7.5 只列概念，不用提前装 |
| `router.go` 没有 `/ping` | 源码 | README 与 01 章的 `/ping` 说法已删除 |
| `pkg/response.OK` 固定 200 | 源码 | 见上方状态码口径 |

基线命令，动手前先跑一遍并保存输出：

```powershell
go version
go test ./...
go build ./cmd/server
```

### 1.1 一个历史隐患：`applyArticleFilter` 与不存在的 `status` 列

旧版 `internal/service/article.go` 里有过这样一段：

**概念伪代码**：下面是旧版源码的摘录，当前仓库已删除，不能运行。

```go
// 概念伪代码：旧版源码，当前仓库已删除。
func applyArticleFilter(db *gorm.DB, status int) *gorm.DB {
	return db.Where("status=?", status)
}
```

`model.Article` 没有 `status` 字段，表也没有这一列。一旦调用它，GORM 会生成 `WHERE status = ?`，MySQL 回 `Unknown column 'status'`。当时这个函数没有任何调用点，所以服务能正常编译运行。当前仓库的 `article.go` 已经重写，含 `Create`、`List`、`Detail`、`Delete` 四个方法，这个函数已不存在。本小节保留它作为教训。

这里的教训比代码本身重要。**能编译通过的函数，语义上仍可能是坏的**。真要做类似过滤，顺序是：给 `model.Article` 加字段，建索引，写版本化迁移给已有表加列，再在 `Count` 与 `Find` 两处同时使用同一个过滤条件。只改 Go 代码会让已有数据库和新安装数据库结构不一致。6.2 加外键时同样要过这一关。

### 1.2 命名口径：本轮统一成动词式

开工前仓库里同一个 service 的方法名一半带实体名、一半不带：`CreateArticle`、`ListArticles` 和 `Delete`、`Detail` 混着用；评论侧又冒出 `ListByArticle` 这种「动词 + 介词」写法。这不是审美问题，照这个趋势写到第 3 周，点赞功能会冒出 `CreateLike`、`AddLike`、`LikeCreate` 三种名字，读代码的人每次都要先猜。

**本轮已经改了源码，不只是文档约定**。统一后的规则：

| 规则 | 说明 |
| --- | --- |
| receiver 已经说明类型，方法名不重复实体名 | `articleSvc.CreateArticle()` 里的 `Article` 是冗余的。Go 官方 Code Review Comments 反对 `http.HTTPServer` 这类重复命名 [来源: https://go.dev/wiki/CodeReviewComments#package-names] |
| service 与 handler 用同一组动词 | `List`、`Detail`、`Create`、`Delete`。`handler.Create` 直接调 `svc.Create`，不用在脑子里做一次名字翻译 |
| 查询范围交给参数表达 | `CommentService.List(ctx, articleID, page, pageSize)` 里的 `articleID` 已经说明按文章过滤，不必写进方法名 |
| 私有辅助函数用动作短语 | `queryDetail`、`InvalidateDetailCache`、`parsePage`、`mapServiceError` 保持不变 |
| 常量按归属放文件 | 文章列表排序常量在 `article.go`，评论的在 `comment.go`，不混放 |

**改名对照表**：

| 层 | 旧名 | 新名 | 说明 |
| --- | --- | --- | --- |
| `ArticleService` | `CreateArticle` | `Create` | 本轮改名 |
| `ArticleService` | `ListArticles` | `List` | 本轮改名 |
| `ArticleService` | `Delete`、`Detail` | 不变 | 本来就是动词式 |
| `CommentService` | `ListByArticle` | `List` | 本轮改名，范围靠 `articleID` 参数表达 |
| `CommentService` | 不存在 | `Create`、`Delete` | 6.7 新增，直接按新规矩命名 |
| `ArticleHandler` | `List`、`Detail`、`Create`、`Delete` | 不变 | 本来就是动词式 |
| `CommentHandler` | `ListByArticle` | `List` | 本轮改名，与 `ArticleHandler.List` 对齐 |
| `CommentHandler` | `Create`、`Delete`（空方法） | 名字不变，6.7 填实现 | 名字本来就对，缺的是函数体 |
| `router.New` | `commentHandler.ListByArticle` | `commentHandler.List` | 跟着 handler 改名 |
| 常量 | `commentListOrder` 定义在 `article.go` | 移到 `comment.go` | 归属对齐 |

**DTO 不跟着改**：`createArticleReq`、`createCommentReq`、`articleResp`、`commentResp` 这些结构体同属 `handler` 包，没有 receiver，只能靠名字区分类型；它们成对出现、口径一致，不属于「不统一」。测试函数名 `TestCreateArticleReqHasNoUserIDField` 同理保留。

**改名的代价怎么兜住**：Go 是静态类型语言，漏改一个调用点会直接编译失败，`go build ./...` 会把它们全部找出来，比运行时才发现要省事。文档里凡是讲历史勘误、必须提到旧名的地方，会写成「旧版叫 `X`」并保留语境；所有代码块一律用新名。

---

## 2. Create：发布文章，逐行拆解

### 2.1 这个接口要守住的规则

1. 作者 id 来自 JWT 中间件，客户端传来的 `user_id` 一律忽略。
2. 标题与正文去首尾空白后不能为空，长度有上限。
3. 数据库写入失败要带上下文返回，客户端只收到通用提示。
4. 返回给前端的是 DTO，字段由服务端明确列出。

### 2.2 核对并规范化 `internal/service/article.go` 里的 `Create`

仓库里的 `Create` 已经是带 `ctx` 的版本，签名与下面这段一致。本轮已把 `internal/service/article.go` 跑过 `gofmt`、整理 import 分组、补齐注释，并把错误提示里的 `mustbe` 之类连写改成可读句子；方法名由旧版 `CreateArticle` 统一成 `Create`，理由见 1.2。下面是讲清每一行的规范版，与源码逐行对照后按需覆盖，别追加第二个同名函数。还有一处**有意保留的演进差异**：仓库当前版本在 `Create` 成功后多一行 `s.InvalidateDetailCache(ctx, a.ID)`，那是 7.6 才接上的缓存失效，下面这段还没写它，别把那行删掉。

**增量修改**：核对并规范化 `internal/service/article.go` 的 `Create`，与源码逐行比对后按需覆盖。

```go
// Create 发布一篇文章。作者身份由中间件提供，客户端无法指定。
func (s *ArticleService) Create(ctx context.Context, userID uint, title, content string) (*model.Article, error) {
	// TrimSpace 去掉首尾空白，避免纯空格标题通过 required 校验。
	title = strings.TrimSpace(title)
	content = strings.TrimSpace(content)

	// 业务规则放在 service，handler 只负责把 HTTP 参数翻译过来。
	if title == "" || content == "" {
		return nil, fmt.Errorf("%w: title and content are required", ErrInvalidInput)
	}
	if len(title) > 128 || len(content) > 50000 {
		return nil, fmt.Errorf("%w: title or content too long", ErrInvalidInput)
	}

	a := &model.Article{
		UserID:  userID, // 来自 JWT，绝不从请求体读取
		Title:   title,
		Content: content,
	}

	// WithContext 把请求取消信号传给数据库；%w 保留底层错误供 errors.Is 判断。
	if err := s.db.WithContext(ctx).Create(a).Error; err != nil {
		return nil, fmt.Errorf("create article for user %d: %w", userID, err)
	}
	return a, nil
}
```

同文件的 import 需要包含 `context`、`fmt`、`strings`、`gorm.io/gorm`、`forum-backend/internal/model`。`ErrInvalidInput` 需要与 `service/user.go` 里的哨兵错误共用同一个变量，不要重复定义。

### 2.3 逐行拆解

| 代码 | 这一行做了什么 | 为什么这样设计 |
| --- | --- | --- |
| `func ... (ctx context.Context, ...)` | 接收上下文，作为第一个参数 | 请求取消和超时能传到数据库 [来源: https://go.dev/wiki/CodeReviewComments] |
| `title = strings.TrimSpace(title)` | 去首尾空白 | Gin 的 `binding:"required"` 只检查非空字符串，`"   "` 会通过 |
| `if title == "" ...` | 拒绝空标题与空正文 | 数据库 `not null` 拦不住空字符串 |
| `if len(title) > 128 ...` | 限制长度 | 与 `gorm:"size:128"` 和请求体上限保持一致，避免数据库报错后才发现问题 |
| `a := &model.Article{...}` | 组装待写入对象 | 字段含义集中在一处，便于检查 |
| `UserID: userID` | 写入作者 | 值来自 token，客户端伪造无效 |
| `s.db.WithContext(ctx).Create(a)` | 生成并执行 INSERT | GORM 会回填自增 ID 与时间戳 |
| `fmt.Errorf("...: %w", err)` | 包装错误 | 日志能看出是哪一步失败，同时保留原始错误链 |
| `return a, nil` | 返回带 ID 的对象 | handler 可以用它组装响应 |

### 2.4 四个设计决策

**作者从 token 取。** 若接受请求体里的 `user_id`，任何人都能冒充别人发帖。中间件把 `userID` 放进 Gin context，handler 用 `c.GetUint("userID")` 读出，再传给 service。

**校验放两层。** handler 负责「类型对不对」，例如 JSON 能否解析、`page` 是否整数。service 负责「业务上合不合规」，例如内容是否为空、是否超长。仅靠 handler 的 `binding` 标签无法覆盖空白字符串，仅靠数据库约束会把 400 错误变成 500。

**错误分两类。** 预期内的客户端错误用哨兵错误 `ErrInvalidInput`，handler 映射成 400。数据库故障属于服务端错误，记日志并返回通用 500。把 SQL 错误文本直接发给客户端会泄露表结构。

**响应不直接吐 model。** `model.Article` 带 `User User` 关联字段，序列化时会把整个用户对象带出去。用 DTO 明确列出允许公开的字段，见 3.8。

### 2.5 常见错误对照

| 写法 | 后果 | 正确做法 |
| --- | --- | --- |
| `UserID: req.UserID` | 任意冒充作者 | 只用 `c.GetUint("userID")` |
| `s.db.Create(a)` 不检查 `.Error` | 写入失败仍返回成功 | 每次都判断并包装 |
| `return nil, err` 裸返回 | 日志里看不出失败阶段 | `fmt.Errorf("create article ...: %w", err)` |
| `response.OK(c, a)` 直接返回 model | 字段形状被数据库结构绑死，关联对象可能外泄 | 转 DTO |

### 2.6 验证

```powershell
go test ./...
go build ./cmd/server
```

接口联调要等第 6 节把路由接好。先用第 05 章的表驱动测试覆盖 `title` 为空、纯空白、超长三种输入。

---

## 3. Read：分页列表，逐行拆解

### 3.1 一个列表接口要回答两个问题

前端分页器需要「一共多少条」和「当前这一页的 10 条」。这对应两条 SQL，一条 `SELECT count(*)`，一条带 `ORDER BY`、`LIMIT`、`OFFSET` 的查询。两条 SQL 的过滤条件必须完全一致，否则会出现「显示共 100 条，却能翻出 150 条」。

### 3.2 核对并规范化 `internal/service/article.go` 里的 `List`

仓库当前签名是 `List(ctx context.Context, page, pageSize int) ([]*model.Article, int64, error)`，由旧版 `ListArticles` 统一改名而来，见 1.2。旧文档写的无 `ctx` 版本已经过时。本轮已把源码 `gofmt`、把错误提示规范化、把排序换成 `articleListOrder` 常量，所以下面这段的校验与空切片初始化都与源码一致。只剩一处**有意保留的讲解差异**：下面把 `Order("created_at DESC, id DESC")` 写成字面量，是为了看清两个排序键；仓库源码用的是同值的 `articleListOrder` 常量，理由见 6.5 A。替换时把旧函数整体删掉，避免同名函数重复定义。

**增量修改**：核对并规范化 `internal/service/article.go` 的 `List`。

```go
// List 返回分页文章列表与符合条件的总数。page 从 1 开始。
func (s *ArticleService) List(ctx context.Context, page, pageSize int) ([]*model.Article, int64, error) {
	// service 做兜底校验，防止将来有内部调用绕过 handler 的参数检查。
	if page < 1 {
		return nil, 0, fmt.Errorf("%w: page must be >= 1", ErrInvalidInput)
	}
	if pageSize < 1 || pageSize > 50 {
		return nil, 0, fmt.Errorf("%w: page_size must be between 1 and 50", ErrInvalidInput)
	}

	var total int64
	// Count 与 Find 各自从 s.db 开始一条新链，避免复用带状态的中间变量。
	if err := s.db.WithContext(ctx).Model(&model.Article{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count articles: %w", err)
	}

	// 空表直接返回空切片。JSON 里是 []，前端读 .length 安全。
	if total == 0 {
		return []*model.Article{}, 0, nil
	}

	// 初始化成空切片。请求页越界时 Find 不填充它，响应仍是 []。
	articles := make([]*model.Article, 0)
	err := s.db.WithContext(ctx).
		Preload("User").                  // 一次额外查询带出作者，避免逐条查
		Order("created_at DESC, id DESC"). // 同秒发布时用 id 决定先后顺序
		Offset((page - 1) * pageSize).     // 跳过前面几页
		Limit(pageSize).                   // 每页最多 50 条
		Find(&articles).Error
	if err != nil {
		return nil, total, fmt.Errorf("list articles page=%d size=%d: %w", page, pageSize, err)
	}
	return articles, total, nil
}
```

同文件 import 需要 `context`、`fmt`、`gorm.io/gorm`、`forum-backend/internal/model`。

### 3.3 逐行拆解

| 代码 | 作用 | 为什么这样设计 |
| --- | --- | --- |
| `if page < 1` | 拒绝非法页码 | 负 Offset 会被 GORM 静默吞掉，详见 3.6 |
| `if pageSize < 1 \|\| pageSize > 50` | 限制每页条数 | 不设上限时，一次请求可让数据库和进程内存同时吃紧 |
| `var total int64` | 承接 count 结果 | `Count` 要求 `*int64` |
| `Model(&model.Article{})` | 指定统计的表 | 不写 `Model` 时 GORM 从 `Dest` 推断，容易出错 |
| `Count(&total).Error` | 执行第一条 SQL | 总数与列表分开查，列表查询才能带 `LIMIT` |
| `if total == 0` | 提前返回 | 空表时省掉第二条查询 |
| `[]*model.Article{}` | 返回空切片 | `nil` 序列化成 `null`，前端 `res.data.list.length` 会报错 |
| `Preload("User")` | 关联查询作者 | 避免 N+1。名字必须与模型字段名完全一致 |
| `Order("created_at DESC, id DESC")` | 稳定排序 | 无 `ORDER BY` 时数据库返回顺序无保证，翻页可能重复或漏数据 |
| `Offset((page - 1) * pageSize)` | 计算跳过行数 | page 从 1 开始，第 1 页跳过 0 行 |
| `Limit(pageSize)` | 限制返回行数 | 与 handler 的上限保持一致 |
| `Find(&articles)` | 执行第二条 SQL 并填充 | 目标类型与返回类型必须一致，见 3.4 |
| `fmt.Errorf("...: %w", err)` | 包装错误 | 日志能区分 count 阶段与 find 阶段 |

关于 `Preload` 的措辞要分清层次。GORM 官方把这一节叫 Preloading (Eager Loading)，原文写的是 "GORM allows eager loading relations in other SQL with Preload"，并说明 "Preload loads the association data in a separate query" [来源: https://gorm.io/docs/preload.html]。官方页面里没有出现 N+1 这个术语，「Preload 解决 N+1」是从「逐条查关联」推导出的工程说法。官方示例连续调用三个 `Preload` 会发出 4 条 SQL，所以「Preload 永远是 2 条 SQL」属于过度简化。

### 3.4 值切片与指针切片：先把「编译错误」这件事说准

Go 不会自动把 `[]model.Article` 转换成 `[]*model.Article`。**声明的切片类型与函数返回类型必须一致**。下面两种写法都能编译：

**概念伪代码**：两种签名只用来说明切片类型差异，`...` 不是合法 Go 代码，不能运行。

```go
// 写法 A，值切片
func ListA(...) ([]model.Article, int64, error) {
	var articles []model.Article
	// ... Find(&articles)
	return articles, total, err
}

// 写法 B，指针切片
func ListB(...) ([]*model.Article, int64, error) {
	var articles []*model.Article
	// ... Find(&articles)
	return articles, total, err
}
```

真正会编译失败的是交叉搭配，例如声明 `var articles []model.Article` 却声明返回 `[]*model.Article`。

对照仓库现状：`internal/service/article.go` 用的是写法 B，声明与返回都是 `[]*model.Article`，可以编译。旧版 03 章给的是写法 A，声明与返回都是 `[]model.Article`，同样可以编译。**旧文档的问题在于方法名和返回类型与当时的仓库对不上**（旧版文档写 `List` 返回 `[]model.Article`，旧版仓库写 `ListArticles` 返回 `[]*model.Article`）。新手照文档复制进已有文件，会得到两个不同名的函数，或者改了返回值却没有改调用点。本轮把仓库方法统一成 `ArticleService.List`，返回类型仍是 `[]*model.Article`，对照表见 1.2。

GORM 的 `Find` 对 `*[]model.Article` 与 `*[]*model.Article` 都支持，它用反射按元素类型创建并填充对象。3.5 节的 DryRun 探针验证过两种目标都能解析通过。

**辨伪：关于方案 B 的「指针失效」说法。** 有一种讲法建议先查到值切片再逐个取地址，同时提醒「切片扩容会导致指针失效」。这个提醒在 Go 里不成立。`&articles[i]` 指向底层数组的第 i 个元素。切片扩容时会分配新数组，旧数组因为仍被这些指针引用而继续存活，指针保持有效。后果是这些指针看不到之后追加到新数组里的元素，属于数据不同步，不会产生悬空指针，也不会崩溃。而且 `Find` 一次性填完数据，取地址之后不再扩容，所以这种转换写法本身安全，只是多一次循环。

**本项目怎么选**：统一用指针切片。理由有三条，减少大结构体复制、与 GORM 官方示例一致、`nil` 元素可以表达缺失。这属于团队风格选择，把它讲成「唯一正确写法」会误导新手。

### 3.5 GORM 链为什么不能随便复用

GORM 官方把方法分成三类 [来源: https://gorm.io/docs/method_chaining.html]：

| 类别 | 例子 | 行为 |
| --- | --- | --- |
| Chain Methods | `Where`、`Preload`、`Order`、`Limit` | 修改当前 `Statement` 的条件 |
| Finisher Methods | `Find`、`First`、`Count`、`Create`、`Delete` | 生成并执行 SQL |
| New Session Methods | `Session`、`WithContext`、`Debug` | 返回可安全复用的 `*gorm.DB` |

官方原文写道，链方法或终结方法返回的已初始化 `*gorm.DB` 不宜复用，因为它可能带上前一次操作的条件，造成 SQL 污染 [来源: https://gorm.io/docs/method_chaining.html]。官方给的坏例子：

**概念伪代码**：GORM 官方文档里的链复用反例，变量未定义，不能运行。

```go
queryDB := DB.Where("name = ?", "jinzhu")

queryDB.Where("age > ?", 10).First(&user)
// SELECT * FROM users WHERE name = "jinzhu" AND age > 10

queryDB.Where("age > ?", 20).First(&user2)
// 条件累积：WHERE name = "jinzhu" AND age > 10 AND age > 20
```

从 `gorm.Open` 得到的**根 db** 可以安全复用。每条从根 db 开始的链都会创建新的 `Statement`。所以下面这种写法安全：

**概念伪代码**：安全写法的示意，省略了变量声明，不能单独运行。

```go
s.db.Model(&model.Article{}).Count(&total)          // 第一条链
s.db.Preload("User").Find(&articles)                // 第二条链，全新的 Statement
```

想复用带条件的 `base` 时，用 New Session Method 隔离 [来源: https://gorm.io/docs/session.html]：

**概念伪代码**：New Session 的示意片段，只用来观察 SQL 条件是否叠加，不要直接放进项目。

```go
// 下面几行位于 service 方法内部，补齐变量声明便于直接复制观察。
var total int64
var articles []*model.Article
// 用模型里真实存在的 user_id 列，避开 1.1 提到的 status 隐患。
base := s.db.WithContext(ctx).Model(&model.Article{}).Where("user_id = ?", 1001)

base.Session(&gorm.Session{}).Count(&total)
base.Session(&gorm.Session{}).Preload("User").Find(&articles)
```

`Session(&gorm.Session{})` 会复制当前条件给每条新链。`Session(&gorm.Session{NewDB: true})` 会清空条件，官方 NewDB 示例明确显示原有 `WHERE` 消失 [来源: https://gorm.io/docs/session.html]。两者只差一个字段，行为完全不同。

**实测记录与它的局限。** 本章用 GORM v1.31.2 的 DryRun 模式做过一组探针，观察生成的 SQL：

| 场景 | 生成的 SQL |
| --- | --- |
| 同一 `base` 先 `Count` 再 `Find` | `Find` 仍是 `SELECT count(*) FROM articles WHERE status = ?` |
| 两次都从根 `db` 开始 | `SELECT count(*)` 与 `SELECT *`，都带 `Where` |
| `base.Session(&gorm.Session{})` 各执行一次 | `SELECT count(*)` 与 `SELECT *`，都带 `Where` |
| `base.Session(&gorm.Session{NewDB: true})` | `SELECT *`，`Where` 消失 |

想自己复现这组结果，可以把下面这段放进临时 `main.go` 运行。DryRun 只生成 SQL，不连数据库，需要项目里已有的 GORM 与 MySQL 驱动。

**概念伪代码**：临时 `main.go` 的 DryRun 片段，只用来观察生成的 SQL，不要放进生产代码。

```go
// 概念伪代码：用于观察 SQL，不要放进生产代码。
// import 需要 fmt、gorm.io/gorm、gorm.io/driver/mysql。
// 把下面这个类型放在包级，字段足够让 GORM 推断表名。
type Article struct {
	ID     uint
	Status int
	Title  string
}

db, err := gorm.Open(gormmysql.New(gormmysql.Config{
	DSN:                       "root:pass@tcp(127.0.0.1:3306)/forum?parseTime=true",
	SkipInitializeWithVersion: true,
}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
if err != nil {
	panic(err)
}
base := db.Model(&Article{}).Where("status = ?", 1)
var total int64
var list []Article
fmt.Println(base.Count(&total).Statement.SQL.String()) // 第一条链
fmt.Println(base.Find(&list).Statement.SQL.String())   // 复用同一个 base
```

DryRun 模式下 `Statement.SQL` 在执行结束后不会被清空，所以第一行属于 DryRun 特有的缓存现象。真实执行时 GORM 的 processor 会 `Reset()` 已生成的 SQL，`Count` 自身也用 `defer` 恢复或删除 `SELECT` 子句。因此「Count 把 SELECT 改成 count(*) 并一直留着」这个机制解释并不准确，具体表现随版本和执行模式变化。**结论仍以官方规则为准：不要复用执行过终结方法的链，用 `Session` 隔离。** 靠「我这次跑通了」建立的安全感很脆弱。

### 3.6 分页、空结果、Preload 的三个常见误判

**`page = 0` 不会报错，反而更危险。** `Offset(-pageSize)` 在 GORM 里被当成「取消 offset」处理。`clause/limit.go` 的 `Build` 只在 `Offset > 0` 时输出 `OFFSET`，`MergeClause` 把负值归零。官方也把 `Offset(-1)` 作为取消条件写入文档 [来源: https://gorm.io/docs/query.html]。探针里 `Offset(-10).Limit(10)` 生成的 SQL 是 `SELECT * FROM articles LIMIT 10`，没有 OFFSET 子句。结果悄悄返回第一页，接口看起来成功，排查时很难发现。所以必须在入口校验。

**`pageSize = 0` 生成 `LIMIT 0`，返回空列表。超大值会拉爆内存。** handler 对越界值返回 400，与 `docs/00` 的合同一致。service 里再做一次兜底校验，防止将来出现绕过 handler 的内部调用。旧版 handler 把越界值静默改成 10，与 00 章「超出范围返回 400」冲突，已统一。

这里有一个值得知道的对照。GORM 官方 Scopes 文档给的分页范例采用钳制策略，`page <= 0` 时改 1，`pageSize > 100` 时改 100，`pageSize <= 0` 时改 10 [来源: https://gorm.io/docs/scopes.html]。钳制与返回 400 都是合法选择，本项目按 00 章 API 合同选了 400，让调用方明确知道自己传错了。关键是全项目只用一个口径，文档、handler、service 与验收脚本保持一致。

**空结果时 Preload 不发第二条 SQL。** GORM 的预加载回调在 `foreignValues` 为空时直接返回 [gorm.io/gorm@v1.31.2/callbacks/preload.go:264]。所以 `total == 0` 时继续查列表只会多发**一条**主查询。有一种讲法先说「带 Preload 会浪费两条 SQL」，后文又说「articles 为空时 Preload 会跳过」，前后矛盾。提前返回的价值是省掉那一条主查询，并保持空响应的形状稳定。

**页越界是另一个容易漏掉的边界**。`total > 0` 但请求页越界时（例如共 3 条却请求第 99 页），`Find` 返回 0 行，切片会保持 `nil`，JSON 里变成 `null`。`total == 0` 的提前返回只覆盖空表这一种情况。所以 3.2 与第 5 节都用 `make(..., 0)` 初始化切片，保证任何情况下响应都是数组。

**排序与索引要辩证看。** 没有 `ORDER BY` 时，数据库返回顺序无保证，翻页可能重叠或遗漏，所以排序是必需的。`created_at` 在同一秒可能重复，加 `id DESC` 做次级排序能得到确定顺序。大表上排序分页建议建 `(created_at, id)` 复合索引并用 `EXPLAIN` 验证；小表全表扫描往往更便宜，「排序字段必须建索引」属于过度断言。

GORM 官方把 `Order` 与 `Limit`、`Offset` 分开写在 Query 文档里，并没有写「分页必须加 ORDER BY」[来源: https://gorm.io/docs/query.html]。排序保证分页稳定属于数据库工程惯例，依据是无 `ORDER BY` 时 SQL 不承诺返回顺序。把惯例说成官方规定，会让人放弃追问它成立的前提。

**Count 与 Find 条件必须一致。** 一旦将来加入「只显示已发布」，把条件抽成函数，两处都从同一个带条件的 `base` 出发并各自 `Session(&gorm.Session{})`。只改一处会造成分页器错乱。启用条件前先按 1.1 补齐 `status` 字段与迁移。

### 3.7 详情接口

**增量修改**：`internal/service/article.go` 的 `Detail` 方法，无缓存版本。

```go
// Detail 按主键查文章及作者。记录不存在时返回 ErrNotFound。
func (s *ArticleService) Detail(ctx context.Context, id uint) (*model.Article, error) {
	var a model.Article
	if err := s.db.WithContext(ctx).Preload("User").First(&a, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get article %d: %w", id, err)
	}
	return &a, nil
}
```

`First` 在查不到时返回 `gorm.ErrRecordNotFound`，GORM 官方 Query 文档明确写出这一点 [来源: https://gorm.io/docs/query.html]。`errors.Is` 判断后交给 handler 映射成 404。旧版写成 `return &a, err`，失败时同时返回非 nil 指针和错误，调用方若忽略 err 就会拿到一篇全零值的文章，已改为返回 `nil`。

`ErrNotFound` 已经定义在 `internal/service/user.go` 的 `var` 块里，和 `ErrInvalidInput`、`ErrUserNameTaken`、`ErrInvalidCredentials` 放在一起。旧文档说它是「本阶段新增」，现在已过时。**先打开文件核对，别再粘贴一次同名变量**，重复定义会直接编译失败。

**概念伪代码**：`internal/service/user.go` 已有的定义，只用来核对，不要重复添加。

```go
// 概念伪代码：核对用，仓库已有，不要重复添加。
ErrNotFound = errors.New("not found")
```

handler 用 `errors.Is(err, service.ErrNotFound)` 映射 404。完整错误清单在 6.4。

### 3.8 为什么需要 DTO

`model.Article` 的 `User` 字段类型是 `User`（值类型），tag 写成 `json:"user,omitempty"`。Go 的 `encoding/json` 对结构体零值不会因 `omitempty` 而省略，该标签对 struct 字段无效。未调用 `Preload` 时，响应里仍会带出一个 `user` 全零对象。用 DTO 把字段明确列出来，可以顺手解决字段外泄和形状不稳定。

Microsoft Learn 列出「不要直接把数据库实体暴露给客户端」的六条理由 [来源: https://learn.microsoft.com/en-us/aspnet/web-api/overview/data/using-web-api-with-entity-framework/part-5]，逐条对照本项目：

| DTO 的理由 | 在本项目对应什么 |
| --- | --- |
| 去掉循环引用 | 文章与作者互相引用时避免无限嵌套 |
| 隐藏不该给客户端看的属性 | `User.Password` 不出现在响应里，本项目还用 `json:"-"` 兜底 |
| 省略字段减小载荷 | 列表只返回摘要字段，正文按需返回 |
| 展平嵌套对象图 | `author` 只给 id 与 username，不整包吐 `User` |
| 防 over-posting | 请求体只接受 title 与 content，作者由 token 决定 |
| 解耦 service 层与数据库层 | 改表结构时响应形状可以保持不变 |

Martin Fowler 把 DTO 定义为「在进程间搬运数据以减少方法调用的对象」，并强调把序列化逻辑集中在一处 [来源: https://martinfowler.com/eaaCatalog/dataTransferObject.html]。GORM 的 `Select` 配合精简结构体还能让 SQL 只取需要的列，官方文档在 API 响应场景下专门提到这一点 [来源: https://gorm.io/docs/advanced_query.html]。

**落点：`internal/handler/dto.go`**。该文件已在 02 章 8.0 创建。当前仓库里除了 `userResp`、`loginResp`、`toUserResp`，下面这三个声明也已经追加进去了，只是格式未 `gofmt`、字段顺序与下面的规范版略有差异、注释也更简略。所以本节的动作是**核对并规范化**，把源码改成下面这段，别再粘贴一次造成重复定义。若你的仓库还没有这三个声明，按下面代码追加即可。不新建 `internal/dto` 包，判断依据见 02 章 8.0。`dto.go` 的 import 块已含 `time` 与 `forum-backend/internal/model`，分别供 `CreatedAt` 和转换函数使用。

**增量修改**：追加到 `internal/handler/dto.go`，3.8 的文章 DTO。

```go
// 增量修改：追加到 internal/handler/dto.go。
// articleResp 是文章对外的形状，字段与数据库模型解耦。
type articleResp struct {
	ID        uint       `json:"id"`
	Title     string     `json:"title"`
	Content   string     `json:"content"`
	CreatedAt time.Time  `json:"created_at"`
	Author    *userBrief `json:"author,omitempty"` // 指针类型，omitempty 才生效
}

// userBrief 只暴露作者的两个字段，避免把整个 User 对象带出去。
type userBrief struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
}

// toArticleResp 把数据库模型转成对外结构体，转换仍留在 handler 层。
func toArticleResp(a *model.Article) articleResp {
	resp := articleResp{
		ID:        a.ID,
		Title:     a.Title,
		Content:   a.Content,
		CreatedAt: a.CreatedAt,
	}
	if a.User.ID != 0 {
		resp.Author = &userBrief{ID: a.User.ID, Username: a.User.Username}
	}
	return resp
}
```

`articleResp` 一直保持上面这五个字段。04 章 4.10 让文章详情显示点赞数时，是另建一个 `articleDetailResp` 匿名嵌入它，没有往这里加字段，所以列表与新建响应不会被带上一个恒为 0 的 `like_count`。

`Author` 用指针，`omitempty` 才会生效；用值类型时会输出一个全零的 `author` 对象，这正是 3.8 开头要解决的问题。`userBrief` 与 `toArticleResp` 都写了 doc comment，理由与 02 章 8.0 相同，属于 `CodeReviewComments` 对非平凡非导出声明的明文要求 [来源: https://go.dev/wiki/CodeReviewComments]。

handler 组装响应时用 `make([]articleResp, 0, len(articles))`：长度给 0，容量预分配，避免 `append` 后混进零值元素，也避免多次扩容。

判断标准始终看依赖方向。只要只有 handler 一个包引用这些类型，就留在同包的 `dto.go`。出现 gRPC 与 HTTP 双入口、v1 与 v2 响应共存，或 service 需要直接产出 DTO，才考虑升格成独立包，详见 02 章 8.0。

### 3.9 验证

```powershell
go test ./...
go vet ./...
go build ./cmd/server
```

接好路由后按第 10 节的顺序测 `page=abc`、`page=0`、`page_size=999999`、不存在的 id，观察状态码与响应体。

---

## 4. Delete：删除文章，逐行拆解

删除要同时守住两件事：只能删自己的，删完不能留下孤儿评论。

**增量修改**：`internal/service/article.go` 的 `Delete` 方法，按 6.2 的外键决策替换旧版删除顺序。

```go
// Delete 只允许文章作者删除，并在事务里清掉该文章的评论。
// 删除顺序由 6.2 的外键决策决定，先删子表，再删父表。
func (s *ArticleService) Delete(ctx context.Context, id, userID uint) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 先清子表。comments.article_id 上有 RESTRICT 外键，先删文章会被拒绝。
		if err := tx.Where("article_id = ?", id).Delete(&model.Comment{}).Error; err != nil {
			return err // 返回错误让整个事务回滚
		}

		// 把所有权放进 WHERE，数据库一次判断，避免「先查再删」之间的竞态。
		res := tx.Where("id = ? AND user_id = ?", id, userID).Delete(&model.Article{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			// 文章不存在与无权删除对外合并成同一个错误，避免泄露归属。
			// 此时评论已被删，返回错误会让事务把评论一并回滚。
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("delete article %d: %w", id, err)
	}
	return nil
}
```

| 代码 | 作用 | 为什么这样设计 |
| --- | --- | --- |
| `Transaction(func(tx *gorm.DB) error {...})` | 包住两步删除 | 任一步失败都回滚，不会出现文章已删、评论还在 |
| 第一步删评论 | 先清子表 | 6.2 的外键默认 RESTRICT 会拒绝先删父表，顺序写反直接报外键错误 |
| `Where("id = ? AND user_id = ?", ...)` | 条件里带作者 | 一条 SQL 完成鉴权与删除，避免两次查询之间的状态变化 |
| `res.RowsAffected == 0` | 判断有没有删到 | `Delete` 没匹配到行时 `Error` 为 nil，只看 `Error` 会误报成功 |
| `ErrNotFound` | 统一对外错误 | 不区分「不存在」与「不是作者」，避免探测他人资源。事务会把第一步删掉的评论恢复 |

`Delete` 不带 `Where` 时 GORM 默认拒绝全局删除并返回 `ErrMissingWhereClause`，官方文档把它列为 Block Global Delete [来源: https://gorm.io/docs/delete.html]。这是保护机制，别用 `AllowGlobalUpdate: true` 绕过。

**为什么先删评论**。6.2 给 `comments.article_id` 加了 RESTRICT 外键。父表还有子行时 MySQL 会立即拒绝删除，所以顺序必须是子表在前。这个顺序带来一个锁代价，无权请求也会先删到评论再回滚，6.2 给出了可选的所有权预检查。

**软删除边界**：当前 `model.Article` 与 `model.Comment` 都没有 `gorm.DeletedAt` 字段，所以上面的 `Delete` 生成物理 `DELETE`。以后若给模型加上 `gorm.DeletedAt`，GORM 会自动把 `Delete` 变成 `UPDATE ... SET deleted_at = 当前时间`，普通查询自动附加 `deleted_at IS NULL` [来源: https://gorm.io/docs/delete.html]。届时本章「事务删除文章与评论」的 SQL、RowsAffected 判断和验收用例都要重新设计。6.2 也会说明，软删除之后 `ON DELETE CASCADE` 不会触发。

---

## 5. 评论：同构代码的三个差异

评论的 service、handler 结构与文章一致，差异集中在这三处：

1. `Comment.ArticleID` 决定评论属于哪篇文章，创建前要确认文章存在。
2. 列表按文章过滤，并且要分页与稳定排序。
3. 删除条件同时带 `id` 与 `user_id`。

### 5.1 核对 `CommentService`（完整文件片段）

`internal/service/comment.go` 已经在仓库里，含 `CommentService`、`NewCommentService` 与 `List`。旧文档说「不在仓库里」，现在已过时。做法是打开该文件，对照下面这段确认类型与构造函数齐全，**别新建同名文件覆盖**，会丢掉已经写好的方法。若你的仓库确实还没有这个文件，再按下面代码保存。确认存在之后才能用 5.2 的列表代码，否则会出现 `undefined: CommentService`。

本轮已在这个文件里补上 `Create` 与 `Delete`，并把排序常量 `commentListOrder` 从 `article.go` 搬过来。**本节下面那段只给出类型与构造函数，是讲解用的最小片段；三个方法的最终完整文件见 6.7.3 片段 A**，别用本节这段覆盖仓库源码。

```go
package service

import "gorm.io/gorm"

// CommentService 评论业务逻辑。
type CommentService struct {
	db *gorm.DB
}

func NewCommentService(db *gorm.DB) *CommentService {
	return &CommentService{db: db}
}
```

### 5.2 评论列表

源码里的 `List` 已在 `internal/service/comment.go`，下面这段把它的逻辑单独拎出来讲。现在的源码已 `gofmt`，排序也换成了 `commentListOrder` 常量；下面保留字面量是为了看清两个排序键，最终版见 6.7.3 片段 A。别重复定义同名方法。

**增量修改**：核对并规范化 `internal/service/comment.go` 的 `List`。

```go
// List 按文章分页查评论。
func (s *CommentService) List(ctx context.Context, articleID uint, page, pageSize int) ([]*model.Comment, int64, error) {
	if page < 1 || pageSize < 1 || pageSize > 50 {
		return nil, 0, fmt.Errorf("%w: invalid pagination", ErrInvalidInput)
	}
	var total int64
	if err := s.db.WithContext(ctx).Model(&model.Comment{}).
		Where("article_id = ?", articleID).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count comments of article %d: %w", articleID, err)
	}
	if total == 0 {
		return []*model.Comment{}, 0, nil
	}
	// 同样初始化成空切片，避免页越界时响应变成 null。
	list := make([]*model.Comment, 0)
	err := s.db.WithContext(ctx).
		Preload("User").
		Where("article_id = ?", articleID).
		Order("created_at ASC, id ASC").
		Offset((page - 1) * pageSize).Limit(pageSize).
		Find(&list).Error
	if err != nil {
		return nil, total, fmt.Errorf("list comments of article %d: %w", articleID, err)
	}
	return list, total, nil
}
```

`Count` 与 `Find` 的 `Where("article_id = ?", articleID)` 必须同时存在。这里为了看得清楚各写一次，工程上更稳的做法是把过滤抽成函数，两处共用同一个 `base` 并各自 `Session(&gorm.Session{})`。

---

## 6. 把片段接成可运行的业务切片

### 6.0 这一节补什么

第 2 到 5 节给出的是 service 层片段。片段能编译，装不进 HTTP 链路就收不到请求。本节把已有片段接成可运行的 MySQL 版本，同时补上现阶段暴露出来的四个缺口。

**执行顺序按 6.1 到 6.5 的小节编号走**。下面这张表是缺口索引，方便回查，表格顺序不代表操作顺序。每一步引用的符号都在它之前落盘。

| 缺口 | 现在的状态 | 补在哪个小节 |
| --- | --- | --- |
| 评论响应没有 DTO | `model.Comment.User` 是值类型，`json:"user,omitempty"` 不生效，直接吐 model 会带出全零 user | 6.3 补 `commentResp` 与两个列表 DTO |
| 外键与级联策略未定义 | `comments.article_id` 没有外键，第 4 节靠应用层事务清理，两者还没对齐 | 6.2 定策略并同步改删除顺序 |
| 错误清单分散 | 3.7 写着「第 6 节列出完整错误清单」，此前本节没有清单，原文还把 `ErrForbidden` 与第 4 节选定的 404 策略写在一起 | 6.4 给映射表与统一函数 |
| 请求体上限与分页解析缺失 | service 里有范围校验，handler 层没有解析与 body 上限 | 6.1 片段 C 落盘 body 上限，6.5 落盘 `paging.go` |

先做无 Redis 的 MySQL 版本，跑通读链路再进缓存。文章的写接口占位在 6.6 替换，评论的两个写方法在 6.7 接通，两步都是线性紧接的，不用往后翻。本节所有代码都标注了完整文件、增量修改或概念伪代码，现阶段状态在 6.11 单独列出。

### 6.1 第 1 步：先落盘能编译的链路骨架

这一节只做一件事，让 `router.New` 引用的每个符号先存在。下面四个片段都在本节就地给出，读到这里就能一路做完，不用往后翻。6.2 到 6.5 再把骨架逐个填成真实实现。

```text
main.go
  -> config.InitDB(...)          得到 *gorm.DB
  -> jwt.NewManager(...)         得到 *jwt.Manager
  -> router.New(db, jwtManager)
       -> service.NewArticleService(db)      service.NewCommentService(db)
       -> handler.NewArticleHandler(svc)    handler.NewCommentHandler(svc)
       -> api.Use(limitBody(...))           分组级请求体上限
       -> 公开读路由                        GET 列表、详情、评论列表
       -> api.Group("", JWTAuth(...))       受保护写路由
```

在 6.1 这一阶段，`main.go` 一行都不用改。它已经把 `db` 与 `jwtManager` 交给 `router.New`，装配全部发生在 router 里。最容易走错的方向是去 `main.go` 里 new 一堆 handler，那样会把路由细节泄漏到启动层。

**演进提示**：上面链路图里的 `router.New(db, jwtManager)` 是本节的「无 Redis」版本。7.2 会把它改成 `router.New(db, jwtManager, rdb)`，并在 `main.go` 里补 `InitRedis`。若当前源码已经是三参数，说明 7.2 已经做过，按 7.2 的三层清单逐项核对即可，别为了对齐本节再改回两参数。

| 步 | 只做这一件事 | 代码在哪 | 做完的验证 |
| --- | --- | --- | --- |
| 1 | 落盘 `internal/handler/article.go` 占位 | 本节片段 A | `go build ./...` 通过 |
| 2 | 落盘 `internal/handler/comment.go` 占位 | 本节片段 B | `go build ./...` 通过 |
| 3 | 落盘 `internal/router/bodylimit.go` | 本节片段 C | `go build ./...` 通过 |
| 4 | 替换 `internal/router/router.go` 的 `New` 函数体 | 本节片段 D | `go build ./...` 通过，启动后 GET 列表得到 501 |
| 5 | 6.2 定外键、6.3 补 DTO、6.4 补错误映射 | 紧随其后的小节 | 每小节末尾各自的验证 |
| 6 | 6.5 把 501 占位换成读接口实现 | 紧随其后的小节 | GET 列表得到 200 与 `[]` |

第 5、6 步之前，所有写接口返回 501。这是占位状态，属于预期，别当成接口已经做完。

**如果已经按旧版动过手**。仓库里可能留着空的 `article.go`、只有空方法的 `comment.go`，或写了一半的 `router.go`。空文件会让 `go build ./...` 报 `expected 'package', found 'EOF'`，`router.go` 里声明了 `commentHandler` 却没注册路由会报 `declared and not used`。做法是用片段 A、B、D **整体覆盖**对应文件与函数体，别在旧代码上继续加行，也别新建同名副本。覆盖前先把旧内容读一遍，确认没有自己写过的逻辑。

**片段 A，完整文件：`internal/handler/article.go` 的第 1 步占位版本。**

```go
package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/service"
	"forum-backend/pkg/response"
)

// ArticleHandler 文章接口，只依赖 service，不认识 GORM。
type ArticleHandler struct {
	svc *service.ArticleService
}

func NewArticleHandler(svc *service.ArticleService) *ArticleHandler {
	return &ArticleHandler{svc: svc}
}

// notImplemented 是占位期间的统一响应。
// 空方法会静默返回 200，容易被误判成接口已经做好，所以显式返回 501。
func notImplemented(c *gin.Context, operation string) {
	response.Error(c, http.StatusNotImplemented, operation+" 尚未实现")
}

func (h *ArticleHandler) List(c *gin.Context)   { notImplemented(c, "list articles") }
func (h *ArticleHandler) Detail(c *gin.Context) { notImplemented(c, "article detail") }
func (h *ArticleHandler) Create(c *gin.Context) { notImplemented(c, "create article") }
func (h *ArticleHandler) Delete(c *gin.Context) { notImplemented(c, "delete article") }
```

`svc` 字段在占位方法里还没用到。Go 允许结构体字段闲置，编译不会报错。等 6.5 填实现时它才被真正使用。

**片段 B，完整文件：`internal/handler/comment.go` 的第 1 步占位版本。**

```go
package handler

import (
	"github.com/gin-gonic/gin"

	"forum-backend/internal/service"
)

// CommentHandler 评论接口，先只保留能编译的占位方法。
type CommentHandler struct {
	svc *service.CommentService
}

func NewCommentHandler(svc *service.CommentService) *CommentHandler {
	return &CommentHandler{svc: svc}
}

func (h *CommentHandler) List(c *gin.Context) { notImplemented(c, "list comments") }
func (h *CommentHandler) Create(c *gin.Context)        { notImplemented(c, "create comment") }
func (h *CommentHandler) Delete(c *gin.Context)        { notImplemented(c, "delete comment") }
```

`notImplemented` 定义在片段 A 的 `article.go`。两个文件同属 `handler` 包，同包内可以直接调用，不用 import，也不用重复定义。最后一个占位方法被替换后，把 `notImplemented` 一起删掉。

**片段 C，完整文件：`internal/router/bodylimit.go`。**

```go
package router

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// limitBody 限制请求体字节数，防止超大 body 占满内存。
func limitBody(n int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, n)
		c.Next()
	}
}
```

`http.MaxBytesReader` 超过限制后 `Read` 返回 `*http.MaxBytesError`，并尽可能通知 `ResponseWriter` 关闭连接 [来源: https://pkg.go.dev/net/http#MaxBytesReader]。这个错误会在 `ShouldBindJSON` 阶段被拿到，handler 按绑定错误返回 400。上限只挡请求体，挡不住 URL 长度与并发数，那两类要分别在代理层与限流里处理。

**片段 D，增量修改：替换 `internal/router/router.go` 里 `New` 的函数体。** import 与文件头保持原样。`limitBody` 来自片段 C，两个 handler 构造函数来自片段 A 与片段 B，全部已经落盘。

```go
func New(db *gorm.DB, jwtManager *jwt.Manager) (*gin.Engine, error) {
	if db == nil {
		return nil, errors.New("router: database is required")
	}
	if jwtManager == nil {
		return nil, errors.New("router: JWT manager is required")
	}

	r := gin.Default()

	userSvc := service.NewUserService(db)
	userHandler := handler.NewUserHandler(userSvc, jwtManager)

	articleSvc := service.NewArticleService(db)
	commentSvc := service.NewCommentService(db)
	articleHandler := handler.NewArticleHandler(articleSvc)
	commentHandler := handler.NewCommentHandler(commentSvc)

	api := r.Group("/api")
	// Use 必须写在路由注册之前，Gin 才会把中间件挂到该分组的所有路由上。
	api.Use(limitBody(1 << 20)) // 1 MiB
	{
		api.POST("/register", userHandler.Register)
		api.POST("/login", userHandler.Login)

		// 读接口公开，不挂 JWTAuth。
		api.GET("/articles", articleHandler.List)
		api.GET("/articles/:id", articleHandler.Detail)
		api.GET("/articles/:id/comments", commentHandler.List)

		auth := api.Group("", middleware.JWTAuth(jwtManager))
		{
			auth.POST("/articles", articleHandler.Create)
			auth.DELETE("/articles/:id", articleHandler.Delete)
			auth.POST("/articles/:id/comments", commentHandler.Create)
			auth.DELETE("/comments/:id", commentHandler.Delete)
		}
	}
	return r, nil
}
```

| 代码 | 作用 | 为什么这样设计 |
| --- | --- | --- |
| `if db == nil` 与 `if jwtManager == nil` | 启动期挡住空依赖 | 空指针等到第一个请求才 panic，排查成本高得多 |
| `service.NewArticleService(db)` | 同一个 `*gorm.DB` 注入多个 service | GORM 的 DB 自带连接池，共享一个实例即可，别在每个 service 里 `gorm.Open` |
| `api.Use(limitBody(...))` | 给整个 `/api` 分组加 body 上限 | 只对写接口加容易漏，分组级一次覆盖全部路由 |
| 读路由挂在 `api` 上 | 列表与详情不需要登录 | 令牌只用于判定作者，读操作没有身份需求 |
| `api.Group("", middleware.JWTAuth(...))` | 空前缀分组挂鉴权 | 沿用仓库已有的写法，避免改动 `main.go` 的注入签名 |
| `auth.POST("/articles", ...)` | 写操作全部经过 JWTAuth | 作者 id 来自 token，请求体里的 `user_id` 一律忽略 |

`middleware.JWTAuth` 用 `c.Set("userID", userID)` 存的是 `jwt.Parse` 返回的 `uint`，所以 handler 里 `c.GetUint("userID")` 类型对得上。类型断言失败时 `GetUint` 返回 0 且不报错，6.4 的 `currentUserID` 会给出 0 值兜底。

**验证第 1 步**。先跑构建，再启动服务，另开一个终端发 GET。

```powershell
go build ./...
go run ./cmd/server
```

```powershell
curl.exe -i http://localhost:8080/api/articles
```

预期 HTTP 501，响应体写明 `list articles 尚未实现`。看到 501 说明路由、依赖注入、body 上限三段都通了。看到 404 说明 `New` 没替换成功。看到编译错误说明片段 A 到 D 有遗漏，按表格从上往下逐个核对。

后面每一节引用的符号都在它之前落盘，顺序不再需要往回翻。

### 6.2 外键与级联策略：定下来再写删除代码

先认清当前数据库里到底有什么。

| 关系 | 模型字段 | AutoMigrate 会建外键吗 |
| --- | --- | --- |
| `articles.user_id → users.id` | `User User` 关联字段 | 会 |
| `comments.user_id → users.id` | `User User` 关联字段 | 会 |
| `comments.article_id → articles.id` | 只有 `ArticleID uint` 加 index | 不会。GORM 只能从关联字段推断关系 |

GORM 官方写明 AutoMigrate 会创建表、缺失的外键、约束、列与索引，且默认自动创建数据库外键约束 [来源: https://gorm.io/docs/migration.html]。MySQL 省略 `ON DELETE` 时默认永远是 `NO ACTION`，等价 `RESTRICT`，父表还有子行时删除被立即拒绝 [来源: https://dev.mysql.com/doc/refman/8.4/en/create-table-foreign-keys.html]。

| 策略 | 数据库做什么 | 应用代码做什么 | 取舍 |
| --- | --- | --- | --- |
| A 保留默认 RESTRICT，事务里先删子表再删父表 | 兜底拒绝任何会产生孤儿的写 | 两步删除仍由事务包住 | 采用。Go 代码里看得见清理逻辑，事务的原子性也保留了 |
| B `ON DELETE CASCADE` | 自动删评论 | 只删文章一行，事务可以省 | 代码最短。删除逻辑藏进 DDL，读 Go 代码看不出来 |
| C 只留索引，不加约束 | 不保证 | 事务加定期巡检 | 大表、分库分表、批量导入场景常见。学习项目会丢掉「约束可执行」这个练习 |

**为什么选 A**。它让数据一致性有两道关卡，应用逻辑出错时数据库还能拦住。代价要如实写，每次插入评论都要检查父表、锁范围变大、迁移必须按父子表顺序、将来分库分表外键会失效、批量导入要先临时禁用检查。B 有一个更隐蔽的陷阱，当前模型没有 `gorm.DeletedAt`，删除是物理 `DELETE`，CASCADE 能触发；以后加软删除，`Delete` 会变成 `UPDATE ... SET deleted_at = ...`，CASCADE 永不触发，评论就没人清了。第 4 节已经写过这个软删除边界，这里把它和级联策略接上。

增量修改：`internal/model/models.go` 的 `Comment`，加一个关联字段让 AutoMigrate 建出约束。

```go
// 增量修改：替换 internal/model/models.go 里的 Comment 结构体。
type Comment struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	ArticleID uint      `gorm:"index;not null" json:"article_id"`
	Article   Article   `gorm:"constraint:OnUpdate:NO ACTION,OnDelete:RESTRICT;" json:"-"`
	UserID    uint      `gorm:"index;not null" json:"user_id"`
	User      User      `json:"user,omitempty"`
	Content   string    `gorm:"type:text;not null" json:"content"`
	CreatedAt time.Time `json:"created_at"`
}
```

| 代码 | 作用 | 为什么这样设计 |
| --- | --- | --- |
| `Article Article` | 让 GORM 认出 belongs-to 关系 | 只有 `ArticleID uint` 时 AutoMigrate 无从推断，加字段才会生成外键 |
| `constraint:OnDelete:RESTRICT` | 显式写出拒绝级联 | 省略也有同样效果，写出来是为了让策略在 Go 代码里可见。官方 tag 语法见 [来源: https://gorm.io/docs/constraints.html] |
| `json:"-"` | 不序列化关联对象 | 关联字段是给 ORM 用的，对外形状由 6.3 的 `commentResp` 定义 |
| `User User` 保持原样 | 作者关联 | 本节不改它，只补 `Article` 这一条关系 |

等值 SQL 供审查，也能看出 AutoMigrate 会做什么。MySQL 官方给的建约束语法就是这个形状 [来源: https://dev.mysql.com/doc/refman/8.4/en/create-table-foreign-keys.html]。

```sql
ALTER TABLE comments
  ADD CONSTRAINT fk_comments_article
  FOREIGN KEY (article_id) REFERENCES articles(id)
  ON DELETE RESTRICT ON UPDATE RESTRICT;
```

已有数据的库执行这条语句前要先清理孤儿评论，否则约束建不上。仓库 `config.InitDB` 走的是 `AutoMigrate`，还没有版本化迁移机制，这类一次性脚本的归属属于 05 章的迁移待办，别把它当成启动流程已有的一部分。

**这一步会直接改变第 4 节的删除代码。** 加了 RESTRICT 外键后，「先删文章」会被父表约束拒绝。第 4 节 `Delete` 已按本节决策改成先删评论再删文章，事务回滚会把无权请求误删的评论恢复。要留意锁代价，无权请求也会先拿到评论行的锁再回滚，高并发下可以改成事务开头先用一条带 `user_id` 的 `SELECT` 判断所有权，再决定要不要进入删除流程。

验证约束真的建上了：

```sql
SHOW CREATE TABLE comments\G
SELECT CONSTRAINT_NAME, COLUMN_NAME, REFERENCED_TABLE_NAME
  FROM INFORMATION_SCHEMA.KEY_COLUMN_USAGE
  WHERE TABLE_SCHEMA = 'forum' AND REFERENCED_TABLE_NAME IS NOT NULL;
```

再故意插一条 `article_id = 999999` 的评论。应当收到外键约束错误，并按 6.4 的映射返回 400，服务端日志留下原始错误。

### 6.3 DTO 补齐评论与列表响应

增量修改：追加到 `internal/handler/dto.go`。当前仓库的 `dto.go` 已含 `userResp`、`loginResp`、`toUserResp`，以及 3.8 的 `articleResp`、`userBrief`、`toArticleResp`，后面三个是后续追加的版本，格式未 `gofmt`。本节补评论与列表响应结构体，下面这段可以直接追加，不会重复定义。`dto.go` 的 import 已含 `time` 与 `model`，不用重复写。

```go
// 增量修改：追加到 internal/handler/dto.go。
// commentResp 是评论对外的形状，不直接序列化 model.Comment。
type commentResp struct {
	ID        uint       `json:"id"`
	ArticleID uint       `json:"article_id"`
	Content   string     `json:"content"`
	CreatedAt time.Time  `json:"created_at"`
	Author    *userBrief `json:"author,omitempty"`
}

// articleListResp 是文章列表接口 data 的形状。
// 字段写错由编译器拦截，别在响应里用 gin.H 拼字符串键。
type articleListResp struct {
	List     []articleResp `json:"list"`
	Total    int64         `json:"total"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
}

// commentListResp 是评论列表接口 data 的形状，字段与文章列表对齐。
type commentListResp struct {
	List     []commentResp `json:"list"`
	Total    int64         `json:"total"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
}

// toCommentResp 把评论模型转成对外结构体。
func toCommentResp(m *model.Comment) commentResp {
	resp := commentResp{
		ID:        m.ID,
		ArticleID: m.ArticleID,
		Content:   m.Content,
		CreatedAt: m.CreatedAt,
	}
	if m.User.ID != 0 {
		resp.Author = &userBrief{ID: m.User.ID, Username: m.User.Username}
	}
	return resp
}
```

`Author` 用指针，`omitempty` 才生效。这正好回应本节开头那个隐患，`model.Comment.User` 是值类型，tag 写成 `json:"user,omitempty"` 并没有用 [来源: https://pkg.go.dev/encoding/json]。Go 还提供 `json:",omitzero"`，可以直接省略零值结构体，本项目 `go 1.26.0` 能用 [来源: https://pkg.go.dev/encoding/json]。DTO 仍然更稳，字段集合由服务端明确列出，理由见 02 章 8.0。

`articleListResp.List` 声明成切片，handler 里用 `make([]articleResp, 0, len(...))` 初始化，无结果时输出 `[]`。`nil` 切片会被序列化成 `null` [来源: https://pkg.go.dev/encoding/json]。

### 6.4 统一错误处理：补上 3.7 承诺的清单

完整错误清单如下，3.7 末尾那句「第 6 节列出完整错误清单」指的就是这张表。

| 错误 | 来源 | HTTP | 客户端提示 |
| --- | --- | --- | --- |
| `service.ErrInvalidInput` | `service/user.go` 已有 | 400 | 参数不符合要求 |
| `service.ErrNotFound` | `service/user.go` 已有 | 404 | 资源不存在 |
| `gorm.ErrForeignKeyViolated` | GORM 提供，`TranslateError: true` 已开 | 400 | 引用的资源不存在 |
| `gorm.ErrDuplicatedKey` | GORM 提供，`TranslateError: true` 已开 | 409 | 资源已存在 |
| 其他任何错误 | 无 | 500 | 通用提示，原文只进服务端日志 |

`TranslateError` 已在 `internal/config/db.go` 的 `gorm.Config` 里打开，GORM 会把数据库特定错误转成 `ErrDuplicatedKey`、`ErrForeignKeyViolated` 这类通用错误 [来源: https://gorm.io/docs/error_handling.html]。少了这个开关，`errors.Is` 就认不出来，只能退回解析 MySQL 错误码 1062、1452。

**`ErrForbidden` 先不要加。** 本节原文建议同时准备 `ErrNotFound` 与 `ErrForbidden`，但第 4 节 `Delete` 已经选了「不区分不存在与不是作者，统一 `ErrNotFound`」，理由是避免探测他人资源。两条口径冲突。

| 策略 | 返回 | 代价 |
| --- | --- | --- |
| 隐藏资源，本项目采用 | 一律 404 | 客户端分不清「没有」与「无权」，调试稍麻烦 |
| 显式授权 | 存在但无权返回 403 | 要先查存在再查作者，多一条 SQL，且攻击者能枚举他人文章 id |

要切到 403，得同时改第 4 节 `Delete`、6.4 的映射函数、9 节验收第 6 步。本阶段统一 404。

完整文件：`internal/handler/errors.go`。

```go
package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"forum-backend/internal/service"
	"forum-backend/pkg/response"
)

// mapServiceError 把 service 与 GORM 的错误映射成统一的 HTTP 响应。
// 所有 handler 共用这一处，避免每个接口写一遍 switch 导致口径漂移。
func mapServiceError(c *gin.Context, operation string, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidInput):
		response.Error(c, http.StatusBadRequest, "参数不符合要求")
	case errors.Is(err, service.ErrNotFound):
		response.Error(c, http.StatusNotFound, "资源不存在")
	case errors.Is(err, gorm.ErrForeignKeyViolated):
		response.Error(c, http.StatusBadRequest, "引用的资源不存在")
	case errors.Is(err, gorm.ErrDuplicatedKey):
		response.Error(c, http.StatusConflict, "资源已存在")
	default:
		logRequestError(c, operation, err)
		response.Error(c, http.StatusInternalServerError, "服务暂时不可用，请稍后重试")
	}
}

// currentUserID 取出 JWTAuth 放进 Gin context 的作者 id。
// GetUint 在类型断言失败时返回 0 且不报错，0 会被当成有效 id 写出脏数据。
func currentUserID(c *gin.Context) (uint, bool) {
	id := c.GetUint("userID")
	if id == 0 {
		return 0, false
	}
	return id, true
}
```

| 代码 | 作用 | 为什么这样设计 |
| --- | --- | --- |
| `errors.Is(...)` | 穿透 `%w` 包装的错误链 | service 用 `fmt.Errorf("...: %w", ErrNotFound)` 加了上下文，直接 `==` 会失配 |
| `switch { case ... }` 无表达式 | 逐个判断条件 | 比 `if/else if` 链更容易插入新错误类型 |
| `gorm.ErrForeignKeyViolated` 在 `ErrNotFound` 之后 | 先判业务错误 | 两条链不会同时命中，顺序仍要固定，方便以后加新分支 |
| `default` 调 `logRequestError` | 复用 02 章已有的日志函数 | 详细错误只留服务端，客户端永远收到通用提示 |
| `currentUserID` 返回 `(uint, bool)` | 强制调用方处理失败 | 只返回一个 `uint` 时，0 很容易被直接传下去 |

`logRequestError` 定义在 `internal/handler/user.go`，同包可直接调用，不需要重复实现。

### 6.5 第 5 步：分页解析、固定排序、把占位换成读接口实现

本步用到的前置件已经全部落盘。6.1 给了骨架与 `bodylimit.go`，6.2 定了外键，6.3 给了 DTO，6.4 给了 `mapServiceError`。本步只新增 `paging.go`，再把三个读接口的 501 占位替换成真实实现。写接口的占位在本步保持不动，6.6 补文章、6.7 补评论。

**片段 A，完整文件：`internal/handler/paging.go`。**

```go
package handler

import (
	"fmt"
	"strconv"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/service"
)

const maxPageSize = 50

// parsePage 解析分页参数。缺省给默认值，越界返回 ErrInvalidInput。
func parsePage(c *gin.Context) (int, int, error) {
	page := 1
	pageSize := 10

	rawPage := c.Query("page")
	if rawPage != "" {
		v, err := strconv.Atoi(rawPage)
		if err != nil || v < 1 {
			return 0, 0, fmt.Errorf("%w: page must be a positive integer", service.ErrInvalidInput)
		}
		page = v
	}

	rawSize := c.Query("page_size")
	if rawSize != "" {
		v, err := strconv.Atoi(rawSize)
		if err != nil || v < 1 || v > maxPageSize {
			return 0, 0, fmt.Errorf("%w: page_size must be between 1 and 50", service.ErrInvalidInput)
		}
		pageSize = v
	}

	// 极大 page 会让 Offset 溢出 int，乘之前先算上界。
	if page > (1<<31-1)/pageSize {
		return 0, 0, fmt.Errorf("%w: page is too large", service.ErrInvalidInput)
	}
	return page, pageSize, nil
}
```

「缺省给默认值」与「越界返回 400」是两件事。参数没传用 1 与 10，传了非法值一律 400，与 `docs/00` 的合同一致。`strconv.Atoi` 会把 `abc`、空串、带空格的值挡掉 [来源: https://pkg.go.dev/strconv]。最后那个乘法上界检查容易被漏掉，`(page-1)*pageSize` 在 32 位整型的边界上可能溢出，service 里 3.6 节的校验接不住一个已经溢出的值。

排序片段写成常量，禁止把 `c.Query("sort")` 拼进 `Order()`。用户输入直接进 SQL 就是注入口。

**增量修改**：把两个排序常量加到 `internal/service/article.go` 的包级，再替换 3.2 与 5.2 的 `Order()`。

```go
// 增量修改：两个常量放在 internal/service/article.go 的包级。
const articleListOrder = "created_at DESC, id DESC"
const commentListOrder = "created_at ASC, id ASC"
```

3.2 的 `List` 与 5.2 的 `List` 把 `Order("...")` 换成对应常量。带上 `id` 作为第二排序键，同一秒内创建的记录顺序才稳定，3.6 节已经讲过原因。

**片段 B，增量修改：替换 `internal/handler/article.go` 里的 `List` 与 `Detail` 两个占位方法**。在该文件 import 块补 `strconv`，原有的 `net/http` 与 `forum-backend/pkg/response` 继续保留。`parsePage` 来自本步片段 A，`mapServiceError` 来自 6.4，`toArticleResp` 与 `articleListResp` 分别来自 3.8 与 6.3，全部已经存在。

```go
// List 处理 GET /api/articles。
func (h *ArticleHandler) List(c *gin.Context) {
	page, pageSize, err := parsePage(c)
	if err != nil {
		mapServiceError(c, "list articles", err)
		return
	}

	articles, total, err := h.svc.List(c.Request.Context(), page, pageSize)
	if err != nil {
		mapServiceError(c, "list articles", err)
		return
	}

	list := make([]articleResp, 0, len(articles))
	for _, a := range articles {
		list = append(list, toArticleResp(a))
	}

	resp := articleListResp{
		List:     list,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}
	response.OK(c, resp)
}

// Detail 处理 GET /api/articles/:id。
func (h *ArticleHandler) Detail(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "id 格式不正确")
		return
	}
	a, err := h.svc.Detail(c.Request.Context(), uint(id))
	if err != nil {
		mapServiceError(c, "article detail", err)
		return
	}
	response.OK(c, toArticleResp(a))
}
```

做法是先把 6.1 里那两行占位方法删掉，再贴入上面这段。别追加第二个同名方法，Go 会报重复定义。

| 代码 | 作用 | 为什么这样设计 |
| --- | --- | --- |
| `parsePage(c)` | 在 handler 边界校验 | service 里还有一次兜底，绕过 HTTP 的内部调用也拦得住 |
| `mapServiceError(...)` | 统一错误映射 | 所有接口一个口径，6.4 的函数 |
| `c.Request.Context()` | 把请求 ctx 传下去 | 客户端断开时数据库查询跟着取消 |
| `strconv.ParseUint(c.Param("id"), 10, 64)` | 路径参数转 uint | 直接把字符串拼进查询条件会引入注入风险，非法值在 handler 边界挡掉 |
| `make(..., 0, len(articles))` | 长度 0 容量预分配 | 输出 `[]` 而非 `null`，也避免多次扩容 |
| `for ... append` | 逐个转 DTO | 显式循环，字段转换一眼可查 |
| `response.OK(c, resp)` | 只交 DTO 给响应包 | 与 02 章 8.0 的约定一致 |

**片段 C，增量修改：替换 `internal/handler/comment.go` 里的 `List` 占位方法**。给该文件 import 块补 `net/http`、`strconv`、`forum-backend/pkg/response`，原有的 `gin` 与 `service` 保留。`toCommentResp` 与 `commentListResp` 来自 6.3，`h.svc.List` 是仓库已有的 service 方法。

```go
// List 处理 GET /api/articles/:id/comments。
func (h *CommentHandler) List(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "id 格式不正确")
		return
	}
	page, pageSize, err := parsePage(c)
	if err != nil {
		mapServiceError(c, "list comments", err)
		return
	}
	list, total, err := h.svc.List(c.Request.Context(), uint(id), page, pageSize)
	if err != nil {
		mapServiceError(c, "list comments", err)
		return
	}
	items := make([]commentResp, 0, len(list))
	for _, m := range list {
		items = append(items, toCommentResp(m))
	}
	resp := commentListResp{
		List:     items,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}
	response.OK(c, resp)
}
```

| 代码 | 作用 | 为什么这样设计 |
| --- | --- | --- |
| `Where("article_id = ?", ...)` 由 service 负责 | 评论列表只含指定文章 | handler 不写 SQL，过滤条件留在 service |
| `commentListResp` | 与文章列表同样的分页形状 | 客户端不用为两个列表写两套解析逻辑 |
| `make([]commentResp, 0, len(list))` | 空结果输出 `[]` | `nil` 切片会被序列化成 `null`，见 6.3 |

**验证第 5 步**。先跑构建，再启动服务，另开一个终端发四条请求。

```powershell
go build ./...
go run ./cmd/server
```

```powershell
curl.exe -i "http://localhost:8080/api/articles?page=1&page_size=10"
curl.exe -i "http://localhost:8080/api/articles?page=abc"
curl.exe -i "http://localhost:8080/api/articles/1/comments"
curl.exe -i -X POST http://localhost:8080/api/articles
```

预期依次是 200 且 `list` 为 `[]`、400、200 且 `list` 为 `[]`、501。第四条在 6.5 结束时仍是占位状态，带 token 也一样；6.6 会把它换成实现。

文章的写接口在紧随其后的 6.6 接通，评论的写接口在 6.7 接通：作者 id 一律走 `currentUserID(c)`（6.4 给出的函数），请求体只定义服务端允许客户端决定的字段，不出现 `user_id`。两步都给了 service 与 handler 的完整代码，不用往后翻。

### 6.6 第 6 步：把文章的两个写接口占位换成实现

6.5 结束时，三个公开读接口已经能返回 200，`POST /api/articles` 与 `DELETE /api/articles/:id` 却仍是 `notImplemented(c, ...)`，返回 501。第 9 节验收第 1 步直接让发创建请求，这一节把中间缺的那一步补齐。

按「一次只做一个模块」的节奏，本步只做文章的 `Create` 与 `Delete`。评论的 `CommentHandler.Create`、`CommentHandler.Delete`，以及 `CommentService` 里对应的两个方法放在紧随其后的 6.7，本步不给实现，也别提前写。做完本步，验收第 1、2、3、6、7、8 步能跑；第 4、5 步等 6.7 落盘后再跑。

#### 6.6.1 先想清楚：作者 id 到底从哪来

写接口比读接口多三件事：判断谁在发、读请求体、把 service 的错误翻译成 HTTP。第一件最容易出错，先把客户端能碰到的入口逐个过一遍。

| 客户端能控制的东西 | 能不能当作者 id | 原因 |
| --- | --- | --- |
| 请求体里的 `user_id` | 不能 | 谁都能改成别人的 id，等于任意冒充发帖 |
| URL 路径或 query 里的参数 | 不能 | 同样可伪造，而且路径里那个是文章 id，语义也不是作者 |
| `Authorization: Bearer <token>` | 能 | 服务端签发并校验签名，客户端改不动里面的内容 |
| Gin context 里的 `userID` | 能 | 02 章 `JWTAuth` 用 `c.Set("userID", userID)` 放进去的，值来自 `jwt.Parse` |

链路因此固定下来：token → 中间件解析 → Gin context → handler 读出来 → service 写进 `UserID`。客户端提交的 `user_id` 连被读取的机会都没有。

有个新手容易误解的点：Go 的 `encoding/json` 反序列化时默认忽略结构体里不存在的字段，不会报错 [来源: https://pkg.go.dev/encoding/json]。所以请求体里带 `user_id` 时，`ShouldBindJSON` 照样成功，那个值只是被丢掉了。安全性来自 DTO 里根本没有这个字段，绑定器并不会因此报错。验收时也不能只看返回 200，要去数据库查 `articles.user_id` 到底写成了谁，6.6.5 给了具体做法。

service 那头还有一道：`Create` 把 `userID` 写进 `model.Article.UserID`，`Delete` 把 `user_id` 放进 `WHERE id = ? AND user_id = ?`。handler 就算哪天写错，数据库这一层也不会让 B 删掉 A 的文章。

想要"带未知字段就报错"的显式拒绝，可以打开 Gin 的 `binding.EnableDecoderDisallowUnknownFields`，它会让带 `user_id` 的请求直接得到 400。两种做法都安全，本项目先用静默忽略，因为它对客户端多传的无关字段更宽容。别把开关打开之后忘了同步改验收脚本。

#### 6.6.2 片段 A：给 `dto.go` 追加请求结构体

增量修改：追加到 `internal/handler/dto.go` 末尾。`dto.go` 已 import `time` 与 `model`，下面这段不用新 import，也不要改动文件里已有的任何结构体。

```go
// 增量修改：追加到 internal/handler/dto.go。

// createArticleReq 是发布文章时允许客户端提交的字段。
// 这里刻意只有 Title 与 Content，不写 UserID：
// 作者只能来自 JWT，多留一个字段就等于给伪造身份开了入口。
// 类型名小写开头，因为它只在 handler 包内部用，不需要导出。
type createArticleReq struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}
```

| 写法 | 为什么这样设计 |
| --- | --- |
| 只有两个字段 | 请求体的字段集合由服务端定义，客户端多传什么都不会进入 service |
| `json:"title"` / `json:"content"` | 显式写出 JSON 键名。Go 默认按字段名匹配，不写 tag 会去找 `Title` 而不是 `title` |
| 用 `string` 不用 `*string` | 缺字段与空字符串在 service 里都会被 `ErrInvalidInput` 挡掉，不需要区分两者 |
| 不写 `binding:"required"` | 必填与空白判断统一放在 `Create` 里，绕过 HTTP 的内部调用也拦得住，见 2.2 |

最后一条是刻意的：Gin 的 `binding:"required"` 挡得住缺字段，挡不住 `"title":"   "` 这种空白串。校验只放一处，就不会出现"handler 说合法、service 说非法"两套口径。

#### 6.6.3 片段 B：替换 `article.go` 的两个占位方法

先把 6.1 A 留在 `internal/handler/article.go` 末尾的这两行删掉：

`func (h *ArticleHandler) Create(c *gin.Context) { notImplemented(c, "create article") }`

`func (h *ArticleHandler) Delete(c *gin.Context) { notImplemented(c, "delete article") }`

再把下面这段贴进去。**这个文件不用加任何 import**：`net/http`、`strconv`、`gin`、`service`、`response` 在 6.5 B 之后都已经在 import 块里。`createArticleReq` 来自片段 A，`currentUserID` 与 `mapServiceError` 来自 6.4，`toArticleResp` 来自 3.8，`Create` 与 `Delete` 是 2.2 与第 4 节的 service 方法。删掉两行占位后，`notImplemented` 这个文件里不再被任何方法调用，函数本身可以留着，编译器不会报未使用函数；想清理掉也行。

**增量修改**：替换 `internal/handler/article.go` 里 `Create` 与 `Delete` 两个占位方法。

```go
// 增量修改：替换 internal/handler/article.go 里 Create 与 Delete 两个占位方法。

// Create 处理 POST /api/articles。
// 这个路由挂在 auth 分组里，鉴权已由 JWTAuth 完成；
// 下面再取一次 userID 是为了拿到具体的值，供 service 写入数据库。
func (h *ArticleHandler) Create(c *gin.Context) {
	// 从 Gin context 取身份。GetUint 在类型不对或没值时返回 0，
	// currentUserID 把 0 判成失败，避免把 0 号用户写进数据库。
	userID, ok := currentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	// 只往两个字段的结构体里绑。请求体里额外的 user_id 之类字段
	// 会被 encoding/json 忽略，不会进入后面的 service 调用。
	var req createArticleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		// JSON 语法错、字段类型错、body 超过 limitBody 的 1 MiB，都落在这里。
		response.Error(c, http.StatusBadRequest, "请求体格式不正确")
		return
	}

	// userID 是第三个参数，来自 token；title 与 content 才是客户端给的。
	a, err := h.svc.Create(c.Request.Context(), userID, req.Title, req.Content)
	if err != nil {
		mapServiceError(c, "create article", err)
		return
	}

	// 只把 DTO 交给响应包，数据库模型不会直接出现在响应里。
	response.OK(c, toArticleResp(a))
}

// Delete 处理 DELETE /api/articles/:id。
func (h *ArticleHandler) Delete(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	// 路径参数先转成数字。非法 id 在 handler 边界挡掉，
	// 不把字符串拼进 SQL 条件。
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "id 格式不正确")
		return
	}

	// 所有权判断在 service 的 WHERE 里完成，handler 不查库。
	if err := h.svc.Delete(c.Request.Context(), uint(id), userID); err != nil {
		mapServiceError(c, "delete article", err)
		return
	}

	// 当前 response 包只有 OK(200)，所以删除成功返回 200 且 data 为 null。
	// 00 章合同里的 204 属于目标状态，要等响应包加 NoContent 之后一起改。
	response.OK(c, nil)
}
```

| 代码 | 作用 | 为什么这样设计 |
| --- | --- | --- |
| `currentUserID(c)` | 读出作者 id 并判 0 | 6.4 的函数，把 `GetUint` 静默返回 0 的坑显式化 |
| `if !ok { 401 }` | 兜底分支 | 正常请求由 `JWTAuth` 拦下，这一支只在路由漏挂中间件时生效 |
| `c.ShouldBindJSON(&req)` | 把 JSON 绑到只有两个字段的结构体 | 字段集合由服务端定，额外的 `user_id` 被忽略 |
| 绑定失败返回 400 | 统一在 handler 边界 | 语法错、类型错、超 1 MiB 都是客户端问题，不进 service |
| `h.svc.Create(ctx, userID, req.Title, req.Content)` | 调用 service | 身份与内容分开传，service 签名 2.2 已经定好，本步不用改 service |
| `mapServiceError(c, "create article", err)` | 统一错误映射 | 空白标题得到 400，外键错得到 400，其余 500，口径与读接口一致 |
| `response.OK(c, toArticleResp(a))` | 返回新建文章 | 与 `List`、`Detail` 同一个 DTO，客户端解析逻辑复用 |
| `strconv.ParseUint(..., 10, 64)` | 路径 id 转数字 | 和 `Detail` 保持同一写法，非法值在边界挡掉 |
| `h.svc.Delete(ctx, uint(id), userID)` | 事务删除并失效缓存 | 第 4 节的 `Delete` 已把所有权放进 `WHERE`，6.4 选的隐藏资源策略得到 404 |
| `response.OK(c, nil)` | 200 加 `data: null` | 当前响应包没有 204，先按 200 判定 |

两个方法都没有直接碰 `s.db` 或 `s.rdb`，handler 依旧不认识 GORM 与 Redis。第 7 节把 `rdb` 放进 `ArticleService` 结构体时，本步写的这两个方法一个字都不用改，这也是 7.2 反复强调的构造注入带来的好处。

#### 6.6.4 这一步的响应长什么样

`POST /api/articles` 成功时是 HTTP 200，`code` 为 0，`data` 大致是：

```json
{
  "code": 0,
  "message": "ok",
  "data": {
    "id": 1,
    "title": "第一篇",
    "content": "正文内容",
    "created_at": "2026-11-07T14:20:00+08:00"
  }
}
```

三点要说清，免得对着响应找半天：

1. **状态码是 200**。`pkg/response.OK` 固定发 200。00 章合同表里的 201 是目标状态，得先给响应包加 `Created`，再同步改 handler、测试和验收脚本。你引用的那句「期望 200（响应包扩展后按 201 判定）」说的就是这件事，本步不改响应包。
2. **响应里没有 `user_id`，也没有 `author`**。`articleResp` 只定义了 `id`、`title`、`content`、`author`、`created_at`，3.8 定 DTO 时就没给 `user_id` 留位置。`Author` 是指针且带 `omitempty`，而 `ArticleService.Create` 只调用了 GORM 的 `Create`，没有 `Preload("User")`，返回的 `a.User` 是零值，`toArticleResp` 里的 `a.User.ID != 0` 不成立，`author` 键就被省略了。想让新建响应带作者，可以在 service 创建成功后回查一次，或者客户端发完再请求详情；这属于下一轮改进，本模块不做。
3. **「不接受伪造的 `user_id`」在两层成立**。请求侧：DTO 没有这个字段，值进不来。响应侧：`articleResp` 没有这个字段，值出不去。真要验，得看数据库，光看响应看不出来。

`DELETE` 成功是 200 且 `data` 为 `null`；目标状态 204 同理，等响应包加 `NoContent`。

#### 6.6.5 验证第 6 步

先格式化并构建。仓库里 `dto.go`、`article.go` 有未 `gofmt` 的历史痕迹，落地后跑一次格式化再看差异。

```powershell
gofmt -w internal/handler/dto.go internal/handler/article.go
go build ./...
go vet ./...
go run ./cmd/server
```

启动前确认 MySQL 与 Redis 都起着。当前 `main.go` 调了 `InitRedis`，连不上会直接退出，这是 7.2 定的启动硬依赖。

**第 1 步，准备两个账号。** 用 02 章的注册接口建 A 与 B，再登录拿两个 token。下面用 PowerShell 变量接住，token 很长，别手抄。

```powershell
$base = "http://localhost:8080"
curl.exe -s -X POST "$base/api/register" -H "Content-Type: application/json" -d '{\"username\":\"alice\",\"password\":\"pass1234\"}'
curl.exe -s -X POST "$base/api/register" -H "Content-Type: application/json" -d '{\"username\":\"bob\",\"password\":\"pass1234\"}'
$tokenA = (curl.exe -s -X POST "$base/api/login" -H "Content-Type: application/json" -d '{\"username\":\"alice\",\"password\":\"pass1234\"}' | ConvertFrom-Json).data.token
$tokenB = (curl.exe -s -X POST "$base/api/login" -H "Content-Type: application/json" -d '{\"username\":\"bob\",\"password\":\"pass1234\"}' | ConvertFrom-Json).data.token
```

**关于 JSON 里的 `\"`**。上面几条命令刻意把双引号写成 `\"`。Windows 的 PowerShell 把单引号包裹的 JSON 传给 `curl.exe` 时，命令行解析会吃掉双引号，服务端收到的是 `{title:第一篇}` 这种非法 JSON，绑定阶段直接失败。本机实测过三种写法：单引号原样 JSON 会丢引号，写成 `\"` 能保住完整 JSON，把 JSON 存成文件再用 `-d @body.json` 也能保住。02 章的示例是 Git Bash 语法，单引号不需要转义，两套写法别混用。

**第 2 步，带伪造的 `user_id` 发文章。** 用 A 的 token，请求体里塞一个 B 的 id（先查 `SELECT id FROM users WHERE username='bob'` 拿到具体数字，这里假设是 2）。

```powershell
curl.exe -i -X POST "$base/api/articles" -H "Content-Type: application/json" -H "Authorization: Bearer $tokenA" -d '{\"title\":\"第一篇\",\"content\":\"正文内容\",\"user_id\":2}'
```

期望 HTTP 200，`data.id` 是一个正整数。这就是第 9 节验收第 1 步能跑通的样子。

**第 3 步，去数据库确认作者。** 这一步不能省。

```sql
SELECT a.id, a.title, a.user_id, u.username
FROM articles a JOIN users u ON u.id = a.user_id
ORDER BY a.id DESC LIMIT 5;
```

期望这一行的 `user_id` 是 A 的 id、`username` 是 `alice`。如果写成 `bob`，说明某个环节把请求体的值用了回去，立刻回头查 `createArticleReq` 与 `Create` 的调用参数。也可以把请求体里的 `user_id` 换成 999999 再发一次，它同样应该被忽略而不会报外键错，这条能顺带证明字段确实没被读到。

**第 4 步，把读链路串起来。**

```powershell
curl.exe -i "$base/api/articles?page=1&page_size=10"
curl.exe -i "$base/api/articles/1"
```

列表里应出现刚建的文章，`total` 加 1；详情连发两次响应一致。

**第 5 步，验所有权与删除。**

```powershell
curl.exe -i -X DELETE "$base/api/articles/1" -H "Authorization: Bearer $tokenB"
curl.exe -i -X DELETE "$base/api/articles/1" -H "Authorization: Bearer $tokenA"
```

第一条期望 404（`ErrNotFound` 经 6.4 映射），A 的文章还在。第二条期望 200 且 `data` 为 `null`。删完用 `SELECT * FROM articles WHERE id = 1` 确认行没了，或用 `GET /api/articles` 看列表。

**删除后 GET 详情现在返回 404。** 早期 `queryDetail` 抛 `gorm.ErrRecordNotFound`，`mapServiceError` 没有对应 case，会落到 `default` 返回 500。03a 章 4.5 记录过这个缺陷，6.4 与 6.5 给了转换与兜底修法，源码现已落地。本步仍建议同时用列表或 SQL 核对删除结果，多一份证据没坏处。

**第 6 步，顺手过一遍边界。**

| 请求 | 期望 | 拦在哪里 |
| --- | --- | --- |
| 不带 `Authorization` 发 POST | 401 | `JWTAuth` 中间件，handler 的 401 兜底不会触发 |
| `{"title":"   ","content":"x"}` | 400 | `Create` 的 `ErrInvalidInput`，经 `mapServiceError` |
| body 超过 1 MiB | 400 | `limitBody` 的 `MaxBytesReader`，`ShouldBindJSON` 报错 |
| `DELETE /api/articles/abc` | 400 | `strconv.ParseUint` |
| `DELETE /api/articles/999999`（自己的 token） | 404 | `Delete` 的 `RowsAffected == 0` 返回 `ErrNotFound` |

**第 7 步，不启服务也能先跑的两条单测。** 上面几步都要 MySQL 与 Redis，有时候只想先确认"伪造的 `user_id` 到底进没进来"。下面这个测试文件只碰 DTO 与序列化，不起数据库，落盘后立刻能跑。

完整文件：`internal/handler/article_write_test.go`。

```go
// 完整文件：internal/handler/article_write_test.go。
package handler

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/model"
)

// TestCreateArticleReqHasNoUserIDField 从两个角度守住作者 id：
// 结构体里不许出现 UserID 字段，带 user_id 的请求体也要能正常绑定。
func TestCreateArticleReqHasNoUserIDField(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// reflect 逐个检查字段。只测绑定成功还不足以证明值进不来，
	// 因为有人可能后来给 DTO 加了 UserID 却忘了改测试。
	typ := reflect.TypeOf(createArticleReq{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Name == "UserID" || f.Tag.Get("json") == "user_id" {
			t.Fatalf("createArticleReq must not expose %s", f.Name)
		}
	}

	// 用一个带伪造 user_id 的请求体走一遍真实的 Gin 绑定。
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	body := `{"title":"第一篇","content":"正文内容","user_id":999999}`
	c.Request = httptest.NewRequest("POST", "/api/articles", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	var req createArticleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		t.Fatalf("ShouldBindJSON should ignore unknown fields: %v", err)
	}
	if req.Title != "第一篇" || req.Content != "正文内容" {
		t.Fatalf("unexpected bound values: %+v", req)
	}
}

// TestArticleRespHasNoUserID 确认响应侧同样不给 user_id 留出口。
func TestArticleRespHasNoUserID(t *testing.T) {
	a := &model.Article{}
	a.ID = 1
	a.Title = "第一篇"
	a.Content = "正文内容"
	a.CreatedAt = time.Now()

	data, err := json.Marshal(toArticleResp(a))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "user_id") || strings.Contains(string(data), "author") {
		t.Fatalf("response leaked an author field: %s", string(data))
	}
}
```

```powershell
go test -run 'CreateArticleReq|ArticleResp' -v ./internal/handler
```

两条 PASS 说明：DTO 里确实没有 `user_id` 字段，带伪造值的请求体照样能绑定，序列化后的响应里也没有 `user_id` 与 `author`。这条命令不需要起服务，改完代码先跑它最省事。

**它的边界要记住**：这个测试没有调用 `ArticleHandler.Create`，也就没有碰数据库。`articles.user_id` 最终写成谁，仍然要按第 3 步查 SQL 才算证据。两条 PASS 只覆盖了 handler 边界，别把它当成接口已经端到端验收。

#### 6.6.6 本步的边界

| 状态 | 内容 |
| --- | --- |
| 本步接通 | 文章的创建与删除，`POST /api/articles` 与 `DELETE /api/articles/:id` 不再返回 501 |
| 不启服务可验 | `article_write_test.go` 的两条单测，覆盖 DTO 不含 `user_id`、响应不含作者字段 |
| 紧随其后接通 | 评论的 `Create` 与 `Delete`，见 6.7 |
| 已知缺陷 | 删除后再 GET 详情得到 500，修法在 03a 章 4.5、6.5 |
| 目标状态未落地 | 创建 201、删除 204，要等 `pkg/response` 扩展 |
| 新建响应的 `author` 为空 | `ArticleService.Create` 没有回查 `User`，见 6.6.4 |

**一处历史勘误，留档不改**：6.1 B 给的 `comment.go` 骨架里，两个写方法写成 `notImplemented` 的 501 占位；本轮开工前仓库里却是 `func (h *CommentHandler) Create(c *gin.Context) {}` 这种空函数体，会静默返回 200 空响应，比 501 更容易被误判成「接口已经好了」。上一版文件清单把四个写接口一律写成「仍返回 501」，对评论这两个不成立。6.7 已把这两个空方法连同 `CommentService` 的两个方法一起补上，这段记录只用来解释旧文档为什么会写错。

### 6.7 第 7 步：把评论的两个写接口接通

6.6 跑通之后，`POST /api/articles/:id/comments` 与 `DELETE /api/comments/:id` 还停在空方法。这一步把它们接通，同时把 1.2 定的命名口径落到评论侧。落盘顺序是 A → B → C：先 service，再 DTO，最后 handler。`router.go` 一行都不用改，因为 6.1 D 注册的就是 `commentHandler.List`、`commentHandler.Create`、`commentHandler.Delete`。

#### 6.7.1 先想清楚：一条评论有几个输入

| 输入 | 从哪来 | 为什么不让客户端给 |
| --- | --- | --- |
| 文章 id | URL 路径 `/api/articles/:id/comments` | 请求体给 `article_id` 就能把评论挂到别人的文章下；路径由路由给定，客户端改不了归属 |
| 作者 id | JWT 中间件写进 Gin context | 请求体给 `user_id` 就是伪造身份，推演见 6.6.1 |
| 内容 | 请求体 `content` | 这是唯一该由客户端决定的字段 |
| 评论 id | `DELETE /api/comments/:id` 路径，或数据库自增 | 只在删除时出现，且必须是评论自己的 id |

结论：`createCommentReq` 只有 `content` 一个字段。`encoding/json` 默认忽略未知字段，请求体里多带 `article_id`、`user_id` 时 `ShouldBindJSON` 照样成功，不会报错。所以**看到 200 不能证明防线生效**，6.7.7 要查库确认 `comments.user_id` 与 `comments.article_id`。

还有一个容易绕晕的点：创建路由里的 `:id` 是**文章 id**，删除路由里的 `:id` 是**评论 id**。两个方法都写 `c.Param("id")`，含义完全不同，注释里必须写明，否则以后加缓存或权限校验时极易拿错。

#### 6.7.2 设计推演：创建前要不要先查文章

先别写代码，把几种做法按「失败会怎样」推一遍：

| 做法 | 文章不存在时 | 并发下文章被删 | 结论 |
| --- | --- | --- | --- |
| 直接 `INSERT`，只靠外键 | MySQL 报外键错误，`mapServiceError` 有 `gorm.ErrForeignKeyViolated` 分支，映射成 400 | 外键同样挡住 | 脏数据进不来，但客户端分不清「文章没了」和「请求格式不对」 |
| 只 `First(&article, id)` 再 `INSERT` | 能返回 404 | 查询与插入之间有时间窗，先查挡不住 | 语义清楚，但不是并发保证 |
| 两者都做（本步采用） | 先查返回 404 | 外键做最终兜底 | 语义与正确性都要 |

第三行有个必须处理的具体坑：`mapServiceError` 只认 `service.ErrNotFound`，不认 `gorm.ErrRecordNotFound`（03a 章 4.5 记录过这个缺陷）。如果 `First` 的错误原样抛出去，会落到 `default` 分支变成 500。所以 `CommentService.Create` 要在 service 边界用 `errors.Is` 判断并转成 `ErrNotFound`。**本模块不改 `handler/errors.go` 的公共映射**，文章的 `Detail` 当时也会因同一原因返回 500，那属于 03a 章的加固范围，别顺手混在一起改。源码里这两处现在都已修好，`queryDetail` 会转换错误，`mapServiceError` 也补了全局兜底，查不存在的文章返回 404。

删除走同一套思路：所有权放进 `WHERE id = ? AND user_id = ?`，`RowsAffected == 0` 一律返回 `ErrNotFound`，「不存在」与「不是你的」对外都是 404，不泄露归属。

#### 6.7.3 片段 A，完整文件：`internal/service/comment.go`

**完整文件**：整体覆盖 `internal/service/comment.go`。5.1 只给了类型与构造函数，这里补上 `List`、`Create`、`Delete`；`commentListOrder` 常量也从 `article.go` 搬过来，各管各的排序。

```go
// Package service 的评论部分：与 article.go 同属 service 包，方法名统一用动词。
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"forum-backend/internal/model"
)

// 评论列表的固定排序：按创建时间升序，同秒创建的记录用 id 决定先后。
// 放在本文件而不是 article.go，因为它只服务评论查询。
const commentListOrder = "created_at ASC, id ASC"

// CommentService 评论业务逻辑。
type CommentService struct {
	db *gorm.DB
}

func NewCommentService(db *gorm.DB) *CommentService {
	return &CommentService{db: db}
}

// List 按文章分页查评论，返回当前页与符合条件的总数。
// 方法名统一用动词：receiver 已经说明这是评论 service，
// 按哪篇文章过滤由 articleID 参数表达，不必再写进方法名。
func (s *CommentService) List(ctx context.Context, articleID uint, page, pageSize int) ([]*model.Comment, int64, error) {
	// 分页兜底校验放在 service，将来有内部调用绕过 handler 时也拦得住。
	if page < 1 || pageSize < 1 || pageSize > 50 {
		return nil, 0, fmt.Errorf("%w: invalid pagination", ErrInvalidInput)
	}

	// Count 与 Find 各自开一条新链，不复用带 Where 的中间变量，避免条件叠加。
	var total int64
	if err := s.db.WithContext(ctx).Model(&model.Comment{}).
		Where("article_id = ?", articleID).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count comments of article %d: %w", articleID, err)
	}
	if total == 0 {
		return []*model.Comment{}, 0, nil
	}

	// 初始化成空切片：页越界时 Find 不填充，JSON 仍是 [] 而不是 null。
	list := make([]*model.Comment, 0)
	err := s.db.WithContext(ctx).
		Preload("User").
		Where("article_id = ?", articleID).
		Order(commentListOrder).
		Offset((page - 1) * pageSize).Limit(pageSize).
		Find(&list).Error
	if err != nil {
		return nil, total, fmt.Errorf("list comments of article %d: %w", articleID, err)
	}
	return list, total, nil
}

// Create 在指定文章下发表一条评论。
// articleID 来自 URL 路径，userID 来自 JWT，content 才是请求体给的。
func (s *CommentService) Create(ctx context.Context, articleID, userID uint, content string) (*model.Comment, error) {
	// 与文章 Create 同样的规则：去空白、判空、限长，都留在 service 层。
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, fmt.Errorf("%w: content is required", ErrInvalidInput)
	}
	if len(content) > 10000 {
		return nil, fmt.Errorf("%w: content too long", ErrInvalidInput)
	}

	// 先查文章是否存在，目的是给客户端一个明确的 404。
	// 这一步不是并发安全的最终保证：查完到插入之间文章可能被删掉，
	// 真正的兜底是 comments.article_id 上的 RESTRICT 外键，见 03 章 6.2。
	var article model.Article
	if err := s.db.WithContext(ctx).First(&article, articleID).Error; err != nil {
		// gorm.ErrRecordNotFound 没有在 mapServiceError 里映射，
		// 直接抛出去会变成 500。这里在 service 边界转成项目统一的 ErrNotFound。
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("check article %d: %w", articleID, ErrNotFound)
		}
		return nil, fmt.Errorf("check article %d: %w", articleID, err)
	}

	m := &model.Comment{
		ArticleID: articleID,
		UserID:    userID,
		Content:   content,
	}
	if err := s.db.WithContext(ctx).Create(m).Error; err != nil {
		// 文章恰好在查完之后被删会触发外键错误，由 mapServiceError 映射成 400。
		return nil, fmt.Errorf("create comment for article %d: %w", articleID, err)
	}
	return m, nil
}

// Delete 只允许评论作者删除自己那条评论。
func (s *CommentService) Delete(ctx context.Context, id, userID uint) error {
	// 所有权直接放进 WHERE：数据库一次判断，避免「先查再删」之间的竞态。
	res := s.db.WithContext(ctx).
		Where("id = ? AND user_id = ?", id, userID).
		Delete(&model.Comment{})
	if res.Error != nil {
		return fmt.Errorf("delete comment %d: %w", id, res.Error)
	}
	if res.RowsAffected == 0 {
		// 不存在与无权删除对外合并成 404，不泄露这条评论是否属于别人。
		return ErrNotFound
	}
	return nil
}
```

**逐行拆解，只看新增的两段**：

| 代码 | 作用 | 为什么这样设计 |
| --- | --- | --- |
| `strings.TrimSpace(content)` + 判空 + 限长 | 与文章 `Create` 同一套规则 | 校验留在 service，绕过 HTTP 的内部调用也拦得住 |
| `First(&article, articleID)` | 确认文章存在 | 给客户端明确的 404，而不是笼统的 400 |
| `errors.Is(err, gorm.ErrRecordNotFound)` | 识别「查无此文章」 | 支持被 `%w` 包过的错误，别用 `==` |
| `return ... ErrNotFound` | 在 service 边界转成项目自己的错误 | 避免落到 `mapServiceError` 的 default 变成 500 |
| `Create(m)` 的错误包装后返回 | 交给 handler 映射 | 文章恰好被删时是外键错误，会被映射成 400 |
| `Where("id = ? AND user_id = ?", ...)` | 一次判断所有权 | 数据库原子完成，没有「先查再删」的竞态 |
| `res.RowsAffected == 0` | 判断到底删掉没有 | GORM 的 `Delete` 不会因为没匹配到行而报错 |
| `commentListOrder` 放在本文件 | 归属对齐 | 文章的排序常量留在 `article.go`，互不串味 |

#### 6.7.4 片段 B，增量修改：`internal/handler/dto.go`

**增量修改**：追加到文件末尾，不用改 import，也不要动已有的 `createArticleReq`。

```go
// createCommentReq 是发表评论的请求体。
// 文章 id 来自 URL 路径，作者 id 来自 JWT，所以请求体只需要 content。
// 多出来的 article_id、user_id 会被 encoding/json 忽略，不会进入 service。
type createCommentReq struct {
	Content string `json:"content"`
}
```

`createArticleReq` 与 `createCommentReq` 是成对的，名字里保留 `Article` / `Comment` 是因为它们同属 `handler` 包，不区分就会撞名。这与 1.2 说的「方法名不重复 receiver」不冲突：结构体没有 receiver，只能靠名字区分类型。

#### 6.7.5 片段 C，完整文件：`internal/handler/comment.go`

**完整文件**：整体覆盖 `internal/handler/comment.go`，把 6.1 B 的骨架和 6.5 C 的列表实现一起换成最终版。

```go
package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/service"
	"forum-backend/pkg/response"
)

// CommentHandler 评论接口，只依赖 service，不认识 GORM。
// 方法名与 ArticleHandler 对齐：List / Create / Delete。
type CommentHandler struct {
	svc *service.CommentService
}

func NewCommentHandler(svc *service.CommentService) *CommentHandler {
	return &CommentHandler{svc: svc}
}

// List 处理 GET /api/articles/:id/comments。
// 这条路由里的 :id 是文章 id，不是评论 id。
func (h *CommentHandler) List(c *gin.Context) {
	// 列表是公开接口，不需要取 currentUserID。
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "id 格式不正确")
		return
	}
	page, pageSize, err := parsePage(c)
	if err != nil {
		mapServiceError(c, "list comments", err)
		return
	}
	list, total, err := h.svc.List(c.Request.Context(), uint(id), page, pageSize)
	if err != nil {
		mapServiceError(c, "list comments", err)
		return
	}

	// make 从空切片开始，保证无评论时响应是 [] 而不是 null。
	items := make([]commentResp, 0, len(list))
	for _, m := range list {
		items = append(items, toCommentResp(m))
	}
	resp := commentListResp{
		List:     items,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}
	response.OK(c, resp)
}

// Create 处理 POST /api/articles/:id/comments。
// 三个输入的来源要分清楚：文章 id 来自路径，作者 id 来自 JWT，内容来自请求体。
func (h *CommentHandler) Create(c *gin.Context) {
	// 路由挂在 JWTAuth 分组下；currentUserID 再兜一层，避免 id 为 0 时写出脏数据。
	userID, ok := currentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	// 这里的 :id 是文章 id。先转成数字，别把字符串拼进任何查询。
	articleID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "id 格式不正确")
		return
	}

	// 只往 content 一个字段的结构体里绑。请求体里伪造的 article_id、user_id
	// 会被 encoding/json 忽略，不会进入后面的 service 调用。
	var req createCommentReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, http.StatusBadRequest, "请求体格式不正确")
		return
	}

	m, err := h.svc.Create(c.Request.Context(), uint(articleID), userID, req.Content)
	if err != nil {
		mapServiceError(c, "create comment", err)
		return
	}

	// 新建时没有 Preload("User")，m.User 是零值，toCommentResp 会省略 author。
	response.OK(c, toCommentResp(m))
}

// Delete 处理 DELETE /api/comments/:id。
// 这条路由的 :id 是评论 id，与 Create 里的文章 id 不是一回事。
func (h *CommentHandler) Delete(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, http.StatusBadRequest, "id 格式不正确")
		return
	}

	// 所有权判断在 service 的 WHERE 里，handler 不查库。
	if err := h.svc.Delete(c.Request.Context(), uint(id), userID); err != nil {
		mapServiceError(c, "delete comment", err)
		return
	}

	// 当前 response 包只有 OK(200)，删除成功是 200 且 data 为 null。
	// 00 章合同里的 204 要等响应包扩展 NoContent 之后一起改。
	response.OK(c, nil)
}
```

**逐行拆解**：

| 代码 | 作用 | 为什么这样设计 |
| --- | --- | --- |
| `currentUserID(c)` | 取 JWT 里的作者 id | 路由已挂 `JWTAuth`，这里再兜一层，防止 id 为 0 时写出脏数据 |
| `strconv.ParseUint(c.Param("id"), 10, 64)` | 把路径参数转成数字 | handler 边界挡掉非法 id，不把字符串拼进任何查询 |
| `var req createCommentReq` + `ShouldBindJSON` | 只绑定 `content` | 请求 DTO 里没有身份字段，伪造值被 `encoding/json` 静默忽略 |
| `h.svc.Create(ctx, uint(articleID), userID, req.Content)` | 三个参数来源分开 | 位置固定后，service 不需要知道 HTTP，也不需要再校验身份 |
| `mapServiceError(c, "create comment", err)` | 统一错误映射 | `ErrNotFound` → 404、`ErrInvalidInput` → 400、外键错误 → 400 |
| `toCommentResp(m)` | 只把 DTO 交给响应包 | 数据库模型不直接出现在响应里 |
| `response.OK(c, nil)` | 删除成功 | 当前响应包只有 200，204 属目标状态 |

#### 6.7.6 装配核对：`router.go` 零改动

6.1 D 落盘的注册行本就是 `commentHandler.List`、`commentHandler.Create`、`commentHandler.Delete`（1.2 统一命名后），所以本步不用碰 `router.go`。改完 A、B、C 三个文件直接编译即可，别为了「改点什么」去动路由。

```powershell
gofmt -w internal/service/comment.go internal/handler/comment.go internal/handler/dto.go
go build ./...
go vet ./...
go test ./...
```

第一条 `gofmt -l` 应该没有输出。三条命令全绿才说明改名后的调用点都同步了；Go 是静态类型语言，漏改一个方法名会直接编译失败，这比运行时才发现要省事。

#### 6.7.7 验证第 7 步

**不启服务也能验的两条单测**。把下面内容存成 `internal/handler/comment_write_test.go`，它不连 MySQL、不连 Redis，只检查 DTO 防线。

**完整文件**：新建 `internal/handler/comment_write_test.go`。

```go
package handler

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"forum-backend/internal/model"
)

// TestCreateCommentReqHasNoIdentityFields 守住「身份字段不进请求体」这条规则。
func TestCreateCommentReqHasNoIdentityFields(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// reflect 逐个检查字段与 json tag，防止后来有人给 DTO 加上身份字段。
	typ := reflect.TypeOf(createCommentReq{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Name == "UserID" || f.Name == "ArticleID" {
			t.Fatalf("createCommentReq must not expose %s", f.Name)
		}
		tag := f.Tag.Get("json")
		if tag == "user_id" || tag == "article_id" {
			t.Fatalf("createCommentReq must not expose json tag %s", tag)
		}
	}

	// 带两个伪造字段走一遍真实的 Gin 绑定，确认它们被忽略而不是报错。
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	body := `{"content":"写得不错","article_id":999999,"user_id":888888}`
	c.Request = httptest.NewRequest("POST", "/api/articles/1/comments", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	var req createCommentReq
	if err := c.ShouldBindJSON(&req); err != nil {
		t.Fatalf("ShouldBindJSON should ignore unknown fields: %v", err)
	}
	if req.Content != "写得不错" {
		t.Fatalf("unexpected bound value: %+v", req)
	}
}

// TestCommentRespHasNoUserID 确认响应侧同样不给 user_id 留出口。
func TestCommentRespHasNoUserID(t *testing.T) {
	m := &model.Comment{}
	m.ID = 1
	m.ArticleID = 7
	m.Content = "写得不错"
	m.CreatedAt = time.Now()

	data, err := json.Marshal(toCommentResp(m))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "user_id") {
		t.Fatalf("response leaked user_id: %s", string(data))
	}
}
```

```powershell
go test -run 'CreateCommentReq|CommentResp' -v ./internal/handler
```

两条 PASS 说明 DTO 里没有 `article_id` / `user_id`，带伪造值的请求体照样能绑定，序列化后的响应里也没有 `user_id`。**它们没碰数据库**，所以 `comments.user_id` 最终写成谁，仍然要按下面第 3 步查 SQL 才算证据。

**端到端七步**（需要 MySQL 与 Redis 都已启动，服务在另一个终端保持运行）。PowerShell 里内联 JSON 的双引号要写成 `\"`，否则会被命令行解析吃掉，实测与原因见 6.6.5。

```powershell
go run ./cmd/server
```

1. 注册两个账号 A 与 B，拿到两份 token。

```powershell
curl.exe -s -X POST http://localhost:8080/api/register -H "Content-Type: application/json" -d '{\"username\":\"authorA\",\"password\":\"12345678\"}'
curl.exe -s -X POST http://localhost:8080/api/register -H "Content-Type: application/json" -d '{\"username\":\"authorB\",\"password\":\"12345678\"}'
```

2. 用 A 的 token 发一篇文章，记下返回的 `data.id` 作为 `article_id`。

```powershell
curl.exe -s -X POST http://localhost:8080/api/articles -H "Content-Type: application/json" -H "Authorization: Bearer {{tokenA}}" -d '{\"title\":\"第一篇\",\"content\":\"正文内容\"}'
```

3. **带两个伪造字段**发评论，这是本步最关键的一条。

```powershell
curl.exe -s -X POST "http://localhost:8080/api/articles/{{article_id}}/comments" -H "Content-Type: application/json" -H "Authorization: Bearer {{tokenA}}" -d '{\"content\":\"写得不错\",\"article_id\":999999,\"user_id\":888888}'
```

期望 200，响应 `data` 里有 `id`、`article_id`、`content`、`created_at`，没有 `user_id`。**只看响应不够**，到 MySQL 里查：

```sql
SELECT c.id, c.article_id, c.user_id, u.username
FROM comments c JOIN users u ON u.id = c.user_id
WHERE c.article_id = {{article_id}};
```

期望 `article_id` 是第 2 步那篇文章的 id、`user_id` 是 A 的 id、`username` 是 `authorA`。若出现 999999 或 888888，说明请求 DTO 被改坏或身份来源接错。记下 `c.id` 作为 `comment_id`。

4. 评论列表要能读到刚写的那条，`total` 为 1，`list` 是数组。

```powershell
curl.exe -s "http://localhost:8080/api/articles/{{article_id}}/comments?page=1&page_size=10"
```

5. 用 B 的 token 删 A 的评论，期望 404，数据库里那条记录仍在。

```powershell
curl.exe -i -X DELETE "http://localhost:8080/api/comments/{{comment_id}}" -H "Authorization: Bearer {{tokenB}}"
```

6. 换几组边界：不存在的文章发评论期望 404；`content` 为空白期望 400；不带 token 期望 401；`page=abc` 期望 400。

```powershell
curl.exe -i -X POST "http://localhost:8080/api/articles/999999/comments" -H "Content-Type: application/json" -H "Authorization: Bearer {{tokenA}}" -d '{\"content\":\"x\"}'
curl.exe -i -X POST "http://localhost:8080/api/articles/{{article_id}}/comments" -H "Content-Type: application/json" -H "Authorization: Bearer {{tokenA}}" -d '{\"content\":\"   \"}'
```

7. 用 A 的 token 删自己的评论，期望 200 且 `data` 为 `null`，再查一次列表 `total` 为 0、`list` 为 `[]`。

```powershell
curl.exe -i -X DELETE "http://localhost:8080/api/comments/{{comment_id}}" -H "Authorization: Bearer {{tokenA}}"
```

#### 6.7.8 本步的边界

| 状态 | 内容 |
| --- | --- |
| 本步接通 | 评论的创建、删除、列表；`CommentService.Create/Delete` 与 `CommentHandler.Create/Delete` 不再是空方法 |
| 不启服务可验 | `comment_write_test.go` 的两条单测 |
| 已修 | `gorm.ErrRecordNotFound` 在 `Create` 里转成 `ErrNotFound`，不存在的文章不会返回 500 |
| 已修（03a 加固落盘） | 文章 `Detail` 删后 GET 返回 404，`queryDetail` 转换错误且 `mapServiceError` 有兜底 |
| 目标状态未落地 | 创建 201、删除 204，要等 `pkg/response` 扩展 |
| 新建响应的 `author` 为空 | `Create` 没有 `Preload("User")`，与文章 `Create` 一致 |
| 缓存 | 评论没有缓存，本步也不加；03a 章的加固只针对文章详情 |

到这里，第 9 节验收里第 4、5 步不再是阻塞。删掉文章时评论会由 `ArticleService.Delete` 的事务先清掉，6.2 定的外键策略在这一步才真正有了可验证的写入路径。

### 6.8 文件清单与贯通判据

表格按落盘顺序排列，从上往下做，每一行引用的符号都在上一行结束时就绪。

| 步骤 | 文件 | 这一步做什么 | 落盘前的状态 | 完成判据 |
| --- | --- | --- | --- | --- |
| 6.1 A | `internal/handler/article.go` | 类型、构造函数、4 个 501 占位方法 | 可能是空文件或旧占位，用片段 A 整体覆盖 | `go build ./...` 通过 |
| 6.1 B | `internal/handler/comment.go` | 类型、构造函数、3 个 501 占位方法 | 可能已有空方法占位，用片段 B 整体覆盖 | 同上 |
| 6.1 C | `internal/router/bodylimit.go` | `limitBody` | 通常还没有，新建即可 | 同上 |
| 6.1 D | `internal/router/router.go` | 替换 `New` 函数体 | 可能已写了一半，用片段 D 整体替换函数体 | GET 列表得到 501 |
| 6.2 | `internal/model/models.go` | `Comment` 加 `Article` 关联与 RESTRICT | 已有三张表，缺 `Comment.Article` | `SHOW CREATE TABLE comments` 能看到外键 |
| 6.3 | `internal/handler/dto.go` | 追加 `commentResp`、两个列表 DTO、`toCommentResp` | 已含用户与文章 DTO | 响应里不出现 `model` 的原始字段 |
| 6.4 | `internal/handler/errors.go` | `mapServiceError`、`currentUserID` | 通常还没有，新建即可 | 所有 handler 共用一个映射 |
| 6.5 A | `internal/handler/paging.go` | `parsePage` | 通常还没有，新建即可 | `page=abc` 返回 400 |
| 6.5 A | `internal/service/article.go`、`comment.go` | 把 `Order()` 换成固定排序常量 | 两者已有基础版本，按 2、3、4、5 节增量修改 | `Order()` 只出现常量 |
| 6.5 B | `internal/handler/article.go` | 把 `List`、`Detail` 占位换成实现 | 6.1 A 已落盘骨架 | GET 列表与详情得到 200 |
| 6.5 C | `internal/handler/comment.go` | 把 `List` 占位换成实现 | 6.1 B 已落盘骨架 | GET 评论列表得到 200 |
| 6.6 A | `internal/handler/dto.go` | 追加 `createArticleReq` | 6.3 已落盘响应 DTO | `go build ./...` 通过 |
| 6.6 B | `internal/handler/article.go` | 把 `Create`、`Delete` 占位换成实现 | 6.5 B 已落盘读接口 | POST 得到 200，B 删 A 的文章得到 404 |
| 6.7 A | `internal/service/comment.go` | 完整覆盖：`List` / `Create` / `Delete` 加 `commentListOrder` | 6.5 已落盘列表方法 | `go build ./...` 通过 |
| 6.7 B | `internal/handler/dto.go` | 追加 `createCommentReq` | 6.6 A 已落盘 `createArticleReq` | `go build ./...` 通过 |
| 6.7 C | `internal/handler/comment.go` | 完整覆盖：填 `Create` / `Delete`，列表方法统一叫 `List` | 6.5 C 已落盘列表实现 | POST 评论 200，B 删 A 的评论 404 |
| 05 章 | `internal/repository/article.go`、`comment.go` | 接口抽象 | 目录里只有空的 `user.go` | 05 章做 mock 时再抽 |

repository 两项在表里保留，落地顺序上可以后放。当前 `service/user.go` 直接持有 `*gorm.DB`，文章与评论照同样写法先跑通，等 05 章做接口抽象与 mock 时再抽 repository。先把层抽干净再写业务，学习阶段容易卡在接口设计上。

### 6.9 必须验证的数据库边界

本节结束时文章与评论的创建、删除都已接通，四个写接口不再是 501 占位或空方法。最后一列标出每条边界什么时候验，别在还没落地的接口上反复排查。

| 边界 | 怎么造 | 期望 | 何时验 |
| --- | --- | --- | --- |
| 空标题、空白正文、超长字段 | 发 `{"title":"  ","content":"x"}` 与超长字符串 | 400，`service.ErrInvalidInput` | 本节，6.6.5 第 6 步已覆盖 |
| 请求体上限 | 发一个超过 1 MiB 的 body 到 `POST /api/articles` | 400，绑定阶段拦住，服务端不 OOM | 本节，6.6 落地后可验 |
| 分页参数非法 | `page=abc`、`page=0`、`page=-1`、`page=999999999`；`page_size=0`、`page_size=51` | 全部 400，`page=0` 不会静默返回第一页 | 本节，6.5 已覆盖 `page=abc` |
| 排序固定 | 代码审阅 `Order()` 的参数 | 只出现常量，没有 `c.Query` 拼接 | 本节 |
| 所有权 | 用两个 token，B 删 A 的文章 | 404，A 的数据仍在。本阶段统一隐藏资源 | 本节，6.6.5 第 5 步已覆盖 |
| 数据库故障 | 断开 MySQL 后 GET 列表 | 500，客户端只看到通用提示，服务端日志有原始错误 | 本节 |
| 事务原子 | 评论删除后故意让第二步失败 | 整笔回滚，文章与评论都恢复，不留半套数据 | 本节，6.7 接通后可通过删文章验证 |
| 外键生效 | 插一条 `article_id = 999999` 的评论，或向不存在的文章发评论 | 约束错误映射成 400，先查文章时返回 404 | 本节，6.7.7 第 6 步已覆盖 |
| 评论过滤与排序 | 先用 SQL 手工插两篇文章的评论，再 GET 评论列表 | 列表只含指定文章，按 `created_at, id` 稳定升序，无评论返回 `[]` | 本节 |
| `Preload` 没有 N+1 | 把 `db.go` 的 Logger 临时调成 Info，看一次列表请求的 SQL 条数 | 一条主查询加一条 `WHERE user_id IN (...)`，条数不随文章数增长 | 本节 |

最后一条别靠「感觉变快了」判断。GORM 的 `Preload` 官方描述是 eager loading 与 separate query [来源: https://gorm.io/docs/preload.html]，数 SQL 才是可复现的判据。

### 6.10 分页快照的一致性边界

`Count` 与 `Find` 是两条独立 SQL，`total` 与列表可能来自不同时刻。常规论坛可以接受轻微变化，比如第二页少一条或末尾重复一条。把这种现象当成 bug 去修，容易引入更贵的东西。

MySQL 官方写明，默认 `REPEATABLE READ` 下同一事务内所有一致性读共享第一次读建立的快照，`READ COMMITTED` 下每条读各自建立新快照 [来源: https://dev.mysql.com/doc/refman/8.4/en/innodb-consistent-read.html]。所以在默认隔离级别下，把 `Count` 与 `Find` 放进同一个事务就能拿到一致快照，通常不必改隔离级别。原文那句「需要严格一致时要设计事务隔离级别」只说对了一半，真正需要先做的是把两条查询圈进同一事务，隔离级别决定这个事务里的快照行为。

| 选择 | 做法 | 代价 |
| --- | --- | --- |
| 接受轻微变化 | 现状，两条独立查询 | 无额外成本，分页结果可能抖动 |
| 单请求内一致 | 两条查询放进同一事务，保持默认 REPEATABLE READ | 多一次事务开销，快照在事务期内保持 |
| 跨请求严格一致 | 游标分页加稳定快照键 | 实现复杂，要定义游标语义与过期策略 |

先确认业务是否需要，再选。多数列表页选第一行。

### 6.11 本节做到哪一步

| 状态 | 内容 |
| --- | --- |
| 文档已给可复制片段 | 6.1 四个骨架片段、6.2 模型与 SQL、6.3 DTO、6.4 `errors.go`、6.5 `paging.go` 与三个读接口、6.6 `createArticleReq` 与文章两个写接口、6.7 `createCommentReq` 与评论 service/handler 完整文件、排序常量、两组 DTO 单测 |
| 仓库源码已落地 | 三个公开读接口，加文章与评论的创建、删除；service 与 handler 方法名统一为 `List` / `Detail` / `Create` / `Delete`，对照表见 1.2 |
| 不再有 501 或空方法 | 四个写接口都填了实现，`notImplemented` 保留给以后新增接口占位 |
| 仍未接通 | repository 两个文件只有空壳，05 章做 mock 时再抽 |
| 已修复项 | 删除文章后立刻 GET 详情曾返回 500，03a 章加固落盘后现在返回 404 |
| 目标状态未落地 | 创建 201、删除 204，要等 `pkg/response` 扩展 |
| 下一步顺序 | ① 按 6.6.5 与 6.7.7 跑写接口验收并查库确认作者 id ② 按 6.9 逐条验数据库边界 ③ 进 03a 章做缓存工程化加固 |

第 7 节之后的 Redis 内容建立在「6.5 读链路接通、6.9 写接口边界逐条通过」这个前提上。写路径缓存失效、空值缓存、TTL 抖动、singleflight、降级超时与指标都已在仓库源码落盘，见 `internal/service/article_cache.go` 与 `docs/03a-第2周进阶-Redis缓存工程化加固.md`。第 3 周的点赞、热榜、限流按 `docs/04-第3周-亮点功能-点赞热榜与限流.md` 的十步跟做；第 10 步会回头改 `ArticleService.queryDetail` 与详情 DTO，做之前先确认自己走到哪一步。

---

## 7. Redis 缓存热点文章

### 7.1 为什么缓存，以及什么时候不该缓存

某篇详情被反复读取时，缓存可能减少 MySQL 查询次数。能否更快、能省多少负载取决于网络、序列化、命中率和数据量。先测量再判断，别预先承诺「快几十倍」。

Cache Aside（旁路缓存）的读写流程：

```text
读详情
  1. 查 Redis  key = article:detail:{id}
  2. 命中且 JSON 可解析  → 直接返回
  3. 未命中或内容损坏    → 查 MySQL → 回填 Redis 并设 TTL → 返回
写文章 / 改文章 / 删文章
  数据库提交成功后删除对应 key，下次读时重建
```

删除缓存放在数据库写入成功之后。MySQL 成功但 Redis 删除失败时，缓存可能短暂返回旧数据，初学版本应记录错误并写明这一限制。查询不存在的文章可以缓存一个短 TTL 的空结果标记，读取时要能区分标记与真正的文章 JSON。7.3 代码没有实现空值缓存，`null` 陷阱见 7.6，完整实现、TTL 抖动与请求合并见 03a 章。

### 7.2 装配 Redis：service、handler、New 各改什么

**勘误：本节旧版把 `rdb` 写成 `Detail` 的方法参数，这个写法是错的。** 旧版 7.3 写 `Detail(ctx context.Context, rdb *redis.Client, id uint)`，7.6 还要求 `Create`、`Delete` 都加 `rdb` 参数。可是 6.1 落盘的 `ArticleHandler` 只有 `svc` 一个字段，`router.New` 收到 `rdb` 之后没有地方可送，handler 也拿不出 `rdb` 交给 service。照旧版抄会编译不过，或者被迫给 handler 再加一个 Redis 字段。

正确做法是在构造 `ArticleService` 时把 `rdb` 注入一次，结构体持有它，方法内部统一用 `s.rdb`。2026-11-07 核对仓库源码，`go build ./...`、`go vet ./...`、`go test ./...` 全部通过，采用的就是这个注入写法。本节按源码重写。

`internal/config/redis.go` 已存在 `InitRedis`，不要再新建同名文件。go-redis 已在 `go.mod` 里（`v9.23.0`），不用执行 `go get`。

#### 先看清 `rdb` 从哪来、到哪去

```text
main.go
  config.InitRedis(&cfg.Redis)             -> rdb (*redis.Client)
  router.New(db, jwtManager, rdb)
       -> service.NewArticleService(db, rdb)     rdb 存进 ArticleService.rdb
       -> handler.NewArticleHandler(articleSvc)  handler 只拿到 svc
            -> h.svc.Detail(ctx, id)             service 内部用 s.rdb
```

`rdb` 只在进程入口创建一次，沿装配链往下传一层，停在 service。handler 从头到尾不知道 Redis 存在。

#### 三层改动清单

| 位置 | 要改什么 | 改完的样子 | 为什么 |
| --- | --- | --- | --- |
| `cmd/server/main.go` | 调 `InitRedis`，把结果传给 `router.New` | `router.New(db, jwtManager, rdb)` | 客户端在入口创建一次，连接错误集中在这里处理 |
| `internal/router/router.go` 的 `New` | 加第三个参数、加 nil 校验、注入 service | `New(db *gorm.DB, jwtManager *jwt.Manager, rdb *redis.Client)` | router 是装配点，依赖在这里汇聚 |
| `internal/service/article.go` 的 `ArticleService` | 结构体加一个字段 | `rdb *redis.Client` | 同一 service 的所有方法共用同一个客户端 |
| `internal/service/article.go` 的 `NewArticleService` | 加第二个参数并赋值给字段 | `NewArticleService(db *gorm.DB, rdb *redis.Client)` | `rdb` 只在这里接一次 |
| `internal/service/article.go` 的 `Detail` | 签名不变，方法里的 `rdb.Get` 换成 `s.rdb.Get` | `Detail(ctx context.Context, id uint)` | 缓存属于实现细节，不改变 service 对外合同 |
| `internal/handler/article.go` | **一行都不用改** | `NewArticleHandler(svc)`、`h.svc.Detail(ctx, id)` | handler 只认识 service |

#### 为什么 `rdb` 放结构体，不放方法参数

1. 一个 service 里不止 `Detail` 用缓存。7.6 的写路径失效、以后 `Create` 之后的清理都要用同一个客户端。放参数就要每个方法都加一遍，每个调用点都要传一遍。
2. 放参数就得让 handler 也持有 `rdb`，HTTP 层开始认识 Redis，01 章讲的分层边界会破掉。
3. 放结构体之后 `Detail(ctx, id)` 的签名和 3.7 的无缓存版完全一致。替换实现时所有调用点零改动，`go build ./...` 直接过。

反方意见要如实写：把依赖当方法参数传也有合理场景，例如某个依赖只在单个方法用一次，或者每次调用要换不同客户端。本项目是全局共享一个 `*redis.Client`。go-redis `v9.23.0` 的 `type Client` 源码注释写着 `Client is a Redis client representing a pool of zero or more underlying connections. It's safe for concurrent use by multiple goroutines.` [来源: 本地模块缓存 `github.com/redis/go-redis/v9@v9.23.0/redis.go:2398-2399`]。客户端自带连接池且并发安全，注入一次更合适。这条有库作者写明的保证，跟个人口味无关。

#### 第 1 步，`cmd/server/main.go`（增量修改）

在 `InitDB` 成功之后、`jwt.NewManager` 之前插入 Redis 初始化，并把 `rdb` 加进 `router.New` 的调用。

```go
	db, err := config.InitDB(&cfg.MySQL)
	if err != nil {
		log.Fatalf("连接数据库失败: %v", err)
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

	r, err := router.New(db, jwtManager, rdb)
```

| 代码 | 作用 | 为什么这样设计 |
| --- | --- | --- |
| `config.InitRedis(&cfg.Redis)` | 创建客户端并 `Ping` 测连通 | 配置结构体已经从 yaml 读好，直接传地址段 |
| `log.Fatalf` | 连不上就退出 | 缓存是详情接口的依赖，启动期暴露比运行期一堆降级日志好排查 |
| `defer rdb.Close()` | 进程退出时释放连接池 | `main` 一直运行到退出才执行，不会提前关掉连接 |
| `router.New(db, jwtManager, rdb)` | 把第三个依赖交给装配点 | 参数顺序按「数据库、鉴权、缓存」固定下来，别每个文件各排一种 |

#### 第 2 步，`internal/router/router.go` 的 `New`（增量修改）

import 块补一行 `"github.com/redis/go-redis/v9"`。下面是替换后的完整 `New`，与仓库源码一致。

```go
// New 注册全部路由。
// db、jwtManager、rdb 都由 main 创建后传入，错误处理集中在 main，router 只负责组装。
func New(db *gorm.DB, jwtManager *jwt.Manager, rdb *redis.Client) (*gin.Engine, error) {
	if db == nil {
		return nil, errors.New("router: database is required")
	}
	if jwtManager == nil {
		return nil, errors.New("router: JWT manager is required")
	}
	if rdb == nil {
		return nil, errors.New("router: Redis client is required")
	}

	r := gin.Default()

	userSvc := service.NewUserService(db)
	userHandler := handler.NewUserHandler(userSvc, jwtManager)

	articleSvc := service.NewArticleService(db, rdb)
	commentSvc := service.NewCommentService(db)
	articleHandler := handler.NewArticleHandler(articleSvc)
	commentHandler := handler.NewCommentHandler(commentSvc)

	api := r.Group("/api")
	api.Use(limitBody(1 << 20)) // 1 MiB
	{
		api.POST("/register", userHandler.Register)
		api.POST("/login", userHandler.Login)

		api.GET("/articles", articleHandler.List)
		api.GET("/articles/:id", articleHandler.Detail)
		api.GET("/articles/:id/comments", commentHandler.List)

		auth := api.Group("", middleware.JWTAuth(jwtManager))
		{
			auth.POST("/articles", articleHandler.Create)
			auth.DELETE("/articles/:id", articleHandler.Delete)
			auth.POST("/articles/:id/comments", commentHandler.Create)
			auth.DELETE("/comments/:id", commentHandler.Delete)
		}
	}

	return r, nil
}
```

相对 6.1 的两参数版本，这个函数只多了三处：签名里的 `rdb *redis.Client`、`if rdb == nil` 校验、`service.NewArticleService(db, rdb)` 的第二个实参。其余路由注册一行没动。

| 代码 | 作用 | 为什么这样设计 |
| --- | --- | --- |
| `rdb *redis.Client` 放在参数末尾 | 新增依赖接在已有两个后面 | 6.1 的调用点只需补一个实参，不用重排 |
| `if rdb == nil` | 启动期挡住空指针 | 空客户端要等第一个请求才 panic，排查成本高得多 |
| `service.NewArticleService(db, rdb)` | 把 `rdb` 注入 service | 这是 `rdb` 链路的终点，后面不再往下传 |

#### 第 3 步，`internal/service/article.go`（增量修改）

结构体加字段，构造函数加参数。import 块按标准库、第三方、本项目三段分组，`gofmt` 只对齐不分组，顺序要自己写对。

```go
import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"forum-backend/internal/model"
)

// ArticleService 文章业务逻辑，同时持有 MySQL 与 Redis 客户端。
type ArticleService struct {
	db  *gorm.DB
	rdb *redis.Client
}

// NewArticleService 由 router 调用，两个客户端在这里一次性注入。
func NewArticleService(db *gorm.DB, rdb *redis.Client) *ArticleService {
	return &ArticleService{db: db, rdb: rdb}
}
```

| 代码 | 作用 | 为什么这样设计 |
| --- | --- | --- |
| `rdb *redis.Client` | service 持有缓存客户端 | `Detail`、`queryDetail` 和以后的写路径失效都能用 `s.rdb` |
| `NewArticleService(db, rdb)` | 构造时收下两个依赖 | service 不自己 `redis.NewClient`，连接由入口统一管理 |
| `return &ArticleService{db: db, rdb: rdb}` | 两个字段一起赋值 | 漏赋 `rdb` 时 `s.rdb` 是 nil，第一次读缓存就 panic，启动期校验挡不住这一层 |

#### 第 4 步，`internal/handler/article.go` 一行都不用改

确认一下现有的两处代码，保持原样即可：

**增量修改**：核对 `internal/handler/article.go` 的 `NewArticleHandler`，与源码一致就保持原样。

```go
func NewArticleHandler(svc *service.ArticleService) *ArticleHandler {
	return &ArticleHandler{svc: svc}
}

a, err := h.svc.Detail(c.Request.Context(), uint(id))
```

`rdb` 在 router 装配时已经进了 `articleSvc`，handler 拿到的 `svc` 里就带着它。handler 的类型、字段、构造函数、方法调用全部不用动。这就是把依赖放进构造函数的直接收益：换掉 service 的内部实现，HTTP 层零改动。

如果按旧版文档给 `Detail` 加 `rdb` 参数，这两行都得跟着改，`ArticleHandler` 还要多一个 `rdb` 字段。多出来的部分没有带来新能力，只把一个实现细节铺到了三层里。

#### 验证与启动前置

```powershell
go build ./...
go vet ./...
```

启动服务前先确认 Redis 在跑：

```powershell
redis-cli ping
go run ./cmd/server
```

`main.go` 现在无条件调用 `InitRedis`，失败就 `log.Fatalf` 退出。`config/config.yaml` 里 `redis.addr` 是 `127.0.0.1:6379`。从第 1 步开始，启动前必须先起 Redis，否则连注册登录都进不去。第 6 节「无 Redis 的 MySQL 版本」是本节之前的状态：6.1 的两参数骨架按原样做完验收，再进本节加第三个参数，别一次改两处。

#### 常见错误对照

| 写法 | 会怎样 | 正确做法 |
| --- | --- | --- |
| 把 `rdb` 塞进 `Detail(ctx, rdb, id)` | handler 拿不出 `rdb`，编译不过；硬改就要给 handler 也加字段 | 结构体持有 `s.rdb` |
| 给 `ArticleHandler` 加 `rdb` 字段 | 能编译，但 HTTP 层开始认识 Redis | 不用改 |
| 只改 `router.New` 签名，忘了 `main.go` | `not enough arguments in call to router.New` | 两个调用点同步 |
| 只改 `NewArticleService` 的调用，忘了改定义 | `too many arguments in call to service.NewArticleService` | 定义与调用同步 |
| 构造函数里漏写 `rdb: rdb` | 编译通过，第一次 `s.rdb.Get` 空指针 panic | 逐字段核对 |
| 又执行一次 `go get github.com/redis/go-redis/v9` | 多余动作，版本已在 `go.mod` | 跳过 |

#### 一处已经修复的旧缺陷（勘误）

**勘误：本节旧版写「`InitRedis` 的 `Ping` 失败分支还缺 `rdb.Close()`」，那是 2026-11-07 之前的快照。** 核对 `internal/config/redis.go` 第 26 行，当前源码已经有 `_ = rdb.Close()`，不要再补一遍。README 与 00 章的同类描述也已更正，完整清单见 03a 章第 11 节。

下面的代码是**概念伪代码**：从源码里截取出来的失败分支，不能单独运行。留在这里只为解释它为什么必须存在，不要复制粘贴。

```go
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, fmt.Errorf("connect redis: %w", err)
	}
```

`_ =` 表示关闭失败也要把原始连接错误返回，别让关闭的错误盖掉真正的失败原因。03a 章 6.6 会在这个文件里继续加连接池与超时配置。

### 7.3 完整增量：带缓存的 Detail

把下面方法加入 `internal/service/article.go`，用它替换 3.7 的 `Detail`。import 清单与结构体字段已在 7.2 第 3 步给出，重复 import 只保留一份。

`Detail` 的签名保持 `(ctx, id)`，和 3.7 的无缓存版完全一样。方法内部改用 `s.rdb`，`rdb` 由 7.2 的构造函数注入。handler 与 router 的调用点一行都不用改。这是本节最容易写错、也最省事的地方。

**增量修改**：替换 `internal/service/article.go` 的 `Detail`，签名保持 `(ctx, id)`。

```go
const articleDetailKey = "article:detail:" // key 前缀，后面拼文章 id

// Detail 先读缓存，未命中回源 MySQL 并回填。
func (s *ArticleService) Detail(ctx context.Context, id uint) (*model.Article, error) {
	// strconv 把 uint 转成十进制字符串，避免用 fmt 做多余格式化。
	key := articleDetailKey + strconv.FormatUint(uint64(id), 10)

	// 只把 redis.Nil 当成「未命中」；其他错误属于缓存故障，降级回源。
	if val, err := s.rdb.Get(ctx, key).Result(); err == nil {
		var a model.Article
		if err := json.Unmarshal([]byte(val), &a); err == nil {
			return &a, nil
		}
		// 缓存内容损坏时删掉坏 key，否则每次都会重复读到坏值。
		log.Printf("invalid article cache %s", key)
		_ = s.rdb.Del(ctx, key).Err()
	} else if err != redis.Nil {
		log.Printf("read article cache %s: %v", key, err)
	}

	// 查事实来源。WithContext 让请求取消能传给数据库。
	a, err := s.queryDetail(ctx, id)
	if err != nil {
		return nil, err
	}

	data, err := json.Marshal(a)
	if err != nil {
		return nil, fmt.Errorf("marshal article %d: %w", id, err)
	}
	// 回填失败不能把这次成功的数据库查询变成接口失败。
	if err := s.rdb.Set(ctx, key, data, 10*time.Minute).Err(); err != nil {
		log.Printf("write article cache %s: %v", key, err)
	}
	return a, nil
}

// queryDetail 只负责查库，方便单独测试与 singleflight 复用。
func (s *ArticleService) queryDetail(ctx context.Context, id uint) (*model.Article, error) {
	var a model.Article
	if err := s.db.WithContext(ctx).Preload("User").First(&a, id).Error; err != nil {
		return nil, err
	}
	return &a, nil
}
```

**与 3.7 版的差别只在缓存那几行，签名不变。** `s` 是方法接收者，`s.db` 和 `s.rdb` 都是结构体字段，不需要谁把它们当参数递进来。`Detail` 被替换成缓存版之后，`internal/handler/article.go` 里的 `h.svc.Detail(c.Request.Context(), uint(id))` 照旧成立。

### 7.4 逐行拆解（挑最容易写错的几行）

| 代码 | 作用 | 为什么这样设计 |
| --- | --- | --- |
| `s.rdb` | 从结构体字段取客户端 | 由 7.2 的 `NewArticleService(db, rdb)` 注入，方法参数里没有它 |
| `err == nil` 分支 | 命中缓存 | 只有解析成功才返回；坏 JSON 要当成未命中 |
| `err != redis.Nil` | 区分故障与未命中 | Redis 断线时继续回源，同时留日志便于发现降级 |
| `_ = s.rdb.Del(...)` | 清理坏缓存 | 删除失败不影响主流程，用 `_` 显式表达忽略 |
| `json.Marshal(a)` | 序列化文章 | 缓存里存的是快照，写入后要让旧快照失效 |
| `10*time.Minute` | 设置 TTL | 防止数据永远不更新；固定 TTL 会让一批 key 同时过期，抖动实现见 03a 章第 3 节 |
| `log.Printf` 只记 key 和错误 | 可观测 | 不记录请求体、token、密码 |

若文章支持编辑或删除，**所有写路径**都要执行缓存失效，否则读到的可能是旧内容。缓存只优化读取，不能替代数据库里的正确数据。

### 7.5 缓存穿透、击穿、雪崩（概念索引）

这一节只做名词索引。**三类故障的时间轴推导、解法取舍、完整可运行代码与验收证据都在 `docs/03a-第2周进阶-Redis缓存工程化加固.md` 第 1、3、4、5、6、9 节**，本章不展开，避免把第 2 周塞成事无巨细的清单。

| 概念 | 触发条件 | 常见解法 | 边界 |
| --- | --- | --- | --- |
| 穿透 | 查询根本不存在的 id，缓存与数据库都没有 | 空值缓存加短 TTL，或布隆过滤器（实现见 03a 第 4 节） | 空标记要与真实 JSON 区分 |
| 击穿 | 单个热点 key 过期瞬间，大量请求同时回源 | 同进程用 `singleflight` 合并，多实例用分布式锁（实现见 03a 第 5 节） | `singleflight.Group` 只在单进程内生效 |
| 雪崩 | 大量 key 同时过期 | TTL 加随机抖动，或多级缓存（实现见 03a 第 3 节） | 抖动幅度要按业务写入节奏定 |

这三个名词是中文工程社区对三类故障时间形状的说法，Redis 官方文档不用这套术语。区分「库作者写明的保证」和「社区总结的惯例」是 00 章的规矩，03a 第 1.7 节列了逐条归属。

`singleflight` 属于 `golang.org/x/sync`，`go.mod` 当前没有这个直接依赖。03a 章步骤 3 才会 `go get`，本章不要提前装：装完没人 import，紧接着 `go mod tidy` 会把它删掉。合并同一进程内相同 key 的请求时，并发调用会共享第一个调用的查询结果和 context，首个请求被取消会连累等待者，03a 第 5.3 节用 `context.WithoutCancel` 处理。

### 7.6 本章代码做到哪一步

**进度快照（2026-11-07 核对源码）**：写路径缓存失效已经落地，下表按仓库真实状态重列。旧版把这一项和 `InitRedis` 的 `rdb.Close()` 一起写成「待实现」，属于快照过期，已在 README、00 章与 03a 章第 11 节更正。

| 状态 | 内容 |
| --- | --- |
| 已实现（7.2、7.3、7.6） | 三层装配链、Cache Aside 读路径、坏缓存删除并回源、Redis 故障时降级回源、回填失败不阻断主流程、写库或事务提交成功后删缓存 |
| 待实现（完整代码与验收在 03a 章） | key 版本化、TTL 随机抖动、空值缓存与空标记编码、singleflight 请求合并、Redis 超时与连接池、降级分级表、命中率指标、`gorm.ErrRecordNotFound` 的 404 映射 |

写路径失效已经落在源码里，下面这段是**概念伪代码**：从源码截取出来供核对，不要重复粘贴。

```go
// InvalidateDetailCache 删除文章详情缓存。失败只记录，不改变已提交的数据库结果。
func (s *ArticleService) InvalidateDetailCache(ctx context.Context, id uint) {
	key := articleDetailKey + strconv.FormatUint(uint64(id), 10)
	if err := s.rdb.Del(ctx, key).Err(); err != nil {
		log.Printf("invalidate article cache %s: %v", key, err)
	}
}
```

`Delete` 在事务成功返回之后、`Create` 在 `Create` 成功之后各调用一次。它和 `Detail` 用的是同一个 `s.rdb`、同一个 key 前缀，删掉的正是读的那一个 key。加上空值缓存之后，`Create` 里这一行还会清掉该 id 可能残留的空标记，理由见 03a 章 4.3 与 7.6。

第 9 节验收里「修改与删除后不返回旧内容」这一项，在 03 章 6.6 与 6.7 落盘后已经能端到端跑通。6.6.5 与 6.7.7 都给了带伪造 `user_id` 的完整验证脚本。03a 章 9.6 里「写接口仍是 501」的阻塞说明已随之解除，删完立刻 GET 详情现在返回 404，03a 章 4.5 记录的 500 缺陷已修。

**空值缓存有一个具体陷阱**。`json.Marshal(nil)` 得到字符串 `null`，`json.Unmarshal([]byte("null"), &a)` 不报错且把 `a` 留成零值结构体。把 `null` 直接当缓存值，7.3 的命中分支会把一篇全零值文章返回给客户端。空标记必须用一个与合法 JSON 明显不同的哨兵值，读取时先比对再决定是否回源。03a 章把这条陷阱写成了可运行的单元测试。

**练习表达（只描述本章已实现部分）**：「我实现了 Cache Aside 读路径与写路径失效。命中直接返回，坏缓存删除并回源，Redis 故障时降级到 MySQL，回填失败不影响本次数据库结果，数据库提交成功后删缓存。空值缓存、TTL 抖动、请求合并还没做，所以不会声称解决了穿透、击穿与雪崩。」完成 03a 章并验收之后，换成 03a 第 10 节里对应真实进度的表达，别提前背。

---

## 8. 勘误与辨伪：那些「看起来很专业」的错话

这一节把本章涉及的常见误判集中列出。判断依据来自仓库源码、GORM 官方英文文档和本章的 DryRun 探针。

| 说法 | 判定 | 依据与正确表述 |
| --- | --- | --- |
| 「`[]model.Article` 配 `[]*model.Article` 返回类型会编译不过」 | 前提要看具体代码 | 仓库代码和旧文档各自的类型声明与返回类型自洽，都能编译。冲突在于两个版本的方法名和返回类型对不上 |
| 「Go 不会自动转指针切片」 | 正确 | 声明与返回类型必须一致，`Find` 对两种切片都支持 |
| 「`&articles[i]` 会因切片扩容导致指针失效」 | 错误 | 扩容分配新数组后旧数组仍被引用，指针继续有效；后果是新旧数据不同步 |
| 「`Find` 一次性填完指针切片，但仍有扩容风险」 | 自相矛盾 | 既已填完就不再扩容，风险不存在 |
| 「`Count` 把 `SELECT` 改成 `count(*)` 并留在链上」 | 机制不准确 | `Count` 用 `defer` 恢复或删除 SELECT 子句，非 DryRun 执行结束还会 `Reset` Statement.SQL；真实风险是共享 Statement 导致的条件累积 |
| 「每次从 `s.db` 重新开始，是 `Model` 和 `Preload` 各自返回 clone」 | 结论对，理由不准 | 根 db 带 clone=1，每条链的第一个方法新建 Statement，链上后续方法共享同一个 Statement |
| 「复用 `base` 可能返回空列表或报错」 | 结论对 | 官方明确说已初始化的 `*gorm.DB` 复用不安全，用 `Session(&gorm.Session{})` 隔离 |
| 「`Session(&gorm.Session{})` 会丢掉 `Where`」 | 错误 | 它复制当前条件；`Session(&gorm.Session{NewDB: true})` 才清空条件 |
| 「`page=0` 产生负 offset，GORM 会报错」 | 错误 | 负 Offset 被当作取消条件，生成无 OFFSET 的 SQL，静默返回第一页 |
| 「`total==0` 时带 `Preload` 会浪费两条 SQL」 | 错误且自相矛盾 | 空结果时预加载回调直接返回，只浪费一条主查询 |
| 「`Preload` 一定只发 2 条 SQL」 | 过于绝对 | 关联类型、嵌套预加载、带条件的预加载都可能增加查询数 |
| 「排序字段必须建索引」 | 过度断言 | 大表排序分页建议复合索引并用 EXPLAIN 验证，小表全表扫描可能更便宜 |
| 「`Find` 失败时返回 `total` 让调用方区分阶段」 | 可以，价值有限 | Go 惯例是 `err != nil` 时忽略其他返回值，错误日志里的阶段信息更实用 |
| 「`json:"user,omitempty"` 能省略零值用户」 | 错误 | `omitempty` 的 empty 定义不含结构体，值类型的 `User` 仍会输出全零对象，要用指针、`omitzero` 或 DTO，见 6.3 |
| 「加了外键后，先删文章再删评论仍然能跑」 | 错误 | RESTRICT 外键会拒绝先删父表，顺序必须改成先删子表，见 6.2 与第 4 节 |
| 「`ON DELETE CASCADE` 一劳永逸」 | 有前提 | 加软删除后 `Delete` 变成 `UPDATE`，CASCADE 永不触发，见 6.2 |
| 「本阶段要同时准备 `ErrForbidden` 与 `ErrNotFound`」 | 口径冲突 | 第 4 节已选隐藏资源 404，加 403 要先改 `Delete`、映射函数与验收，见 6.4 |
| 「`Count` 与列表对不上就是 bug，必须改隔离级别」 | 过度断言 | 默认 REPEATABLE READ 下把两条查询放进同一事务即可，隔离级别决定快照行为，见 6.10 |
| 「`applyArticleFilter` 现在还在仓库里」 | 已过时 | 当前 `article.go` 已重写，该函数已删除，见 1.1 |
| 「`CommentService` 不在仓库里，要新建 `comment.go`」 | 已过时 | 文件已存在且含 `List` / `Create` / `Delete`，新建同名文件会覆盖已有代码，见 5.1 与 6.7.3 |
| 「`ListArticles` 还没有 `ctx`，本章才补」 | 已过时 | 源码已带 `ctx`；本轮方法统一改名为 `ArticleService.List`，`gofmt`、排序常量与提示文字都已落地，见 1.2、3.2 |
| 「`ErrNotFound` 要往 `var` 块新增一行」 | 已过时 | `service/user.go` 已定义，重复粘贴会编译失败，见 3.7 |
| 「文章链路还没传 ctx」 | 已过时 | `article.go` 与 `comment.go` 都已传，缺的是 handler 与 router 接线，见 01 章 |
| 「DTO 补在 `internal/model/models.go` 就行」 | 分层错误 | DTO 是 handler 的输出契约，放进 model 包会把 API 形状写进数据层。统一放 `internal/handler/dto.go`，见 02 章 8.0 与本章 3.8 |
| 「Go 官方要求一个结构体单独放一个文件」 | 无依据 | 官方明文是导出声明要写 doc comment，非平凡的非导出声明也建议写；文件切分属于团队约定 [来源: https://go.dev/wiki/CodeReviewComments] |
| 「error 字符串首字母大写、带句号更规范」 | 与 Go 官方相反 | 错误字符串不大写开头，也不带句末标点，便于被其他日志上下文拼接 [来源: https://go.dev/wiki/CodeReviewComments] |
| 「Go 官方要求包注释必须单独放 doc.go」 | 无依据 | 官方只要求多文件包把包注释放在一个源文件里，doc.go 是常见做法 [来源: https://go.dev/doc/comment] |
| 「`Delete` 一定把记录从数据库里删掉」 | 有前提 | 含 `gorm.DeletedAt` 时生成 UPDATE 软删除，无 WHERE 的批量删除直接被拒绝 [来源: https://gorm.io/docs/delete.html] |
| 「Gin 官方要求 service 与 repository 都收 `c.Request.Context()`」 | 混淆层次 | 逐层传 Context 是 Go 官方明文，Gin 侧的具体写法属于工程惯例 [来源: https://go.dev/blog/context] |
| 「缓存版 `Detail` 要写成 `Detail(ctx, rdb, id)`」 | 错误 | `rdb` 由 `NewArticleService(db, rdb)` 注入到结构体，方法内部用 `s.rdb`，`Detail(ctx, id)` 签名不变，handler 零改动，见 7.2、7.3 |
| 「接了 Redis 就要给 handler 也加 `rdb` 字段」 | 分层错误 | 依赖在 router 装配时进入 service，handler 只认识 `svc`，见 7.2 第 4 步 |

读技术讲解时养成一个习惯：**把「结论」和「理由」分开验证**。上面多条说法结论正确、机制讲错，照错误的机制去推理，下一个场景就会踩坑。

---

## 9. 本周验收

### Postman 端到端顺序

**前置条件**：6.5 的读链路已接通，6.6 的文章写接口与 6.7 的评论写接口都已落盘，并分别通过 6.6.5 与 6.7.7 的验证。若 `router.go` 里仍只有注册登录，下面每一步都会得到 404，先回第 6 节接线；若 `POST /api/articles` 仍返回 501，先按 6.6 补完；若评论接口静默返回 200 空响应，先按 6.7 补完。别把接线问题当成缓存问题。

先启动 MySQL、Redis 和 Go 服务。当前源码的 `main.go` 已按 7.2 调用 `InitRedis`，连不上 Redis 会直接 `log.Fatalf` 退出，所以 Redis 是启动硬依赖。若你停在 6 节的无 Redis 版本，`main.go` 里不要调 `InitRedis`，`router.New` 保持两参数，第 3 步与第 9 步的缓存相关验收先跳过。Postman 建 `forum-local` environment，变量 `base_url=http://localhost:8080`、`token`、`article_id`、`comment_id`。受保护请求用 Authorization → Bearer Token。

1. `POST {{base_url}}/api/articles`，JSON `{"title":"第一篇","content":"正文内容"}`。期望 200（响应包扩展后按 201 判定），响应不接受客户端伪造的 `user_id`。做法见 6.6；完整命令、伪造 `user_id` 的数据库核验与所有权测试见 6.6.5。只看返回 200 不够，要查 `articles.user_id`。
2. `GET {{base_url}}/api/articles?page=1&page_size=10`。确认列表、total 与刚创建的文章。
3. `GET {{base_url}}/api/articles/{{article_id}}` 连发两次，响应一致。7.3 代码只在异常分支打日志，命中与未命中的区分要先补指标或调试日志，完整计数器与验收方法见 03a 章 6.1、9.3，别只凭「第二次更快」下结论。
4. `POST {{base_url}}/api/articles/{{article_id}}/comments`，JSON `{"content":"写得不错"}`。做法见 6.7，完整命令、带伪造 `article_id` / `user_id` 的请求与 `SELECT ... JOIN users` 查库核验见 6.7.7。只看 200 不够，要确认 `comments.user_id` 是 token 对应的用户。
5. `GET {{base_url}}/api/articles/{{article_id}}/comments?page=1&page_size=10`。确认按文章过滤。
6. 换一个用户删除他人的文章与评论，按 6.4 选定的隐藏资源策略得到 404，数据库内容仍在。文章看 6.6.5 第 5 步，评论看 6.7.7 第 5 步，两者都已能验。
7. 原作者删除文章，再读详情与评论，确认评论没有孤儿记录。写路径失效在 service 层已落地，6.6 与 6.7 落盘后这一步能端到端跑通。删完立刻 GET 详情现在返回 404，`queryDetail` 已转换错误且 `mapServiceError` 已补兜底；仍建议同时用列表或 SQL 确认删除结果。
8. 依次测 `page=abc`、`page=0`、超大 `page_size`、不存在的 id、空白内容、超长 body、缺 token。查不存在的文章现在返回 404。
9. 关闭 Redis 后请求详情，确认按设计回源 MySQL。完整降级分级表与超时配置见 03a 章第 8 节。写库成功而删缓存失败的场景，按 6.6 接通文章写接口后就能构造。

每个请求成功后还要在 MySQL 与 Redis 里检查副作用。状态码正确不代表数据一致。

### 勾选项

- [ ] 发布文章、评论与分页列表可用，作者 id 全部来自 JWT（按 6.6.5 与 6.7.7 查库确认）
- [ ] 空集合返回 `[]`，总数与列表条件一致，排序稳定
- [ ] 越界分页参数返回 400，`page=0` 不会静默返回第一页
- [ ] 详情连续访问第二次走缓存（命中率证据见 03a 章 9.3）；6.6 与 6.7 接通后「发布后读详情」「删除后读详情」可验，03a 章 9.6 里的写接口阻塞已解除，删后 GET 详情现在返回 404
- [ ] 删除文章按事务规则清理评论，中途失败不留半套数据
- [ ] Redis 停止时详情按设计回源，错误可观测
- [ ] `go test ./...`、`go test -race ./...`、`go vet ./...`、`go build ./cmd/server` 通过

> 下一步：[`03a-第2周进阶-Redis缓存工程化加固.md`](03a-第2周进阶-Redis缓存工程化加固.md)（第 2 周进阶专题，不占周次，源码已落盘）；之后进 `04-第3周-亮点功能-点赞热榜与限流.md`，它已改成十步跟做版，最后一步会把点赞数接进文章详情。想直接看亮点功能可以跳过 03a，04 章不依赖它。

---

## 10. 本章参考来源

全部为英文权威来源，或项目锁定版本的库源码注释，技术结论可回溯：

- GORM Method Chaining（链方法、终结方法、New Session 方法与不安全复用）：https://gorm.io/docs/method_chaining.html
- GORM Session（NewDB 清空条件、WithContext 定义）：https://gorm.io/docs/session.html
- GORM Query（First 与 ErrRecordNotFound、Order、Limit、Offset(-1)）：https://gorm.io/docs/query.html
- GORM Preload（eager loading 与 separate query）：https://gorm.io/docs/preload.html
- GORM Delete（Block Global Delete 与软删除）：https://gorm.io/docs/delete.html
- GORM Advanced Query（Smart Select、Count）与 Scopes（官方 Paginator 范例）：https://gorm.io/docs/advanced_query.html 、 https://gorm.io/docs/scopes.html
- GORM Error Handling（TranslateError、ErrRecordNotFound、ErrDuplicatedKey、ErrForeignKeyViolated）：https://gorm.io/docs/error_handling.html
- GORM Migration（AutoMigrate 自动建外键、不删无用列）：https://gorm.io/docs/migration.html
- GORM Constraints（constraint tag 与 OnDelete/OnUpdate）：https://gorm.io/docs/constraints.html
- MySQL 8.4 FOREIGN KEY Constraints（默认 NO ACTION、索引要求、CASCADE 行为）：https://dev.mysql.com/doc/refman/8.4/en/create-table-foreign-keys.html
- MySQL 8.4 Consistent Nonlocking Reads（REPEATABLE READ 与 READ COMMITTED 的快照差异）：https://dev.mysql.com/doc/refman/8.4/en/innodb-consistent-read.html
- net/http MaxBytesReader（请求体上限与 MaxBytesError）：https://pkg.go.dev/net/http#MaxBytesReader
- go-redis v9.23.0 `type Client` 源码注释（连接池与并发安全，本地模块缓存 `redis.go:2398-2399`）：https://pkg.go.dev/github.com/redis/go-redis/v9#Client
- encoding/json（omitempty 的 empty 定义、omitzero、nil 切片编码成 null）：https://pkg.go.dev/encoding/json
- strconv（Atoi 解析失败返回 error）：https://pkg.go.dev/strconv
- Go Code Review Comments（context 作为第一参数、错误字符串、导出标识符注释）：https://go.dev/wiki/CodeReviewComments
- Go Effective Go、Go Doc Comments、Go Blog Context：https://go.dev/doc/effective_go#commentary 、 https://go.dev/doc/comment 、 https://go.dev/blog/context
- Microsoft Learn DTO 与 Martin Fowler DTO：https://learn.microsoft.com/en-us/aspnet/web-api/overview/data/using-web-api-with-entity-framework/part-5 、 https://martinfowler.com/eaaCatalog/dataTransferObject.html
- Gin 官方绑定与校验、Quickstart：https://gin-gonic.com/en/docs/binding/binding-and-validation 、 https://gin-gonic.com/en/docs/quickstart/
- Microsoft Learn N-tier（分层的收益与代价）：https://learn.microsoft.com/en-us/azure/architecture/guide/architecture-styles/n-tier
- go-redis 官方命令文档：https://redis.io/docs/latest/develop/reference/commands/
- 本地源码核对：`C:\Users\admin\go\pkg\mod\gorm.io\gorm@v1.31.2`（finisher_api.go、gorm.go、callbacks/preload.go、clause/limit.go、callbacks.go）

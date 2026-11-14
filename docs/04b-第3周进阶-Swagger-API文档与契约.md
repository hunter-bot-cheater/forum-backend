# 04b · 第 3 周进阶：Swagger API 文档与契约防漂移

> 这一章接在 04a 之后，同样不占周次。第 4 周仍然是 05 章。
>
> 目标：让接口文档从代码里的注解自动生成，本机打开一个可交互页面就能带 token 试接口，同时用一条 CI 检查守住「注解和代码不同步」这件事。
>
> 本章沿用前两章的约定：**文档给可落盘的代码，不替你改源码**。开工前确认 04 章十步与 04a 章的勾选项已经跑完，否则注解里引用的 DTO 和通知路由还不存在。

## 本章交付物

- [ ] 能说清 swag、生成物、gin-swagger 三者各管一段
- [ ] 通用 API 注解带标题、版本、`BasePath`、`Bearer` 安全定义
- [ ] 十个路径上的十三个 handler 方法有注解，`@Success` 能显示统一响应壳里的具体 `data` 类型
- [ ] `swag init` 生成到 `api/swagger/`，不污染 `docs/` 学习文档
- [ ] 调试模式能打开 `/swagger/index.html`，release 模式访问不到
- [ ] 生成物纳入版本控制，CI 用重跑加 diff 守住契约
- [ ] 能解释 Swagger 2.0 与 OpenAPI 3.x 的区别，以及本章明确没做什么

---

## 0. 为什么需要 API 文档

### 0.1 手写表格一定会漂移

00 章有一张「目标 API 合同」表，04 章又不断往里加点赞、热榜、限流接口，04a 章再加两个通知接口。表是人写的，代码是编译器管的。只要有一次改了路径参数、忘了改表，新人照着表写前端就会踩坑。

简历上还有一层区别。能说「我给每个 handler 写注解，一条命令生成可交互文档，并用 CI 校验生成物没有过期」，说明项目有契约意识和工程闭环。只说「我写了 README 里的接口列表」，听起来还是课程作业。

### 0.2 三种做法对比

| 做法 | 文档从哪来 | 漂移风险 | 本项目的判断 |
| --- | --- | --- | --- |
| 手写 Markdown 表格 | 人 | 高，靠自觉维护 | 00 章合同表保留，作为业务规则的权威描述 |
| swaggo/swag 注解生成 | handler 上方的注释 | 中，注解与代码可能不同步 | 本章采用，再用 CI diff 把风险压下去 |
| 先写 OpenAPI YAML 再生成服务端代码 | 规格文件 | 低，但改接口要先改规格 | 规格优先适合多人协作，本机学习项目改接口太慢 |

选注解生成的关键理由是「注解离代码最近」。改 `Like` 的响应结构体时，注解就在同一个文件里，顺手一起改的概率比翻到另一份文档高。

### 0.3 三个组件各管一段

```text
swag CLI                  读 Go 注解，生成 swagger.json / swagger.yaml / docs.go
  -> api/swagger/          生成物，纳入版本控制
gin-swagger + files       把生成物挂成 /swagger/*any 的可交互页面
  -> 浏览器                带 Bearer token 直接试接口
```

`docs.go` 里有一个 `SwaggerInfo` 变量，空白导入它会把文档注册进 `github.com/swaggo/swag` 的全局注册表，`gin-swagger` 从这里读到 JSON 渲染页面。少了这个空白导入，页面能打开但内容是空的。

### 0.4 前置条件与进度快照

| 位置 | 期望状态 | 不满足时怎么办 |
| --- | --- | --- |
| 04 章十步 | 点赞、热榜、限流、详情 `like_count` 已落盘 | 先补完，`handler.likeResp`、`hotListResp` 才存在 |
| 04a 章 | 通知路由与 `notificationListResp` 已落盘 | 先补完，否则通知接口的注解引用不到类型 |
| `cmd/server/main.go` | 04a 章的完整版本，含 `signal.NotifyContext` 与 `WaitGroup` | 本章在它上面加注解和一行空白导入 |
| `internal/router/router.go` | 04a 章的路由版本 | 本章只加 import 与一个 `GET` |
| `docs/` | 全是 `.md` 学习文档 | 本章用 `-o api/swagger` 避开它 |
| Go 环境 | `go env GOPATH` 可查，`$GOPATH/bin` 在 PATH 里 | 见 1.2 |

本章会改 `main.go`。04 章 0.5 的「main.go 零改动」只对 04 章成立，04a 已经整体覆盖过一次，本章是第二次增量，三句话描述的是三个阶段的快照。

### 0.5 本章验收合同

| 编号 | 必须拿到的证据 |
| --- | --- |
| B1 | `swag init` 成功，生成三个文件，且 `docs/` 下没有多出 `.go` 或 `.json` |
| B2 | `swagger.json` 的 `swagger` 为 `2.0`、`basePath` 为 `/api`、含 `securityDefinitions.Bearer` |
| B3 | 十个路径出现在 `paths` 里，`@Success` 的 schema 带 `allOf` 与具体 `data` 类型 |
| B4 | `go build ./...`、`go vet ./...`、`go test ./...` 全通过 |
| B5 | 调试模式启动后 `/swagger/doc.json` 返回 JSON，`/swagger/index.html` 能打开 |
| B6 | `GIN_MODE=release` 时 `/swagger/index.html` 返回 404 |
| B7 | 改一处注解但忘了重跑生成时，CI diff 检查会失败 |

B7 依赖接入 Git 与 CI。`.github/workflows/ci.yml` 已落盘，仓库推到 GitHub 后才会真正触发。本机没有 `.git`，首次运行未验证。

---

## 1. 先把工具链钉死

### 1.1 版本与实测结论

本章命令在 2026-11-08 用本机 `go1.27.3` 实测过。`go install ...@latest` 装到的 CLI 自报 `swag.exe version v1.16.4`，`go get` 解析到的库版本是 `gin-swagger v1.6.1`、`files v1.0.1`、`swag v1.16.6`。版本会变，以你自己 `go.mod` 实际解析出的为准，把命令里的结论当成形状参考，别当成永久事实。

口语里常把这套工具叫 swagin 或 swagger，Go 生态里对应的是 `swaggo/swag`。三个包的名字要分开记：`swag` 是 CLI 与运行时注册库，`gin-swagger` 是 Gin 中间件，`files` 是 Swagger UI 的静态资源。

### 1.2 安装与依赖

```powershell
go install github.com/swaggo/swag/cmd/swag@latest
swag --version
go get github.com/swaggo/gin-swagger github.com/swaggo/files github.com/swaggo/swag
go mod tidy
```

`go install` 装到 `$GOPATH/bin`，本机是 `C:\Users\admin\go\bin`。如果 `swag --version` 报「无法识别」，把这个目录加进 PATH，或者整条命令写全路径 `C:\Users\admin\go\bin\swag.exe`。

`go get` 里第三个包容易被漏掉。生成物 `docs.go` 会 import `github.com/swaggo/swag`，少了它 `go build` 会直接报缺依赖。实测三个包一起加，编译一次通过。

### 1.3 一个真实坑：默认输出目录会盖住学习文档

`swag init` 默认输出到 `./docs`，生成 `docs.go`、`swagger.json`、`swagger.yaml` 三个文件。本项目的 `docs/` 装的是整套学习文档，直接跑会把生成物混进章节目录里。副本实测的输出是：

```text
create docs.go at docs/docs.go
create swagger.json at docs/swagger.json
create swagger.yaml at docs/swagger.yaml
```

所以本章把生成物放到 `api/swagger/`，与 `docs/` 完全分开。生成目录名会决定 `docs.go` 里的包名，实测 `api/swagger` 生成的是 `package swagger`。空白导入用路径 `_ "forum-backend/api/swagger"`，不写包名，因此不用关心这一点。

### 1.4 生成命令逐项解释

```powershell
swag init -g cmd/server/main.go -o api/swagger --parseInternal
```

| 参数 | 作用 | 去掉会怎样 |
| --- | --- | --- |
| `-g cmd/server/main.go` | 指定通用 API 注解所在文件。项目入口在 `cmd/server/`，根目录没有 `main.go` | 默认找 `./main.go`，会报找不到 |
| `-o api/swagger` | 指定生成物目录 | 落进 `docs/`，见 1.3 |
| `--parseInternal` | 允许解析 Go `internal` 包里的类型定义 | 实测本项目不加也能生成，因为 DTO 就在默认扫描目录里。DTO 挪到别的 internal 包、或引用更深的依赖时会需要它，保留更稳 |

副本实测会打印一条 warning：

```text
warning: failed to get package name in dir: ./, error: ... no Go files in <project root>
```

原因是项目根目录没有 `.go` 文件，`go list ./` 拿不到包名。它不影响生成，三个文件照常产出，也不用为此在根目录塞一个空 `main.go`。

### 1.5 把命令固定下来

**增量修改**：打开 `Makefile`，把 `.PHONY` 行和文件末尾改成下面两处。其余 target 保持原样。

```makefile
.PHONY: run build test tidy swagger
```

```makefile
swagger: ## 重新生成 API 文档
	swag init -g cmd/server/main.go -o api/swagger --parseInternal
```

以后改完注解跑 `make swagger` 就行。也可以把同一行写成 `go:generate` 指令，放在 `main.go` 顶部，跑 `go generate ./...` 时触发。本章只固定 Makefile 一处，避免两条入口各自跑出不同参数。

---

## 2. 第 1 步：写通用 API 注解

### 2.1 增量修改：入口文件

**增量修改**：打开 `cmd/server/main.go`（04a 章的完整版本），在文件最开头、`package main` 之前插入下面这一整块注释。`package` 与后面的 `main` 函数一个字都不改。

```go
//go:generate swag init -g cmd/server/main.go -o api/swagger --parseInternal

// @title           Forum Backend API
// @version         1.0
// @description     社区论坛后端学习项目，统一响应格式为 { code, message, data }
// @host            localhost:8080
// @BasePath        /api
// @securityDefinitions.apikey Bearer
// @in              header
// @name            Authorization
```

生成物的空白导入放在 4.1 的 `router.go` 里，本章只放一处。`main.go` 里不要重复加，加了虽然也能编译，但两个文件都声明「文档在这里注册」只会让维护的人多找一个地方。

### 2.2 逐行拆解

| 注解 | 作用 |
| --- | --- |
| `@title` / `@version` | 必填，缺了生成会报错。版本跟项目阶段走，接口不兼容变更时再升 |
| `@host` | 本机地址。部署到别的环境要改成配置或从 UI 里手填，别把这个值当成生产承诺 |
| `@BasePath /api` | 所有 `@Router` 都写相对路径，生成时统一拼上 `/api`，和 `router.New` 里的 `r.Group("/api")` 对齐 |
| `@securityDefinitions.apikey Bearer` | 定义名为 `Bearer` 的 apiKey 安全方案 |
| `@in header` / `@name Authorization` | 该方案从 `Authorization` 请求头取值，受保护接口用 `@Security Bearer` 引用 |
| `//go:generate` | 让 `go generate ./...` 也能触发同一条命令，参数与 Makefile 完全一致 |

`@description` 里那句统一响应格式很重要。`pkg/response` 会把每个响应包成 `{ code, message, data }`，看文档的人第一眼就该知道 `data` 才是业务数据。

### 2.3 统一响应壳怎么进 Swagger

这是本项目接 Swagger 时最值得讲清的一处。`response.OK(c, resp)` 里的 `resp` 类型在运行时是 `any`，swag 靠静态注解推断不出泛型壳里的具体类型。

做法是定义一个只用于文档的壳类型，再用 swag 的组合语法在每个接口上覆盖 `data`。

**完整文件**：`internal/handler/swagger_dto.go`。新建文件。

```go
package handler

// SwaggerEnvelope 描述统一响应壳，只用于生成 API 文档。
// Data 声明成 interface{}，具体形状由每个接口的 @Success 注解给出。
// 这个结构体不参与运行时代码，不要把它传给 response.OK。
type SwaggerEnvelope struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data"`
}
```

注解写法：

```text
@Success 200 {object} handler.SwaggerEnvelope{data=handler.articleResp}
```

副本实测生成的 `swagger.json` 里，这个响应长成下面这样。

```json
{
  "200": {
    "description": "OK",
    "schema": {
      "allOf": [
        { "$ref": "#/definitions/handler.SwaggerEnvelope" },
        {
          "type": "object",
          "properties": {
            "data": { "$ref": "#/definitions/handler.articleResp" }
          }
        }
      ]
    }
  }
}
```

`allOf` 把壳和 `data` 的具体类型拼在一起。于是文档既能显示 `code`、`message`，又能展开 `data` 里的字段，且引用的是真实 DTO，不复制一份字段定义。字段改了但忘了改注解时，swag 会报找不到类型或生成旧形状，CI diff 会拦下。

`handler.articleResp` 是小写开头的未导出类型。副本实测 swag 用 AST 解析，能正常生成 `handler.articleResp`、`handler.createArticleReq`、`handler.userBrief` 三个定义。注解里的类型名带包名前缀更稳，本项目统一写 `handler.xxx`。

---

## 3. 第 2 步：给十个路径上的十三个方法写注解

### 3.1 注解放在哪一层

注解贴在 **handler 方法上方**，紧挨着 `func`。swag 扫描的是 Go 源文件里的注释，注解写进 service 或 router 里生成不出接口。

每个接口按同一套顺序写，读起来不会漏项。

```text
<方法名> godoc
@Summary       一句话
@Tags          分组
@Accept        json        （有请求体时）
@Produce       json
@Param         参数列表
@Success       200 {object} handler.SwaggerEnvelope{data=<DTO>}
@Failure       4xx/5xx
@Security      Bearer      （受保护接口）
@Router        <相对路径> [方法]
```

`@Router` 里的路径不带 `/api`，那是 `@BasePath` 的活。路径参数用花括号，例如 `/articles/{id}`。

### 3.2 完整文件：注解清单

**增量修改**：下面五组共十三块注解分别加到对应 handler 方法的原有注释上方。为了不让你在五个文件里来回拼接，这里按文件分组给出**完整的注解块**，复制后插到对应方法前面即可。方法体一行都不用改。

插入时有一条硬要求：**注解块和方法原有的中文注释之间不能留空行**。Go 的 AST 会把空行前后的注释放进两个不同的注释组，swag 只读紧邻 `func` 的那一组。留了空行，命令照样成功、`swagger.json` 里 `paths` 却是空的，属于最难查的一类错。正确形状是注解的最后一行 `@Router` 下面直接接 `// List 处理 GET ...`，再接 `func`。

**增量修改**：`internal/handler/user.go`（注册与登录）的两块注解。

```go
// Register godoc
// @Summary      用户注册
// @Tags         users
// @Accept       json
// @Produce      json
// @Param        body body handler.registerReq true "用户名与密码"
// @Success      200  {object}  handler.SwaggerEnvelope{data=handler.userResp}
// @Failure      400  {string}  string    "请求体格式不正确"
// @Failure      409  {string}  string    "用户名已存在"
// @Router       /register [post]

// Login godoc
// @Summary      用户登录
// @Tags         users
// @Accept       json
// @Produce      json
// @Param        body body handler.registerReq true "用户名与密码"
// @Success      200  {object}  handler.SwaggerEnvelope{data=handler.loginResp}
// @Failure      401  {string}  string    "凭据错误"
// @Router       /login [post]
```

**增量修改**：`internal/handler/article.go`（列表、详情、创建）的三块注解。

```go
// List godoc
// @Summary      文章分页列表
// @Tags         articles
// @Produce      json
// @Param        page       query  int  false  "页码，默认 1"
// @Param        page_size  query  int  false  "每页条数，默认 10，最大 50"
// @Success      200  {object}  handler.SwaggerEnvelope{data=handler.articleListResp}
// @Failure      400  {string}  string    "分页参数越界"
// @Router       /articles [get]

// Detail godoc
// @Summary      文章详情
// @Tags         articles
// @Produce      json
// @Param        id   path  int  true  "文章 ID"
// @Success      200  {object}  handler.SwaggerEnvelope{data=handler.articleResp}
// @Failure      400  {string}  string    "id 格式不正确"
// @Failure      404  {string}  string    "文章不存在"
// @Router       /articles/{id} [get]

// Create godoc
// @Summary      发布文章
// @Tags         articles
// @Accept       json
// @Produce      json
// @Param        body body handler.createArticleReq true "标题与正文"
// @Success      200  {object}  handler.SwaggerEnvelope{data=handler.articleResp}
// @Failure      400  {string}  string    "请求体格式不正确"
// @Failure      401  {string}  string    "未登录"
// @Security     Bearer
// @Router       /articles [post]
```

**增量修改**：`internal/handler/like.go`（点赞、取消、热榜）的三块注解。

```go
// Like godoc
// @Summary      点赞文章
// @Tags         likes
// @Produce      json
// @Param        id   path  int  true  "文章 ID"
// @Success      200  {object}  handler.SwaggerEnvelope{data=handler.likeResp}
// @Failure      401  {string}  string    "未登录"
// @Failure      404  {string}  string    "文章不存在"
// @Security     Bearer
// @Router       /articles/{id}/like [put]

// Unlike godoc
// @Summary      取消点赞
// @Tags         likes
// @Produce      json
// @Param        id   path  int  true  "文章 ID"
// @Success      200  {object}  handler.SwaggerEnvelope{data=handler.likeResp}
// @Failure      401  {string}  string    "未登录"
// @Security     Bearer
// @Router       /articles/{id}/like [delete]

// Hot godoc
// @Summary      热门文章 Top N
// @Tags         likes
// @Produce      json
// @Param        limit  query  int  false  "返回条数，默认 10，最大 100"
// @Success      200  {object}  handler.SwaggerEnvelope{data=handler.hotListResp}
// @Failure      400  {string}  string    "limit 非法"
// @Router       /articles/hot [get]
```

**增量修改**：`internal/handler/notification.go`（04a 章新增的两个接口）的两块注解。

```go
// List godoc
// @Summary      站内通知列表
// @Tags         notifications
// @Produce      json
// @Param        page       query  int  false  "页码，默认 1"
// @Param        page_size  query  int  false  "每页条数，默认 10，最大 50"
// @Success      200  {object}  handler.SwaggerEnvelope{data=handler.notificationListResp}
// @Failure      401  {string}  string    "未登录"
// @Security     Bearer
// @Router       /notifications [get]

// ReadAll godoc
// @Summary      标记全部通知已读
// @Tags         notifications
// @Produce      json
// @Success      200  {object}  handler.SwaggerEnvelope
// @Failure      401  {string}  string    "未登录"
// @Security     Bearer
// @Router       /notifications/read-all [put]
```

**增量修改**：`internal/handler/comment.go`（文章评论的三个接口）的三块注解。

```go
// List godoc
// @Summary      文章评论列表
// @Tags         comments
// @Produce      json
// @Param        id         path   int  true   "文章 ID"
// @Param        page       query  int  false  "页码，默认 1"
// @Param        page_size  query  int  false  "每页条数，默认 10，最大 50"
// @Success      200  {object}  handler.SwaggerEnvelope{data=handler.commentListResp}
// @Failure      400  {string}  string    "id 或分页参数不正确"
// @Router       /articles/{id}/comments [get]

// Create godoc
// @Summary      发表评论
// @Tags         comments
// @Accept       json
// @Produce      json
// @Param        id    path  int                        true  "文章 ID"
// @Param        body  body  handler.createCommentReq   true  "评论内容"
// @Success      200  {object}  handler.SwaggerEnvelope{data=handler.commentResp}
// @Failure      400  {string}  string    "id 或请求体格式不正确"
// @Failure      401  {string}  string    "未登录"
// @Failure      404  {string}  string    "文章不存在"
// @Security     Bearer
// @Router       /articles/{id}/comments [post]

// Delete godoc
// @Summary      删除自己的评论
// @Tags         comments
// @Produce      json
// @Param        id   path  int  true  "评论 ID"
// @Success      200  {object}  handler.SwaggerEnvelope
// @Failure      400  {string}  string    "id 格式不正确"
// @Failure      401  {string}  string    "未登录"
// @Failure      403  {string}  string    "只能删除自己的评论"
// @Failure      404  {string}  string    "评论不存在"
// @Security     Bearer
// @Router       /comments/{id} [delete]
```

评论这组最容易写错的是路径。列表与新增挂在 `/articles/{id}/comments` 下，这里的 `{id}` 是文章 id；删除挂在顶层的 `/comments/{id}`，这里的 `{id}` 是评论 id。两个 `{id}` 含义不同，`@Router` 必须严格照着 router 里的注册写，写顺手了很容易把删除也挂到文章路径下，生成的文档会指向一条不存在的接口。

`article.go`、`comment.go` 与 `notification.go` 各有一个 `List godoc`，分属不同方法，靠 `@Router` 区分。副本实测过同名方法加不同 `@Router` 会各自生成一条路径，不会互相覆盖。`ReadAll` 与评论删除的 `@Success` 只写壳，因为它们的 `data` 是运行时拼的 `gin.H` 或 `null`，没有对应的结构体，这两处如实不编造类型。

### 3.3 两处按进度调整的注解

| 位置 | 现在写的 | 什么时候改 |
| --- | --- | --- |
| `article.go` 的 `Detail` | `data=handler.articleResp` | 已按 04 章 4.10 落盘 `articleDetailResp` 之后，改成 `data=handler.articleDetailResp`，文档才会显示 `like_count` |
| `user.go` 的 `Login` | `body handler.registerReq` | 当前源码里 Login 复用了 Register 的请求结构体，没有独立的 `loginReq`。将来若拆出登录专用结构体，两个注解都要跟着改 |

这两处是「文档跟着进度走」的具体体现。02 章 8.0 把响应 DTO 集中到 `dto.go`，请求结构体仍留在 `user.go`。本章的注解按当前工作树核对过：注册与登录都用 `registerReq`，响应分别是 `userResp` 与 `loginResp`。写代码时若类型名与注解不一致，swag 会直接报找不到类型，属于能立刻发现的错。

### 3.4 常见错误对照

| 现象 | 原因 | 修法 |
| --- | --- | --- |
| `swag: command not found` | `$GOPATH/bin` 不在 PATH | 加 PATH 或写全路径 |
| `cannot find type definition: handler.xxx` | 注解里的类型名和源码不一致，或该类型还没落盘 | 按 3.3 核对工作树 |
| 生成成功但 UI 里路径全是错的 | `@Router` 里写了 `/api/...`，又被 `@BasePath` 拼了一次 | 路径写成相对 `/articles/{id}` |
| `go build` 报缺 `github.com/swaggo/swag` | 漏了 `docs.go` 需要的运行时库 | 三个包一起 `go get` |
| UI 能打开但文档是空的 | 少了 `_ "forum-backend/api/swagger"` 空白导入 | 按 2.1 补上 |
| `docs/` 里多出 `docs.go` | 用了默认 `-o docs` | 按 1.4 改成 `-o api/swagger`，把误生成的文件删掉 |
| `@Security Bearer` 没生效 | 安全定义名与注解里的名字不一致 | 两处都写 `Bearer`，大小写敏感 |
| 根目录下的 `no Go files` warning | 项目入口在 `cmd/server/`，根目录没有 `.go` 文件 | 实测无害，忽略即可 |

| 生成没报错，但 `swagger.json` 的 `paths` 是空数组 | 注解块与 `func` 之间隔了空行，被拆成两个注释组 | 把 `@Router` 那一行紧贴方法原有的中文注释，副本实测过这个坑 |
| 只有部分接口出现在文档里 | 注解漏了 `@Router`，或路径写错 | 按 5.2 打印路径清单逐个对 |

`go build` 不读注解，注解写错了编译照样通过。所以必须把 `swag init` 当成构建步骤的一部分，别等页面空了才回头查。

---

## 4. 第 3 步：挂上可交互的 Swagger UI

### 4.1 增量修改：路由

**增量修改**：打开 `internal/router/router.go`。加两个 import、一行空白导入，再在 `r := gin.Default()` 后面加一个条件路由块。其余构造与路由注册一行不改，`router.New` 签名不变。

```go
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
```

**增量修改**：同一个 import 块里再加一行空白导入。路径写模块名加目录，别写成相对路径。

```go
	_ "forum-backend/api/swagger"
```

**增量修改**：`r := gin.Default()` 之后加这个条件路由块。

```go
	// API 文档只在调试模式暴露。生产用 GIN_MODE=release 启动即可关闭。
	if gin.IsDebugging() {
		r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	}
```

空白导入只放在 `router.go`。路由文件是文档入口的注册点，`main.go` 里只留通用注解和 `//go:generate`，两处职责分开，找代码时不用猜。

`github.com/swaggo/files` 是独立的静态资源包。旧版教程里写 `github.com/swaggo/gin-swagger/swaggerFiles`，实测当前版本的导入路径是前者，以官方 README 与 `go.mod` 解析结果为准。

### 4.2 为什么用 `gin.IsDebugging()` 当开关

| 做法 | 代价 |
| --- | --- |
| 无条件挂路由 | 生产环境把全部接口形状、字段名、错误码摆在公网上 |
| 新增 `config.Server.EnableSwagger` 配置项 | 要改 `Config` 结构体、`config.yaml`、`router.New` 签名，牵动 02 章以来所有装配说明 |
| `gin.IsDebugging()` | 一行判断，零签名改动。用 `GIN_MODE=release` 关闭 |

第三条是学习阶段的最小改动。要如实说清它的性质：这是环境开关，不是访问控制。真要上线，Swagger UI 应该放到内网或加一层鉴权，00 章上线检查里有一条对应的待办。

### 4.3 装配链回看

```text
cmd/server/main.go
  -> 通用注解 + //go:generate
  -> signal.NotifyContext + WaitGroup（04a）
  -> router.New(db, jwtManager, rdb)        签名未变
       -> _ "forum-backend/api/swagger"     注册生成物
       -> gin.IsDebugging() ? /swagger/*any
       -> /api 组（业务路由全部不变）
```

从 02 章到本章，`router.New` 的签名一直是三参数。加缓存、加点赞、加通知、加文档都没动它，这是 03 章 7.2 定下装配链之后最值得保住的性质。

---

## 5. 第 4 步：生成、编译与验收

### 5.1 一条完整命令链

```powershell
swag init -g cmd/server/main.go -o api/swagger --parseInternal
gofmt -w cmd/server/main.go internal/router/router.go internal/handler/swagger_dto.go
go build ./...
go vet ./...
go test ./...
```

顺序不能倒。注解改完先生成，`docs.go` 不存在时 `go build` 会报找不到包。

生成物 `docs.go`、`swagger.json`、`swagger.yaml` 由 swag 自己格式化，不要手工 `gofmt -w api/swagger/docs.go` 之后再改内容。它们属于生成物，唯一正确来源是重跑命令。

### 5.2 检查生成内容

```powershell
python -c "import json;d=json.load(open('api/swagger/swagger.json',encoding='utf-8'));print(d['swagger'],d['basePath'],len(d['paths']));print(sorted(d['paths']))"
```

期望看到 `2.0 /api` 和十个路径（`/articles` 与 `/articles/{id}/like`、`/articles/{id}/comments` 各含两个方法）。再查安全定义与 `allOf`：

```powershell
python -c "import json;d=json.load(open('api/swagger/swagger.json',encoding='utf-8'));print(d['securityDefinitions']);print(d['paths']['/articles/{id}']['get']['responses']['200'])"
```

### 5.3 启动后核对

```powershell
go run ./cmd/server
```

另开一个终端：

```powershell
curl.exe http://localhost:8080/swagger/doc.json
curl.exe -o NUL -w "%{http_code}" http://localhost:8080/swagger/index.html
```

浏览器打开 `http://localhost:8080/swagger/index.html`，点右上角 Authorize，填入 `Bearer <token>`（本项目 `pkg/jwt` 与 `middleware.JWTAuth` 要求完整的 `Bearer ` 前缀），再试一个受保护接口，例如 `PUT /articles/{id}/like`。

```powershell
$env:GIN_MODE="release"
go run ./cmd/server
curl.exe -o NUL -w "%{http_code}" http://localhost:8080/swagger/index.html
```

第二次应返回 404。验证完把 `GIN_MODE` 从当前会话移除，避免影响后面章节的调试输出。

### 5.4 勾选项

- [ ] B1：三个生成文件落在 `api/swagger/`，`docs/` 里没有多出 `.go` 或 `.json`
- [ ] B2：`swagger`、`basePath`、`securityDefinitions.Bearer` 三项都对
- [ ] B3：十个路径都在，成功响应带 `allOf` 与具体 `data` 类型
- [ ] B4：三项 go 命令通过
- [ ] B5：调试模式能打开 UI，`doc.json` 有内容，带 token 能试接口
- [ ] B6：release 模式返回 404
- [ ] B7：能说清 CI diff 检查的做法（当前仓库无 `.git`，只要求能描述）
- [ ] 能解释统一响应壳为什么要用 `SwaggerEnvelope{data=...}`

---

## 6. 契约防漂移

### 6.1 生成物纳入版本控制，CI 重跑后比 diff

注解生成解决了「手写表格会漂移」，还剩一个口子：有人改了注解或 DTO，忘了重跑 `swag init`，仓库里的 `swagger.json` 就是旧的。

标准做法是三个生成文件都提交，CI 里重跑同一条命令，再检查有没有差异。

```powershell
swag init -g cmd/server/main.go -o api/swagger --parseInternal
git diff --exit-code -- api/swagger
```

有差异就说明开发者改了代码没重新生成，CI 直接失败。`ci.yml` 里的 diff 步骤用 `git add -N api/swagger` 让新增的生成物也参与比较，避免 untracked 文件被漏掉。本机没有 `.git`，工作流推送后才会真正执行。

### 6.2 一个守住路径清单的小测试

CI diff 能发现「生成物过期」，发现不了「注解整个漏写」。再加一个轻量测试，直接读生成物核对关键路径。

**完整文件**：`internal/router/swagger_contract_test.go`。新建文件。

```go
package router

import (
	"encoding/json"
	"os"
	"testing"
)

// TestSwaggerContractCoversCorePaths 核对生成物里的核心路径。
// 文件缺失直接失败，CI 里先跑 swag init 再跑本测试。
func TestSwaggerContractCoversCorePaths(t *testing.T) {
	raw, err := os.ReadFile("../../api/swagger/swagger.json")
	if err != nil {
		t.Fatalf("read swagger.json: %v", err)
	}
	var doc struct {
		Swagger  string                     `json:"swagger"`
		BasePath string                     `json:"basePath"`
		Paths    map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse swagger.json: %v", err)
	}
	if doc.Swagger != "2.0" {
		t.Fatalf("swagger version = %q, want 2.0", doc.Swagger)
	}
	if doc.BasePath != "/api" {
		t.Fatalf("basePath = %q, want /api", doc.BasePath)
	}

	want := []string{
		"/articles",
		"/articles/{id}",
		"/articles/{id}/like",
		"/articles/hot",
		"/notifications",
		"/notifications/read-all",
	}
	for _, path := range want {
		if _, ok := doc.Paths[path]; !ok {
			t.Errorf("missing path %s", path)
		}
	}
}
```

测试放在 `internal/router` 包，相对路径 `../../api/swagger/swagger.json` 从该包目录出发指向项目根下的生成物。仓库已落盘的版本用 `t.Fatalf`，文件缺失直接失败。CI 先跑 `swag init` 再跑 `go test ./...`，顺序保证生成物一定存在。

没生成过文档的环境只跑 `go test ./...` 会红，这是有意的。契约检查应当是硬门槛。

### 6.3 明确不做

| 未做 | 说明 |
| --- | --- |
| OpenAPI 3.x | `swag` 当前生成 Swagger 2.0。需要 3.x 要换工具或升级到支持 OAS3 的版本，别把 2.0 说成 3.0 |
| 自动生成的客户端 SDK | 需要 openapi-generator 一类的工具，本项目没有前端消费方 |
| 每个接口的多组示例响应 | 本章每接口给一组 `@Success` 与主要 `@Failure`。要展示更多错误分支，按 3.1 的模板继续加即可 |
| 契约测试（按文档自动发请求比对响应） | 6.2 只检查路径清单，不校验字段值与状态码 |
| Swagger UI 的鉴权与内网隔离 | 只用 `gin.IsDebugging()` 做环境开关，见 4.2 |
| Postman collection 与文档双向同步 | 03 章验收脚本仍是手工维护的 |
| CI 文件本身 | `.github/workflows/ci.yml` 已落盘，单 job 六步；未做 matrix、缓存、MySQL 与 Redis service、部署阶段 |

---

## 7. 简历与面试表达

### 7.1 一句话描述模板

> 为主要 HTTP 接口补充声明式注解，用 `swag init` 从代码生成 Swagger 2.0 文档与可交互 UI；通过 `SwaggerEnvelope{data=...}` 组合语法让统一响应壳展示出具体 DTO；生成物纳入版本控制，配合 CI 重跑 diff 与路径清单测试防止契约漂移，生产环境用 `GIN_MODE=release` 关闭文档入口。

用之前先按 06 章的原则核对：接口真的验收过、生成物真的跑出来过、release 模式真的返回过 404。没跑过的句子不要写。

### 7.2 追问准备

| 追问 | 应答要点 |
| --- | --- |
| 生成的是 OpenAPI 3 还是 Swagger 2.0 | 实测 `swagger.json` 里是 `"swagger": "2.0"`。要主动说清版本，别混用术语 |
| 为什么注解写在 handler 而不是 router | swag 扫 Go 文件里的注释，handler 方法上方能同时看到参数解析与响应类型；router 只有一行注册，信息不够 |
| 统一响应 `{code,message,data}` 怎么在文档里表达 | 定义 `SwaggerEnvelope`，每个接口用 `allOf` 覆盖 `data`，引用真实 DTO，不复制字段 |
| 怎么防止注解和代码不同步 | 生成物入库，CI 重跑 `swag init` 后 `git diff --exit-code`；再用一个测试守住核心路径清单 |
| UI 挂在生产环境安全吗 | 本项目用 `gin.IsDebugging()` 关闭，属于环境开关，不是访问控制。真上线还要内网隔离或鉴权 |
| 为什么生成物放 `api/swagger/` | 默认 `docs/` 会和学习文档混在一起，副本实测过这个坑 |
| 加 `--parseInternal` 有什么用 | 允许解析 Go internal 包的类型定义。实测本项目不加也能生成，保留是为了 DTO 挪包后不返工 |

---

## 8. 本章勘误、进度快照与验证记录

| 状态 | 内容 |
| --- | --- |
| 本章文档已给 | 通用注解、十个路径上十三个方法的注解、壳类型、UI 路由、Makefile、生成与验收命令、契约测试 |
| 仓库源码当前状态 | 注解、壳类型、UI 路由、Makefile、`ci.yml`、契约测试与三个生成物均已落盘。教程正文的「增量修改」仍可照着跟做 |
| 已具备的前置件 | `router.New` 三参数装配链、`pkg/response` 统一响应壳、JWT 中间件、通知 DTO |
| 明确不做 | 见 6.3 |
| 明确未跑 | 真实浏览器里带 token 点接口、GitHub Actions 首次运行、release 模式的端到端演练、`go test -race`（本机 `CGO_ENABLED=0`） |

**实测记录**（2026-11-08，项目副本）：`go install github.com/swaggo/swag/cmd/swag@latest` 得到 CLI v1.16.4，`go get` 解析出 gin-swagger v1.6.1、files v1.0.1、swag v1.16.6。`swag init -g cmd/server/main.go -o api/swagger --parseInternal` 把三个文件生成到 `api/swagger/`，`docs/` 未被污染；`swagger.json` 为 Swagger 2.0、`basePath=/api`、8 个路径、13 个 definition、`securityDefinitions.Bearer` 正确，详情响应的 200 schema 带 `allOf` 且 `data` 指向 `handler.articleResp`，点赞接口带 `Bearer`。改动过的文件 `gofmt -l` 无输出，`go build ./...`、`go vet ./...`、`go test ./...` rc=0，`TestSwaggerContractCoversCorePaths` PASS。默认 `-o` 会落进 `docs/`、注解与 `func` 之间留空行导致 `paths` 为空、根目录 `no Go files` warning 三项均为副本实测。副本已删除，原项目源码未改。真实浏览器试用、CI diff、release 模式演练仍未执行。

**落盘记录**（2026-11-09，真实工作树）：按 2.1、3.2、4.1 把通用注解、十块接口注解、`swagger_dto.go`、UI 路由写入源码；新增 `.github/workflows/ci.yml` 与 `internal/router/swagger_contract_test.go`（硬失败版）；`Makefile` 补 `swagger` target；`go mod tidy` 后三个 swaggo 依赖升为直接依赖。CLI 固定 `v1.16.6`，注意上游 `version.go` 里 `Version` 常量仍写 `v1.16.4`，`swag --version` 显示的不是模块版本。真实项目里 `swag init` 产出 8 个路径、13 个 definition、详情 `allOf` 指向 `handler.articleResp`、点赞带 `Bearer`；二次生成哈希一致；`gofmt -l` 无输出，`go build ./...`、`go vet ./...`、`go test ./...` 全绿。`Detail` 的注解按当前真实返回写 `articleResp`，等 handler 切到 `toArticleDetailResp` 后要同步改成 `articleDetailResp`。GitHub Actions 首次运行、浏览器试用、release 模式演练仍未执行。

**补齐记录**（2026-11-11，真实工作树）：初版漏了 `internal/handler/comment.go`，导致评论的三个接口不在 `paths` 里、Swagger UI 上也看不到。补上五组中的第三组后重新生成，`paths` 从 8 条变 10 条、definition 从 13 个变 16 个，`/articles/{id}/comments` 含 get 与 post、`/comments/{id}` 含 delete。`swagger_contract_test.go` 的期望清单同步扩到十条，以后任何 handler 漏注解都会在 CI 里失败。二次生成哈希一致，`gofmt -l` 无输出，`go build`、`go vet`、`go test` 全绿。

---

## 9. 参考来源

技术结论以英文官方文档与本机实测为准。

- swaggo/swag 声明式注解格式、`swag init` 参数、Swagger 2.0 输出：https://github.com/swaggo/swag
- swaggo/gin-swagger 导入路径、`WrapHandler` 用法、嵌套目录示例：https://github.com/swaggo/gin-swagger
- swaggo/files 静态资源包：https://github.com/swaggo/files
- Swagger 2.0 规范与 OpenAPI 3.x 的区别：https://swagger.io/specification/v2/ 、 https://spec.openapis.org/oas/v3.1.0.html
- Gin `gin.IsDebugging()` 与 `GIN_MODE`：https://gin-gonic.com/en/docs/deployment/
- Go `go generate` 与 `go install`：https://go.dev/blog/generate 、 https://go.dev/ref/mod#go-install
- Go Doc Comments 与导出标识符注释：https://go.dev/doc/comment 、 https://go.dev/wiki/CodeReviewComments

**本套文档内部**：00 章「目标 API 合同」「教程代码块的统一标识」「上线前总检查」，02 章 8.0（DTO 落点），03 章 7.2（三层装配链），04 章 0.5（装配链一次定死）、4.10（详情 DTO），04a 章（通知 DTO、main 的优雅关闭），05 章（测试与发布准备），06 章（简历表达原则）。

> 下一步：[`04c-第3周进阶-WebSocket实时通知.md`](04c-第3周进阶-WebSocket实时通知.md)。把 04a 与 04b 的验收勾选完再往下走，04c 要用到 04a 的 `NotificationWorker`，也会改动 `router.New` 的签名，前面的验收没跑完就动手，出问题时分不清是哪一章引入的。

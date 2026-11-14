# 03a · 第 2 周进阶：Redis 缓存工程化加固

> 定位：这一章接在 03 章第 7 节之后。03 章把 Cache Aside 接通，得到一个能跑的详情缓存；这一章把它加固到出事时不会把 MySQL 打垮，并且把每一步的设计理由讲透。
>
> 章名里的 `03a` 只影响目录排序。现有周次一个都不动，第 3 周仍是 04 章，第 4 周仍是 05 章。理由见 0.4。
>
> 本章给出完整可跟做的代码。仓库源码现已包含这套加固，正文按落盘前的时间点逐步推演。文档写了不等于验收过了，这条规矩见 00 章。
>
> 快照日期：2026-11-07。开工前按 0.2 重新核对源码，别把这张快照当成永久事实。
>
> **现状更新（同日复核）**：本章的加固代码已经落盘，`internal/service/article_cache.go`、`internal/service/article.go` 的 `Detail`、`internal/handler/errors.go` 的全局兜底都已存在。正文按「当时还没修」的时间点逐步推演，读到「当前」「现在」时按演进起点理解，别照着把已修好的源码改回去。4.5、6.4、6.5 讲的缺陷在源码里已经修复，删完文章立刻 GET 详情现在返回 404。

## 0. 先把现状对齐

### 0.1 你现在手里有什么

下表按 2026-11-07 的 `internal/service/article.go`、`internal/config/redis.go`、`internal/handler/errors.go` 逐行核对。左边是能力，右边是它在源码里的位置。

| 已有能力 | 源码位置 | 具体行为 |
| --- | --- | --- |
| Cache Aside 读路径 | `article.go` 的 `Detail` | 先 `Get`，命中且 JSON 可解析就返回，否则回源 `queryDetail` 再 `Set` 回填 |
| 坏缓存自愈 | `Detail` 的命中分支 | 反序列化失败时 `log.Printf` 并 `Del` 掉坏 key |
| Redis 读故障降级 | `Detail` 的 `else if err != redis.Nil` | 断连等错误记日志，继续回源 MySQL |
| 回填失败不阻断 | `Detail` 末尾 | `Set` 失败只记日志，本次数据库结果照常返回 |
| 写路径缓存失效 | `Create` 第 50 行、`Delete` 第 105 行 | 数据库写入或事务提交成功后调用 `InvalidateDetailCache` |
| 失效方法已实现 | `article.go` 第 157 行 | `Del` 同一个 `articleDetailKey + id`，失败只记日志 |
| 客户端失败时释放资源 | `config/redis.go` 第 26 行 | `Ping` 失败分支里有 `_ = rdb.Close()` |
| 三层装配链 | `main.go` → `router.New` → `NewArticleService` | `rdb` 在入口创建一次，注入 `ArticleService.rdb`，handler 不认识 Redis |

这三项在旧版 README、00 章、03 章 7.6 里被写成「待实现」或「还缺」，属于进度快照过期，本次已一并更正。**写路径失效和 `rdb.Close()` 都已落地，本章不会再让你写第二遍。**

### 0.2 开工前的核对动作

```powershell
go version
go build ./...
go test ./...
gofmt -l ./cmd ./internal ./pkg
```

本章核对时是 `go1.27.3`，`go.mod` 声明 `go 1.26.0`，`go build ./...`、`go vet ./...` 均通过。`gofmt -l` 会列出十几个文件，那是学习过程中手写代码的正常状态，本章新增的两个文件要自己保证格式，别顺手把全仓库 `gofmt -w` 一遍，否则 diff 会淹没你正在学的东西。

再打开三个文件确认下面几点，任何一条对不上就先停下来读源码，别照抄本章代码：

1. `ArticleService` 结构体有 `db` 和 `rdb` 两个字段。
2. `Detail` 的签名是 `Detail(ctx context.Context, id uint) (*model.Article, error)`。
3. `Create` 与 `Delete` 里已经有 `s.InvalidateDetailCache(...)` 这一行。

### 0.3 还差什么

| 缺口 | 现在的具体表现 | 后果 | 本章哪一节补 |
| --- | --- | --- | --- |
| TTL 固定 | `Set(..., 10*time.Minute)` | 同一批写入的 key 会在同一秒集体过期 | 3、6.1 |
| 空值缓存缺失 | 查不存在的 id 每次都回源 | 恶意或偶然的无效 id 直接压到数据库 | 4、6.4 |
| 并发回源未合并 | 热点 key 过期瞬间 N 个请求各查一次库 | 一次过期打成 N 条 SQL | 5、6.4 |
| `ErrRecordNotFound` 未映射 | `queryDetail` 直接返回 GORM 错误，`mapServiceError` 只认 `service.ErrNotFound` | 起点状态下查不存在的文章返回 500，空值缓存也无从判断「不存在」 | 4.5、6.5 |
| key 无版本 | `article:detail:{id}` | 改了 JSON 形状后老缓存会一直被读到，直到过期 | 2.3、6.1 |
| 缓存调用没有独立超时 | 直接把请求 `ctx` 传给 Redis，`InitRedis` 没设 timeout | 网络卡住会拖住 HTTP 请求 | 8.2、6.6 |
| 降级行为没有定义 | 代码里散落着 `log.Printf` | 出事时说不清接口会怎样，也没法验收 | 8.1 |
| 没有可观测数据 | 命中与未命中只靠「第二次好像快一点」 | 三大问题有没有解决，拿不出证据 | 9、6.1 |

### 0.4 为什么单独成章，不把后面周次后移

把「缓存加固」插成正式的第 3 周，再把点赞、热榜、限流推到第 4 周，测试推到第 5 周，看起来更整齐。本章不这么做，有三个理由，都是设计取舍，不是偷懒：

1. **加固是对第 2 周代码的回头修改**。它改的是 `article.go` 的 `Detail`、`config/redis.go`、`handler/errors.go`，全都是第 2 周刚写过的文件。放在紧邻的进阶专题里，读者刚写完的代码还热着，改起来心智负担最小。挪到一周之后，中间隔着点赞、热榜、限流三个新模块，再回头动 `Detail`，很容易把新功能带崩。
2. **周次后移会制造大量交叉引用修正**。README 的学习路线、00 章的阶段表、01 章的四周地图、03 章末尾的「下一步」、05 章的测试说明里都写着周次与文档编号。改一周要同步七八处，漏一处就会让新手在文档之间迷路。03 章 6.1 刚因为「装配顺序太乱」返工过一次，不该再引入同类风险。
3. **专题可以有自己的节奏**。这一章的代码量接近一整周，但概念密度更高，适合一口气做完。做成 `03a` 之后，想先去 04 章看亮点的人可以跳过，想扎扎实实把缓存讲清楚的人可以按顺序做，两条路径都不破坏周次编号。

代价要如实写：目录里多了一个非整数编号，01 章的「4 周学习地图」表格之外多了一句说明；04 章开工前要提示「做过 03a 的人 `golang.org/x/sync` 已经装好」。这两处本次都已补齐。

### 0.5 这一章的验收合同

做完本章之后，你可以说：

- 详情缓存的 TTL 每次不同，落在一个可核对的区间里。
- 查不存在的 id 会写短 TTL 的空标记，第二次不再回源，且返回 404。
- 同一 key 的并发未命中只回源一次。
- 写库成功后删缓存，写后读不返回旧内容。
- Redis 读写失败时详情接口仍能回源，并且有日志和计数器可查。

你不能说：

- 解决了多实例下的缓存一致性（singleflight 只在单进程内生效）。
- 解决了恶意构造海量不同 id 的攻击（本章只做了 id 格式与范围校验）。
- 有性能提升数字（本章没有压测，任何数字都是编的）。
- 具备生产级熔断、多级缓存、布隆过滤器或 Prometheus 告警。

00 章的规矩在这里同样适用：**判断完成度用「验收通过」，不用「代码写完了」。**

---

## 1. 先学会怎么想

这一章不讲一个新 API，讲一套可以反复用的思考顺序。缓存加固的难点从来不在记住三个名词，而在于看见一段代码能自己问出「这里会怎么坏」。

### 1.1 第一 principle：缓存是副本

先把身份定死，后面所有判断都从这一句推出来。

```text
MySQL 里的数据是事实（source of truth）
Redis 里的数据是某时刻的副本（copy）
```

由此得到四条直接推论，本章每个设计都在这四条里：

| 推论 | 意思 | 对应的代码决策 |
| --- | --- | --- |
| 副本可以丢 | Redis 全清空，服务仍然要能返回正确数据 | 所有 Redis 错误都降级回源，不能让接口失败 |
| 副本会旧 | 事实变了，副本不会自动跟着变 | 写路径必须主动删缓存，且必须给 TTL 兜底 |
| 副本可以被伪造 | 缓存里的字节可能损坏、可能是旧格式 | 命中后先解析、解析失败当未命中，key 带版本号 |
| 副本不该放大故障 | 缓存的引入不能让数据库比以前更容易被打垮 | 空标记、抖动、请求合并，以及给 Redis 设超时 |

新手最容易犯的错，是把缓存当成「另一个数据库」来写：先写缓存、再写库，或者缓存写失败就把整次请求判成失败。副本的身份一定下来，这两种写法立刻就不成立。

### 1.2 四问法

拿到任何一个要缓存的接口，先回答四个问题，再动手写代码。本章用 `GET /api/articles/:id` 做示范。

| 问题 | 本项目的答案 | 写错会怎样 |
| --- | --- | --- |
| 谁读 | 任意游客，读频率高，单篇内容改得少 | 给写多读少的接口加缓存，命中率低，白付复杂度 |
| 谁写 | 只有作者本人删除；当前没有编辑接口；创建时 id 由 MySQL 自增 | 漏掉一个写路径，就会一直读到旧内容 |
| 什么时候失效 | 数据库事务提交成功之后删 key | 删早了，事务回滚会留下脏删；删晚了，旧数据窗口变长 |
| 失效了会怎样 | 下次读回源重建；删失败时旧数据最多存活一个 TTL | 没有 TTL 兜底时，一次删失败就是永久脏数据 |

「谁写」这一问最容易被漏。03 章 7.4 已经提醒过：以后加编辑接口，编辑方法里也要调 `InvalidateDetailCache`。本章第 7 节会把所有写路径列成一张表，加新接口时照表补一行。

### 1.3 故障注入思维：把链路画出来逐环问

把一次详情请求摊成一条链，然后对每一个箭头问「这一步挂了、慢了、返回了怪东西，会怎样」。

```text
HTTP 请求
  │
  ├─① 解析 id                 挂了：400，与缓存无关
  │
  ├─② GET Redis key          挂了：连接错、超时
  │      ├─ 命中真实 JSON       正常：返回
  │      ├─ 命中空标记          正常：返回 404
  │      ├─ 命中坏字节          异常：解析失败
  │      └─ redis.Nil          正常：未命中
  │
  ├─③ 合并并发请求            挂了：所有等待者一起拿到同一个错误
  │
  ├─④ SELECT MySQL            挂了：500；查无此行：404
  │
  ├─⑤ SET Redis 回填          挂了：本次结果仍要返回
  │
  └─⑥ 返回响应
```

逐环的结论：

| 环节 | 失败形态 | 本章定的行为 | 理由 |
| --- | --- | --- | --- |
| ② 连接错、超时 | `Get` 返回非 `redis.Nil` 的 error | 记日志、记降级计数、继续走 ④ | 副本不可用，事实还在 |
| ② 坏字节 | `json.Unmarshal` 报错 | 删掉坏 key，继续走 ④ | 不修的话每个请求都会重复读到坏值 |
| ② 空标记 | 命中哨兵字符串 | 直接返回 404，不进 ④ | 空标记就是为了挡住这一步 |
| ③ 第一个请求被取消 | 闭包捕获的 ctx 失效 | 用 `WithoutCancel` 脱离取消，另设 3 秒超时 | 否则等待者会一起拿到 `context.Canceled` |
| ④ 查无此行 | `gorm.ErrRecordNotFound` | 转成 `ErrNotFound` → 404，并写空标记 | 落盘前的代码会掉进 default 返回 500 |
| ④ 数据库真错 | 连接错、超时 | 返回 500，**不写空标记** | 把故障缓存成「不存在」会在恢复后继续错 60 秒 |
| ⑤ `Set` 失败 | Redis 写不进去 | 只记日志和降级计数 | 副本写失败不能推翻已经成功的事实查询 |

这张表就是本章的降级规格。第 8 节会把它按「读、写、删、整体宕机」重新整理成对外可见的分级表。

### 1.4 三个经典故障，是从时间轴推出来的

「穿透、击穿、雪崩」是中文工程社区常用的三个说法，Redis 官方文档里没有这三个术语。它们是对三类不同时间形状的故障的命名。别背定义，画时间轴自己推。

先约定一件事：缓存 key 只在「未命中回源」时被写入，写入时带上 TTL。所以 **key 的过期时刻 = 回源时刻 + TTL**。这是后面所有推导的起点。

#### 穿透：查询的 key 永远不会被写入有效值

```text
请求 id=999999（数据库里没有）
  → GET 返回 redis.Nil
  → SELECT 返回「查无此行」
  → 没有文章可以序列化，于是什么都不写
  → 下一个 id=999999 的请求，又从 GET redis.Nil 开始
```

时间轴上没有任何一个点会让这条链停下来。一万次请求就是一万次 SELECT。故障形状是「**单个不存在的 key，持续不断地回源**」。

要注意一个真实缺陷：当前代码里 `queryDetail` 直接把 `gorm.ErrRecordNotFound` 抛给 handler，而 `mapServiceError` 没有对应分支，会落到 default 返回 500。也就是说，现在连「正确地回答这是一篇不存在的文章」都没做到。加空值缓存之前必须先修这个，见 4.5。

#### 击穿：一个热点 key 在过期瞬间被并发回源

```text
t0  热点文章 id=1 的 key 存在，1000 QPS 全部命中
t1  key 到达 TTL，Redis 删除它
t1+ε  同时来了 200 个请求，每个都 GET 到 redis.Nil
      每个都认为自己该回源
  → 200 条一模一样的 SELECT 同时打到 MySQL
t1+ε+δ  200 个请求各自 SET 同一个 key
```

故障形状是「**一个 key，一个瞬间，N 次回源**」。注意 200 次回填里有 199 次是纯浪费，结果完全相同。

这里有个容易误判的点：热点 key 过期本身很正常，不正常的是「N 个请求同时发现它过期」。所以解法要打断的是「同时发现」到「各自回源」这一环。

#### 雪崩：大量 key 在同一时刻集体过期

```text
t0  服务启动，缓存预热脚本把 1000 篇文章依次写进 Redis
    每次 SET 都用 10*time.Minute
    写入时间集中在 t0 到 t0+3s
t0+10m±3s  1000 个 key 几乎同时到期
    1000 篇文章的读请求同时 miss
    同时回源 1000 条 SELECT，MySQL 连接池被打满
    每个请求又要回填，Redis 也跟着忙
    响应变慢 → 请求堆积 → 更多超时 → 重试 → 更慢
```

故障形状是「**很多 key，同一时刻，集体回源**」。固定 TTL 加上「同一批写入」必然制造这个结果，哪怕业务流量本身很平稳。

还有第二种常被一起叫雪崩的情况：Redis 整体不可用，所有 key 同时失去作用，请求全部回源。抖动解决不了这一种，因为它没有「过期时刻」可摊。第 8 节单独处理它。

#### 三者怎么区分，别记混

| 维度 | 穿透 | 击穿 | 雪崩 |
| --- | --- | --- | --- |
| key 数量 | 一个（或一批不同的无效 id） | 一个 | 很多 |
| key 是否存在过 | 从来没写入有效值 | 写入过，刚好过期 | 写入过，集体到期 |
| 是否热点 | 无所谓 | 必须是热点 | 无所谓 |
| 时间形状 | 持续、平坦 | 一个尖峰 | 一个更宽的尖峰 |
| 数据库看到的 | 一直有无效 SELECT | 一瞬间 N 条相同 SELECT | 一瞬间 N 条不同 SELECT |
| 根因 | 缓存没有「不存在」这个状态 | 并发没有协调 | 过期时刻被对齐了 |

### 1.5 每个解法打断时间轴上的哪一环

把解法挂回刚才的时间轴，写代码时就知道自己到底在防什么。

| 解法 | 打断的那一环 | 本章位置 |
| --- | --- | --- |
| 空值缓存（哨兵 + 短 TTL） | 穿透链里「什么都不写」那一步，改成写一个可识别的「不存在」状态 | 4、6.4 |
| id 格式与范围校验 | 穿透链最前面，把明显非法的 id 挡在缓存之前 | 4.2 |
| TTL 随机抖动 | 雪崩里「过期时刻 = 回源时刻 + 固定 TTL」这一等式，把固定值换成区间 | 3、6.1 |
| singleflight | 击穿里「N 个请求各自回源」，改成一次回源、N 个请求共享结果 | 5、6.4 |
| 双重检查缓存 | singleflight 里「等待者被唤醒后也去回源」，避免合并白做 | 5.4、6.4 |
| 写后删缓存 + TTL 兜底 | 副本变旧的时间窗口，让它有上界 | 7 |
| Redis 超时 + 连接池上限 | 第二种雪崩里「请求被拖住」的时间，给它一个上界 | 8.2 |
| 降级回源 | 缓存故障时「接口跟着失败」这一环 | 1.3、8.1 |

### 1.6 加固不是免费的

每加一招都要付代价。设计就是把代价摆到台面上再选，而不是把听过的招全堆上去。

| 解法 | 换来了什么 | 付出的代价 | 本章的判断 |
| --- | --- | --- | --- |
| TTL 抖动 | 过期时刻摊开 | 数据可能比固定 TTL 多旧最多 2 分钟 | 接受，详情内容改动少 |
| 空值缓存 | 挡住无效 id 回源 | 占用内存；后来真写入同 id 时会有最多 60 秒的「查不到」 | 接受，并在写路径主动删空标记 |
| singleflight | 并发回源合并成一次 | 引入 `golang.org/x/sync`；同 key 的请求会有轻微等待；一个失败会连累所有等待者 | 接受，用超时和 `WithoutCancel` 控制 |
| 布隆过滤器 | 更早挡住穿透 | 要引入依赖或自己实现、有误判率、删除元素麻烦、要预热与重建 | 本阶段不做，理由见 4.7 |
| 分布式锁 | 跨实例也能合并 | 要处理锁超时、续期、误删、Redis 故障时的死锁 | 本阶段不做，理由见 5.2 |
| 逻辑过期（不设 TTL，后台刷新） | 彻底消除过期尖峰 | 要额外线程、要处理刷新失败、内存常驻 | 本阶段不做，代码量超出学习切片 |
| 延迟双删 | 缩小脏数据窗口 | sleep 时长没有依据、吞吐下降、仍不彻底 | 讲原理不实现，见 7.5 |

### 1.7 官方明文与工程惯例分开记

延续 00 章的规矩，把「库或语言保证的」和「社区总结的」分开，别把惯例当铁律。

| 结论 | 归属 | 依据 |
| --- | --- | --- |
| `math/rand/v2` 顶层函数并发安全 | Go 官方明文 | 本机 `D:\Go SDK\src\math\rand\v2\rand.go:12-13` 原文：`Top-level functions, such as [Float64] and [Int], are safe for concurrent use by multiple goroutines.` |
| `singleflight.Do` 同一 key 同一时刻只执行一次，重复调用者拿到同一份结果 | `x/sync` 官方明文 | 本机 `golang.org/x/sync@v0.24.0/singleflight/singleflight.go:86-90` 的 `Do` 注释 |
| `ContextTimeoutEnabled` 控制客户端是否尊重 ctx 的超时 | go-redis 官方明文 | 本机 `github.com/redis/go-redis/v9@v9.23.0/options.go:174-176` 字段注释 |
| `SET` 带过期参数后，到期自动删除 key | Redis 官方明文 | https://redis.io/docs/latest/commands/set/ |
| 穿透、击穿、雪崩这三个名词及分类 | 中文工程社区惯例 | Redis 官方文档不用这套术语，它们描述的是三类故障时间形状 |
| 「TTL = 基础值 + 随机抖动」「空标记用短 TTL」「热点合并用 singleflight」 | 工程惯例 | 由上面几条官方机制组合推导，不是任何一份官方文档的硬性要求 |
| 抖动幅度取基础 TTL 的 20% | 本项目自定 | 3.2 有推导过程，不是通用常数 |
| 逐行拆解表、落盘顺序表 | 本套教程自定 | 00 章「本套文档怎么写」 |

---

## 2. 落地蓝图

### 2.1 每一步都能编译

本章按「文件落盘顺序」组织，不是按「想到哪写到哪」。每一步结束时都给出该步的 `go build ./...` 或 `go test` 命令，通不过就不要往下走。

| 步骤 | 打开哪个文件 | 动作 | 该步结束时的状态 | 节 |
| --- | --- | --- | --- | --- |
| 1 | `internal/service/article_cache.go` | 新建 | 新增纯函数与计数器，不碰任何现有文件，直接能编译 | 6.1 |
| 2 | `internal/service/article_cache_test.go` | 新建 | 三个测试通过，先把 TTL 与哨兵的正确性钉死 | 6.2 |
| 3 | `go.mod` | `go get` | 加入 `golang.org/x/sync`，此时还没人 import 它 | 6.3 |
| 4 | `internal/service/article.go` | 增量修改 | 替换 import、结构体、`Detail`，新增四个私有辅助方法，`queryDetail` 改成错误转换版 | 6.4 |
| 5 | `internal/handler/errors.go` | 增量修改（可选加固） | 补一条全局兜底映射 | 6.5 |
| 6 | `internal/config/redis.go` | 完整文件替换 | 加连接池与超时参数 | 6.6 |
| 7 | `internal/handler/cache.go`、`internal/router/router.go` | 增量修改（可选） | 本地验收用的计数快照接口 | 6.7 |

步骤 4 是唯一的大改。为了减少手工拼接出错，那一节给的是**替换后的完整函数**，读者整段替换即可，不需要自己往旧函数里插行。

### 2.2 装配链会不会变

不会。这是本章刻意守住的一条线。

```text
main.go  config.InitRedis → rdb
  → router.New(db, jwtManager, rdb)
      → service.NewArticleService(db, rdb)     构造函数一行都不用改
      → handler.NewArticleHandler(articleSvc)  handler 一行都不用改
           → h.svc.Detail(ctx, id)            签名一行都不用改
```

新增的 `singleflight.Group` 与 `cacheMetrics` 都是**零值可用**的类型，放进结构体字段就行，构造函数不需要传新参数。这一条很重要：它意味着 03 章 7.2 讲的三层装配结论在本章完全不变，reader 不用回头改 `main.go` 或 `router.go`（步骤 7 的可选 debug 路由除外）。

### 2.3 会动的 key 格式

当前 key 是 `article:detail:{id}`，本章改成 `article:detail:v1:{id}`。

改 key 的后果要提前说清楚：切换之后，旧 key 不会被读到，会在原来的 TTL 到期后自然消失。本地开发可以直接 `redis-cli FLUSHDB` 清掉，但这是**学习环境**的做法，生产库绝对不要对共用一个 Redis 的实例执行。版本号的作用在 6.1 细讲。

---

## 3. 雪崩：固定 TTL 为什么必然出事

### 3.1 先把算例算完

假设一个很常见的场景：服务重启后跑一段预热，把最近 1000 篇文章依次写进缓存。代码里每次都用 `10*time.Minute`，预热耗时 3 秒。

```text
第 1 篇  写入 t0+0.000s   过期 t0+600.000s
第 2 篇  写入 t0+0.003s   过期 t0+600.003s
...
第 1000 篇 写入 t0+3.000s  过期 t0+603.000s
```

1000 个 key 的过期时刻挤在 3 秒里。到点之后，只要这 1000 篇还有人读，MySQL 就会在 3 秒内收到接近 1000 条 SELECT。平时这些请求是分散在 10 分钟里的，此刻被压缩到几秒。

固定 TTL 的问题不在数值大小。把 10 分钟改成 1 小时，只是把尖峰推后一小时，尖峰本身一点没变。**根因是「过期时刻 = 写入时刻 + 同一个常数」这个等式把一批 key 对齐了。**

### 3.2 抖动幅度怎么定，别拍脑袋

要打断对齐，把常数换成区间：

```text
TTL = base + random(0, jitter)
过期时刻 = 写入时刻 + base + random(0, jitter)
```

原来 3 秒的写入时间分布，现在被摊到 `3s + jitter` 的窗口里。抖动幅度决定摊得多开，它应该按「一批写入有多集中」来定，而不是随手写 `+ 10*time.Second`。

本章的推导：

| 输入 | 值 | 怎么来的 |
| --- | --- | --- |
| 基础 TTL | 10 分钟 | 沿用 03 章 7.3 的取值，不无故改动已验收的行为 |
| 一批写入的跨度 | 秒级（预热、批量导入、冷启动第一波） | 当前项目没有批量任务，按最坏情况估到秒级 |
| 期望摊开的窗口 | 2 分钟 | 让 1000 个 key 的回源分散到 120 秒，平均每秒不到 10 条，MySQL 完全吃得下 |
| 抖动幅度 | 2 分钟（基础 TTL 的 20%） | 等于期望窗口，且明显大于写入跨度 |

为什么是 20%：抖动太小（比如 5 秒）摊不开 1000 个 key；抖动太大（比如 10 分钟）会让一部分数据比预期多旧一倍，写后删缓存失败时的脏数据窗口被拉长。20% 是「够摊开」和「别让副本太旧」之间的折中，属于本项目自定的工程取值，不是通用常数。生产环境要按实际批量写入规模、QPS 和可容忍的陈旧时间重新算。

还有一个常被忽略的点：**抖动必须在每次写入时重新取随机数**。写成包级变量 `var ttl = 10*time.Minute + rand...` 只会算一次，所有 key 又对齐了，抖动等于没加。

### 3.3 随机源怎么选

**概念伪代码**：下面这一行是 6.1 `articleDetailTTL()` 里的核心表达式，单独贴出来讲选型，不能当成完整文件保存或单独编译。

```go
jitter := rand.Int64N(int64(articleDetailTTLJitter))
```

三个判断，逐条都有依据：

1. 用 `math/rand/v2`，不用 `math/rand`。Go 1.22 起 `math/rand/v2` 的顶层函数自动随机播种，不需要 `rand.Seed`，也没有「不 Seed 时每次启动序列相同」的老坑。项目 `go.mod` 声明 `go 1.26.0`，可以直接用。
2. 用顶层函数，不自己 `rand.New(rand.NewSource(...))`。本机 `math/rand/v2/rand.go:12-13` 的包注释明文写着顶层函数并发安全；同一文件第 31 行写着 `A Source is not safe for concurrent use by multiple goroutines`。自己造一个 `*Rand` 就得自己加锁，纯属给自己找事。
3. 这不是安全场景。`Int64N` 是可预测的伪随机数，用来打散过期时刻足够了；`math/rand/v2` 包注释第 18 行也提醒了，安全敏感用途要用 `crypto/rand`。TTL 抖动不涉及密钥或 token。

### 3.4 常见错误对照

| 写法 | 会怎样 | 正确做法 |
| --- | --- | --- |
| `const ttl = 10*time.Minute + time.Duration(rand.Int64N(...))` | 编译不过，`const` 不能调用运行时函数 | 用函数 `articleDetailTTL()` 每次算 |
| 把随机数算在包级 `var` 里 | 全进程只有一个值，对齐问题原样保留 | 每次 `Set` 前调用函数 |
| 用 `time.Now().UnixNano() % jitter` 当抖动 | 同一毫秒内的并发请求拿到同一个值，反而形成新的对齐 | 用 `rand.Int64N` |
| 用 `rand.Int63n`（老 `math/rand` 的拼写） | `math/rand/v2` 里没有这个函数，编译不过 | `Int64N`，注意大写 N |
| 给「过期时间戳」加抖动，却仍用 `Set` 的 expiration 参数 | 两套时间口径并存，实际 TTL 没变 | 统一在 `articleDetailTTL()` 里算好，传给 `Set` |
| 抖动幅度设得比基础 TTL 还大 | 一部分 key 存活时间接近两倍，删缓存失败时脏数据很久 | 控制在基础 TTL 的一到两成 |
| 用 `FLUSHDB` 或 `DEL` 一堆 key 来「验证」抖动 | 验证的是清理，不是过期分布 | 用 `TTL` 命令或 6.2 的单测 |

### 3.5 这一招的边界

抖动只处理「很多 key 恰好同一时刻到期」。它不处理：

- Redis 整体宕机（第 8 节）。
- 单个热点 key 的并发回源（第 5 节）。
- 缓存内容本身就是旧的（第 7 节）。

代码位置：`article_cache.go` 的 `articleDetailTTL()` 与 `article.go` 的 `writeArticleCache()`，见 6.1、6.4。

---

## 4. 穿透：让「不存在」也成为一个可缓存的状态

### 4.1 第一道防线其实不是缓存

`GET /api/articles/:id` 的 id 来自 URL。`internal/handler/article.go` 现在用 `strconv.ParseUint(c.Param("id"), 10, 64)` 解析，这一步已经能挡住 `abc`、负数、溢出值和空串。它没有挡住的是「合法但不存在」的 id，比如 `999999999`。

真实的攻击或误用往往分两层：

| 层 | 例子 | 现有防线 | 本章补什么 |
| --- | --- | --- | --- |
| 格式非法 | `/api/articles/abc`、`-1`、超长数字 | `ParseUint` 返回 400 | 不用补 |
| 格式合法但不存在 | `/api/articles/999999999` | 无，每次回源 | 空值缓存；另外可加业务上限，见 4.7 |

先做参数校验，再做缓存，顺序不能反。把明显非法的输入也写进缓存，等于给攻击者一个大内存写入器。

### 4.2 空标记怎么编码才不会骗过自己

03 章 7.6 已经点破过一次，这里把机制讲完整。

**概念伪代码**：下面这段只用来演示 `null` 陷阱，不能运行，也不要复制到项目里。6.2 的 `TestNullCacheValueTrap` 才是可运行版本。

```go
// 概念伪代码，只用来说明陷阱，不要复制到项目里
data, _ := json.Marshal(nil)        // data == []byte("null")
var a model.Article
err := json.Unmarshal(data, &a)     // err == nil，且 a 是全零值结构体
```

`encoding/json` 对 `null` 反序列化到结构体时，不报错也不改动目标。如果缓存值写成字符串 `"null"`，现有 `Detail` 的命中分支会认为解析成功，把一篇 `id=0`、标题为空的假文章返回给客户端，状态码还是 200。这比穿透本身更糟。

方案对比：

| 方案 | 怎么表示「不存在」 | 风险 | 结论 |
| --- | --- | --- | --- |
| 缓存 `null` | 值就是 `"null"` | 反序列化成零值结构体，静默返回假数据 | 明确排除 |
| 缓存空对象 `{}` | 值就是 `"{}"` | 同样反序列化成零值结构体 | 明确排除 |
| 独立 key 前缀 | `article:absent:{id}` | 读写两套 key，`Del` 要删两个，漏一个就出错 | 可用但啰嗦 |
| 带类型标签的 JSON | `{"kind":"missing"}` | 要定义结构体、要多一层解析 | 可用，超出当前需要 |
| 不可能与文章 JSON 撞车的哨兵字符串 | `"__ARTICLE_NOT_FOUND__"` | 几乎无 | 本章选用 |

为什么哨兵安全：`json.Marshal(model.Article)` 的结果一定是一个 JSON 对象，第一个非空字符是 `{`。哨兵是以下划线开头的普通字符串，既不是合法 JSON 对象，也不会被误解析。读缓存时**先比字符串，再谈反序列化**，顺序反了就会多一次无意义的解析错误日志。

### 4.3 空标记的 TTL 为什么必须短

正常缓存 10 分钟，空标记本章给 60 秒。理由是两者的失效方式不同：

| | 正常文章缓存 | 空标记 |
| --- | --- | --- |
| 什么时候会变 | 作者删除、以后可能的编辑 | 数据库里被插入了这条记录 |
| 有主动失效吗 | 有，写路径 `Del` | 有，`Create` 也会 `Del` 同一个 key |
| 主动失效失败时 | 旧数据最多存活一个 TTL | 假的 404 最多存活一个 TTL |
| 误判代价 | 读到旧内容 | 明明有文章却说不存在 |

空标记是「为了挡住无效 id 而临时记下的一个否定结论」。否定结论比肯定结论更容易过期，所以 TTL 要短，把误判窗口压到一分钟以内。

这里有个本项目特有的、值得高兴的细节：源码里 `Create` 成功后已经调用了 `s.InvalidateDetailCache(ctx, a.ID)`。刚看到那行时容易觉得多余，新文章刚创建，哪来的旧缓存。加上空值缓存之后它就变得必要了：如果有人先查过这个 id 并留下空标记，创建成功后必须把空标记删掉，否则新文章会被 404 挡住最多 60 秒。空标记和真实文章用同一个 key，所以这一句同时覆盖两种情况，不用改代码，只需要在文档里把理由补齐。见 7.6。

### 4.4 只有「确认不存在」才能写空标记

这一条是新手最容易写错、后果也最严重的地方。

```text
SELECT 报「查无此行」     → 可以写空标记，这是事实
SELECT 报「连接超时」     → 绝不能写空标记，这是故障
Redis 读超时             → 绝不能写空标记，这是故障
```

把数据库故障写成「这篇文章不存在」，等于把一次短暂故障复制成 60 秒的错误结论，覆盖所有被查询的 id。故障恢复之后，接口还在继续返回 404。所以 `queryDetail` 必须先把错误分成两类，只有 `ErrNotFound` 能触发 `markArticleAbsent`。

同理，写空标记本身失败（Redis 挂了）也不能影响这次 404 的返回，只记日志和降级计数。

### 4.5 先修一个真实缺陷：现在查不存在的文章返回 500

**概念伪代码**：下面两段是从仓库源码截取的片段，用来定位缺陷，不能单独运行。完整修法在 6.4 与 6.5。

翻源码：

```go
// internal/service/article.go 当前版本
func (s *ArticleService) queryDetail(ctx context.Context, id uint) (*model.Article, error) {
	var a model.Article
	if err := s.db.WithContext(ctx).Preload("User").First(&a, id).Error; err != nil {
		return nil, err        // gorm.ErrRecordNotFound 原样往上抛
	}
	return &a, nil
}
```

**概念伪代码（第二段）**：同样是从源码截取的片段，单独不能运行。

```go
// internal/handler/errors.go 当前版本
switch {
case errors.Is(err, service.ErrInvalidInput):   // 400
case errors.Is(err, service.ErrNotFound):       // 404
case errors.Is(err, gorm.ErrForeignKeyViolated): // 400
case errors.Is(err, gorm.ErrDuplicatedKey):     // 409
default:                                        // 500
}
```

GORM 的 `First` 查不到记录时返回 `gorm.ErrRecordNotFound`，它不等于 `service.ErrNotFound`，于是掉进 default，返回 500。两个后果：

1. 用户视角：访问一篇不存在的文章得到「服务暂时不可用」，语义完全错。
2. 工程视角：加了空值缓存之后，代码无法区分「确实没有」和「数据库出错」，4.4 的规则无从实现。

本章在 `queryDetail` 里做转换，让 service 层对外只说自己的错误类型。6.5 另外给一个可选的全局兜底，用来护住其它 service（比如评论）的同类问题。

### 4.6 常见错误对照

| 写法 | 会怎样 | 正确做法 |
| --- | --- | --- |
| 缓存 `"null"` | 命中后返回全零值假文章，200 | 用非 JSON 的哨兵字符串 |
| 先 `Unmarshal` 再判断是不是空标记 | 哨兵触发解析错误，日志刷屏 | 先比字符串 |
| 所有 `queryDetail` 的错误都写空标记 | 数据库故障时把 500 缓存成 404 | 只认 `errors.Is(err, ErrNotFound)` |
| 空标记给和正常缓存一样的 TTL | 误判窗口长达 10 分钟 | 短 TTL，本章 60 秒 |
| 只写空标记，写路径不删 | 新建文章被 404 挡住 | `Create` 里保留那行 `Del` |
| 把恶意构造的海量不同 id 全缓存下来 | Redis 内存被打满 | 4.7 的边界，本阶段靠参数校验与 04 章的限流 |
| 用 `redis.Nil` 当「不存在」写进缓存值 | `redis.Nil` 是客户端返回的错误，不是字符串 | 用自定义常量 |

### 4.7 为什么这一章不做布隆过滤器

布隆过滤器是穿透的标准进阶解法：把所有存在的 id 放进一个位图，查询前先问一次，不存在就直接拒绝。本章不做，理由是成本对不上当前阶段：

| 需要解决的事 | 本章状态 |
| --- | --- |
| 引入依赖或自己实现位图与哈希 | 无，会显著拉长章节 |
| 服务启动时预热全部文章 id | 无预热流程 |
| 新建/删除文章时同步更新位图 | 标准布隆过滤器不支持删除，要换 Counting Bloom Filter 或定期重建 |
| 误判率与容量的取舍 | 无压测数据可调参 |
| Redis 丢失后的重建 | 与 00 章「Redis 数据可重建」的规则要一起设计 |

一句话：布隆过滤器解决的是「海量不同 id」这一档的问题，而当前空值缓存加参数校验已经把「同一个无效 id 反复查」这一档挡住了。等 04 章做了限流、05 章有了压测数据，再回来评估它更合适。写进文档的设计不算已实现，这条要守住。

---

## 5. 击穿：把同一 key 的并发回源合并成一次

### 5.1 先算清楚损失

接着 1.4 的热点算例：200 个请求在同一毫秒发现 key 过期。

```text
没有合并：200 条 SELECT + 200 次 SET，其中 199 组结果完全一样
有合并：  1 条 SELECT + 1 次 SET，其余 199 个请求等结果
```

回源次数从 200 降到 1。这个数字可以用计数器直接验证，见 9.2，比「感觉快了」靠谱得多。

注意合并的对象是**同一个 key**。200 个请求查 200 篇不同文章，本来就该各查各的，合并反而会把它们串成一条队。所以 `singleflight` 的 key 必须就是缓存 key。

### 5.2 方案对比，为什么不选分布式锁

| 方案 | 怎么做 | 优点 | 代价 | 本章判断 |
| --- | --- | --- | --- | --- |
| 进程内 `sync.Mutex` | 全局一把锁或按 key 分桶加锁 | 不用新依赖 | 一把锁会把不同 key 的请求也串起来；分桶要自己写 | 不选，`singleflight` 已经把分桶做好了 |
| `singleflight.Group` | `Do(key, fn)` 合并同 key 的调用 | 零值可用、标准库生态、代码短 | 只在单进程生效；共享同一份结果和 error | **选用** |
| Redis 分布式锁 | `SET NX PX` 抢锁，抢到的回源 | 跨实例生效 | 锁超时、续期、误删别人的锁、Redis 故障时的死锁，全都要处理 | 不选，超出学习切片 |
| 逻辑过期 | value 里存一个逻辑过期时间，物理 TTL 设很长或不过期 | 完全没有「过期瞬间」 | 要后台刷新、要处理刷新失败、内存常驻 | 不选，代码量最大 |
| 互斥重建 + 后台定时刷新 | 热点 key 由后台任务定期更新 | 请求永远命中 | 要识别哪些是热点、要跑后台任务 | 04 章 4.7 兑现进程内互斥重建这部分，后台定时刷新仍不做 |

`singleflight` 的边界必须写清楚：**它只合并同一个 Go 进程内的调用。** 服务起 3 个实例，最坏情况就是 3 次回源，不是 1 次。这是可以接受的：3 次和 200 次不是一个量级。真要跨实例严格合并，得上分布式锁，代价见上表。

### 5.3 `singleflight.Do` 的心智模型与 context 陷阱

先看库作者写的契约。本机 `golang.org/x/sync@v0.24.0/singleflight/singleflight.go:86-90` 的 `Do` 注释原文：

> Do executes and returns the results of the given function, making sure that only one execution is in-flight for a given key at a time. If a duplicate comes in, the duplicate caller waits for the original to complete and receives the same results.

拆成四条：

| 契约 | 含义 | 写代码时要注意什么 |
| --- | --- | --- |
| 同一 key 同一时刻只跑一个 `fn` | 后来者不执行 `fn`，直接等 | `fn` 里必须包含完整的「读二次缓存 → 回源 → 回填」 |
| 后来者拿到**同一份结果** | 包括同一个 `error`、同一个返回值指针 | 返回值别在等待者手里被修改；一个请求失败，所有等待者一起失败 |
| `shared` 表示结果被多人领取 | 只用于计数，不表示成功 | 不要拿它判断是否出错 |
| `fn` 的闭包捕获第一个调用者的变量 | 包括它的 `context.Context` | 这是最容易踩的坑，下一段展开 |

**context 陷阱**：`fn` 是在第一个请求里定义的闭包，用的是第一个请求的 `ctx`。假设第一个用户按了浏览器停止键，`ctx` 被取消，那么：

```text
请求 1 的 ctx 取消
  → fn 里 s.db.WithContext(ctx) 立刻返回 context.Canceled
  → fn 返回 error
  → 其余 199 个等待者全部拿到同一个 context.Canceled
  → 199 个完全正常的请求被一个取消连累
```

本章的处理如下。**概念伪代码**：这两行是 6.4 `Detail` 里 `fn` 开头的摘录，单独贴出来讲机制，不能单独运行。

```go
loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
defer cancel()
```

`context.WithoutCancel`（Go 1.21 起，本机 `context.go:586` 有定义）返回一个不再响应父级取消、但保留父级里所有 value 的新 context；再套一个 3 秒超时给它上界。这样回源不会被某一个请求的取消打断，也不会无限跑下去。

代价要讲：即使所有 HTTP 请求都断开了，这次回源仍会跑完最多 3 秒，多做一点无用功。对一个学习项目，用这点浪费换「不连累别人」是划算的。另一个方案是用 `DoChan` 加 `select`，让每个等待者只等自己的 ctx，代码会更复杂，本章不展开，但要知道有这条路。

### 5.4 双重检查为什么必须有

`Do` 只保证「同一时刻只有一个 `fn` 在跑」，不保证「`fn` 结束后等待者不会再做别的事」。看这段错误写法：

```text
// 概念伪代码，不能运行，只展示错误结构
v, _, _ := group.Do(key, func() (any, error) {
    return queryDetail(ctx, id)     // 第一个请求回源并回填
})
// 等待者拿到的也是 v，看起来没问题？
```

问题在于：如果 `fn` 里面没有「再读一次缓存」，那么任何在 `fn` 执行期间**没能进入 `Do`**、或者在 `fn` 刚结束又到达的请求，仍会各自回源。更常见的情况是代码写成了「`Do` 外面先查一次缓存，未命中就 `Do(fn)`，`fn` 里直接查库」，此时 `fn` 结束后若有别的路径已经回填了缓存，等待者依旧拿不到那次结果。

本章的写法把整个「二次读缓存 → 回源 → 回填」放进 `fn`：

```text
Detail
  ├─ 第一遍 GET（命中就返回，绝大多数请求到此结束）
  └─ 未命中才 Do(key, fn)
        ├─ fn 内第二遍 GET  ← 双重检查
        │    └─ 命中（含空标记）就直接返回，不再 SELECT
        ├─ Loads.Add(1) 后 SELECT
        └─ 回填 / 写空标记
```

双重检查的意义：第一个请求在 `fn` 里完成回填之后，所有排队等待的同 key 请求共享它的结果；而稍晚到达、恰好错过这次 `Do` 的请求，会在自己的第一遍 GET 里直接命中刚写好的缓存。两层合起来，才把回源次数真正压到 1。

还有一条同样重要：**不要把整个 `Detail` 都塞进 `Do`**。命中缓存的请求占绝大多数，让它们也排队会白白增加延迟，还把 `Do` 的 key 表撑大。`Do` 只包住未命中分支。

### 5.5 常见错误对照

| 写法 | 会怎样 | 正确做法 |
| --- | --- | --- |
| 把整个 `Detail` 塞进 `Do` | 命中缓存的请求也排队，延迟上升 | 只包未命中分支 |
| `fn` 里直接查库，不做二次读缓存 | 合并效果打折 | `fn` 开头再 `readArticleCache` 一次 |
| `fn` 里用第一个请求的原始 `ctx` | 一个请求取消，所有等待者一起失败 | `WithoutCancel` + `WithTimeout` |
| 直接 `v.(*model.Article)` 强转 | 以后 `fn` 返回 nil 时会 panic | 带 `ok` 的类型断言并给出明确错误 |
| 把 `shared` 当成「成功」 | 失败也会被共享，`shared` 只表示结果被多人领取 | 只看 `err` |
| 用 `group.Do` 当限流 | 它只合并同 key 的回源，不限制总请求量 | 限流是 04 章的内容 |
| 认为起 3 个实例也只回源 1 次 | 实际最多 3 次 | 讲清单进程边界 |
| `go get` 之后立刻 `go mod tidy` | 还没有 import，依赖被删掉 | 先写完步骤 4 再 tidy |
| 给 `fn` 传一个会被外部改动的切片或指针 | 所有等待者共享同一份可变数据 | 返回只读结果，别在等待者里改 |

---

## 6. 按落盘顺序把代码写完

这一节是本章唯一要动手的部分。每个代码块前都按 00 章的规矩标了类型。做完一步就跑该步给出的命令，别攒到最后一起编译。

### 6.1 步骤 1：新建缓存配置与工具文件

**完整文件**：`internal/service/article_cache.go`

为什么要单独建一个文件：`Detail` 已经承担了「读、判、合并、回源、回填」五件事，再把 key 拼法、TTL 常量、哨兵、计数器也塞进去，函数会膨胀到看不清。这个文件只放**不依赖数据库、不依赖 HTTP、不依赖 `ArticleService`** 的纯函数和类型，所以它有两个额外好处：可以单独写单元测试，也不会和 `article.go` 互相 import。

拆文件属于工程惯例，Go 官方没有规定「常量必须单独放一个文件」。判断依据是 00 章反复用的那条：当前只有 service 一个包需要这些东西，所以只拆文件，不新建包。

```go
// Package service 的文章详情缓存配置与工具函数。
// 本文件只放常量、纯函数和计数器，不碰数据库，也不碰 HTTP。
package service

import (
	"math/rand/v2"
	"strconv"
	"sync/atomic"
	"time"
)

// ---------- 一、key ----------

// articleDetailKeyPrefix 是详情缓存 key 的前缀。
// 抽成常量是为了让「读缓存」「删缓存」「redis-cli 排查」三处用同一个名字，
// 不会出现读 article:detail:1、删 article:info:1 这种低级不一致。
const articleDetailKeyPrefix = "article:detail:"

// articleDetailCacheVersion 是缓存值的格式版本。
// 以后给 model.Article 加字段、改 JSON tag 时，把 v1 改成 v2：
// 新代码只读 v2 的 key，老 key 因为没人再读会自然过期，
// 不需要半夜跑脚本批量清理，也不会把旧格式的数据解析成新结构体。
const articleDetailCacheVersion = "v1"

// articleDetailKey 拼出一篇详情的缓存 key，形如 article:detail:v1:42。
// 用 strconv.FormatUint 而不是 fmt.Sprintf：这里只需要十进制转换，
// strconv 不做格式解析，意图更明确。
func articleDetailKey(id uint) string {
	return articleDetailKeyPrefix + articleDetailCacheVersion + ":" + strconv.FormatUint(uint64(id), 10)
}

// ---------- 二、TTL ----------

// articleDetailTTLBase 是详情缓存的基础存活时间。
// 它也决定「数据库改了但删缓存失败」时，旧数据最多能被读多久。
// 10 分钟沿用 03 章 7.3 的取值，推导见 3.2，不是通用最优值。
const articleDetailTTLBase = 10 * time.Minute

// articleDetailTTLJitter 是叠加在基础 TTL 上的随机抖动上限。
// 最终 TTL 落在 [10m, 10m+2m)，把一批写入的过期时刻摊到 2 分钟窗口。
const articleDetailTTLJitter = 2 * time.Minute

// articleEmptyTTL 是「查无此文」空标记的存活时间。
// 必须明显短于正常缓存：空标记挡的是不存在的 id，
// 留太久会让后来真写入这条记录时短暂查不到，见 4.3。
const articleEmptyTTL = 60 * time.Second

// articleDetailTTL 返回本次回填应该用的 TTL。
// 每次调用都重新取随机数，所以同一批写入的 key 不会撞在同一秒过期。
// math/rand/v2 的顶层函数并发安全（包注释第 12-13 行明文），
// 不需要自己加锁，也不要再用 rand.New(rand.NewSource(time.Now().UnixNano())) 那套老写法。
func articleDetailTTL() time.Duration {
	// Int64N 返回 [0, n) 区间里的整数，转成 time.Duration 后加到基础值上。
	jitter := rand.Int64N(int64(articleDetailTTLJitter))
	return articleDetailTTLBase + time.Duration(jitter)
}

// ---------- 三、空标记 ----------

// articleEmptySentinel 是「数据库里确实没有这篇文章」的缓存值。
// 合法的详情缓存一定是 json.Marshal(model.Article) 的结果，以 '{' 开头；
// 这个字符串以下划线开头，永远不可能和真实文章 JSON 混淆。
// 绝对不能用 "null"：json.Unmarshal([]byte("null"), &a) 不报错，
// 会把 a 留成全零结构体，客户端会收到一篇 id=0 的假文章。
const articleEmptySentinel = "__ARTICLE_NOT_FOUND__"

// ---------- 四、计数器 ----------

// cacheMetrics 用原子计数器记录缓存路径上发生了什么。
// 本章不引入 Prometheus，先用最小代价让「命中率」「回源次数」可见，
// 否则第 9 节的验收只能靠猜「第二次好像快了一点」。
// 字段必须用 atomic.Int64：Detail 会被多个请求并发调用，
// 普通 int64 的 ++ 不是原子操作，go test -race 会直接报数据竞争。
type cacheMetrics struct {
	Hits      atomic.Int64 // 命中真实文章 JSON 的次数
	EmptyHits atomic.Int64 // 命中空标记、直接判定不存在的次数
	Misses    atomic.Int64 // 未命中（含 Redis 故障降级）的次数
	Loads     atomic.Int64 // 真正回源 MySQL 的次数，验证击穿时看这个
	Shares    atomic.Int64 // 被 singleflight 合并掉的请求数
	Degrades  atomic.Int64 // Redis 读写出错、走降级的次数
	Writes    atomic.Int64 // 成功回填缓存的次数
}

// CacheStats 是缓存计数器的只读快照，供调试接口和测试读取。
// 故意用普通 int64：调用方拿到的是一份拷贝，读的时候不会看到半成品，
// 也不用知道 atomic 怎么用。
// json tag 写成小写下划线，是因为 6.7 的可选调试接口会直接把它序列化出去，
// Go 默认导出的字段名是 Hits、EmptyHits 这种大写形式，不适合当 API 字段。
type CacheStats struct {
	Hits      int64 `json:"hits"`
	EmptyHits int64 `json:"empty_hits"`
	Misses    int64 `json:"misses"`
	Loads     int64 `json:"loads"`
	Shares    int64 `json:"shares"`
	Degrades  int64 `json:"degrades"`
	Writes    int64 `json:"writes"`
}

// snapshot 把原子计数器读成一份普通结构体。
// 每个字段单独 Load，严格说这不是同一个瞬间的快照；
// 对学习项目里看趋势、做验收断言够用，要精确一致得另加锁。
func (m *cacheMetrics) snapshot() CacheStats {
	var s CacheStats
	s.Hits = m.Hits.Load()
	s.EmptyHits = m.EmptyHits.Load()
	s.Misses = m.Misses.Load()
	s.Loads = m.Loads.Load()
	s.Shares = m.Shares.Load()
	s.Degrades = m.Degrades.Load()
	s.Writes = m.Writes.Load()
	return s
}
```

**逐行拆解表**（挑最容易写错的地方）：

| 代码 | 作用 | 为什么这样设计 |
| --- | --- | --- |
| `articleDetailCacheVersion = "v1"` | key 里带格式版本 | 改 JSON 形状时换版本，老 key 自然过期，不用批量清理 |
| `articleDetailKey(id)` 单独成函数 | 读写删三处共用同一个拼法 | 03 章的 `articleDetailKey` 是前缀常量，拼接散在 `Detail` 和 `InvalidateDetailCache` 两处，改前缀时容易漏一处 |
| `articleDetailTTL()` 是函数不是常量 | 每次取新的随机数 | 写成包级 `var` 只会算一次，抖动等于没加 |
| `rand.Int64N(int64(...))` | 取 `[0, jitter)` 的整数 | `math/rand/v2` 的函数名是 `Int64N`，大写 N；老包的 `Int63n` 在这里不存在 |
| `articleEmptySentinel` 以下划线开头 | 保证不是合法 JSON 对象 | 与 `json.Marshal(model.Article)` 的结果不可能相等 |
| `atomic.Int64` 而不是 `int64` | 并发安全计数 | `go test -race` 能抓出普通整数的竞争 |
| `CacheStats` 用导出类型 | 别的包（handler）能读快照 | 6.7 的可选调试接口要跨包使用 |
| `snapshot()` 逐字段 `Load()` | 返回普通结构体 | 调用方不必理解原子操作 |

**设计决策，包括被放弃的方案**：

| 想过怎么做 | 为什么没采用 |
| --- | --- |
| 把常量继续放在 `article.go` 顶部 | `Detail` 已经很长，常量与纯函数混在业务方法里会盖住主线；单独成文件还能独立写测试 |
| 新建 `internal/cache` 包 | 当前只有 service 一个包用，新建包要设计导出 API 和依赖方向，收益小于成本。00 章 8.0 的 DTO 落点用了同样的判断 |
| TTL 等参数放进 `config.yaml` | 学习阶段多一层配置读取会掩盖缓存逻辑本身；等有压测数据、需要不重启调参时再挪进配置 |
| 用 Prometheus 计数器 | 要引入依赖、注册 endpoint、讲指标类型，超出本章范围。先用原子计数把「有没有效」证明出来 |

**验证**：

```powershell
go build ./...
```

此时 `go build ./...` 应该通过。新文件没有引用任何还不存在的符号，`article.go` 里旧的 `const articleDetailKey` 也还在，两者暂时不冲突（一个叫 `articleDetailKeyPrefix`，一个叫 `articleDetailKey`）。步骤 4 会替换掉旧常量。

### 6.2 步骤 2：先把 TTL 与哨兵的正确性钉死

**完整文件**：`internal/service/article_cache_test.go`

为什么要先写测试：后面要改 `Detail`，改完再测，出了问题分不清是缓存逻辑错了还是常量本来就错。这三个测试只测纯函数，不需要 MySQL，也不需要 Redis，几毫秒就能跑完。

```go
package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"forum-backend/internal/model"
)

// TestArticleDetailKey 确认 key 格式稳定，且不同 id 不会撞车。
// 以后改 key 版本时，这个测试会失败，提醒你同步更新文档和排查命令。
func TestArticleDetailKey(t *testing.T) {
	got := articleDetailKey(42)
	want := "article:detail:v1:42"
	if got != want {
		t.Fatalf("articleDetailKey(42) = %q, want %q", got, want)
	}
	if articleDetailKey(1) == articleDetailKey(2) {
		t.Fatal("different ids must not share a cache key")
	}
}

// TestArticleDetailTTLJitter 验证两件事：TTL 落在区间内，且取值足够分散。
// 只看「在区间内」不够：一个永远返回固定值的函数也能通过区间检查。
func TestArticleDetailTTLJitter(t *testing.T) {
	const rounds = 1000
	lo := articleDetailTTLBase
	hi := articleDetailTTLBase + articleDetailTTLJitter

	seen := make(map[time.Duration]bool)
	for i := 0; i < rounds; i++ {
		ttl := articleDetailTTL()
		if ttl < lo || ttl >= hi {
			t.Fatalf("ttl %v out of range [%v, %v)", ttl, lo, hi)
		}
		seen[ttl] = true
	}

	// 1000 次调用如果只得到很少几种取值，说明过期时刻仍然被对齐了。
	if len(seen) < 50 {
		t.Fatalf("jitter looks degenerate: only %d distinct TTLs in %d rounds", len(seen), rounds)
	}
}

// TestArticleEmptySentinelIsNotArticleJSON 证明哨兵不会和真实文章 JSON 撞车。
// 空标记的全部安全性都建立在这一条上。
func TestArticleEmptySentinelIsNotArticleJSON(t *testing.T) {
	data, err := json.Marshal(&model.Article{ID: 1, Title: "x"})
	if err != nil {
		t.Fatalf("marshal article: %v", err)
	}
	if string(data) == articleEmptySentinel {
		t.Fatal("sentinel collides with a real article JSON")
	}
	if strings.HasPrefix(articleEmptySentinel, "{") {
		t.Fatal("sentinel must not look like a JSON object")
	}
}

// TestNullCacheValueTrap 把 03 章 7.6 说过的陷阱跑成可复现的事实。
// 结论：不能把 "null" 当空标记，否则命中分支会把零值文章返回出去。
func TestNullCacheValueTrap(t *testing.T) {
	var a model.Article
	if err := json.Unmarshal([]byte("null"), &a); err != nil {
		t.Fatalf("unmarshal null into struct should not error: %v", err)
	}
	if a.ID != 0 {
		t.Fatal("unmarshal null left a non-zero id")
	}
}
```

**逐行拆解表**：

| 代码 | 作用 | 为什么这样设计 |
| --- | --- | --- |
| `const rounds = 1000` | 固定采样次数 | 测试要可重复，别用随机数决定测多少轮 |
| `seen := make(map[time.Duration]bool)` | 记录出现过多少种不同 TTL | 只检查区间抓不出「抖动其实没生效」 |
| `len(seen) < 50` | 判定退化 | 2 分钟抖动按纳秒计有海量取值，1000 次采样得到 50 种以下是极不可能的 |
| `json.Marshal(&model.Article{...})` | 造一个真实缓存值 | 直接验证「哨兵不等于任何合法文章 JSON」这个前提 |
| `TestNullCacheValueTrap` | 把陷阱固化成测试 | 以后有人把哨兵改成 `"null"`，这个测试能解释为什么当年不这么做 |

**验证**：

```powershell
go test ./internal/service -run "TestArticle|TestNull" -v
```

期望四个测试全部 `PASS`。这一步不需要启动 MySQL 或 Redis。

### 6.3 步骤 3：装请求合并的依赖

```powershell
go get golang.org/x/sync
go list -m golang.org/x/sync
```

这里有个新手常困惑的点：`go list -m all` 里其实已经能看到 `golang.org/x/sync v0.19.0`（它被别的依赖间接引入，标记为 `Indirect: true`），但 `go.mod` 的 require 块里没有它。间接依赖不等于可以直接 import，显式 `go get` 会把它提升成直接依赖并更新版本。本章按本机模块缓存里的 `v0.24.0` 核对了 `singleflight` 的源码注释，你装到的版本号可能不同，`Do` / `DoChan` 在本章涉及的语义上是一致的。

**这一步不要跑 `go mod tidy`**。刚装好还没人 import，tidy 会把它从 `go.mod` 里删掉，然后步骤 4 编译报「no required module provides package」。这个坑 04 章开工前也提醒过。等步骤 4 写完 `article.go` 再 tidy。

### 6.4 步骤 4：替换 `internal/service/article.go`

**完整文件**：`internal/service/article.go`

这是本章唯一的大改。为了减少手工拼接出错，这里给替换后的整个文件。与仓库当前版本相比，改动集中在五处：import 块、`ArticleService` 结构体、删掉旧的 `const articleDetailKey`、`Detail` 与四个新辅助方法、`queryDetail` 的错误转换、`InvalidateDetailCache` 改用 `articleDetailKey()`。`Create`、`List`、`Delete` 的业务逻辑保持原样，只把 `Create` 里那行失效调用的注释补清楚。

```go
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"

	"forum-backend/internal/model"
)

// 固定排序 排序片段写成常量，禁止把 c.Query("sort") 拼进 Order()。用户输入直接进 SQL 就是注入口
const articleListOrder = "created_at DESC, id DESC"
const commentListOrder = "created_at ASC, id ASC"

// ArticleService 文章业务逻辑，同时持有 MySQL 与 Redis 客户端。
// group 与 metrics 都是零值可用的类型，所以 NewArticleService 不用改签名，
// main.go、router.go、handler 一行都不用动，见 2.2。
type ArticleService struct {
	db      *gorm.DB
	rdb     *redis.Client
	group   singleflight.Group // 合并同一 key 的并发回源，只在当前进程内生效
	metrics cacheMetrics       // 缓存计数器；含 atomic 字段，service 必须一直用指针传递
}

// NewArticleService 由 router 调用，两个客户端在这里一次性注入。
func NewArticleService(db *gorm.DB, rdb *redis.Client) *ArticleService {
	return &ArticleService{db: db, rdb: rdb}
}

// 发布文章
func (s *ArticleService) Create(ctx context.Context, userID uint, title, content string) (*model.Article, error) {
	title = strings.TrimSpace(title)
	content = strings.TrimSpace(content)

	if title == "" || content == "" {
		return nil, fmt.Errorf("%w:title and content are required", ErrInvalidInput)
	}
	if len(title) > 128 || len(content) > 50000 {
		return nil, fmt.Errorf("%w: title or content too long", ErrInvalidInput)
	}
	a := &model.Article{
		UserID:  userID,
		Title:   title,
		Content: content,
	}
	if err := s.db.WithContext(ctx).Create(a).Error; err != nil {
		return nil, fmt.Errorf("create article for user %d: %w", userID, err)
	}
	// id 由 MySQL 自增生成，写库成功后删这个 key。
	// 这一句同时清掉两样东西：该 id 可能残留的旧快照，以及别人先查过留下的空标记。
	// 少了它，新建的文章会被 60 秒的空标记挡成 404，见 4.3 与 7.6。
	s.InvalidateDetailCache(ctx, a.ID)
	return a, nil
}

// List 分页列表：page 从 1 开始
func (s *ArticleService) List(ctx context.Context, page, pageSize int) ([]*model.Article, int64, error) {
	if page < 1 {
		return nil, 0, fmt.Errorf("%w:page mustbe >=1", ErrInvalidInput)
	}
	if pageSize < 1 || pageSize > 50 {
		return nil, 0, fmt.Errorf("%w:pageSize must be 1-50", ErrInvalidInput)
	}

	var total int64
	if err := s.db.WithContext(ctx).Model(&model.Article{}).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count articles: %w", err)
	}

	if total == 0 {
		return []*model.Article{}, 0, nil
	}

	articles := make([]*model.Article, 0)
	err := s.db.WithContext(ctx).Preload("User").Order(articleListOrder).Offset((page - 1) * pageSize).Limit(pageSize).Find(&articles).Error
	if err != nil {
		return nil, total, fmt.Errorf("list articles page=%d size=%d: %w", page, pageSize, err)
	}
	return articles, total, nil
}

// Delete 只允许文章作者删除，并在事务里清掉该文章的评论。
func (s *ArticleService) Delete(ctx context.Context, id, userID uint) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 先清子表。comments.article_id 上有 RESTRICT 外键，先删文章会被拒绝。
		if err := tx.Where("article_id = ?", id).Delete(&model.Comment{}).Error; err != nil {
			return err
		}
		res := tx.Where("id = ? AND user_id = ?", id, userID).Delete(&model.Article{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("delete article %d: %w", id, err)
	}
	// 事务成功提交之后再删缓存。放在事务里会在回滚时留下一次多余的删除，
	// 更糟的是：事务还没提交就删了，并发读可能把未提交前的旧值又回填进去。
	s.InvalidateDetailCache(ctx, id)
	return nil
}

// Detail 先读缓存，未命中时合并并发请求、回源 MySQL、回填 Redis。
// 读路径的四件事都在这一层完成，handler 与 router 完全不知道缓存的存在。
// 签名与 03 章 3.7 的无缓存版完全一致，所有调用点零改动。
func (s *ArticleService) Detail(ctx context.Context, id uint) (*model.Article, error) {
	key := articleDetailKey(id)

	// 第一遍读缓存：绝大多数请求在这一步就返回了，不会进 singleflight。
	// 把整个 Detail 都塞进 Do 会让命中缓存的请求也排队，反而变慢。
	a, handled, err := s.readArticleCache(ctx, key)
	if err != nil {
		return nil, err
	}
	if handled {
		return a, nil
	}

	// 未命中才进合并。第一个请求执行 fn，其余同 key 请求等它的结果。
	v, err, shared := s.group.Do(key, func() (any, error) {
		// 脱离调用方的取消信号，但保留 ctx 里的值。
		// fn 的闭包捕获的是第一个请求者的 ctx，它一旦被取消，
		// 所有等待者都会拿到同一个 context.Canceled，见 5.3。
		// WithoutCancel 再套一个 3 秒超时，避免这次回源无限跑下去。
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()

		// 双重检查：等待者被唤醒时，第一个请求可能已经把结果写进缓存了。
		// 不查这一遍，稍晚到达的请求会各自再回源一次，合并就白做了。
		cached, handled, cacheErr := s.readArticleCache(loadCtx, key)
		if cacheErr != nil {
			return nil, cacheErr
		}
		if handled {
			return cached, nil
		}

		// 到这里才真正查库。计数器 +1，第 9 节用它证明「只回源一次」。
		s.metrics.Loads.Add(1)
		loaded, err := s.queryDetail(loadCtx, id)
		if err != nil {
			// 只有「确实不存在」能写空标记。数据库故障绝不能写成空标记，
			// 否则故障期间查过的 id 会在恢复后继续错 60 秒，见 4.4。
			if errors.Is(err, ErrNotFound) {
				s.markArticleAbsent(loadCtx, key)
			}
			return nil, err
		}

		// 回填失败只记日志，不能把这次成功的数据库查询变成接口失败。
		s.writeArticleCache(loadCtx, key, loaded)
		return loaded, nil
	})

	if shared {
		// shared 只表示结果被多个请求领取，不代表成功。
		s.metrics.Shares.Add(1)
	}
	if err != nil {
		return nil, err
	}

	// fn 只可能返回 *model.Article。用带检查的类型断言，
	// 万一以后改了 fn 的返回类型，这里给出明确错误而不是 panic。
	article, ok := v.(*model.Article)
	if !ok {
		return nil, fmt.Errorf("cache loader returned unexpected type %T", v)
	}
	return article, nil
}

// readArticleCache 读一次详情缓存，并判断这次请求还需不需要回源。
// handled 为 true 表示「已经有结论」：要么是文章，要么是确定的不存在。
// Redis 故障、key 不存在、缓存内容损坏都返回 handled=false，让调用方回源；
// 它们不该让接口直接失败，所以不计入 error。
func (s *ArticleService) readArticleCache(ctx context.Context, key string) (*model.Article, bool, error) {
	val, err := s.rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		// 正常未命中，不是故障。
		s.metrics.Misses.Add(1)
		return nil, false, nil
	}
	if err != nil {
		// 断连、超时等：降级回源，同时留下日志与计数证据。
		s.metrics.Misses.Add(1)
		s.metrics.Degrades.Add(1)
		log.Printf("read article cache %s: %v", key, err)
		return nil, false, nil
	}

	// 先比空标记，再谈反序列化。顺序不能反：
	// 哨兵不是合法 JSON，交给 Unmarshal 只会得到一次没必要的解析错误。
	if val == articleEmptySentinel {
		s.metrics.EmptyHits.Add(1)
		return nil, true, ErrNotFound
	}

	var a model.Article
	if err := json.Unmarshal([]byte(val), &a); err != nil {
		// 坏缓存自愈：删掉它，否则每次请求都会重复读到坏值。
		s.metrics.Degrades.Add(1)
		log.Printf("invalid article cache %s: %v", key, err)
		_ = s.rdb.Del(ctx, key).Err()
		return nil, false, nil
	}

	s.metrics.Hits.Add(1)
	return &a, true, nil
}

// writeArticleCache 把查到的文章写回缓存。
// 回填失败不能改变本次数据库查询的结果，所以没有返回值，只记日志和计数器。
func (s *ArticleService) writeArticleCache(ctx context.Context, key string, a *model.Article) {
	data, err := json.Marshal(a)
	if err != nil {
		// model.Article 全是普通字段，正常情况不会失败；仍要显式处理，别吞掉。
		log.Printf("marshal article cache %s: %v", key, err)
		return
	}
	// 每次调用 articleDetailTTL() 都会重新取随机数，这是防雪崩的关键。
	if err := s.rdb.Set(ctx, key, data, articleDetailTTL()).Err(); err != nil {
		s.metrics.Degrades.Add(1)
		log.Printf("write article cache %s: %v", key, err)
		return
	}
	s.metrics.Writes.Add(1)
}

// markArticleAbsent 写入「查无此文」的空标记，只给短 TTL。
// 调用前必须已经用 errors.Is(err, ErrNotFound) 确认过原因，见 4.4。
func (s *ArticleService) markArticleAbsent(ctx context.Context, key string) {
	if err := s.rdb.Set(ctx, key, articleEmptySentinel, articleEmptyTTL).Err(); err != nil {
		s.metrics.Degrades.Add(1)
		log.Printf("write empty article cache %s: %v", key, err)
	}
}

// queryDetail 只负责查库，并把 GORM 的「没查到」翻成项目内部的 ErrNotFound。
// 在这一层转换，handler 的错误映射就不用认识 gorm.ErrRecordNotFound，
// 依赖方向保持 service 向外、handler 只认 service 的错误。
func (s *ArticleService) queryDetail(ctx context.Context, id uint) (*model.Article, error) {
	var a model.Article
	err := s.db.WithContext(ctx).Preload("User").First(&a, id).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("article %d: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("query article %d: %w", id, err)
	}
	return &a, nil
}

// InvalidateDetailCache 删除详情缓存。数据库写入或事务提交成功之后才调用。
// 空标记和真实文章用的是同一个 key，所以这一句也能清掉空标记。
// 失败只记录，不改变已提交的数据库结果；TTL 是最后一道兜底。
func (s *ArticleService) InvalidateDetailCache(ctx context.Context, id uint) {
	key := articleDetailKey(id)
	if err := s.rdb.Del(ctx, key).Err(); err != nil {
		s.metrics.Degrades.Add(1)
		log.Printf("invalidate article cache %s: %v", key, err)
	}
}

// CacheStats 返回当前进程的缓存计数快照，供本地验收与将来的监控接入使用。
// 进程重启后计数归零，它不是持久化指标。
func (s *ArticleService) CacheStats() CacheStats {
	return s.metrics.snapshot()
}
```

**逐行拆解表**（按「它解决哪个问题」分组）：

| 代码 | 作用 | 对应问题 | 设计理由 |
| --- | --- | --- | --- |
| `key := articleDetailKey(id)` | 带版本的 key | 全部 | 读写删三处统一，改格式时不用批量清理 |
| 第一遍 `readArticleCache` | 命中直接返回 | 性能 | 不让命中请求进 `Do` 排队 |
| `s.group.Do(key, fn)` | 合并同 key 的并发回源 | 击穿 | 一次 SELECT 服务 N 个请求 |
| `context.WithoutCancel(ctx)` | 脱离第一个请求的取消 | 击穿 | 避免一个取消连累所有等待者 |
| `context.WithTimeout(..., 3*time.Second)` | 给回源上界 | 降级 | 防止 `fn` 无限挂住 |
| `fn` 内第二遍 `readArticleCache` | 双重检查 | 击穿 | 稍晚到达的请求直接命中刚回填的缓存 |
| `s.metrics.Loads.Add(1)` | 记录真实回源次数 | 验收 | 第 9 节用它证明合并生效 |
| `errors.Is(err, ErrNotFound)` | 区分不存在与故障 | 穿透 | 只有前者允许写空标记 |
| `s.markArticleAbsent(...)` | 写 60 秒空标记 | 穿透 | 让无效 id 不再持续回源 |
| `s.writeArticleCache(...)` | 回填并设抖动 TTL | 雪崩 | 把过期时刻摊开 |
| `if shared { Shares.Add(1) }` | 记录被合并的请求数 | 验收 | `shared` 不代表成功，只看 `err` |
| `article, ok := v.(*model.Article)` | 带检查的类型断言 | 健壮性 | 以后改返回类型时给明确错误，不 panic |
| `val == articleEmptySentinel` 在 `Unmarshal` 之前 | 空标记判定 | 穿透 | 避开 `null` 陷阱与无意义的解析错误日志 |
| `err == redis.Nil` 单独分支 | 区分未命中与故障 | 降级 | 协议级的 Nil 不是错误 |
| `_ = s.rdb.Del(ctx, key).Err()` | 坏缓存自愈 | 降级 | 清理失败不影响主流程 |
| `gorm.ErrRecordNotFound` → `ErrNotFound` | 错误翻译 | 穿透 | 修掉当前返回 500 的缺陷 |
| `s.InvalidateDetailCache` 里用 `articleDetailKey(id)` | 读写删同一个 key | 一致性 | 旧版两处手工拼接，改前缀容易漏 |

**验证**：

```powershell
gofmt -w internal/service/article.go internal/service/article_cache.go internal/service/article_cache_test.go
go build ./...
go vet ./...
go test ./...
go mod tidy
```

`go mod tidy` 放在这一步的**最后**：此时 `article.go` 已经 import 了 `singleflight`，tidy 不会把它删掉。如果 tidy 之后 `go build` 报缺包，说明 import 没写对，先回去核对步骤 4 的 import 块。

**一个 Go 细节**：`cacheMetrics` 里有 `atomic.Int64`，它内部带 `noCopy` 标记。把这样的结构体按值复制会被 `go vet` 的 copylocks 检查器警告。本项目所有地方都用 `*ArticleService`，构造函数返回指针，方法接收者也是指针，所以不会触发。这解释了一件事：为什么 `metrics` 是值字段却不能把 `ArticleService` 当值传来传去。

### 6.5 步骤 5：给错误映射补一条兜底（可选但推荐）

**增量修改**：`internal/handler/errors.go`，在 `service.ErrNotFound` 分支后面插入一个 case。

主修法在 6.4 的 `queryDetail`：文章详情的错误已经在 service 层转成 `ErrNotFound`。这一条是给**其它没转换的 service** 兜底。补充说明：`internal/service/comment.go` 的 `Create` 在本轮 03 章 6.7 里已经自己用 `errors.Is` 把 `gorm.ErrRecordNotFound` 转成 `ErrNotFound`，不依赖这条全局兜底；但以后新加的 service 仍可能漏，所以两条作用不同，不要互相替代。

```go
	case errors.Is(err, service.ErrNotFound):
		response.Error(c, http.StatusNotFound, "资源不存在")
	case errors.Is(err, gorm.ErrRecordNotFound):
		// 兜底：有些 service 还没把 GORM 的「查无此行」翻成 service.ErrNotFound。
		// 放着不管会掉进 default 变成 500，语义完全错。
		response.Error(c, http.StatusNotFound, "资源不存在")
	case errors.Is(err, gorm.ErrForeignKeyViolated):
```

**逐行拆解表**：

| 代码 | 作用 | 为什么这样设计 |
| --- | --- | --- |
| 放在 `service.ErrNotFound` 之后 | 两条都映射 404 | 顺序不影响结果，但把项目内部错误放前面，读起来先看到主口径 |
| 用 `errors.Is` 而不是 `==` | 支持被 `fmt.Errorf("%w", ...)` 包过的错误 | 00 章与 6.4 都用了同一套包装方式 |
| 只做兜底，不删 service 层转换 | 分层职责 | service 对外说自己的错误类型，handler 不必认识每一家库的私有错误 |

**验证**：

```powershell
go build ./...
```

### 6.6 步骤 6：给 Redis 客户端设连接池与超时

**完整文件**：`internal/config/redis.go`

当前版本只填了 `Addr`、`Password`、`DB`，其余全用 go-redis 默认值。默认值里有两个和本章直接相关的：

- `ReadTimeout` / `WriteTimeout` 默认 5 秒（`options.go:735-750` 的归一化逻辑）。一次网络卡住会把 HTTP 请求拖 5 秒才降级。
- `ContextTimeoutEnabled` 默认 false。`redis.go:1889-1896` 的 `baseClient.context` 注释和实现写着：关闭时会把调用方的 ctx 换成 `context.Background()`。也就是说我们在 6.4 里用 `context.WithTimeout` 设的超时，传不到 socket 读写这一层。

```go
package config

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// InitRedis 创建一个全局共享的 Redis 客户端，并先 Ping 一次确认可用。
// 客户端自带连接池、并发安全，整个进程只需要一个实例，见 03 章 7.2。
func InitRedis(cfg *RedisConfig) (*redis.Client, error) {
	if cfg == nil {
		return nil, fmt.Errorf("redis config is nil")
	}
	if cfg.Addr == "" {
		return nil, fmt.Errorf("redis addr is empty")
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,

		// PoolSize 是连接池上限。默认是 10 * GOMAXPROCS，
		// 在多核机器上会开出很多连接；学习项目写小一点，
		// 出问题时更容易判断「是不是连接不够用」。
		PoolSize: 10,
		// MinIdleConns 预留空闲连接，避免冷启动第一波请求全都在拨号。
		MinIdleConns: 2,

		// 建连、读、写三类超时分开设。不设的话 go-redis 各给 5 秒，
		// 网络卡住时 HTTP 请求会被拖住 5 秒才走到降级分支。
		DialTimeout:  2 * time.Second,
		ReadTimeout:  500 * time.Millisecond,
		WriteTimeout: 500 * time.Millisecond,

		// 关键开关：默认 false 时 go-redis 在底层把调用方 ctx 换成 Background，
		// 我们用 context.WithTimeout 设的超时就到不了 socket 读写。
		// 依据见 v9.23.0 的 options.go:174-176 与 redis.go:1889-1896。
		ContextTimeoutEnabled: true,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		// 失败分支必须关掉刚创建的客户端，否则连接池资源泄漏。
		// 这一行仓库源码里已经有了，旧版 03 章 7.2 写「还缺」是过时的快照。
		_ = rdb.Close()
		return nil, fmt.Errorf("connect redis: %w", err)
	}

	return rdb, nil
}
```

**逐行拆解表**：

| 代码 | 作用 | 为什么这样设计 |
| --- | --- | --- |
| `PoolSize: 10` | 限制并发连接数 | 默认值随 CPU 核数放大，学习阶段不需要那么多 |
| `MinIdleConns: 2` | 预留两条空闲连接 | 减小冷启动第一波的拨号延迟 |
| `DialTimeout: 2s` | 建连超时 | 连不上要快速失败，别让启动卡住 |
| `ReadTimeout/WriteTimeout: 500ms` | socket 读写超时 | 缓存是加速路径，慢了就尽快降级回源 |
| `ContextTimeoutEnabled: true` | 让 ctx 的 deadline 生效 | 不打开它，6.4 里 `WithTimeout(3s)` 在 socket 层被丢掉 |
| `Ping` 用 3 秒超时的 ctx | 启动期连通性检查 | 与 00 章「所有 DB 与 Redis 操作接收 ctx」一致 |
| `_ = rdb.Close()` | 失败时释放资源 | 关闭失败也要返回原始的 Ping 错误，别盖掉真正原因 |

**设计决策**：这些数字是学习项目的起点，不是生产推荐值。500ms 对本地 Redis 绰绰有余，如果 Redis 在远端、跨机房，可能不够，要按实测 P99 和网络抖动重定。`PoolSize` 也一样，要看并发量和 Redis 的 `maxclients`。本章没有压测，所以不写「这个值最优」这种结论。

**验证**：

```powershell
gofmt -w internal/config/redis.go
go build ./...
redis-cli ping
go run ./cmd/server
```

### 6.7 步骤 7：可选的本地计数快照接口

**增量修改**：新建 `internal/handler/cache.go`，并在 `internal/router/router.go` 的 `New` 里注册一条路由。

这一步是**可选的**，只为了让第 9 节的验收能从 HTTP 直接读到计数器。跳过它也能用单元测试读 `svc.CacheStats()`，或者干脆只看 `redis-cli` 与日志。做之前先想清楚：这个接口会暴露进程内部信息，生产环境必须加鉴权或干脆不注册。

完整文件：`internal/handler/cache.go`

```go
package handler

import (
	"github.com/gin-gonic/gin"

	"forum-backend/internal/service"
	"forum-backend/pkg/response"
)

// CacheStatsHandler 暴露缓存计数快照，仅用于本地验收。
// 它依赖具体的 ArticleService，而不是一个接口，范围刻意做小。
type CacheStatsHandler struct {
	svc *service.ArticleService
}

// NewCacheStatsHandler 由 router 装配，复用已经建好的 articleSvc。
func NewCacheStatsHandler(svc *service.ArticleService) *CacheStatsHandler {
	return &CacheStatsHandler{svc: svc}
}

// Show 返回当前进程的缓存计数。响应形状沿用 pkg/response 的 { code, message, data }。
func (h *CacheStatsHandler) Show(c *gin.Context) {
	response.OK(c, h.svc.CacheStats())
}
```

增量修改：`internal/router/router.go` 的 `New`，在 `commentHandler := ...` 之后加一行变量，在公开路由组里加一条注册。

```go
	articleSvc := service.NewArticleService(db, rdb)
	commentSvc := service.NewCommentService(db)
	articleHandler := handler.NewArticleHandler(articleSvc)
	commentHandler := handler.NewCommentHandler(commentSvc)
	cacheStatsHandler := handler.NewCacheStatsHandler(articleSvc) // 复用同一个 articleSvc，别再建一个 service
```

**增量修改（同一处的第二块）**：公开路由组里再加一条注册。

```go
		api.GET("/articles/:id/comments", commentHandler.List)
		api.GET("/debug/cache", cacheStatsHandler.Show) // 本地验收用；上线前删掉或加鉴权
```

**为什么复用同一个 `articleSvc`**：计数器是 `ArticleService` 实例上的字段。如果为了这个接口再 `service.NewArticleService(db, rdb)` 一次，得到的是另一组全零计数器，接口永远返回 0，验收会得出错误结论。这是依赖注入里很典型的一个坑：同一个进程里要有几份有状态对象，必须在装配时想清楚。

**验证**：

```powershell
gofmt -w internal/handler/cache.go internal/router/router.go
go build ./...
go vet ./...
go test ./...
```

### 6.8 整体回归：确认 03 章的能力没被改坏

改完 `Detail` 之后，最容易悄悄退化的是原来已经做对的那几件事。逐条对照：

| 03 章已有的能力 | 本章对应代码 | 有没有丢 |
| --- | --- | --- |
| 命中且 JSON 可解析直接返回 | `readArticleCache` 的 `Hits.Add(1)` 分支 | 保留 |
| 坏 JSON 删 key 并回源 | `Unmarshal` 失败分支的 `Del` | 保留，且补了降级计数 |
| Redis 故障时降级回源 | `err != redis.Nil` 分支 | 保留 |
| 回填失败不阻断主流程 | `writeArticleCache` 无返回值 | 保留 |
| 写库成功后删缓存 | `Create`、`Delete` 两处调用 | 保留，且换了带版本的 key |
| `Detail(ctx, id)` 签名不变 | 6.4 的函数签名 | 保留，handler 零改动 |
| 三层装配顺序不变 | `NewArticleService` 仍收两个参数 | 保留，只多了可选步骤 7 的路由 |

```powershell
gofmt -l internal/service/article.go internal/service/article_cache.go internal/service/article_cache_test.go internal/handler/cache.go internal/config/redis.go
go build ./...
go vet ./...
go test ./...
go test -race ./...
```

第一条 `gofmt -l` 应该没有输出。有输出就说明某个文件没格式化，先 `gofmt -w` 再继续。

**本机环境实测（2026-11-07）**：把本章所有 Go 代码块抽到项目副本里落地后，`go build ./...`、`go vet ./...`、`go test ./...` 全部通过，`gofmt -l` 无输出，6.2 的四个纯函数测试也通过。`go test -race` 在本机跑不起来：默认提示 `-race requires cgo; enable cgo by setting CGO_ENABLED=1`，改成 `CGO_ENABLED=1` 之后运行时初始化失败，属于本机 Go 工具链与 C 编译环境的限制，不是本章代码的问题。换一台装好 C 编译器的机器或放到 CI 里再补这一项，**别把没跑过的检查写成「已通过」**。

---

## 7. 写路径与一致性：把「先写库再删缓存」讲透

代码已经落盘，这一节回头把设计理由补完整。面试里这一块被追问得最多，也最容易答成背口诀。

### 7.1 为什么是删缓存，不是更新缓存

两条写请求并发时，更新缓存会乱序：

```text
请求 A：把标题改成 "A"，数据库提交成功
请求 B：把标题改成 "B"，数据库提交成功
（A 的缓存更新因为网络慢，晚于 B 到达）
请求 A：SET key = {"title":"A"}
请求 B：SET key = {"title":"B"}
请求 A：SET key = {"title":"A"}   ← 后写，但值是旧的
```

最终数据库是 "B"，缓存是 "A"，而没有任何机制会再来纠正它，直到 TTL 到期。改成删缓存就没有这个问题：两次 `DEL` 无论谁先谁后，结果都是「key 不存在」，下次读自然按数据库里的最新值重建。

代价是删完之后第一次读会回源一次。对详情这种读多写少的接口，这点代价可以忽略。

### 7.2 为什么删在事务提交之后

`Delete` 里的事务包含两步：先删评论，再删文章。把 `DEL` 放进事务里会出两种问题：

```text
情况一：事务最后回滚了
  缓存已经被删，数据库里文章还在
  结果：白白多一次回源，虽然数据没错，但缓存被无故击穿

情况二：DEL 在事务提交前执行
  并发读请求在事务还没提交时回源，读到旧值并回填
  事务随后提交，缓存里留着旧值，直到 TTL 到期
```

所以源码把 `s.InvalidateDetailCache(ctx, id)` 放在 `Transaction(...)` 返回并且 `err == nil` 之后。这个顺序在 03 章 6.2 定删除策略时就已经确定，本章没有改动它，只是把理由写全。

### 7.3 删失败会怎样

`DEL` 失败只记日志和 `Degrades` 计数，接口照常返回成功。此时旧缓存还在，用户会读到旧内容，最长读多久取决于 TTL。这就是 **TTL 是最后一道防线** 的意思：它的作用是给所有失效失败兜底，并不承担定期刷新的职责。

本章把 TTL 从固定 10 分钟改成 `[10m, 12m)`，代价就是脏数据窗口多了最多 2 分钟。3.2 的推导里把这一条算进去了，属于明知故犯的取舍，不是疏忽。

### 7.4 写路径清单

以后每加一个会改文章内容的接口，照这张表补一行。**注意「改文章内容」不只有改标题正文**：详情响应里任何会被别的写路径改动的字段都算，点赞数就是本轮补进来的那一类。

| 写操作 | 现在的状态 | 要不要删缓存 | 源码位置 |
| --- | --- | --- | --- |
| 创建文章 | handler 与 service 都已实现（03 章 6.6） | 要，删新 id 的 key（含空标记） | `Create` 已调用 |
| 删除文章 | handler 与 service 都已实现（03 章 6.6） | 要，事务提交后删 | `Delete` 已调用 |
| 编辑文章 | 还没有这个接口 | 要，提交后删 | 将来加 |
| 点赞 | 04 章 4.10 已给出完整落盘步骤，详情响应带 `like_count` | 要，`changed=true` 时删 | `LikeService.apply` 调 `InvalidateDetailCache`（按 4.10 落盘后） |
| 管理员批量导入 | 还没有 | 要，或者换 key 版本 | 将来加 |

**「点赞」这一行本轮已在文档里补上。** 04 章 4.10 给出了完整增量与验收：让文章详情显示 `like_count`，`LikeService.apply` 在关系真的改变时调用 `InvalidateDetailCache` 删掉详情缓存，下一次读详情回源重算。源码按 4.10 自己落盘，别把「文档写了」当成「已经实现」。这正是 1.2 四问法里「谁写」那一问的用途：先出现「详情缓存里多了一份会被点赞改动的数据」，才需要回头把写路径重新过一遍。

同一轮还把这个方法的名字统一了。它原本是私有的 `invalidateArticleCache`，只有 `ArticleService` 自己调；04 章 4.10 多了点赞这个调用方，全库文档已同步改成导出的 `InvalidateDetailCache`。本章前面出现的代码块按最终名字写，跟做时不必再改一次。

剩下「编辑文章」「管理员批量导入」两行仍是给未来留的钩子。

**04a 章的补充**：事务发件箱里的事件写入发生在事务**内部**，和这张表讨论的「提交成功之后再删缓存」是两类动作，不需要补进清单。点赞接口按 04a 章改完之后，事务里会多一行 `outbox_events`，事务提交之后的 Redis Set、ZSET 与详情缓存失效仍归这张表管。把两类写入混成一列，最容易犯的错是把 `XADD` 也塞进事务函数里，Redis 不会跟着 MySQL 回滚。04a 章 1.1 画的就是这个失败窗口。

### 7.5 经典脏数据窗口与延迟双删

即使顺序全对，仍有一个理论上存在的窗口：

```text
t1  读请求 A：GET miss
t2  读请求 A：SELECT 拿到旧值 V1
t3  写请求 B：UPDATE 成 V2，提交成功
t4  写请求 B：DEL key
t5  读请求 A：SET key = V1（把读到的旧值回填进去）
t6  之后所有读请求都命中 V1，直到 TTL 到期
```

A 的回源发生在 B 的删除之前，回填却发生在删除之后。这个窗口很窄，但确实存在。

社区里常见的对策叫「延迟双删」：写库前删一次，写库后 `sleep` 一小段再删一次，指望第二次删能清掉 t5 那样的迟到回填。本章不实现，理由有三条：

1. `sleep` 时长没有依据。设短了清不掉慢请求，设长了白白拖住写接口，而回源耗时是随负载变化的。
2. 它仍然不彻底。只要有一次回填比第二次 `DEL` 更晚，脏数据照样留下。
3. 它把「副本可能旧」这个事实藏进了时间假设里，反而让系统更难推理。

更可靠的工程做法是给缓存值加版本号、订阅数据库 binlog 做失效、或者用消息队列串行化同一 key 的失效。这些都要引入新组件，超出学习项目范围。本章的选择是诚实承认窗口存在，并用 TTL 给它一个明确上界。（6.4 的 `singleflight` 顺带缩小了这个窗口：同一 key 的并发回源被合并，t2 与 t5 之间插进别的读请求的机会变少了，但它没有消除窗口。）

### 7.6 `Create` 里那行 `Del` 的真正用意

03 章 7.6 让用户补这一行时，理由是「写路径要失效」。当时看不太出必要：新文章刚创建，缓存里哪来的旧值。

加上空值缓存之后它变得关键：

```text
t1  有人请求 GET /api/articles/999999，数据库没有 → 写入空标记，TTL 60 秒
t2  系统在某种情形下真的插入了 id=999999 的文章（导入、手工指定 id、自增回绕）
t3  Create 成功后调用 InvalidateDetailCache(ctx, a.ID)
      → DEL article:detail:v1:999999，空标记被清掉
t4  下一次读正常回源并回填文章 JSON
```

少了这一行，新文章会被 404 挡住最多 60 秒。因为空标记和真实文章共用同一个 key，一句 `DEL` 就覆盖两种情况，代码不用改。

在当前模型里，id 由 MySQL 自增生成，且没有导入接口，t2 其实不太会发生。把这个设计写出来，是为了说明一个更一般的规则：**每加一种缓存状态，都要回头把写路径重新过一遍**。这就是 1.2 四问法里「谁写」那一问的实际用途。

---

## 8. 降级、超时与故障分级

### 8.1 分级表：出事时接口到底会怎样

把 1.3 的链路表按「对外可见的行为」重新整理。这张表就是本章的降级规格，第 9 节会逐条验收。

| 故障 | 触发方式 | 代码分支 | 对外表现 | 可观测证据 |
| --- | --- | --- | --- | --- |
| 读缓存超时 / 断连 | 停 Redis，或网络抖动 | `readArticleCache` 的 `err != redis.Nil` | 详情照常返回 200，数据来自 MySQL | 日志 `read article cache ...`、`Degrades` +1 |
| 缓存值损坏 | `redis-cli SET` 一个非 JSON 字符串 | `Unmarshal` 失败分支 | 删除坏 key 并回源，返回正确数据 | 日志 `invalid article cache ...`、`Degrades` +1 |
| 回填失败 | Redis 只读、或写超时 | `writeArticleCache` 的 `Set` 失败 | 本次结果照常返回，只是下次还要回源 | 日志 `write article cache ...`、`Degrades` +1 |
| 写空标记失败 | 同上 | `markArticleAbsent` 的 `Set` 失败 | 404 照常返回，下次仍会回源 | 日志 `write empty article cache ...`、`Degrades` +1 |
| 删缓存失败 | 写接口之后 Redis 故障 | `InvalidateDetailCache` 的 `Del` 失败 | 写操作成功，详情可能旧到 TTL 到期 | 日志 `invalidate article cache ...`、`Degrades` +1 |
| Redis 整体宕机 | 停掉 Redis 进程 | 所有 `Get` / `Set` 都报错 | 所有读请求回源 MySQL，接口仍可用，但数据库压力上升 | `Degrades` 持续增长 |
| MySQL 故障 | 停 MySQL | `queryDetail` 返回非 `ErrNotFound` 的错误 | 500，**不写空标记** | `mapServiceError` 的 default 分支日志 |
| 查无此文 | 请求不存在的 id | `queryDetail` 转 `ErrNotFound` | 404，并写 60 秒空标记 | `EmptyHits` +1、`redis-cli GET` 能看到哨兵 |

一条贯穿全表的规则：**凡是 Redis 的失败，都不能改变数据库结果的对外表现。** 缓存是加速路径，不是正确性来源。

### 8.2 fail-open 与 fail-closed 怎么选

这是设计降级时必须明确选一次的问题。

| | fail-open（故障就放行） | fail-closed（故障就拒绝） |
| --- | --- | --- |
| 缓存读失败 | 回源 MySQL，接口可用 | 直接返回 503 |
| 适用组件 | 缓存、非关键装饰器 | 鉴权、限流、支付校验 |
| 风险 | 故障期间数据库压力上升 | 故障期间服务不可用 |

本章选 fail-open，因为缓存是副本，MySQL 才是事实。04 章做限流时会重新问一次这个问题：限流组件故障时放行还是拒绝，答案可能不同，取决于你要保可用性还是保防护。把两类组件分开判断，比统一套一个策略合理。

要如实写 fail-open 的代价：Redis 全挂时，所有请求都会回源，MySQL 可能先扛不住。生产环境要配熔断、限流、本地缓存来兜住这一层，本章只做到了「设短超时 + 不把请求拖死」，没有熔断。这是 04 章之后的内容。

### 8.3 超时预算怎么对得上

把 6.4 与 6.6 两处超时放一起看：

```text
HTTP 请求 ctx（Gin 给，没有显式超时）
  └─ first GET Redis        受请求 ctx 取消影响；socket 层受 ReadTimeout 500ms 限制
       └─ singleflight fn
            ├─ loadCtx = WithoutCancel(请求 ctx) + WithTimeout(3s)
            ├─ second GET Redis     受 3s 与 500ms 双重限制
            ├─ SELECT MySQL        受 3s 限制（WithContext）
            └─ SET Redis 回填      受 3s 与 500ms 双重限制
```

三个数字的关系：

| 数字 | 位置 | 作用 |
| --- | --- | --- |
| 500ms | `InitRedis` 的读写超时 | 单次 Redis 命令最多卡 500ms |
| 3s | `Detail` 里 `loadCtx` | 整段回源（二次读、查库、回填）最多 3s |
| 10m ~ 12m | `articleDetailTTL()` | 副本最长存活时间，也是失效失败时的脏数据上界 |

`ContextTimeoutEnabled: true` 是把 3s 和请求 ctx 的 deadline 真正传到 socket 层的开关。关掉它，`WithoutCancel` 之后新建的 3s 超时在 Redis 命令上不生效，只剩 500ms 的 socket 超时在起作用，行为会和文档描述不一致。改 `config/redis.go` 时别漏这一行。

### 8.4 常见错误对照

| 写法 | 会怎样 | 正确做法 |
| --- | --- | --- |
| Redis 读失败就 `return nil, err` | 缓存挂了接口跟着挂 | 降级回源 |
| 把 `redis.Nil` 当故障记降级日志 | 正常未命中也会刷屏，`Degrades` 失去意义 | 单独分支，只加 `Misses` |
| 回填失败返回 error | 数据库查成功了却报 500 | 只记日志 |
| 查库报错也写空标记 | 故障被缓存成 404，恢复后继续错 | 只认 `ErrNotFound` |
| 不给 Redis 设超时 | 网络卡住拖住整个 HTTP 请求 | `DialTimeout` / `ReadTimeout` / `WriteTimeout` |
| 设了 `WithTimeout` 却没开 `ContextTimeoutEnabled` | ctx 的 deadline 在底层被丢弃 | 两个一起改 |
| 把 fail-open 用在鉴权上 | 鉴权挂了等于门户大开 | 按组件分别选策略，见 8.2 |

---

## 9. 验收：拿证据，不靠感觉

### 9.1 证据表

写完了代码不等于问题解决。每一项都要有一个能看见的输出。

| 要证明的事 | 看哪里 | 期望 | 若不符，先查什么 |
| --- | --- | --- | --- |
| TTL 真的抖动 | 单测 + `redis-cli TTL` | 落在 `[600, 720)` 秒，多篇不同 | 是不是把随机数算在包级变量里了 |
| 空标记真的写入 | `redis-cli GET article:detail:v1:<不存在的 id>` | `__ARTICLE_NOT_FOUND__` | `queryDetail` 有没有转 `ErrNotFound` |
| 空标记 TTL 短 | `redis-cli TTL` | 不超过 60 | 是不是误用了 `articleDetailTTL()` |
| 空标记挡住回源 | 第二次请求后的 `Loads` / `EmptyHits` | `EmptyHits` +1，`Loads` 不变 | 第一遍 `Get` 有没有先比哨兵 |
| 并发只回源一次 | `Loads` 增量 | 50 个并发请求，`Loads` 只 +1 | 有没有双重检查、`Do` 的 key 是不是缓存 key |
| 空标记不与文章 JSON 撞车 | 单测 `TestArticleEmptySentinelIsNotArticleJSON` | PASS | 哨兵是不是被改成 `null` 或 `{}` |
| 查不存在的文章返回 404 | `curl.exe -i` | 404，不是 500 | 6.5 的兜底 case 有没有加 |
| Redis 故障时接口仍可用 | 停 Redis 后请求详情 | 200，日志有降级记录 | 是不是把缓存错误当成 error 返回了 |
| 写后不返回旧内容 | 写后读 | 见 9.6 的边界说明 | 03 章 6.6、6.7 已接通写接口；删后 GET 详情现在返回 404，4.5 的缺陷已修 |

### 9.2 前置条件

```powershell
go test ./...
go build ./cmd/server
redis-cli ping
go run ./cmd/server
```

服务要在另一个终端保持运行。若你做了 6.7 的可选步骤，用 `curl.exe http://localhost:8080/api/debug/cache` 确认能拿到七个计数；没做就跳过所有读 `Loads` 的步骤，改用 `redis-cli MONITOR` 观察命令次数，或只跑 6.2 的纯函数单测。

`main.go` 仍然把 Redis 当启动硬依赖，**先起服务再停 Redis**，别反过来。反过来连启动都进不去，测的不是降级。

### 9.3 雪崩：看 TTL 分布

```powershell
curl.exe http://localhost:8080/api/articles/1
curl.exe http://localhost:8080/api/articles/2
redis-cli TTL article:detail:v1:1
redis-cli TTL article:detail:v1:2
```

两篇文章的 TTL 都应在 600 到 719 之间，且数值不同。想看更直观的分布，重复请求几篇不同的文章再逐个 `TTL`。key 换成 `v1` 之后，旧的 `article:detail:1` 不会被读到，本地可以用 `redis-cli KEYS article:detail:*` 看一眼两种格式同时存在，旧 key 会在原 TTL 到期后消失。

更稳的证据是 6.2 的单测，它跑 1000 次并检查取值分散程度，不依赖 Redis 是否运行。

### 9.4 穿透：看空标记

```powershell
curl.exe -i http://localhost:8080/api/articles/999999
redis-cli GET article:detail:v1:999999
redis-cli TTL article:detail:v1:999999
curl.exe -i http://localhost:8080/api/articles/999999
```

期望：两次都是 404；`GET` 返回 `__ARTICLE_NOT_FOUND__`；`TTL` 不超过 60。若能看到 `/api/debug/cache`，第二次请求之后 `empty_hits` 应该 +1，`loads` 不变。

第一次返回 500 说明 6.4 的 `queryDetail` 转换或 6.5 的兜底没落地，先修这个再做后面的验收。

用 MySQL general log 或 GORM 的 SQL 日志可以进一步确认第二次没有发出 SELECT。学习阶段不必开 general log，看 `loads` 计数已经够。

### 9.5 击穿：50 个并发只回源一次

**临时验收脚本（完整文件）**：`tmp/cache_probe.go`。它不属于项目交付物，验完可以删掉。作用是在同一瞬间发出 50 个详情请求，并比较前后的 `loads` 计数。

```go
// 临时验收脚本，不属于交付物。
// 用法：go run ./tmp/cache_probe.go http://localhost:8080 1
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
)

// statsResp 只取调试接口里我们关心的三个计数。
// 字段名对上 6.1 里 CacheStats 的 json tag。
type statsResp struct {
	Data struct {
		Loads  int64 `json:"loads"`
		Shares int64 `json:"shares"`
		Hits   int64 `json:"hits"`
	} `json:"data"`
}

// readStats 读一次调试接口，把 data 解析成结构体。
func readStats(base string) (statsResp, error) {
	var out statsResp
	resp, err := http.Get(base + "/api/debug/cache")
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("debug endpoint status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, err
	}
	return out, nil
}

func main() {
	if len(os.Args) != 3 {
		fmt.Println("usage: go run ./tmp/cache_probe.go <base_url> <article_id>")
		os.Exit(2)
	}
	base := os.Args[1]
	id, err := strconv.Atoi(os.Args[2])
	if err != nil {
		fmt.Println("article id must be a number")
		os.Exit(2)
	}

	before, err := readStats(base)
	if err != nil {
		fmt.Println("read stats before:", err)
		fmt.Println("did you register the optional /api/debug/cache route from section 6.7?")
		os.Exit(1)
	}

	const concurrency = 50
	var wg sync.WaitGroup
	var mu sync.Mutex
	okCount := 0

	// 先让 50 个 goroutine 都准备好，再一起放行，尽量制造同一瞬间的并发。
	start := make(chan struct{})
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			resp, err := http.Get(fmt.Sprintf("%s/api/articles/%d", base, id))
			if err != nil {
				return
			}
			// 读都不读就关会妨碍连接复用，正式客户端要处理；
			// 这里只发 50 个请求，简单关掉即可。
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				mu.Lock()
				okCount++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	after, err := readStats(base)
	if err != nil {
		fmt.Println("read stats after:", err)
		os.Exit(1)
	}

	fmt.Printf("successful requests: %d / %d\n", okCount, concurrency)
	fmt.Printf("loads delta:  %d (expect 1 if the key was cold)\n", after.Data.Loads-before.Data.Loads)
	fmt.Printf("shares delta: %d (how many callers were merged)\n", after.Data.Shares-before.Data.Shares)
	fmt.Printf("hits delta:   %d\n", after.Data.Hits-before.Data.Hits)
}
```

跑之前必须先把目标 key 删掉，否则 50 个请求全部命中缓存，测的是命中路径而不是合并：

```powershell
redis-cli DEL article:detail:v1:1
go run ./tmp/cache_probe.go http://localhost:8080 1
```

期望输出：`successful requests: 50 / 50`，`loads delta` 为 1。`shares delta` 与 `hits delta` 不设强断言：请求未必全部落在同一个 `Do` 窗口里，稍晚到达的那些会在自己的第一遍 `Get` 里直接命中刚回填的缓存，这正是 5.4 双重检查设计的行为。

如果 `loads delta` 明显大于 1，按这个顺序查：

1. `redis-cli DEL` 有没有执行成功（返回 1 表示删掉了 1 个 key）。
2. `Do` 的 key 是不是 `articleDetailKey(id)`，写成固定字符串会把所有文章合并成一串，写成请求级唯一值则完全不合并。
3. `fn` 里有没有第二遍 `readArticleCache`。
4. 是不是起了多个服务进程，`Loads` 只统计当前进程。

### 9.6 一致性：当前有一个诚实的阻塞

写路径失效在 service 层已经落地（`Create`、`Delete` 各一行）。03 章 6.6 与 6.7 已把文章、评论的写接口 handler 全部落盘，所以**端到端的「发布后读详情」「删除后读详情」现在能跑通了**，旧版 9.6 记的「写接口仍是 501」阻塞已解除。4.5 记录的 500 缺陷已在源码修好，删完文章立刻 GET 详情现在返回 404。验收时仍建议同时用列表或 SQL 确认删除结果，多一份证据没坏处。

两条路：

| 做法 | 适合谁 | 说明 |
| --- | --- | --- |
| 先按 03 章 6.6、6.7 接通文章与评论写接口并跑通 6.6.5、6.7.7 的验证，再回来验收 | 按顺序跟做的人 | 推荐，一次把垂直切片打通 |
| 写一个 service 层集成测试，直接调 `Create`、`Detail`、`Delete` | 只想先验证缓存的人 | 需要本地 MySQL 与 Redis，属于 05 章的集成测试范围 |

在写接口通过验收之前，9.8 的对应勾选项保持未勾选，不要在简历里写「写后读一致性已验证」。03 章第 9 节的验收项也标了同样的前置条件。

### 9.7 降级：停掉 Redis

服务保持运行，另开终端：

```powershell
redis-cli shutdown nosave
curl.exe -i http://localhost:8080/api/articles/1
```

期望：详情仍返回 200 与正确数据；Go 服务端日志出现 `read article cache article:detail:v1:1: ...` 或 `write article cache ...`；`/api/debug/cache` 的 `degrades` 持续增长。

第一次请求会比平时慢，因为要等 500ms 的 `ReadTimeout` 或 2 秒的 `DialTimeout` 才判定失败。这是 8.3 超时预算的实际表现，不是卡死。测完记得把 Redis 起回来再继续其它验收。

反过来再测一次「Redis 恢复」：起回 Redis，连续请求两次，第二次 `hits` 应该 +1。

### 9.8 勾选项

- [ ] `go build ./...`、`go vet ./...`、`go test ./...` 全部通过；`go test -race ./...` 若受本机 CGO 环境限制，如实记为「未执行」，别勾
- [ ] 6.2 的四个纯函数测试通过，TTL 落在 `[600, 720)` 秒且取值分散
- [ ] 多篇文章的 `redis-cli TTL` 数值不同
- [ ] 不存在的 id 返回 404，Redis 里能看到哨兵且 TTL 不超过 60
- [ ] 第二次请求不存在的 id 不产生新的 `loads`
- [ ] 50 个并发冷启动请求只让 `loads` 增加 1
- [ ] 停掉 Redis 后详情仍返回 200，日志与 `degrades` 有记录
- [ ] `Create` 与 `Delete` 的失效调用仍在，key 用 `articleDetailKey(id)`
- [ ] 03 章 6.6、6.7 的写接口已落盘；修改与删除后不返回旧内容（删后 GET 详情现在返回 404）
- [ ] `gofmt -l` 对本章新增或修改的文件无输出

只有勾上的项才能写进简历。没勾上的，说明它还在文档里，不在项目里。

---

## 10. 怎么把这一章讲出来

### 10.1 先对比 03 章 7.6 的旧表达

旧版给的是：

> 我实现了 Cache Aside 读路径。命中直接返回，坏缓存删除并回源，Redis 故障时降级到 MySQL，回填失败不影响本次数据库结果。空值缓存、TTL 抖动、请求合并和写路径失效已在设计中，尚未实现，所以还不会声称解决了穿透、击穿与雪崩。

这段话在当时的进度下是准确的，也是诚实的。本章落盘并验收之后，它就应该改了。**照自己当前的真实进度说，别提前背新版本。**

### 10.2 验收通过之后可以怎么说

> 我在文章详情的 Cache Aside 读路径上做了三处加固。第一，TTL 从固定 10 分钟改成 10 分钟加 2 分钟随机抖动，把一批写入的 key 过期时刻摊开，避免集体回源。第二，用一个不可能与文章 JSON 撞车的哨兵值缓存「查无此文」，只给 60 秒短 TTL，并且只在确认是 ErrRecordNotFound 时才写，数据库故障不会被缓存成不存在。第三，用 singleflight 把同一 key 的并发回源合并成一次，函数内部做二次读缓存，并用 WithoutCancel 加独立超时，避免第一个请求取消时连累其它等待者。写路径在数据库事务提交成功后删缓存，删失败只记日志，靠 TTL 兜底。缓存读写失败一律降级回源，客户端超时单独配置。整个过程用进程内原子计数器做验收证据。
>
> 这些只在单进程内生效，跨实例、缓存与数据库的一致性窗口、熔断限流都还没做，也没有压测数据，所以不会给性能数字。

### 10.3 追问准备

| 追问 | 该怎么答 |
| --- | --- |
| 三个问题到底区别在哪 | 用 1.4 的时间轴：穿透是「一个不存在的 key 持续回源」，击穿是「一个热点 key 过期瞬间 N 次回源」，雪崩是「很多 key 同一时刻集体过期」。数量、热点、时间形状都不一样 |
| 抖动幅度怎么定的 | 3.2 的推导：基础 TTL 10 分钟、批量写入集中在秒级、期望摊到 2 分钟窗口，取 20%。它是本项目取值，不是通用常数，生产要按实测重算 |
| 为什么用哨兵字符串，不用 `null` | `json.Unmarshal([]byte("null"), &a)` 不报错，会把结构体留成全零值，客户端会收到一篇假文章。哨兵不以 `{` 开头，不可能与 `json.Marshal(model.Article)` 的结果相同 |
| 空值缓存有没有副作用 | 有。后来真插入同 id 记录时会被挡最多 60 秒，所以 `Create` 成功后也删这个 key。空标记和真实文章共用一个 key，一句 `DEL` 覆盖两种情况 |
| 数据库报超时会不会被缓存成不存在 | 不会。只有 `errors.Is(err, ErrNotFound)` 才写空标记；其它错误直接返回 500，不写任何缓存 |
| singleflight 多实例还管用吗 | 不管用。它只合并同一 Go 进程内的调用，N 个实例最坏回源 N 次。跨实例要分布式锁或逻辑过期，本阶段没做，理由见 5.2 |
| `fn` 里为什么要用 `WithoutCancel` | `Do` 的闭包捕获第一个请求者的 ctx，它一被取消，所有等待者会拿到同一个 `context.Canceled`。`WithoutCancel` 脱离取消、保留 value，再套 3 秒超时给上界；代价是请求全断了这次回源仍会跑完 |
| 为什么写后是删缓存，不是更新缓存 | 两条写并发时，更新缓存会乱序覆盖，最终缓存里留旧值。删缓存无论顺序如何结果都一致，下次读按数据库最新值重建 |
| 缓存和数据库能保证强一致吗 | 不能。7.5 的脏数据窗口客观存在，本章没有消除它，只用 TTL 给了上界。延迟双删、binlog 订阅都能缩小窗口，但都不是零成本 |
| Redis 全挂了会怎样 | 所有读请求回源 MySQL，接口仍返回正确数据，但数据库压力上升。本章只做了短超时和降级，没有熔断和多级缓存，这是明确的边界 |
| 有没有性能数据 | 没有压测，不给数字。能给的证据是 TTL 分布、空标记命中、并发下的 `loads` 增量，都是功能验证，不是性能结论 |

### 10.4 复用清单：下次遇到缓存问题怎么自己推

这一章真正要带走的是下面七步，换一个接口、换一种存储也照样能用。

1. **定身份**：谁是事实，谁是副本。副本可以丢、会旧、可能被伪造。
2. **四问**：谁读、谁写、何时失效、失效了会怎样。把「谁写」列成清单，加新接口时照表补。
3. **画链路**：从请求进来到响应出去，每个箭头都标上正常与异常两种输出。
4. **逐环注入故障**：挂了、慢了、返回怪东西，分别定对外行为，写成降级规格表。
5. **画时间轴**：把「同时」「持续」「集体」这三种时间形状分开，别把三个名词当成同一类问题。
6. **给每个解法找代价**：内存、一致性窗口、新依赖、单进程边界，摆到台面上再选，堆招不等于加固。
7. **先想怎么验收**：写代码之前先问「拿什么证明它生效」。答不上来，就先补可观测，再动手。

第 7 步最容易被跳过，也最能区分「听说过」和「做过」。

---

## 11. 本章对前面文档的勘误

延续 00 章「文档跟着进度长」的规矩，把这次核对出来的过时表述列清楚。这些位置本次已同步修正。

| 位置 | 旧表述 | 2026-11-07 核对结果 | 处理 |
| --- | --- | --- | --- |
| README「当前代码状态」 | 未完成里含「缓存写路径失效」 | `Create` 第 50 行、`Delete` 第 105 行已调用，`InvalidateDetailCache` 已实现 | 移到已完成，未完成清单改为工程化加固 |
| README「当前代码状态」、00 章 | `InitRedis` 的 `Ping` 失败分支缺 `rdb.Close()` | `config/redis.go` 第 26 行已有 `_ = rdb.Close()` | 改为已修复 |
| README「gofmt」 | 只列 4 个文件未格式化 | `gofmt -l` 实际列出 12 个 | 改为给出检查命令，不再列具体文件名 |
| 03 章 7.2 末 | 「还需补上的一个仓库缺陷」 | 缺陷已修，标题与代码仍在 | 改为勘误说明，保留代码作教学示例 |
| 03 章 7.4 表格 | `10*time.Minute` 那行指向「见 7.5」 | 7.5 只列概念，实现在 03a | 改指向 03a 第 3 节 |
| 03 章 7.5 | 三个解法都标「本章代码待实现」 | 03a 给出完整实现 | 改为指向 03a，保留概念表 |
| 03 章 7.6 状态表 | 写路径缓存失效列在「待实现」 | 已实现 | 移到已实现，其余待办指向 03a |
| 03 章 7.6 正文 | 「7.1 的写流程描述的是目标顺序，当前代码尚未接线」 | 已接线 | 改为已接线，并说明 `Create` 那行在空值缓存下的新含义 |
| 03 章 7.6 练习表达 | 「尚未实现，所以还不会声称解决」 | 按读者自己的进度判断 | 加一条指向 03a 第 10 节的提示 |
| 03 章第 9 节 | 验收步骤里多处写「7.6 待办」 | 部分已落地，其余在 03a | 逐处改指向 |
| 00 章阶段 C | 负缓存、抖动、失效时机、指标都写在第 2 周后半 | 03 章 7 节只完成基础读路径与失效 | 拆出阶段 C+ 指向 03a，阶段 D/E 周次不变 |
| 04 章开工前 | 「真正动手实现 03 章 7.6 时再 go get golang.org/x/sync」 | 03a 步骤 3 已经装好 | 改成两条路径都自洽的表述 |
| 01 章四周地图 | 表里没有进阶专题 | 新增 03a | 表格不动，表下加一句说明 |
| 06 章模板前核对 | 「写路径缓存失效、空值缓存、TTL 抖动、singleflight 尚未实现」 | 写路径失效已实现，其余在 03a 给出代码 | 改成按读者进度核对，并指向 03a 第 10 节 |

还有一条**源码缺陷**不在旧文档的勘误范围内，是本次新发现的：`queryDetail` 直接抛 `gorm.ErrRecordNotFound`，而 `mapServiceError` 没有对应分支，查不存在的文章会返回 500。修法在 6.4 与 6.5，验收在 9.4。该缺陷现已在仓库源码修好，本节保留它是为了说明当时为什么必须先修它再加空值缓存。

---

## 12. 参考来源

技术结论以英文官方文档或项目锁定版本的库源码为准。中文工程社区的三个故障名词不列作权威依据，1.7 已说明它们的归属。

**Go 标准库与官方约定**

- `math/rand/v2` 顶层函数并发安全、`Int64N`、包注释第 12-13 行与第 31 行：https://pkg.go.dev/math/rand/v2
- `context.WithoutCancel`：https://pkg.go.dev/context#WithoutCancel
- `encoding/json` 对 `null` 与结构体的行为、omitempty：https://pkg.go.dev/encoding/json
- `sync/atomic` 的 `Int64` 与 noCopy：https://pkg.go.dev/sync/atomic#Int64
- `net/http` 客户端与响应体关闭：https://pkg.go.dev/net/http#Response
- Go Code Review Comments（Context、错误字符串、导出标识符注释）：https://go.dev/wiki/CodeReviewComments
- Go Doc Comments：https://go.dev/doc/comment

**项目锁定版本的库源码与文档**

- `golang.org/x/sync/singleflight` 的 `Do` / `DoChan` 契约：https://pkg.go.dev/golang.org/x/sync/singleflight
- 本机模块缓存 `golang.org/x/sync@v0.24.0/singleflight/singleflight.go:86-90`（`Do` 注释）、`:71-84`（`Group` 与 `Result`）
- go-redis v9 `Options` 字段说明：https://pkg.go.dev/github.com/redis/go-redis/v9#Options
- 本机模块缓存 `github.com/redis/go-redis/v9@v9.23.0/options.go:133-176`（Dial/Read/WriteTimeout、ContextTimeoutEnabled）、`:314-357`（PoolSize、MinIdleConns）、`:688-750`（默认值归一化）
- 本机模块缓存 `github.com/redis/go-redis/v9@v9.23.0/redis.go:1889-1896`（`baseClient.context` 在开关关闭时丢弃调用方 ctx）
- 本机模块缓存 `github.com/redis/go-redis/v9@v9.23.0/string_commands.go:139,478,616`（`Get` / `Set` / `SetNX` 签名）
- go-redis `type Client` 连接池与并发安全注释（03 章 7.2 已引用）：https://pkg.go.dev/github.com/redis/go-redis/v9#Client

**Redis 与 GORM**

- Redis `SET` 及过期参数：https://redis.io/docs/latest/commands/set/
- Redis `EXPIRE` / `TTL`：https://redis.io/docs/latest/commands/expire/ 、 https://redis.io/docs/latest/commands/ttl/
- Redis key 过期机制：https://redis.io/docs/latest/develop/reference/commands/
- GORM Error Handling（`ErrRecordNotFound`、TranslateError）：https://gorm.io/docs/error_handling.html
- GORM Query（`First` 与检索行为）：https://gorm.io/docs/query.html
- GORM Delete（软删除与全局删除保护）：https://gorm.io/docs/delete.html

**本套文档内部**

- 00 章「本套文档怎么写」「文档跟着进度长」「官方明文与工程惯例要分开」「教程代码块的统一标识」
- 02 章 8.0（DTO 落点与「只拆文件不新建包」的同构判断）
- 03 章 1.2（命名统一）、6.1、6.6、6.7 与 6.8（骨架先落盘、文章与评论写接口实现、文件清单按落盘顺序）、7.2（三层装配链）、7.6（`null` 陷阱）
- 04 章开工前（依赖现状与 `go mod tidy` 的坑）、05 章（集成测试与 `go test -race`）

> 下一步：[`04-第3周-亮点功能-点赞热榜与限流.md`](04-第3周-亮点功能-点赞热榜与限流.md)。进入 04 章之前，先确认 03a 的 9.8 勾选项里与你当前进度对应的那些已经通过。



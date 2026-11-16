// Package article 文章模块：发布、分页列表、详情（带缓存）、删除。
// 这一层是业务逻辑，只认识 model 和 domain 错误，不认识 gin 和 HTTP 状态码。
// 详情接口的缓存策略是本模块最复杂的部分：读缓存 → 合并并发回源 → 回填 → 失败降级，
// 相关常量与计数器在 cache.go。
package article

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"forum-backend/internal/cache"
	"forum-backend/internal/domain"
	"forum-backend/internal/keys"
	"forum-backend/internal/metrics"
	"forum-backend/internal/model"
	"forum-backend/internal/paging"
	"forum-backend/internal/textutil"
)

// 固定排序：排序片段写成常量，禁止把 c.Query("sort") 拼进 Order()。
// 用户输入直接进 SQL 就是注入口，排序字段尤其容易漏，因为它的值没法用占位符代替。
const articleListOrder = "created_at DESC, id DESC"

// maxTitleRunes 是标题的字符上限，与 articles.title 的 varchar(128) 对齐。
// varchar 在 MySQL 里按字符计，所以判断必须用字符而不是字节。
const maxTitleRunes = 128

// maxContentBytes 是正文的字节上限，与 articles.content 的 text 列对齐。
// text 的上限是 65535 字节，取 50000 留出余量；按字节判是因为
// 列本身按字节计，按字符放行会让 50000 个汉字（15 万字节）撞上列上限。
const maxContentBytes = 50000

// maxKeywordRunes 是搜索关键词的字符上限。
//
// 搜索走 LIKE '%kw%'，用不上索引，关键词越长扫得越慢；
// 50 个字足够表达一次真实的搜索意图，再长的输入基本是误粘贴。
// 上限放在 handler 判 400，让用户知道是参数问题，而不是悄悄截断后返回奇怪结果。
const maxKeywordRunes = 50

// articleListColumns 是列表查询要取的列：不取 content 全文，只截开头当摘要。
// 原因是一页 10 篇、单篇正文上限 50000 字节，取全文等于每次往内存搬几百 KB，
// 而列表只需要一小段开头。LEFT() 在 MySQL 里按字符计数，中文不会被截成半个。
// 上限取 dto.go 的 articleExcerptSourceLimit，两边共用一个数字，改一处就够。
var articleListColumns = fmt.Sprintf(
	"id, user_id, title, LEFT(content, %d) AS excerpt, view_count, created_at",
	articleExcerptSourceLimit,
)

// ArticleService 文章业务逻辑。
//
// 三个依赖各有分工：
//   - db：MySQL，真正的数据源
//   - rdb：Redis，只放详情缓存，挂了也要能降级跑
//   - group：把「同一个 key 的并发回源」合并成一次，只在当前进程内生效
//   - tagger：没填标签时让大模型起一个，可为 nil（见 NewArticleService）
//
// 缓存计数不在结构体里：统一记到 metrics 包的共享计数器上，
// 这样 /metrics 与 CacheStats() 用的是同一份数字。
type ArticleService struct {
	db     *gorm.DB
	rdb    *redis.Client
	group  singleflight.Group
	tagger TagSuggester
}

// NewArticleService 由 router 在装配期调用一次。group 是零值可用类型，不用初始化。
//
// tagger 为 nil 表示不做自动标签（配置里没开大模型，或者没有密钥）。
// 做成接口而不是直接依赖 llm.Client：service 的测试里塞一个假实现就能验证
// 「没填标签会补一个」「模型失败不影响发布」，不用起 HTTP 服务。
func NewArticleService(db *gorm.DB, rdb *redis.Client, tagger TagSuggester) *ArticleService {
	return &ArticleService{db: db, rdb: rdb, tagger: tagger}
}

// Create 发布一篇文章。作者身份由中间件确认后传入，客户端无法指定。
// tagNames 是客户端给的标签名，清洗与数量校验都在 normalizeTags 里；
// 一个都没填时只往 Stream 投一条补标签任务就返回，模型由后台 worker 慢慢调，
// 不压发布这条链路的延迟。
// 返回的错误只有三种：domain.ErrInvalidInput（标题、正文或标签不合法）、
// domain.ErrDuplicateContent（十分钟内发过一模一样的内容）、以及包装过的数据库错误。
func (s *ArticleService) Create(ctx context.Context, userID uint, title, content string, tagNames []string) (*model.Article, error) {
	// 先去掉首尾空白，否则一串空格也能通过「非空」校验。
	title = strings.TrimSpace(title)
	content = strings.TrimSpace(content)

	if err := validateArticleInput(title, content); err != nil {
		return nil, err
	}
	tags, err := normalizeTags(tagNames)
	if err != nil {
		return nil, err
	}

	// 先用内容摘要占位，再写库。顺序不能反：
	// 如果先写库再占位，两个并发请求会双双通过判重，库里就多出一篇一模一样的文章。
	// 占位失败要区分错误类型——Redis 故障时放行（判重是防御性检查，不是业务前提），
	// 只有「key 已存在」才是真的重复。
	dupKey := keys.ArticleDupKey(userID, contentDigest(title, content))
	reserved, dupErr := s.rdb.SetNX(ctx, dupKey, "1", articleDupWindow).Result()
	if dupErr != nil {
		slog.WarnContext(ctx, "reserve duplicate-check key", "user_id", userID, "error", dupErr)
	} else if !reserved {
		return nil, fmt.Errorf("create article for user %d: %w", userID, domain.ErrDuplicateContent)
	}

	a := &model.Article{
		UserID:  userID,
		Title:   title,
		Content: content,
	}

	// 文章与标签关联必须一起成功：只写进文章、标签丢了，
	// 用户会以为是自己没填，重新编辑一次才发现问题。
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(a).Error; err != nil {
			return fmt.Errorf("create article for user %d: %w", userID, err)
		}
		// a.ID 由上面的 INSERT 回填，关联表要用它。
		return replaceArticleTags(tx, a.ID, tags)
	})
	if err != nil {
		// 写库失败就把占位还回去。不还的话，用户修好问题再发一次，
		// 会被自己刚才那次失败拦住整整 10 分钟，而且提示是「刚发过一样的内容」，
		// 完全对不上他实际遇到的情况。
		if dupErr == nil {
			if delErr := s.rdb.Del(ctx, dupKey).Err(); delErr != nil {
				slog.WarnContext(ctx, "release duplicate-check key", "user_id", userID, "error", delErr)
			}
		}
		return nil, err
	}

	// 回填给调用方：发布接口会把这篇的完整形状回给前端，带上标签可以直接渲染。
	a.Tags = tags

	// 一个标签都没填就丢一条任务给后台 worker，由它慢慢调模型补上。
	// 这里不等待：模型最慢要 8 秒，而作者正盯着「发布」按钮；
	// 标签晚几秒出现没人受损，发布多等一秒都可能让他重复提交。
	// 编辑接口（Update）永远不补：把标签删光是明确意图，补回来等于删不掉。
	if len(tags) == 0 && s.tagger != nil {
		s.enqueueTagJob(ctx, a.ID)
	}

	// id 是写库后由 MySQL 自增生成的，理论上这个 id 不会有旧缓存。
	// 仍然删一次，是因为 MySQL 重启后自增计数按表内最大 id 重建，
	// 删掉过最大 id 的情况下新行可能复用到旧 id；一次 DEL 的成本远低于读到脏缓存的代价。
	s.InvalidateDetailCache(ctx, a.ID)
	return a, nil
}

// validateArticleInput 校验标题与正文，Create 与 Update 共用。
//
// 两处各写一份必然漏改一处，以后调上限就会出现「发得出去、改不进去」这种怪现象。
// 调用方负责先 trim：空标题与「一串空格」要在这里被判成同一件事。
func validateArticleInput(title, content string) error {
	if title == "" || content == "" {
		return fmt.Errorf("%w: title and content are required", domain.ErrInvalidInput)
	}
	// 标题按字符判：articles.title 是 varchar(128)，MySQL 按字符计，
	// 用 len() 会把 43 个汉字误判成超长。
	// 正文按字节判：articles.content 是 text，容量上限就是 65535 字节，
	// 50000 字节留出余量，同时挡住「把整个文件贴进来」这类请求。
	if textutil.TooLongRunes(title, maxTitleRunes) || textutil.TooLongBytes(content, maxContentBytes) {
		return fmt.Errorf("%w: title or content too long", domain.ErrInvalidInput)
	}
	return nil
}

// Update 编辑文章，只允许作者本人操作，返回编辑后的完整文章（含点赞数与标签）。
//
// 权限判断和写入放在同一条 UPDATE 里，由数据库一次完成「是不是作者」与「改内容」，
// 避免「先查归属再更新」两步之间的竞态（查的时候还是作者，改的时候文章已被转手）。
//
// 标签是整体替换：客户端送来的是「这篇应该有哪些标签」，不是增量。
func (s *ArticleService) Update(ctx context.Context, id, userID uint, title, content string, tagNames []string) (*model.Article, error) {
	title = strings.TrimSpace(title)
	content = strings.TrimSpace(content)

	if err := validateArticleInput(title, content); err != nil {
		return nil, err
	}
	tags, err := normalizeTags(tagNames)
	if err != nil {
		return nil, err
	}

	// 文章字段与标签关联要么一起改，要么都不改：
	// 只改了正文而标签还是旧的，用户看到的是一份半成品。
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 用 map 而不是结构体更新：GORM 用结构体时会跳过零值字段，
		// 以后要是允许把正文改成空串，结构体更新会静默不生效。
		res := tx.Model(&model.Article{}).
			Where("id = ? AND user_id = ?", id, userID).
			Updates(map[string]any{"title": title, "content": content})
		if res.Error != nil {
			return fmt.Errorf("update article %d: %w", id, res.Error)
		}

		// 影响行数为 0 只有两种可能：文章不存在，或者存在但不是这个人的。
		// 两者都回 404，不泄露「这篇文章属于谁」，与 Delete 的口径一致。
		//
		// 注意「内容一个字都没改」不会走到这里：GORM 的 Updates 会顺带更新 updated_at，
		// MySQL 按真正改变的行计数，那一行仍然算改过（TestUpdateSameContentSucceeds 守着这一点）。
		if res.RowsAffected == 0 {
			return fmt.Errorf("article %d: %w", id, domain.ErrNotFound)
		}
		return replaceArticleTags(tx, id, tags)
	})
	if err != nil {
		return nil, err
	}

	// 内容变了就必须清详情缓存，顺序不能反：先删缓存再写库，中间有窗口会让
	// 别的请求把旧内容重新回填进去。删除失败只记日志，TTL 是最后一道兜底。
	s.InvalidateDetailCache(ctx, id)

	// 重新走一次 Detail 而不是拿内存里的 title/content 拼返回值：
	// 客户端要的是库里的真值（created_at、作者、点赞数都在库里），
	// 缓存刚被删掉，这一次 Detail 必然回源，不会读到旧值。
	return s.Detail(ctx, id)
}

// List 返回一页文章和总数，page 从 1 开始。
//
// authorID 大于 0 时只看这个人的文章（用户主页的标签页），0 表示全站。
// keyword 模糊匹配标题与正文；tagName 非空时只留挂了该标签的文章。三者可以叠加。
// 这里做了两次查询：先 Count 总数（前端要算总页数），再按 Offset 取当前页。
// 数据量大时 Count 会成为瓶颈，那时该换成游标分页或用缓存存总数，现在不值得。
//
// 取回的 article.Content 是空的（列表查询不取全文），正文开头被塞进 Excerpt。
// 想拿全文必须走 Detail，别在列表结果上读 Content。
func (s *ArticleService) List(ctx context.Context, authorID uint, keyword, tagName string, page, pageSize int) ([]*model.Article, int64, error) {
	// handler 层已经用 httputil.ParsePage 校验过一遍，这里再校验是为了保住 service 的边界：
	// 别的地方（比如以后的后台任务、命令行工具）直接调 service 时也不会传进非法值。
	// 规则统一走 paging.New，handler 与 service 共用一份上界，不会各写一个数字而漂移。
	q, err := paging.New(page, pageSize)
	if err != nil {
		return nil, 0, err
	}

	// 关键词在这里再 trim 一次：handler 会 trim 并判长度，但 service 也可能被
	// 测试或后台脚本直接调用，一串空格不该变成「搜所有含空格的正文」。
	keyword = strings.TrimSpace(keyword)
	tagName = strings.TrimSpace(tagName)

	// 作者、关键词、标签三个条件必须同时作用于 Count 与 Find。
	// 只给 Find 加，总数仍是全站的，前端按 ceil(total/pageSize) 算页数会多出好几页空页；
	// 只给 Count 加，则列表里会混进别人的文章、或混进不含关键词的文章。
	// 写成闭包是为了让两处共用同一份条件。
	scope := func(tx *gorm.DB) *gorm.DB {
		if authorID > 0 {
			tx = tx.Where("user_id = ?", authorID)
		}
		if keyword != "" {
			// 标题和正文都要搜：用户记得往往是正文里的某个词，
			// 只搜标题会让人以为「这篇明明有却搜不到」。
			// 括号是显式写出来的：当前 GORM 会自动补（实测生成的 SQL 就是
			// user_id = ? AND (title LIKE ? OR content LIKE ?)），
			// 但这是框架的隐式行为，换版本或以后改成拼接原生 SQL 时，
			// 少一对括号就会变成 (user_id = ? AND title LIKE ?) OR content LIKE ?，
			// 别人的文章只要正文命中就会混进来。
			pattern := likePattern(keyword)
			tx = tx.Where("(title LIKE ? OR content LIKE ?)", pattern, pattern)
		}
		if tagName != "" {
			// 用子查询而不是 JOIN：Count 与 Find 共用这个 scope，
			// JOIN 会让 Count 按关联行数翻倍（一篇文章挂三个标签就数成三篇）。
			// 子查询只取 article_id，外层仍然是 articles 表的行数。
			sub := s.db.WithContext(ctx).Model(&model.ArticleTag{}).
				Select("article_tags.article_id").
				Joins("JOIN tags ON tags.id = article_tags.tag_id").
				Where("tags.name = ?", tagName)
			tx = tx.Where("id IN (?)", sub)
		}
		return tx
	}

	var total int64
	if err := scope(s.db.WithContext(ctx).Model(&model.Article{})).Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count articles author=%d keyword=%q: %w", authorID, keyword, err)
	}

	// 总数为 0 时直接返回空切片，省掉一次没有意义的查询。
	// 注意返回的是空切片而不是 nil：nil 会被 JSON 序列化成 null，
	// 前端拿到的 data.list 就不是数组了，遍历会报错。
	if total == 0 {
		return []*model.Article{}, 0, nil
	}

	articles := make([]*model.Article, 0)
	// Preload("User", preloadAuthor) 会额外发一条 SELECT 把作者信息一起查出来，
	// 否则序列化时 a.User 是空结构体，列表里会看不到作者名字。
	// 第二个参数限定只取展示用的四列，别把密码哈希读进来（见 preloadAuthor）。
	// Select 只取列表需要的列，正文只截开头（见 articleListColumns）。
	// Offset 跳过前面几页，Limit 取当前页大小；排序由常量固定，不接收用户输入。
	err = scope(s.db.WithContext(ctx)).
		Select(articleListColumns).
		Preload("User", preloadAuthor).
		Order(articleListOrder).
		Offset(q.Offset()).
		Limit(q.PageSize).
		Find(&articles).Error
	if err != nil {
		return nil, total, fmt.Errorf("list articles author=%d keyword=%q page=%d size=%d: %w", authorID, keyword, page, pageSize, err)
	}

	// 标签单独一次 JOIN 查回来再挂到每篇上，不放进上面那条查询：
	// 一篇多个标签会让主查询出现重复行，还得在 Go 里再去重一次。
	// 列表页没有标签就渲染不出筛选入口，所以这里失败直接报错，不做「降级成空标签」。
	if err := s.fillTags(ctx, articles); err != nil {
		return nil, total, err
	}
	return articles, total, nil
}

// likePattern 把用户输入包成 LIKE 的「包含」模式，并转义 LIKE 的三个元字符。
//
// 不转义的话，搜一个 "%" 会匹配全部文章、搜 "_" 会匹配任意单字，
// 用户看到的结果与输入毫无关系，还会被当成「搜索坏了」。
// 反斜杠必须排在第一个替换：先替换 % 的话，转义产生的 \% 里的反斜杠
// 会在下一步被再转一次，变成 \\%，匹配的就不是字面量百分号了。
//
// LIKE 的默认转义字符是反斜杠，MySQL 在两种 sql_mode 下都这么认，
// 所以这里不需要额外写 ESCAPE 子句。
func likePattern(keyword string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + replacer.Replace(keyword) + "%"
}

// preloadAuthor 限定 Preload("User") 只取展示作者需要的列。
//
// 不传参数的 Preload 会发 SELECT *，把 bcrypt 密码哈希一起读进内存。
// DTO 层因为不序列化 Password 挡住了外泄，但详情缓存是
// json.Marshal(model.Article)，哈希会跟着文章一起写进 Redis。
// 缓存里的东西一旦被 dump 或有人手工 GET 一下，拿到的就是可离线爆破的哈希，
// 所以在查询阶段就只取这四列。
func preloadAuthor(db *gorm.DB) *gorm.DB {
	return db.Select("id", "username", "nickname", "avatar_url")
}

// Delete 删除一篇文章。只允许作者本人，删的是「标记」不是数据。
//
// 软删除之后这里不再有事务，也不再清理 comments / article_likes / article_tags：
// 外键的 RESTRICT 只在真正 DELETE 一行时才会触发，而 GORM 现在生成的是
// UPDATE articles SET deleted_at = ?，子表根本不受影响。子表数据原样留着，
// 以后要「恢复误删」时评论和标签都还在，这正是不做硬删的收益。
//
// 仍然要清三类东西：详情缓存（否则还能读到已删内容）、点赞集合（避免幽灵计数）、
// 热榜成员（否则榜单里会出现点进去 404 的条目）。
func (s *ArticleService) Delete(ctx context.Context, id, userID uint) error {
	// 把所有权也放进 WHERE，让数据库一次判断「是不是作者」并删除，
	// 避免「先查是不是作者、再删」这两步之间文章被别人改动的竞态。
	// GORM 的软删除会自动追加 deleted_at IS NULL，所以重复删同一篇会命中 0 行。
	res := s.db.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).Delete(&model.Article{})
	if res.Error != nil {
		return fmt.Errorf("delete article %d: %w", id, res.Error)
	}
	// 影响行数为 0 有三种可能：文章不存在、已经被删过、或者不是这个人的。
	// 对外统一 404，既不泄露归属，也让重复删除得到确定的结果。
	if res.RowsAffected == 0 {
		return fmt.Errorf("article %d: %w", id, domain.ErrNotFound)
	}

	// 缓存必须在写库成功之后再删。写在前面的话，一旦更新失败，
	// 数据库里文章还在，缓存却被删了（虽然只是多一次回源，但逻辑上是错的）。
	s.InvalidateDetailCache(ctx, id)

	// 还要清掉 like 与 hot 模块维护的两个 Redis 投影，否则点赞数和热榜里会留下幽灵条目。
	// 这两个 key 由别的模块拥有，拼法统一收在 keys 包，这里只引用不自己拼。
	// 清理失败只记日志：文章已经删了，不能让缓存清理失败把接口变成 500。
	if err := s.rdb.Del(ctx, keys.LikeSetKey(id)).Err(); err != nil {
		slog.ErrorContext(ctx, "drop like set", "article_id", id, "error", err)
	}
	if err := s.rdb.ZRem(ctx, keys.HotArticlesKey, strconv.FormatUint(uint64(id), 10)).Err(); err != nil {
		slog.ErrorContext(ctx, "drop hot member", "article_id", id, "error", err)
	}
	return nil
}

// Detail 读一篇文章详情，缓存优先。
//
// 完整路径分五步：
//  1. 先无锁读一次缓存，命中就返回（绝大多数请求走到这里）
//  2. 未命中才进 singleflight 排队，同一个 id 只放一个请求去查库
//  3. 进门后二次读缓存，防止等排队期间别人已经回填好了
//  4. 查库成功后回填缓存；查到「确实不存在」写一个短 TTL 的空标记防穿透
//  5. 结果广播给所有等待者
func (s *ArticleService) Detail(ctx context.Context, id uint) (*model.Article, error) {
	key := articleDetailKey(id)

	// 第一遍读缓存：把整个 Detail 都塞进 Do 的话，命中缓存的请求也要排队等锁，反而更慢。
	a, handled, err := s.readArticleCache(ctx, key)
	if err != nil {
		return nil, err
	}
	if handled {
		return a, nil
	}

	// 未命中才进合并。第一个请求执行闭包，其余同 key 的请求阻塞等它的结果，
	// 拿到的是同一份返回值，不会各自打一次数据库。
	v, err, shared := s.group.Do(key, func() (any, error) {
		// 为什么要脱离调用方的 ctx：闭包捕获的是第一个请求者的 ctx，
		// 它一旦取消（用户关页面、客户端超时），所有等待者都会一起收到 context.Canceled。
		// WithoutCancel 去掉取消信号但保留 ctx 里的值，再套 3 秒超时，
		// 防止这次回源无限期跑下去。
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()

		// 双重检查：等待者被唤醒时，第一个请求可能已经把结果写进缓存了。
		// 少了这一遍，稍晚到达的请求会各自再回源一次，合并就白做了。
		cached, handled, cacheErr := s.readArticleCache(loadCtx, key)
		if cacheErr != nil {
			return nil, cacheErr
		}
		if handled {
			return cached, nil
		}

		// 到这里才真正查库。
		metrics.ArticleCacheEvents.WithLabelValues(cacheEventLoad).Inc()
		loaded, err := s.queryDetail(loadCtx, id)
		if err != nil {
			// 只有「确实不存在」才能写空标记。数据库故障绝不能写成空标记，
			// 否则故障期间查过的 id 会在恢复后继续错 60 秒（把故障固化成了「没有这篇」）。
			if errors.Is(err, domain.ErrNotFound) {
				s.markArticleAbsent(loadCtx, key)
			}
			return nil, err
		}

		// 回填失败只记日志，不能把这次成功的数据库查询变成接口失败。
		s.writeArticleCache(loadCtx, key, loaded)
		return loaded, nil
	})

	if shared {
		// shared 只表示结果被多个请求领取，不代表成功，所以只是计数，不影响返回。
		metrics.ArticleCacheEvents.WithLabelValues(cacheEventShare).Inc()
	}
	if err != nil {
		return nil, err
	}

	// 闭包返回的是 any。这里用带检查的类型断言，
	// 万一以后改了闭包的返回类型，会得到明确错误而不是 panic。
	article, ok := v.(*model.Article)
	if !ok {
		return nil, fmt.Errorf("cache loader returned unexpected type %T", v)
	}
	return article, nil
}

// RecordView 给一篇文章的浏览数 +1，同一访问者 30 分钟内只算一次。
//
// 去重靠 Redis 的 SETNX：每个「访问者 + 文章」在窗口内只留一个标记，
// 抢到标记的那个请求才去写库。不用「先 GET 再 SET」，那两步之间是竞态，
// 双击打开两个标签页会同时判定「没看过」，把一次阅读记成两次。
//
// viewer 由 handler 拼好传进来（登录用户是 u:<id>，游客是 ip:<地址>），
// service 不认识 gin，也不该自己去看 HTTP 头。
//
// 写库用一条 UPDATE view_count = view_count + 1，交给数据库做原子自增：
// 先 SELECT 再写 +1 的写法在两个并发请求下会读到同一个旧值，最后少算一次。
//
// 调用方必须在确认文章存在之后再调用：否则刷不存在 id 的请求
// 会在 Redis 里留下一堆 30 分钟才过期的去重标记，等于把缓存穿透换个地方重现。
func (s *ArticleService) RecordView(ctx context.Context, id uint, viewer string) error {
	key := keys.ArticleViewKey(id, viewer)

	dedupCtx, cancel := cache.WithTimeout(ctx)
	fresh, err := s.rdb.SetNX(dedupCtx, key, "1", viewDedupTTL).Result()
	cancel()

	// Redis 出错时降级成「这次算数」：浏览数是低价值数据，
	// 宁可 Redis 抖动期间多记几次，也不要一条都不记，让热榜彻底失去依据。
	// 降级方向和详情缓存相反——那边失败要让请求继续回源，这边失败不能丢数据。
	if err != nil {
		slog.WarnContext(ctx, "dedup article view", "article_id", id, "viewer", viewer, "error", err)
		fresh = true
	}
	if !fresh {
		return nil
	}

	// UpdateColumn 而不是 Update：后者会顺带更新 updated_at，
	// 于是每被看一次，文章的「更新时间」就跳到当前，按更新时间排序的地方全会乱。
	// 浏览数不是内容变更，不该留下编辑痕迹。
	res := s.db.WithContext(ctx).Model(&model.Article{}).
		Where("id = ?", id).
		UpdateColumn("view_count", gorm.Expr("view_count + 1"))
	if res.Error != nil {
		return fmt.Errorf("record view for article %d: %w", id, res.Error)
	}
	// 影响行数为 0 说明文章在这一瞬间被删了。不是错误，只是这次浏览没有落点。
	return nil
}

// ViewCount 读一篇文章当前的浏览数，供详情接口展示。
//
// 为什么不直接用详情缓存里的 view_count：那个值是回填缓存那一刻的快照，
// 缓存 TTL 是 10~12 分钟，期间浏览数一直在涨。直接展示会越看越旧，
// 更糟的是同一读者刷新两次，第二次命中缓存反而看到比第一次小的数字。
//
// 代价是详情接口多一次主键单列查询。这一步比回源查正文轻得多
// （不走 JOIN、不 COUNT 点赞、不搬 content），换来的是数字准确且单调不减。
// 真到了扛不住的量级，再给这一列单开一层 Redis 计数器，而不是把旧值塞回去。
func (s *ArticleService) ViewCount(ctx context.Context, id uint) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&model.Article{}).
		Where("id = ?", id).
		Pluck("view_count", &n).Error
	if err != nil {
		return 0, fmt.Errorf("read view count for article %d: %w", id, err)
	}
	return n, nil
}

// readArticleCache 读一次详情缓存，并判断这次请求还需不需要回源。
// handled 为 true 表示「已经有结论」：要么是文章，要么是确定的不存在。
// Redis 故障、key 不存在、缓存内容损坏都返回 handled=false，让调用方回源；
// 它们不该让接口直接失败，所以不计入 error。
//
// 这里是详情页最热的一步，也是 Redis 卡住时最容易被拖慢的一步，
// 所以每一步 Redis 操作都套上 cache 包统一的短超时，不沿用客户端的 500ms。
func (s *ArticleService) readArticleCache(ctx context.Context, key string) (*model.Article, bool, error) {
	readCtx, cancel := cache.WithTimeout(ctx)
	val, err := s.rdb.Get(readCtx, key).Result()
	cancel()

	if errors.Is(err, redis.Nil) {
		// redis.Nil 是「key 不存在」的正常未命中，不是故障，别当错误往上抛。
		metrics.ArticleCacheEvents.WithLabelValues(cacheEventMiss).Inc()
		return nil, false, nil
	}
	if err != nil {
		// 断连、超时等：降级回源，同时留下日志与计数证据，别静默吃掉。
		metrics.ArticleCacheEvents.WithLabelValues(cacheEventMiss).Inc()
		metrics.ArticleCacheEvents.WithLabelValues(cacheEventDegrade).Inc()
		slog.ErrorContext(ctx, "read article cache", "key", key, "error", err)
		return nil, false, nil
	}

	// 先比空标记，再谈反序列化。顺序不能反：
	// 哨兵不是合法 JSON，交给 Unmarshal 只会得到一次没必要的解析错误。
	if val == articleEmptySentinel {
		metrics.ArticleCacheEvents.WithLabelValues(cacheEventEmptyHit).Inc()
		return nil, true, domain.ErrNotFound
	}

	var a model.Article
	if err := json.Unmarshal([]byte(val), &a); err != nil {
		// 坏缓存自愈：删掉它，否则每次请求都会重复读到坏值、重复报错。
		metrics.ArticleCacheEvents.WithLabelValues(cacheEventDegrade).Inc()
		slog.ErrorContext(ctx, "invalid article cache", "key", key, "error", err)
		delCtx, delCancel := cache.WithTimeout(ctx)
		_ = s.rdb.Del(delCtx, key).Err()
		delCancel()
		return nil, false, nil
	}

	metrics.ArticleCacheEvents.WithLabelValues(cacheEventHit).Inc()
	return &a, true, nil
}

// writeArticleCache 把查到的文章写回缓存。
// 回填失败不能改变本次数据库查询的结果，所以没有返回值，只记日志和计数器。
func (s *ArticleService) writeArticleCache(ctx context.Context, key string, a *model.Article) {
	data, err := json.Marshal(a)
	if err != nil {
		// model.Article 全是普通字段，正常不会失败；仍要显式处理，别吞掉。
		slog.ErrorContext(ctx, "marshal article cache", "key", key, "error", err)
		return
	}
	// 每次调用 articleDetailTTL() 都会重新取随机数，这是防雪崩的关键。
	// 回填同样套短超时：写失败只影响下次是否命中，不该拖慢这次响应。
	writeCtx, cancel := cache.WithTimeout(ctx)
	defer cancel()
	if err := s.rdb.Set(writeCtx, key, data, articleDetailTTL()).Err(); err != nil {
		metrics.ArticleCacheEvents.WithLabelValues(cacheEventDegrade).Inc()
		slog.ErrorContext(ctx, "write article cache", "key", key, "error", err)
		return
	}
	metrics.ArticleCacheEvents.WithLabelValues(cacheEventWrite).Inc()
}

// markArticleAbsent 写入「查无此文」的空标记，只给短 TTL。
// 调用前必须已经用 errors.Is(err, domain.ErrNotFound) 确认过原因，
// 否则会把数据库故障误记成「这篇文章不存在」。
func (s *ArticleService) markArticleAbsent(ctx context.Context, key string) {
	markCtx, cancel := cache.WithTimeout(ctx)
	defer cancel()
	if err := s.rdb.Set(markCtx, key, articleEmptySentinel, articleEmptyTTL).Err(); err != nil {
		metrics.ArticleCacheEvents.WithLabelValues(cacheEventDegrade).Inc()
		slog.ErrorContext(ctx, "write empty article cache", "key", key, "error", err)
	}
}

// queryDetail 只负责查库，并把 GORM 的「没查到」翻成项目内部的 domain.ErrNotFound。
// 在这一层转换，handler 的错误映射就不用认识 gorm.ErrRecordNotFound，
// 依赖方向保持 service 往外、handler 只认 domain 错误。
func (s *ArticleService) queryDetail(ctx context.Context, id uint) (*model.Article, error) {
	var a model.Article
	// 这里的查询结果会整体 json.Marshal 进 Redis，所以更要限定作者列：
	// 不限定的话，密码哈希会跟着缓存值一起落到 Redis 里。
	err := s.db.WithContext(ctx).Preload("User", preloadAuthor).First(&a, id).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("article %d: %w", id, domain.ErrNotFound)
		}
		return nil, fmt.Errorf("query article %d: %w", id, err)
	}

	// 详情要显示点赞数。articles.like_count 列始终不维护，
	// 这里从 article_likes 现算一次，装进内存字段随文章一起写进缓存，
	// 后续命中缓存的请求就不用再查了。
	var likeCount int64
	if err := s.db.WithContext(ctx).Model(&model.ArticleLike{}).
		Where("article_id = ?", id).Count(&likeCount).Error; err != nil {
		return nil, fmt.Errorf("count article likes %d: %w", id, err)
	}
	a.LikeCount = likeCount

	// 标签跟着文章一起进出缓存：命中缓存的请求不用再查一次关联表。
	// 代价是编辑标签之后必须删缓存，Update 里已经这么做了。
	if err := s.fillTags(ctx, []*model.Article{&a}); err != nil {
		return nil, err
	}
	return &a, nil
}

// InvalidateDetailCache 删除详情缓存，必须在数据库写入或事务提交成功之后调用。
//
// 导出是因为改动详情内容的写路径不止本文件：LikeService 点赞与取消成功后也要调用它。
// 空标记和真实文章用的是同一个 key，所以这一句同时也能清掉空标记。
// 失败只记录，不改变已提交的数据库结果；TTL 是最后一道兜底。
func (s *ArticleService) InvalidateDetailCache(ctx context.Context, id uint) {
	key := articleDetailKey(id)
	if err := s.rdb.Del(ctx, key).Err(); err != nil {
		metrics.ArticleCacheEvents.WithLabelValues(cacheEventDegrade).Inc()
		slog.ErrorContext(ctx, "invalidate article cache", "key", key, "error", err)
	}
}

// CacheStats 返回当前进程的缓存计数快照，供本地验收与将来的监控接入使用。
// 进程重启后计数归零，它不是持久化指标。
func (s *ArticleService) CacheStats() CacheStats {
	return snapshot()
}

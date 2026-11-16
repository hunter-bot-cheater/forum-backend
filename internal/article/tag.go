// 本文件收拢文章标签的读写：清洗、替换关联、批量回查、热门标签。
// 单独成文件是因为 service.go 已经在管发布、列表、缓存三条主线，
// 把标签规则塞进去会让「详情缓存为什么带标签」这类问题更难查。
package article

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"gorm.io/gorm"

	"forum-backend/internal/domain"
	"forum-backend/internal/model"
	"forum-backend/internal/textutil"
)

// maxTagsPerArticle 是一篇文章最多能挂几个标签。
//
// 标签用来归类，不是关键词堆砌：超过三个基本等于没分类。
// 上限同时是前端输入框的上限，两边用同一个数字，避免前端放行、后端 400。
const maxTagsPerArticle = 3

// maxTagRunes 是单个标签的字符上限，与 tags.name 的 varchar(32) 对齐。
// MySQL 的 varchar 按字符计，所以判断用 rune 而不是字节。
const maxTagRunes = 32

// tagsPerArticleLimit 是热门标签接口默认返回的条数。
// 标签云一屏能扫完的量就这么多，再多就不叫「云」了。
const topTagsLimit = 20

// TagCount 是一个标签与它下面的文章数，供热门标签接口使用。
type TagCount struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

// tagListResp 是热门标签接口 data 的形状。
// 包一层而不是直接回数组：以后要加「标签总数」之类的字段时不用改前端的解析路径，
// 也和其它列表接口（data 里都有 list）保持同一套形状。
type tagListResp struct {
	List []TagCount `json:"list"`
}

// TagSuggester 给一篇还没填标签的文章起一个标签。
//
// 由 internal/llm 的 Client 实现，但接口定义在这里：article 只声明「我需要什么」，
// 不关心是哪个供应商、怎么发 HTTP。装配期由 router 决定注入真实实现还是 nil。
type TagSuggester interface {
	// SuggestTag 返回一个候选标签名（未清洗），失败时返回 error。
	// 调用方负责清洗与兜底，实现方不要自己往库里写东西。
	SuggestTag(ctx context.Context, title, content string) (string, error)
}

// suggestTag 让大模型给一篇文章起一个标签，返回清洗后的标签名（最多一个）。
//
// 三个调用方共用这一份实现：后台补标签的 worker、写作页的草稿建议，
// 以及以前同步补标签的发布链路。清洗规则只写一处，
// 才不会出现「手填能过、模型给的不行」这种两套标准。
//
// 任何失败都返回 nil 而不是 error：标签是附加信息，模型超时、密钥过期、
// 返回了一句废话，都不该让用户发不出文章。这里只记一条 warn，
// 运维能从日志里发现「最近标签全是空的原来是密钥过期了」。
func (s *ArticleService) suggestTag(ctx context.Context, title, content string) []string {
	if s.tagger == nil {
		return nil
	}

	// 正文只送开头一段。模型判断主题看几百字就够，
	// 整篇发过去除了按 token 计费之外没有任何提升。
	title = strings.TrimSpace(title)
	content = textutil.TruncateRunes(strings.TrimSpace(content), tagSuggestContentRunes)
	// 标题和正文都空的时候没必要问模型：它只会随便编一个。
	if title == "" && content == "" {
		return nil
	}

	name, err := s.tagger.SuggestTag(ctx, title, content)
	if err != nil {
		slog.WarnContext(ctx, "suggest article tag failed", "title", title, "error", err)
		return nil
	}

	// 复用 normalizeTags 而不是直接把模型输出塞进库：
	// 模型同样会吐出超长、首尾带空白、甚至只有标点的内容，
	// 走同一套清洗与上限，落库的规则才和用户手填的完全一致。
	names, err := normalizeTags([]string{name})
	if err != nil || len(names) == 0 {
		slog.WarnContext(ctx, "suggested tag rejected", "tag", name, "error", err)
		return nil
	}
	return names
}

// normalizeTags 清洗客户端传来的标签：去空白、丢掉空项、按不区分大小写去重，再校验数量与长度。
//
// 去重必须在服务端做：前端可能因为大小写或首尾空格送出两个「Go」，
// 落库后就是两条关联指向同一个标签，列表里会出现同一个标签出现两次。
// 保留第一次出现的写法（"Go" 而不是后来的 "go"），让用户看到自己输入的形态。
func normalizeTags(names []string) ([]string, error) {
	result := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if textutil.TooLongRunes(name, maxTagRunes) {
			return nil, fmt.Errorf("%w: tag too long", domain.ErrInvalidInput)
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, name)
	}
	if len(result) > maxTagsPerArticle {
		return nil, fmt.Errorf("%w: too many tags", domain.ErrInvalidInput)
	}
	return result, nil
}

// replaceArticleTags 把某篇文章的标签整体换成 names，调用方负责提供事务。
//
// 先删旧关联再写新的：编辑时标签可能变少，逐条比对增删要处理三种情况，
// 而这段逻辑只在一篇文章的几条关联上跑，整体替换更简单也更不容易漏。
func replaceArticleTags(tx *gorm.DB, articleID uint, names []string) error {
	if err := tx.Where("article_id = ?", articleID).Delete(&model.ArticleTag{}).Error; err != nil {
		return fmt.Errorf("clear tags of article %d: %w", articleID, err)
	}
	if len(names) == 0 {
		return nil
	}

	tagIDs := make([]uint, 0, len(names))
	for _, name := range names {
		var tag model.Tag
		err := tx.Where("name = ?", name).FirstOrCreate(&tag, model.Tag{Name: name}).Error
		if err != nil {
			// 两个请求同时用新标签发帖时，后到的那个会撞 uk_tag_name。
			// 撞了说明对方刚插好，再按名字查一次就能拿到 id，不用把错误抛给用户。
			var existed model.Tag
			if requeryErr := tx.Where("name = ?", name).First(&existed).Error; requeryErr != nil {
				return fmt.Errorf("resolve tag %q: %w", name, err)
			}
			tag = existed
		}
		tagIDs = append(tagIDs, tag.ID)
	}

	rows := make([]model.ArticleTag, 0, len(tagIDs))
	for _, tagID := range tagIDs {
		rows = append(rows, model.ArticleTag{ArticleID: articleID, TagID: tagID})
	}
	if err := tx.Create(&rows).Error; err != nil {
		return fmt.Errorf("link tags to article %d: %w", articleID, err)
	}
	return nil
}

// tagsOf 一次查回一批文章的标签，返回 article_id → 标签名（按标签 id 升序，即创建顺序）。
//
// 列表一页最多 50 篇，逐篇查是 50 次往返；这里一条 JOIN 就够。
// 没有标签的文章不会出现在结果里，调用方按「空」处理。
func (s *ArticleService) tagsOf(ctx context.Context, articleIDs []uint) (map[uint][]string, error) {
	result := make(map[uint][]string, len(articleIDs))
	if len(articleIDs) == 0 {
		return result, nil
	}

	// 匿名结构体接 JOIN 的结果：这里只有两列，为它单独定义一个 model 反而多一层。
	type tagRow struct {
		ArticleID uint
		Name      string
	}
	rows := make([]tagRow, 0, len(articleIDs))
	err := s.db.WithContext(ctx).
		Table("article_tags").
		Select("article_tags.article_id, tags.name").
		Joins("JOIN tags ON tags.id = article_tags.tag_id").
		Where("article_tags.article_id IN ?", articleIDs).
		Order("article_tags.article_id ASC, tags.id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("load tags of articles %v: %w", articleIDs, err)
	}
	for _, row := range rows {
		result[row.ArticleID] = append(result[row.ArticleID], row.Name)
	}
	return result, nil
}

// fillTags 给一批文章补上各自的标签，空结果统一写成空切片。
//
// 统一成空切片是为了让 JSON 稳定输出 []：nil 会被序列化成 null，
// 前端就得在三种「没有标签」的形态之间判断。
func (s *ArticleService) fillTags(ctx context.Context, articles []*model.Article) error {
	if len(articles) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(articles))
	for _, a := range articles {
		ids = append(ids, a.ID)
	}
	byArticle, err := s.tagsOf(ctx, ids)
	if err != nil {
		return err
	}
	for _, a := range articles {
		tags := byArticle[a.ID]
		if tags == nil {
			tags = []string{}
		}
		a.Tags = tags
	}
	return nil
}

// TopTags 返回按文章数倒序的前若干个标签，供前端做标签云与筛选入口。
//
// 排序带上 tags.id 作为平手项：文章数一样的标签不能每次返回不同的顺序，
// 否则页面刷新一次顺序就变，用户会以为榜单在乱跳。
func (s *ArticleService) TopTags(ctx context.Context, limit int) ([]TagCount, error) {
	// limit 来自 handler 的固定值，这里只做一次下限兜底，防止以后有调用方传 0 或负数。
	if limit <= 0 {
		limit = topTagsLimit
	}

	rows := make([]TagCount, 0, limit)
	err := s.db.WithContext(ctx).
		Table("article_tags").
		Select("tags.name AS name, COUNT(*) AS count").
		Joins("JOIN tags ON tags.id = article_tags.tag_id").
		Group("tags.id, tags.name").
		Order("count DESC, tags.id ASC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("load top tags: %w", err)
	}
	return rows, nil
}

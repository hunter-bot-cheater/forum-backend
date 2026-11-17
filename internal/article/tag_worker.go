// 本文件是「自动补标签」的后台任务：发布接口只把任务丢进 Stream，由这里慢慢调模型。
//
// 为什么不在发布请求里同步调：
// 一次大模型调用 0.5~8 秒，而这个时间完全压在看「发布」按钮的作者身上。
// 模型慢的时候他会以为提交失败、再点一次，于是多出一篇文章。
// 标签是附加信息，晚几秒出现没有任何人受损；发布是核心动作，多等一秒都是伤害。
package article

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"forum-backend/internal/event"
	"forum-backend/internal/model"
)

const (
	// TagJobStream 是补标签任务的 Stream 名。带 v1 的理由同 keys 包：
	// 以后 payload 结构要改，换个名字就能让旧任务自然过期，不必写迁移。
	TagJobStream = "article:tags:v1"
	// TagJobGroup 是消费组名。通知、私信推送各用自己的组，
	// 同一组内多实例会分摊消息，不同组互不影响。
	TagJobGroup = "article-tag-workers"
)

// TagWorker 消费补标签任务：查文章 → 调模型 → 写关联 → 清缓存。
type TagWorker struct {
	svc *ArticleService
	rdb *redis.Client
}

// NewTagWorker 由 main 在装配期创建一次。只依赖 article 服务：
// 数据库、模型客户端都已经在它身上，这里不重复持有。
func NewTagWorker(svc *ArticleService, rdb *redis.Client) *TagWorker {
	return &TagWorker{svc: svc, rdb: rdb}
}

// Run 启动消费循环，ctx 取消后返回。由 main 放在独立 goroutine 里跑。
//
// FromStart 用 true：这个任务的性质是「把没标签的文章补上」，
// 进程重启后应该把积压的历史任务补处理一遍，而不是只接新消息。
// 重复处理是安全的——handle 里会先看这篇文章现在有没有标签。
func (w *TagWorker) Run(ctx context.Context) {
	consumer := &event.Consumer{
		RDB:       w.rdb,
		Stream:    TagJobStream,
		Group:     TagJobGroup,
		Name:      "tag-worker",
		Handler:   w.handle,
		FromStart: true,
		LogTag:    "article-tag",
	}
	consumer.Run(ctx)
}

// handle 处理一条任务：article id 在消息的 id 字段里。
//
// 返回 nil 表示这条消息可以 ACK 了。两种情况返回 nil 但什么也不做：
//   - 文章已经不存在（或已软删）：任务没有意义，重试只会一直失败；
//   - 文章已经有标签：说明用户在异步窗口里自己补了，或者这条是重复投递。
//
// 只有真正的临时故障（查库报错、写库报错）才返回 error，让它留在 PEL 里下一轮重试。
func (w *TagWorker) handle(ctx context.Context, msg redis.XMessage) error {
	raw, ok := msg.Values["id"]
	if !ok {
		// 结构不对的消息留在 PEL 里只会每轮失败一次，直接丢弃。
		slog.WarnContext(ctx, "tag job without article id", "message_id", msg.ID)
		return nil
	}
	// 解析失败时 ParseUint 返回 0，和「消息里真的写了 0」一起被下面这条挡掉。
	// 这类消息留在 PEL 里只会每轮失败一次，记录一条就丢弃。
	id, _ := strconv.ParseUint(fmt.Sprint(raw), 10, 64)
	if id == 0 {
		slog.WarnContext(ctx, "tag job with bad article id", "message_id", msg.ID, "raw", raw)
		return nil
	}
	articleID := uint(id)

	// 查文章。软删的文章被 GORM 的默认作用域过滤掉，First 会返回 ErrRecordNotFound。
	var a model.Article
	if err := w.svc.db.WithContext(ctx).First(&a, articleID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return fmt.Errorf("load article %d for tag job: %w", articleID, err)
	}

	// 已经有标签就跳过。这一条是「重复投递会加倍消耗模型额度」的闸门：
	// Consumer 从 PEL 重放、多实例分摊、进程重启都会让同一条任务被处理多次。
	existing, err := w.svc.tagsOf(ctx, []uint{articleID})
	if err != nil {
		return err
	}
	if len(existing[articleID]) > 0 {
		return nil
	}

	tags := w.svc.suggestTag(ctx, a.Title, a.Content)
	if len(tags) == 0 {
		// 模型失败或返回了不可用的内容。这是「尽力而为」，不再重试：
		// 重试会一直撞同一个问题（密钥过期、额度耗尽），把 PEL 撑大而没有任何收益。
		return nil
	}

	// written 记录这次事务有没有真的写进标签。
	// 事务内二次确认发现作者已经自己填了标签时会直接返回，那时不该清缓存，
	// 也不该打「已保存」的日志——否则日志里会出现一堆没写过的「成功」。
	written := false
	if err := w.svc.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 事务内再确认一次标签表还是空的。外面那次检查在模型调用之前，
		// 而模型最慢要 8 秒，这 8 秒里作者完全可能在编辑页自己补了标签。
		// 只在为空时写，才不会用模型的猜测覆盖掉用户亲手填的分类。
		var count int64
		if err := tx.Model(&model.ArticleTag{}).Where("article_id = ?", articleID).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
		if err := replaceArticleTags(tx, articleID, tags); err != nil {
			return err
		}
		written = true
		return nil
	}); err != nil {
		return fmt.Errorf("save suggested tags of article %d: %w", articleID, err)
	}
	if !written {
		// 作者在这几秒里自己补了标签，模型的建议作废。
		// 这是正常的分支（不是错误），只记一条 info 方便解释「为什么这篇没有自动标签」。
		slog.InfoContext(ctx, "tag job skipped: article already has tags", "article_id", articleID)
		return nil
	}

	// 必须清详情缓存：异步补标签发生在文章发布之后，
	// 发布那一刻可能已经有请求把「没有标签」的版本缓存了 10 分钟。
	w.svc.InvalidateDetailCache(ctx, articleID)
	slog.InfoContext(ctx, "suggested tags saved", "article_id", articleID, "tags", tags)
	return nil
}

// enqueueTagJob 把补标签任务投进 Stream。
//
// 失败只记日志，不让发布失败：标签补不上是「文章少一个标签」，
// 而抛出错误会让作者以为文章没发出去，重复提交的代价更大。
func (s *ArticleService) enqueueTagJob(ctx context.Context, articleID uint) {
	_, err := s.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: TagJobStream,
		// 只留最近 1000 条。Stream 不会自己回收已 ACK 的消息，
		// 不设上限的话每发一篇无标签文章就留一条，一年后是几十万条常驻内存。
		// 1000 条远大于正常积压（worker 每秒能处理几十条），
		// Approx 允许 Redis 按整节点裁剪，比精确 MAXLEN 便宜。
		MaxLen: 1000,
		Approx: true,
		Values: map[string]any{"id": articleID},
	}).Result()
	if err != nil {
		slog.WarnContext(ctx, "enqueue tag job", "article_id", articleID, "error", err)
	}
}

// 本文件是「写作页标签建议」：用户在写的时候先算一次，发布时就不用等模型。
//
// 它同时解决两个问题：
//  1. 冷启动：模型第一次调用往往比后续慢（连接建立、服务端排队），
//     在写作页提前打一次，真正需要结果时链路已经热了；
//  2. 用户可改：建议直接显示在标签框旁边，作者觉得不对就不采用，
//     比「发布后偷偷补一个」更透明。
//
// 发布时没填也没采用建议的话，仍由 TagWorker 兜底（见 tag_worker.go）。
package article

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"forum-backend/internal/domain"
	"forum-backend/internal/keys"
)

const (
	// tagSuggestPerHour 是单个用户每小时的建议次数上限。
	//
	// 这个接口每次都会真的调一次大模型（要花钱），而它是登录就能调的。
	// 正常写作一小时开几次编辑器，20 次远远够用；脚本刷的话会立刻撞上限。
	tagSuggestPerHour = 20
	// tagSuggestWindow 是配额窗口。一小时与「写作一次会话」的量级接近，
	// 跨窗口补满，不会出现「昨天用完了今天还用不了」。
	tagSuggestWindow = time.Hour
	// tagSuggestContentRunes 是送给模型的正文上限。
	// 模型判断主题只看开头几百字就够，整篇发过去除了费额度没有任何提升。
	tagSuggestContentRunes = 1200
)

// SuggestTagForDraft 给还没发布的内容提一个标签，供写作页展示。
//
// 返回值三种情况：
//   - 有标签：模型给出了可用结果；
//   - 空串 + nil：模型不可用、超时或吐了不可用的内容——前端不显示建议即可，
//     这不是用户需要处理的错误，不该弹提示；
//   - error：只有「配额用完」或「Redis 读写失败」这类需要告诉用户的情况。
func (s *ArticleService) SuggestTagForDraft(ctx context.Context, userID uint, title, content string) (string, error) {
	if s.tagger == nil {
		return "", nil
	}

	// 先扣配额再调模型。反过来的话，模型每失败一次用户就少一次机会，
	// 而失败往往是服务端的问题，不该算在用户头上。
	count, err := s.rdb.Incr(ctx, keys.TagSuggestKey(userID)).Result()
	if err != nil {
		// Redis 挂了就放行：这是防滥用，不是业务前提。
		// 代价是这段时间没有配额保护，但至少写作页还能用。
		return s.generateTag(ctx, title, content)
	}
	if count == 1 {
		// 第一次计数时设过期，形成「从第一次请求起算一小时」的窗口。
		// 设不上只留下一个不过期的计数器，不值得为它让这次建议失败，记一条日志就够。
		if err := s.rdb.Expire(ctx, keys.TagSuggestKey(userID), tagSuggestWindow).Err(); err != nil {
			slog.WarnContext(ctx, "set tag suggest quota ttl", "user_id", userID, "error", err)
		}
	}
	if count > tagSuggestPerHour {
		return "", fmt.Errorf("tag suggest for user %d: %w", userID, domain.ErrRateLimited)
	}

	return s.generateTag(ctx, title, content)
}

// generateTag 调模型并取第一个候选，是上面那个方法的主体。
// 抽出来是为了让「Redis 不可用」与「正常计数」两条路径共用同一段生成逻辑。
//
// 清洗、截断、空内容判断都已经在 suggestTag 里，这里只负责把数组收成一个字符串：
// 写作页的输入框只装得下一个标签，多给的候选反而让用户要做选择题。
func (s *ArticleService) generateTag(ctx context.Context, title, content string) (string, error) {
	tags := s.suggestTag(ctx, title, content)
	if len(tags) == 0 {
		return "", nil
	}
	return tags[0], nil
}

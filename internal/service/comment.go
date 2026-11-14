// Package service 的评论部分：与 article.go 同属 service 包，方法名统一用动词。
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"forum-backend/internal/event"
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

// Create 在指定文章下发表一条评论。 并在同一事务里写出领域事件
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
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(m).Error; err != nil {
			return fmt.Errorf("create comment for article %d: %w", articleID, err)
		}
		// m.ID 由 tx.Create(m) 回填，必须在 INSERT 之后才能生成事件。
		// Target 是评论作用的文章，收件人靠它回查作者。
		// Created 是刚插入的这条评论，通知点进去要定位到它。
		// 消费端按事件类型分发，不再靠某个字段是否为 0 猜类型。
		target := event.Target{Type: event.TargetArticle, ID: articleID}
		created := event.Target{Type: event.TargetComment, ID: m.ID}
		return event.EmitCreated(ctx, tx, event.TypeCommentCreated, userID, target, &created)
	})
	if err != nil {
		return nil, err
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

// Package notification 放站内通知相关的 handler、service 与后台任务：
// 把 outbox 事件投进 Redis Stream、消费事件生成站内通知并推送给在线收件人。
package notification

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"forum-backend/internal/domain"
	"forum-backend/internal/model"
)

// NotificationService 负责站内通知的读取与已读标记。
// 通知的写入不在这里：写入发生在 NotificationWorker 消费事件时。
type NotificationService struct {
	db *gorm.DB
}

// NewNotificationService 由装配层（router 的 Deps）在启动时调用一次。
func NewNotificationService(db *gorm.DB) *NotificationService {
	return &NotificationService{db: db}
}

// List 返回当前用户的通知分页、总数与未读数。
// 返回值顺序是：这一页的数据、总数、未读数、错误。
//
// 三条查询各自新开一条链，不复用带 Where 的中间变量：
// gorm 的链式调用会把条件累积在同一个 *gorm.DB 上，
// 复用会让第二条 Count 也带上 Limit/Offset，算出来的总数是错的。
func (s *NotificationService) List(ctx context.Context, userID uint, page, pageSize int) ([]*model.Notification, int64, int64, error) {
	if page < 1 || pageSize < 1 || pageSize > 50 {
		return nil, 0, 0, fmt.Errorf("%w: invalid pagination", domain.ErrInvalidInput)
	}

	// 总数：只用来给前端算总页数。
	var total int64
	err := s.db.WithContext(ctx).Model(&model.Notification{}).
		Where("user_id = ?", userID).Count(&total).Error
	if err != nil {
		return nil, 0, 0, fmt.Errorf("count notifications: %w", err)
	}

	// 未读数：前端红点用，走 idx_notify_user_read 索引。
	var unread int64
	err = s.db.WithContext(ctx).Model(&model.Notification{}).
		Where("user_id = ? AND is_read = ?", userID, false).Count(&unread).Error
	if err != nil {
		return nil, 0, 0, fmt.Errorf("count unread notifications: %w", err)
	}

	// 本页数据：按 id 倒序，id 自增所以等价于按时间倒序，且比 created_at 排序稳定。
	list := make([]*model.Notification, 0)
	err = s.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("id DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).
		Find(&list).Error
	if err != nil {
		return nil, 0, 0, fmt.Errorf("list notifications: %w", err)
	}
	return list, total, unread, nil
}

// MarkAllRead 把当前用户的未读通知一次改完，返回受影响行数。
// 一条 UPDATE 搞定，不逐条回写；前端拿 affected 判断要不要提示「没有未读」。
func (s *NotificationService) MarkAllRead(ctx context.Context, userID uint) (int64, error) {
	res := s.db.WithContext(ctx).Model(&model.Notification{}).
		Where("user_id = ? AND is_read = ?", userID, false).
		Update("is_read", true)
	if res.Error != nil {
		return 0, fmt.Errorf("mark notifications read: %w", res.Error)
	}
	return res.RowsAffected, nil
}

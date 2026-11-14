package service

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"forum-backend/internal/model"
)

// NotificationService 负责站内通知的读取与已读标记。
type NotificationService struct {
	db *gorm.DB
}

func NewNotificationService(db *gorm.DB) *NotificationService {
	return &NotificationService{db: db}
}

// List 返回当前用户的通知分页、总数与未读数。
// 三条查询各开一条新链，不复用带 Where 的中间变量，理由见 03 章 3.5。
func (s *NotificationService) List(ctx context.Context, userID uint, page, pageSize int) ([]*model.Notification, int64, int64, error) {
	if page < 1 || pageSize < 1 || pageSize > 50 {
		return nil, 0, 0, fmt.Errorf("%w: invalid pagination", ErrInvalidInput)
	}

	var total int64
	err := s.db.WithContext(ctx).Model(&model.Notification{}).
		Where("user_id = ?", userID).Count(&total).Error
	if err != nil {
		return nil, 0, 0, fmt.Errorf("count notifications: %w", err)
	}

	var unread int64
	err = s.db.WithContext(ctx).Model(&model.Notification{}).
		Where("user_id = ? AND is_read = ?", userID, false).Count(&unread).Error
	if err != nil {
		return nil, 0, 0, fmt.Errorf("count unread notifications: %w", err)
	}

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
func (s *NotificationService) MarkAllRead(ctx context.Context, userID uint) (int64, error) {
	res := s.db.WithContext(ctx).Model(&model.Notification{}).
		Where("user_id = ? AND is_read = ?", userID, false).
		Update("is_read", true)
	if res.Error != nil {
		return 0, fmt.Errorf("mark notifications read: %w", res.Error)
	}
	return res.RowsAffected, nil
}

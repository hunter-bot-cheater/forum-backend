package user

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"forum-backend/internal/domain"
	"forum-backend/internal/model"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// UserService 用户业务逻辑
type UserService struct {
	db *gorm.DB // db 是小写——包外不可访问，外部只能通过方法操作，不能绕过 Service 直接改 db
}

func NewUserService(db *gorm.DB) *UserService {
	return &UserService{db: db}
}

const bcryptMaxBytes = 72 // bcrypt 加密的最大字节数限制，超过会报错

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

func (s *UserService) Register(ctx context.Context, username, password string) (*model.User, error) {
	if err := validateCredentials(username, password); err != nil {
		return nil, err
	}
	// 先哈希再插入，避免把明文密码写入数据库。
	// 重名判断由数据库唯一索引保证，不能只依赖应用层的预检查。
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	user := &model.User{Username: username, Password: string(hashed)}
	if err := s.db.WithContext(ctx).Create(user).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, domain.ErrUserNameTaken
		}
		return nil, fmt.Errorf("create user: %w", err)
	}

	return user, nil
}

func validateCredentials(username, password string) error {
	if l := len(username); l < 3 || l > 32 {
		return fmt.Errorf("%w: username length must be 3-32", domain.ErrInvalidInput)
	}
	if !usernameRe.MatchString(username) {
		return fmt.Errorf("%w: username contains illegal characters", domain.ErrInvalidInput)
	}
	if len(password) < 8 || len(password) > bcryptMaxBytes {
		return fmt.Errorf("%w: password length invalid", domain.ErrInvalidInput)
	}
	return nil
}

// Login 校验用户名密码，返回用户（不含明文密码）。
func (s *UserService) Login(ctx context.Context, username, password string) (*model.User, error) {
	var user model.User
	if err := s.db.WithContext(ctx).Where("username = ?", username).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrInvalidCredentials
		}
		// 数据库故障不是密码错误，保留上下文供服务端排查。
		return nil, fmt.Errorf("find user: %w", err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err != nil {
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			return nil, domain.ErrInvalidCredentials
		}
		return nil, fmt.Errorf("compare password hash: %w", err)
	}
	return &user, nil
}

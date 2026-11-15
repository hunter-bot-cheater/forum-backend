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

// UserService 是用户模块的业务逻辑，目前只管注册与登录校验。
// 它直接持有 *gorm.DB，不经过 repository：这两个操作都只有一条 SQL，
// 多套一层只是把同样的调用挪个文件，真需要复用查询时再加。
type UserService struct {
	// db 是小写字段，包外不可访问：外部只能通过方法操作数据，
	// 不能绕过校验直接改库。
	db *gorm.DB
}

// NewUserService 由 router 在装配期调用一次，之后整个进程共用这一个实例。
func NewUserService(db *gorm.DB) *UserService {
	return &UserService{db: db}
}

// bcrypt 只处理前 72 个字节，超长会直接返回错误而不是静默截断。
// 所以校验密码时必须把这个上限也挡掉，否则用户设了长密码却在注册时报错。
const bcryptMaxBytes = 72

// 用户名只允许字母、数字和下划线。正则先编译成包级变量，
// 每次注册都重新编译会白白多花一次解析开销。
var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

// Register 创建新用户，密码以 bcrypt 哈希形式入库。
// 返回的错误有三种：domain.ErrInvalidInput（格式不对）、
// domain.ErrUserNameTaken（重名）、以及包装过的数据库错误。
func (s *UserService) Register(ctx context.Context, username, password string) (*model.User, error) {
	// 先校验再动数据库，避免明显非法的请求打到库上。
	if err := validateCredentials(username, password); err != nil {
		return nil, err
	}

	// 哈希开销由 bcrypt.DefaultCost 决定（默认 10，约几十毫秒），
	// 这个耗时是故意的：暴力破解每次尝试都要付同样的代价。
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	// 重名不靠「先查再插」，那样两个并发注册会同时查到不重名然后都插入。
	// 真正的保证是数据库上 username 的唯一索引：插入冲突时 GORM 返回 ErrDuplicatedKey。
	user := &model.User{Username: username, Password: string(hashed)}
	if err := s.db.WithContext(ctx).Create(user).Error; err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, domain.ErrUserNameTaken
		}
		return nil, fmt.Errorf("create user: %w", err)
	}

	return user, nil
}

// validateCredentials 校验用户名与密码的格式，失败统一返回 domain.ErrInvalidInput。
// 具体原因拼在错误信息里（只进日志），客户端只会看到「参数不符合要求」。
func validateCredentials(username, password string) error {
	if l := len(username); l < 3 || l > 32 {
		return fmt.Errorf("%w: username length must be 3-32", domain.ErrInvalidInput)
	}
	if !usernameRe.MatchString(username) {
		return fmt.Errorf("%w: username contains illegal characters", domain.ErrInvalidInput)
	}
	// 密码下限 8 位，上限就是 bcrypt 的 72 字节。
	if len(password) < 8 || len(password) > bcryptMaxBytes {
		return fmt.Errorf("%w: password length invalid", domain.ErrInvalidInput)
	}
	return nil
}

// Login 校验用户名密码，成功时返回用户记录（其中 Password 字段仍是哈希，不是明文）。
// 密码比对交给 bcrypt，它内部是按哈希重新计算再定长比较，天然能抵抗时序攻击。
func (s *UserService) Login(ctx context.Context, username, password string) (*model.User, error) {
	var user model.User
	if err := s.db.WithContext(ctx).Where("username = ?", username).First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 用户不存在与密码错误返回同一个错误：区别对待等于告诉攻击者哪些用户名已被注册。
			return nil, domain.ErrInvalidCredentials
		}
		// 数据库故障不是密码错误，保留上下文供服务端排查。
		return nil, fmt.Errorf("find user: %w", err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err != nil {
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			return nil, domain.ErrInvalidCredentials
		}
		// 走到这里说明库里的哈希本身损坏了，属于数据问题，不能当成密码错。
		return nil, fmt.Errorf("compare password hash: %w", err)
	}
	return &user, nil
}

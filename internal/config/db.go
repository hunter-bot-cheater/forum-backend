package config

import (
	"fmt"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"

	"forum-backend/internal/model"
)

// InitDB 建立 MySQL 连接池，返回可用的 *gorm.DB。
// 它只负责连接，不建表：建表是独立的一步，见 Migrate。
// 只在进程启动时调用一次；返回错误表示配置有误或数据库连不上，进程应当直接退出。
func InitDB(cfg *MySQLConfig) (*gorm.DB, error) {
	// 用 driver 的 Config 拼 DSN，而不是手写字符串：
	// 密码里带 @、/ 这类字符时手拼容易拼出非法 DSN，FormatDSN 会自动转义。
	// ParseTime 让 DATETIME 直接读成 time.Time，Loc 决定时区，两者必须一起设。
	dsnConfig := mysqlDriver.Config{
		User:      cfg.User,
		Passwd:    cfg.Password,
		Net:       "tcp",
		Addr:      fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		DBName:    cfg.DBName,
		Params:    map[string]string{"charset": "utf8mb4"},
		ParseTime: true,
		Loc:       time.Local,
	}
	dsn := dsnConfig.FormatDSN()

	db, err := gorm.Open(gormmysql.Open(dsn), &gorm.Config{
		// TranslateError 把驱动的原始错误转成 gorm.ErrDuplicatedKey 这类哨兵值，
		// 业务层判断「是不是唯一索引冲突」时不用去匹配 MySQL 错误码。
		TranslateError: true,
		// SQL 日志接到 slog：只记错误和慢查询，成功的语句不记。
		// 阈值来自 mysql.slow_query_ms，默认 200ms。
		Logger: newGormLogger(time.Duration(cfg.SlowQueryMS) * time.Millisecond),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	// gorm.Open 是懒连接，这里取底层 *sql.DB 才能设置连接池参数。
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql.DB: %w", err)
	}

	// 最大连接数要小于 MySQL 的 max_connections，
	// 剩下的留给 mysql 客户端、备份和其他服务，不要吃满。
	sqlDB.SetMaxOpenConns(20)
	sqlDB.SetMaxIdleConns(10)
	// 必须小于 MySQL 的 wait_timeout（默认 8 小时），
	// 否则会从池里拿到被服务端单方面关掉的连接，第一次用就报错。
	sqlDB.SetConnMaxLifetime(time.Hour)

	return db, nil
}

// Migrate 按模型定义建表 / 补列 / 补索引，是幂等的，重复执行不会丢数据。
//
// 为什么不放进 InitDB：生产上改表结构属于一次明确的部署动作，
// 不该跟「进程启动」绑在一起。多个实例同时启动时各跑一次迁移，
// 轻则互相等锁，重则建到一半失败；交给独立的 migrate 命令，
// 部署流水线里先迁移、成功后再滚动重启服务，顺序就是确定的。
func Migrate(db *gorm.DB) error {
	// AutoMigrate 只加表、加列、加索引，不删列也不改类型，所以可以安全重复跑。
	// 新增模型时要记得补进这个列表，否则表不会建出来。
	if err := db.AutoMigrate(&model.User{}, &model.Article{}, &model.Comment{}, &model.ArticleLike{}, &model.OutboxEvent{}, &model.Notification{}, &model.FloorEvent{}, &model.FloorPrize{}, &model.Follow{}, &model.Conversation{}, &model.Message{}); err != nil {
		return fmt.Errorf("auto-migrate database: %w", err)
	}
	return nil
}

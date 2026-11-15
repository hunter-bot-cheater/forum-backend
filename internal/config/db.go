package config

import (
	"fmt"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"forum-backend/internal/model"
)

func InitDB(cfg *MySQLConfig) (*gorm.DB, error) {
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
		TranslateError: true,
		// SQL 错误由 main/HTTP handler 在边界记录一次，避免 GORM 和应用重复打印。
		Logger: gormlogger.Default.LogMode(gormlogger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	// 增量修改：internal/config/db.go，在 AutoMigrate 之前
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql.DB: %w", err)
	}
	// 上限要小于 MySQL 的 max_connections。
	// 剩下的留给 mysql-cli、备份、其他服务，不要吃满。
	sqlDB.SetMaxOpenConns(20)
	sqlDB.SetMaxIdleConns(10)
	// 小于 MySQL 的 wait_timeout（默认 8 小时），避免拿到被服务端单方面关掉的连接。
	sqlDB.SetConnMaxLifetime(time.Hour)

	if err := db.AutoMigrate(&model.User{}, &model.Article{}, &model.Comment{}, &model.ArticleLike{}, &model.OutboxEvent{}, &model.Notification{}, &model.FloorEvent{}, &model.FloorPrize{}); err != nil {
		return nil, fmt.Errorf("failed to auto-migrate database: %w", err)
	}
	return db, nil
}

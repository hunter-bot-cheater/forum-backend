// Command migrate 只做一件事：连上数据库，按模型定义建表 / 补列 / 补索引。
//
// 生产部署时把它当成一次独立的部署步骤，跑在重启服务之前：
//
//	CONFIG_PATH=/etc/forum/config.yaml ./forum-migrate
//	# 成功后再滚动重启 forum-server
//
// 退出码：
//
//	0  迁移完成（重复执行也返回 0，AutoMigrate 是幂等的）
//	2  配置有误、连不上库或建表失败
//
// 服务端的 mysql.auto_migrate 设为 false 时不会自动建表，改表就靠这个命令；
// 本机开发可以继续留 true，省掉手动执行这一步。
package main

import (
	"log/slog"
	"os"

	"forum-backend/internal/config"
	"forum-backend/pkg/logging"
)

// main 只负责把退出码交给 os.Exit，逻辑在 run 里。
func main() {
	os.Exit(run())
}

// run 加载配置、连接数据库、执行一次迁移，返回进程退出码。
func run() int {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("加载配置失败", "error", err)
		return 2
	}

	// 一次性命令只打控制台、不落文件：日志文件同一时刻只允许一个进程写，
	// 让迁移命令去写 server 的文件会互相抢改名。
	_, closeLog, err := logging.Setup(logging.Config{
		Level:   cfg.Log.Level,
		Format:  cfg.Log.Format,
		Console: true,
	})
	if err != nil {
		slog.Error("初始化日志失败", "error", err)
		return 2
	}
	defer closeLog()

	db, err := config.InitDB(&cfg.MySQL)
	if err != nil {
		slog.Error("连接数据库失败", "error", err)
		return 2
	}
	// 迁移命令跑完就退，连接池不关也无所谓，但显式关闭能让 MySQL 侧的会话立刻回收。
	sqlDB, err := db.DB()
	if err == nil {
		defer sqlDB.Close()
	}

	if err := config.Migrate(db); err != nil {
		slog.Error("迁移失败", "error", err)
		return 2
	}

	slog.Info("迁移完成", "db", cfg.MySQL.DBName, "host", cfg.MySQL.Host, "port", cfg.MySQL.Port)
	return 0
}

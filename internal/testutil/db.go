// Package testutil 提供集成测试需要的依赖：真实 MySQL 与内存 Redis。
//
// 只在 *_test.go 里 import。业务代码不要引用它，否则测试专用的依赖会被拖进产物。
//
// 为什么用真实 MySQL 而不是 sqlite 内存库：
// 本项目的 SQL 依赖 LEFT() 截摘要、RESTRICT 外键、条件 UPDATE 抢名额这几样东西，
// sqlite 要么不支持、要么语义不同，拿它跑出来的绿是假绿。
// Redis 侧用 miniredis：命令兼容性够用，又不需要额外装服务、不怕跑脏开发数据。
package testutil

import (
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"forum-backend/internal/model"
)

// dsnEnv 是测试库连接串的环境变量名。
// 不设它时所有集成测试直接 skip，所以没装 MySQL 的机器也能跑纯单测。
const dsnEnv = "TEST_MYSQL_DSN"

// testTables 是每个测试开始前要清空的表。
// 新增模型时必须补进来：漏一张表，脏数据就会串到下一个测试，
// 典型表现是「单独跑这个测试能过，整个包一起跑就挂」。
var testTables = []string{
	"messages",
	"conversations",
	"comments",
	"article_likes",
	// 关联表要排在 articles / tags 前面：虽然 TRUNCATE 前已经关了外键检查，
	// 但顺序按「子表在前」写，将来改成 DELETE 也不会被外键挡住。
	"article_tags",
	"tags",
	"outbox_events",
	"notifications",
	"floor_prizes",
	"floor_events",
	"follows",
	// reports 没有任何外键（目标用 target_type + target_id 表达多态），
	// 放在 users 前面只是为了保持「业务表在前、用户表在后」的既有顺序。
	"reports",
	// 台球三张表用 GORM 默认的复数表名（模型上没有 TableName 覆盖）。
	// pool_shots 语义上是 pool_matches 的子表，放在前面。
	"pool_shots",
	"pool_matches",
	"pool_ratings",
	"articles",
	"users",
}

// dbEntry 缓存同一个 DSN 的连接池与初始化结果。
// 整个测试进程共用一个连接池：每个测试都 gorm.Open 会让连接数随用例数线性上涨，
// 而 MySQL 默认 max_connections 只有 151。
type dbEntry struct {
	once sync.Once
	db   *gorm.DB
	err  error
}

var (
	dbMu    sync.Mutex
	dbCache = make(map[string]*dbEntry)
)

// NewDB 返回连到测试库的 *gorm.DB，并在返回前清空所有表。
//
// 用法：db := testutil.NewDB(t)，之后直接把 db 传给要测的 service 构造函数。
// 没设 TEST_MYSQL_DSN 时本函数会 skip 当前测试，不需要在每个用例里自己判断。
func NewDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("未设置 %s，跳过需要 MySQL 的集成测试", dsnEnv)
	}

	db, err := openTestDB(dsn)
	if err != nil {
		t.Fatalf("连接测试库失败（检查 %s）: %v", dsnEnv, err)
	}

	truncateAll(t, db)
	return db
}

// ReopenDB 用同一个 DSN 新开一条连接池，不清表、不做迁移。
//
// 用途是验证「数据真的落到了 MySQL」：拿着这条新连接配一个新的 service 去读，
// 读到的内容只可能来自数据库，不会是原 service 或原连接池里的内存状态。
// 换连接不能证明「进程重启后还在」，但能排掉「其实只在内存里」这种最常见的假持久化。
func ReopenDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("未设置 %s，跳过需要 MySQL 的集成测试", dsnEnv)
	}

	// 这里不复用 dbCache：要的就是一条全新的连接，复用等于没验证。
	// 每个用例多一条连接池可以接受，测试结束由进程退出回收。
	db, err := gorm.Open(gormmysql.Open(dsn), &gorm.Config{
		Logger:         gormlogger.Default.LogMode(gormlogger.Silent),
		TranslateError: true,
	})
	if err != nil {
		t.Fatalf("重新连接测试库失败（检查 %s）: %v", dsnEnv, err)
	}
	return db
}

// openTestDB 按 DSN 取连接池，同一个 DSN 只连一次、只迁移一次。
// 迁移失败的错误会被缓存，后续用例拿到同一个错误直接失败，不会误以为库是好的。
func openTestDB(dsn string) (*gorm.DB, error) {
	dbMu.Lock()
	entry, ok := dbCache[dsn]
	if !ok {
		entry = &dbEntry{}
		dbCache[dsn] = entry
	}
	dbMu.Unlock()

	entry.once.Do(func() {
		entry.db, entry.err = connectAndMigrate(dsn)
	})
	return entry.db, entry.err
}

// connectAndMigrate 建库、连库、建表。
// 建库单独走一条不绑定库名的连接：库还不存在时，带库名的 DSN 根本连不上。
func connectAndMigrate(dsn string) (*gorm.DB, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("DSN 格式不对: %w", err)
	}
	// 库名必须显式写在 DSN 里，否则清表时不知道该清哪个库。
	// 这是刻意的：绝不允许「忘了写库名，于是连到开发库把数据清了」。
	if cfg.DBName == "" {
		return nil, fmt.Errorf("DSN 必须带库名，例如 root:root@tcp(127.0.0.1:3306)/forum_test?parseTime=true")
	}

	serverDSN := cfg.Clone()
	serverDSN.DBName = ""
	conn, err := sql.Open("mysql", serverDSN.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("连接 MySQL 服务失败: %w", err)
	}
	// CREATE DATABASE 用反引号包库名：库名来自 DSN，不能直接拼进语句。
	//nolint:gocritic // 反引号是 SQL 标识符语法；%#q 生成的是 Go 字符串字面量，语义不对
	createSQL := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci", cfg.DBName)
	if _, err := conn.Exec(createSQL); err != nil {
		conn.Close()
		return nil, fmt.Errorf("创建测试库 %s 失败: %w", cfg.DBName, err)
	}
	if err := conn.Close(); err != nil {
		return nil, fmt.Errorf("关闭建库连接失败: %w", err)
	}

	// 测试里不需要看 SQL 日志：断言失败时错误信息已经带上了出错的那一步，
	// 把每句 SQL 都打出来只会淹没真正的失败原因。
	// TranslateError 必须和 config.Open 保持一致：业务代码判断
	// errors.Is(err, gorm.ErrDuplicatedKey) 依赖它把驱动错误翻译成哨兵值，
	// 测试库不翻译的话，生产能跑通的幂等分支在测试里会整条走偏。
	db, err := gorm.Open(gormmysql.Open(dsn), &gorm.Config{
		Logger:         gormlogger.Default.LogMode(gormlogger.Silent),
		TranslateError: true,
	})
	if err != nil {
		return nil, fmt.Errorf("打开测试库失败: %w", err)
	}

	// 与 config.Migrate 保持同一份模型清单。
	// 这里不调用 config.Migrate 是为了不让测试依赖 config 包（它会读配置文件）。
	if err := db.AutoMigrate(
		&model.User{},
		&model.Article{},
		&model.Tag{},
		&model.ArticleTag{},
		&model.Comment{},
		&model.ArticleLike{},
		&model.OutboxEvent{},
		&model.Notification{},
		&model.FloorEvent{},
		&model.FloorPrize{},
		&model.Follow{},
		&model.Conversation{},
		&model.Message{},
		&model.Report{},
		&model.PoolMatch{},
		&model.PoolShot{},
		&model.PoolRating{},
	); err != nil {
		return nil, fmt.Errorf("迁移测试库失败: %w", err)
	}
	return db, nil
}

// truncateAll 清空所有测试表，并把自增 ID 重置回 1。
//
// 用 TRUNCATE 而不是 DELETE：TRUNCATE 顺带重置 AUTO_INCREMENT，
// 用例里就能放心断言「第一条评论 id 是 1」，不用迁就上一个用例留下的序号。
// 代价是 TRUNCATE 会被外键挡住，所以要先关掉当前连接的外键检查。
func truncateAll(t *testing.T, db *gorm.DB) {
	t.Helper()

	// FOREIGN_KEY_CHECKS 是会话级变量，而连接池每次 Exec 可能换一条连接。
	// 必须用 Connection 把整段钉在同一条连接上，否则关检查的是 A 连接、
	// TRUNCATE 跑在 B 连接上，照样被外键挡住，而且报错信息看起来莫名其妙。
	err := db.Connection(func(tx *gorm.DB) error {
		if err := tx.Exec("SET FOREIGN_KEY_CHECKS = 0").Error; err != nil {
			return fmt.Errorf("关闭外键检查: %w", err)
		}
		for _, table := range testTables {
			// 表名来自本文件的常量列表，不是外部输入，直接拼反引号是安全的。
			if err := tx.Exec("TRUNCATE TABLE `" + table + "`").Error; err != nil {
				return fmt.Errorf("清空表 %s: %w", table, err)
			}
		}
		if err := tx.Exec("SET FOREIGN_KEY_CHECKS = 1").Error; err != nil {
			return fmt.Errorf("恢复外键检查: %w", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("清空测试库失败: %v", err)
	}
}

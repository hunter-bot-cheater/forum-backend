package testutil

import (
	"fmt"
	"testing"

	"gorm.io/gorm"

	"forum-backend/internal/model"
)

// Fixture 是一个用例的数据工厂：把 *testing.T 与 *gorm.DB 绑在一起。
//
// 为什么要有它：一个需要「作者 + 文章 + 三个点赞用户」的用例，
// 每造一条数据都要写一遍 (t, db) 两个参数，而且 username 有唯一索引，
// 手写名字时很容易在同一个用例里撞车（写测试时踩过：第二次刷赞又从 a 开始编号）。
// Fixture 自带递增序号，同一个用例里造几个用户都不会重名。
//
// 它不是 SeedUser / SeedArticle 的替代品，方法内部直接转调包级函数，
// 只有一份实现。已有用例继续用包级函数就好，不必为了统一而改写——
// 那些用例的 fixture 结构体各自带着自己的 service 字段，替换成内嵌
// 反而会把「这个用例依赖什么」藏起来。
type Fixture struct {
	t  *testing.T
	db *gorm.DB
	// seq 是自动命名的递增序号，只在 User 里用。
	seq int
}

// NewFixture 准备干净库，返回数据工厂。
// 每张表都会被清空，所以同一个用例里看到的只有自己造的数据。
func NewFixture(t *testing.T) *Fixture {
	t.Helper()

	return &Fixture{t: t, db: NewDB(t)}
}

// DB 返回底层连接，用例要直接建模型（比如点赞关系）时用。
func (f *Fixture) DB() *gorm.DB {
	return f.db
}

// User 建一个用户，用户名由 prefix 加序号组成，保证同一用例内不重复。
func (f *Fixture) User(prefix string) *model.User {
	f.seq++
	return SeedUser(f.t, f.db, fmt.Sprintf("%s-%d", prefix, f.seq))
}

// NamedUser 建一个指定用户名的用户。
// 用例需要断言「列表里第一个是 alice」这类带名字的期望时用它，
// 否则用 User 让序号去处理唯一性。
func (f *Fixture) NamedUser(name string) *model.User {
	return SeedUser(f.t, f.db, name)
}

// Article 给指定作者建一篇文章。
func (f *Fixture) Article(authorID uint, title string) *model.Article {
	return SeedArticle(f.t, f.db, authorID, title)
}

// ArticleWithAuthor 一步建「一个作者和他的文章」。
// 大部分用例的开场都是这两步，分开写时容易把文章挂到别的作者 id 上。
func (f *Fixture) ArticleWithAuthor(title string) (*model.User, *model.Article) {
	author := f.User("author")
	return author, f.Article(author.ID, title)
}

// Count 返回指定表的行数，语义与 CountRows 相同。
func (f *Fixture) Count(table string) int64 {
	return CountRows(f.t, f.db, table)
}

// SeedUser 插入一个用户并返回，用户名重复会让测试直接失败。
//
// 密码字段塞一个固定字符串就够了：评论、文章这些用例不校验密码，
// 需要登录凭证的用例自己在用例里调 jwt.Manager 签发。
func SeedUser(t *testing.T, db *gorm.DB, username string) *model.User {
	t.Helper()

	user := &model.User{Username: username, Password: "bcrypt-hash-placeholder"}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("插入测试用户 %s 失败: %v", username, err)
	}
	return user
}

// SeedArticle 插入一篇文章并返回，authorID 必须是已存在的用户。
//
// 直接落库而不走 ArticleService：这里要的是「库里有篇文章」这个前提，
// 用例真正要测的是评论的行为，不想让文章模块的校验规则参与进来。
func SeedArticle(t *testing.T, db *gorm.DB, authorID uint, title string) *model.Article {
	t.Helper()

	article := &model.Article{
		UserID:  authorID,
		Title:   title,
		Content: "测试正文",
	}
	if err := db.Create(article).Error; err != nil {
		t.Fatalf("插入测试文章 %q 失败: %v", title, err)
	}
	return article
}

// CountRows 返回指定表的行数，用于断言「该写的写了」「该回滚的没留下」。
// 表名由调用方给出（测试代码里的常量），不走用户输入。
func CountRows(t *testing.T, db *gorm.DB, table string) int64 {
	t.Helper()

	var count int64
	if err := db.Table(table).Count(&count).Error; err != nil {
		t.Fatalf("统计表 %s 行数失败: %v", table, err)
	}
	return count
}

package testutil

import (
	"testing"

	"gorm.io/gorm"

	"forum-backend/internal/model"
)

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

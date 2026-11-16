package article

import (
	"context"
	"errors"
	"strings"
	"testing"

	"forum-backend/internal/domain"
	"forum-backend/internal/testutil"
)

// TestCreateAcceptsChineseTitle 是「中文标题被误拒」的回归测试。
//
// 标题列是 varchar(128)，MySQL 按字符计；早先的实现用 len(title) > 128，
// len 取的是字节数，100 个汉字是 300 字节，会被判成超长而拒绝。
// 这条用例在旧实现下必然失败，是判断修复是否真的生效的判据。
func TestCreateAcceptsChineseTitle(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t))
	author := testutil.SeedUser(t, db, "author")

	title := strings.Repeat("测", 100)
	created, err := svc.Create(context.Background(), author.ID, title, "正文")
	if err != nil {
		t.Fatalf("100 个汉字的标题（300 字节）应当通过，实际报错: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("创建后 ID 应当被回填，实际是 0")
	}
	if created.Title != title {
		t.Errorf("标题被改动：%q", created.Title)
	}
}

// TestCreateRejectsOversizeInput 验证两类上限真的会拦人。
//
// 标题与正文单位不同（字符 / 字节），分别越界一次：
// 只测其中一种，另一条判断写错也发现不了。
func TestCreateRejectsOversizeInput(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t))
	author := testutil.SeedUser(t, db, "author")

	cases := []struct {
		name    string
		title   string
		content string
	}{
		{"标题超字符上限", strings.Repeat("好", maxTitleRunes+1), "正文"},
		// 正文按字节判：maxContentBytes+1 个字母正好越过上限。
		{"正文超字节上限", "正常标题", strings.Repeat("a", maxContentBytes+1)},
	}
	for _, tc := range cases {
		_, err := svc.Create(context.Background(), author.ID, tc.title, tc.content)
		if !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("%s：错误 = %v，期望 ErrInvalidInput", tc.name, err)
		}
	}
}

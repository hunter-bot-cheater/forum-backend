package article

import (
	"strings"
	"testing"
	"time"

	"forum-backend/internal/model"
)

// TestExcerptOf 覆盖摘要的三个关键行为：空白压缩、按字符截断、空正文。
func TestExcerptOf(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"空正文", "", ""},
		{"短正文原样返回", "只有一句话。", "只有一句话。"},
		{"换行与连续空格压成单个空格", "第一段\n\n第二段\t结尾", "第一段 第二段 结尾"},
		{"恰好等于上限时不加省略号", strings.Repeat("字", articleExcerptLimit), strings.Repeat("字", articleExcerptLimit)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := excerptOf(tc.in); got != tc.want {
				t.Fatalf("excerptOf(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestExcerptOfTruncates 单独验证超长正文：截断长度按 rune 算，
// 不能把中文截成半个字节，且末尾要带省略号提示还有正文。
func TestExcerptOfTruncates(t *testing.T) {
	got := excerptOf(strings.Repeat("字", articleExcerptLimit+50))

	if !strings.HasSuffix(got, "…") {
		t.Fatalf("超长正文应以省略号结尾，实际 %q", got[len(got)-3:])
	}
	// 去掉省略号后必须是 articleExcerptLimit 个完整汉字。
	body := strings.TrimSuffix(got, "…")
	if runes := []rune(body); len(runes) != articleExcerptLimit {
		t.Fatalf("截断后长度 = %d 个字符，want %d", len(runes), articleExcerptLimit)
	}
}

// TestToArticleListItem 确认列表项只用摘要、不带正文，
// 并确认没 Preload 作者时 author 字段被省略而不是给一个 id=0 的空对象。
func TestToArticleListItem(t *testing.T) {
	created := time.Now()
	a := &model.Article{
		ID:        7,
		Title:     "标题",
		Content:   "这段正文在列表里不该出现",
		Excerpt:   "这段正文在列表里",
		CreatedAt: created,
	}

	item := toArticleListItem(a)
	if item.Excerpt != "这段正文在列表里" {
		t.Fatalf("Excerpt = %q, 应取自 a.Excerpt", item.Excerpt)
	}
	if item.Author != nil {
		t.Fatalf("没有作者信息时 Author 应为 nil，实际 %+v", item.Author)
	}

	a.User = model.User{ID: 3, Username: "alice"}
	item = toArticleListItem(a)
	if item.Author == nil || item.Author.ID != 3 || item.Author.Username != "alice" {
		t.Fatalf("Author = %+v, want id=3 username=alice", item.Author)
	}
}

// TestAuthorBriefCarriesDisplayFields 验证作者信息带上昵称与头像。
//
// 用户主页、关注列表显示的都是昵称 + 头像；如果文章和评论只回 username，
// 前端就得为同一个「作者」写两套渲染，改口径时也容易漏掉其中一处。
// 昵称为空时这里必须回退成用户名，规则来自 user.Brief.DisplayName。
func TestAuthorBriefCarriesDisplayFields(t *testing.T) {
	a := &model.Article{
		User: model.User{ID: 5, Username: "alice", Nickname: "爱丽丝", AvatarURL: "https://cdn.example.com/a.png"},
	}
	brief := authorBrief(a)
	if brief == nil {
		t.Fatal("有作者信息时 Author 不应为 nil")
	}
	if brief.Nickname != "爱丽丝" || brief.AvatarURL != "https://cdn.example.com/a.png" {
		t.Errorf("作者 = %+v，期望带上昵称与头像", brief)
	}

	// 昵称为空：展示名回退成用户名，前端不用判空。
	a.User.Nickname = ""
	if got := authorBrief(a); got.Nickname != "alice" {
		t.Errorf("空昵称时 nickname = %q，期望回退成 alice", got.Nickname)
	}
}

package article

import (
	"context"
	"errors"
	"testing"

	"forum-backend/internal/domain"
	"forum-backend/internal/keys"
	"forum-backend/internal/testutil"
)

// TestCreateRejectsDuplicateContent 验证同一份内容在窗口内只能发一次。
//
// 判重键里带 userID：这里用同一个作者，第二次必须被拦。
func TestCreateRejectsDuplicateContent(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
	ctx := context.Background()
	author := testutil.SeedUser(t, db, "dup")

	if _, err := svc.Create(ctx, author.ID, "重复发布", "正文内容", nil); err != nil {
		t.Fatalf("第一次发布失败: %v", err)
	}
	if _, err := svc.Create(ctx, author.ID, "重复发布", "正文内容", nil); !errors.Is(err, domain.ErrDuplicateContent) {
		t.Fatalf("第二次发布 error = %v, want ErrDuplicateContent", err)
	}
	// 库里只能有一篇，说明第二次确实没写进去。
	if got := testutil.CountRows(t, db, "articles"); got != 1 {
		t.Errorf("articles 有 %d 行，期望 1", got)
	}
}

// TestCreateAllowsChangedContent 验证改一个字就允许发。
// 判重的是内容本身而不是「作者 10 分钟内发过文章」，
// 否则连发两篇不同主题的文章会莫名其妙被拦。
func TestCreateAllowsChangedContent(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
	ctx := context.Background()
	author := testutil.SeedUser(t, db, "dup2")

	if _, err := svc.Create(ctx, author.ID, "第一版", "正文", nil); err != nil {
		t.Fatalf("第一次发布失败: %v", err)
	}
	if _, err := svc.Create(ctx, author.ID, "第一版", "正文。", nil); err != nil {
		t.Fatalf("改了标点应当能发，实际报错: %v", err)
	}
	if got := testutil.CountRows(t, db, "articles"); got != 2 {
		t.Errorf("articles 有 %d 行，期望 2", got)
	}
}

// TestCreateDuplicateIsPerUser 验证判重按人隔离。
//
// 两个人在同一个话题下发同样的内容是完全正常的（比如都贴同一段报错信息），
// 键里不带 userID 的话第二个人会被误拦。
func TestCreateDuplicateIsPerUser(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
	ctx := context.Background()
	first := testutil.SeedUser(t, db, "dupA")
	second := testutil.SeedUser(t, db, "dupB")

	if _, err := svc.Create(ctx, first.ID, "同一段内容", "同样的正文", nil); err != nil {
		t.Fatalf("A 发布失败: %v", err)
	}
	if _, err := svc.Create(ctx, second.ID, "同一段内容", "同样的正文", nil); err != nil {
		t.Fatalf("B 发同样的内容不该被拦，实际报错: %v", err)
	}
}

// TestCreateSucceedsAfterDuplicateKeyExpires 验证判重只依赖那个 Redis 键。
// 手动删掉键等于模拟 10 分钟窗口过去，此时同样的内容应该又能发。
func TestCreateSucceedsAfterDuplicateKeyExpires(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewArticleService(db, testutil.NewRedis(t), nil)
	ctx := context.Background()
	author := testutil.SeedUser(t, db, "dup3")

	if _, err := svc.Create(ctx, author.ID, "会重复的标题", "会重复的正文", nil); err != nil {
		t.Fatalf("第一次发布失败: %v", err)
	}
	// 直接用与 service 相同的 key 拼法删掉，验证窗口过期后的行为。
	key := keys.ArticleDupKey(author.ID, contentDigest("会重复的标题", "会重复的正文"))
	if err := svc.rdb.Del(ctx, key).Err(); err != nil {
		t.Fatalf("删除判重键失败: %v", err)
	}
	if _, err := svc.Create(ctx, author.ID, "会重复的标题", "会重复的正文", nil); err != nil {
		t.Fatalf("窗口过期后应当能再发，实际报错: %v", err)
	}
}

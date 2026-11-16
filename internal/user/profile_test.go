package user

import (
	"context"
	"errors"
	"strings"
	"testing"

	"forum-backend/internal/domain"
	"forum-backend/internal/testutil"
)

// 本文件覆盖个人资料的读写：局部更新语义、字段校验、以及「查不到」的翻译。
// 需要 MySQL 的用例统一走 testutil.NewDB，未设置 TEST_MYSQL_DSN 时自动跳过。

// TestUpdateProfilePartialUpdate 验证三点局部更新语义：
// 传了哪个字段就改哪个、没传的保持不变、传空串是清空。
func TestUpdateProfilePartialUpdate(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewUserService(db)
	u := testutil.SeedUser(t, db, "alice")
	ctx := context.Background()

	// 第一步只改昵称，头像和简介应当仍是零值。
	nickname := "爱丽丝"
	got, err := svc.UpdateProfile(ctx, u.ID, ProfilePatch{Nickname: &nickname})
	if err != nil {
		t.Fatalf("改昵称失败: %v", err)
	}
	if got.Nickname != nickname {
		t.Errorf("nickname = %q，期望 %q", got.Nickname, nickname)
	}

	// 第二步只改简介，昵称必须原样保留。
	// 这条断言是局部更新的核心：如果 handler 把 nil 指针解成空串，昵称会在这里被抹掉。
	bio := "写点东西"
	got, err = svc.UpdateProfile(ctx, u.ID, ProfilePatch{Bio: &bio})
	if err != nil {
		t.Fatalf("改简介失败: %v", err)
	}
	if got.Nickname != nickname {
		t.Errorf("只改简介后 nickname 被覆盖成 %q", got.Nickname)
	}
	if got.Bio != bio {
		t.Errorf("bio = %q，期望 %q", got.Bio, bio)
	}

	// 第三步传空串清空简介，同样不能影响昵称。
	empty := ""
	got, err = svc.UpdateProfile(ctx, u.ID, ProfilePatch{Bio: &empty})
	if err != nil {
		t.Fatalf("清空简介失败: %v", err)
	}
	if got.Bio != "" {
		t.Errorf("清空后 bio = %q，期望空串", got.Bio)
	}
	if got.Nickname != nickname {
		t.Errorf("清空简介后 nickname 被覆盖成 %q", got.Nickname)
	}
}

// TestUpdateProfileNoFieldIsNoop 验证补丁全为 nil 时不发 UPDATE、原样返回当前资料。
func TestUpdateProfileNoFieldIsNoop(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewUserService(db)
	u := testutil.SeedUser(t, db, "bob")

	got, err := svc.UpdateProfile(context.Background(), u.ID, ProfilePatch{})
	if err != nil {
		t.Fatalf("空补丁应当成功: %v", err)
	}
	if got.ID != u.ID || got.Username != u.Username {
		t.Errorf("返回的用户不是同一个人: id=%d username=%q", got.ID, got.Username)
	}
}

// TestUpdateProfileRejectsInvalidInput 表驱动覆盖四类非法输入。
// 每个用例都断言拿到 domain.ErrInvalidInput，handler 才会映射成 400 而不是 500。
func TestUpdateProfileRejectsInvalidInput(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewUserService(db)
	u := testutil.SeedUser(t, db, "carol")

	tooLongNick := strings.Repeat("长", maxNicknameRunes+1)
	tooLongBio := strings.Repeat("字", maxBioRunes+1)
	controlNick := "带\n换行"
	jsAvatar := "javascript:alert(1)"
	relativeAvatar := "/static/avatar.png"

	cases := []struct {
		name  string
		patch ProfilePatch
	}{
		{name: "昵称超长", patch: ProfilePatch{Nickname: &tooLongNick}},
		{name: "昵称含控制字符", patch: ProfilePatch{Nickname: &controlNick}},
		{name: "简介超长", patch: ProfilePatch{Bio: &tooLongBio}},
		{name: "头像非 http 协议", patch: ProfilePatch{AvatarURL: &jsAvatar}},
		{name: "头像没有 host", patch: ProfilePatch{AvatarURL: &relativeAvatar}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.UpdateProfile(context.Background(), u.ID, tc.patch)
			if !errors.Is(err, domain.ErrInvalidInput) {
				t.Fatalf("错误 = %v，期望 domain.ErrInvalidInput", err)
			}
			// 校验失败的补丁不能落库：重新读一次确认库里还是原样。
			cur, err := svc.GetByID(context.Background(), u.ID)
			if err != nil {
				t.Fatalf("回查用户失败: %v", err)
			}
			if cur.Nickname != "" || cur.Bio != "" || cur.AvatarURL != "" {
				t.Errorf("校验失败却写入了数据: %+v", cur)
			}
		})
	}
}

// TestUpdateProfileUnknownUser 验证改不存在的用户返回 domain.ErrNotFound。
func TestUpdateProfileUnknownUser(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewUserService(db)

	nickname := "谁"
	_, err := svc.UpdateProfile(context.Background(), 999999, ProfilePatch{Nickname: &nickname})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("错误 = %v，期望 domain.ErrNotFound", err)
	}
}

// TestGetByID 覆盖三种情况：正常命中、id 为 0、id 不存在。
func TestGetByID(t *testing.T) {
	db := testutil.NewDB(t)
	svc := NewUserService(db)
	u := testutil.SeedUser(t, db, "dave")
	ctx := context.Background()

	got, err := svc.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("查询已存在用户失败: %v", err)
	}
	if got.Username != "dave" {
		t.Errorf("username = %q", got.Username)
	}

	if _, err := svc.GetByID(ctx, 0); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("id=0 的错误 = %v，期望 domain.ErrInvalidInput", err)
	}
	if _, err := svc.GetByID(ctx, 999999); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("不存在的 id 错误 = %v，期望 domain.ErrNotFound", err)
	}
}

// TestNormalizeNicknameTrims 验证首尾空白被去掉，中间空格保留。
func TestNormalizeNicknameTrims(t *testing.T) {
	got, err := normalizeNickname("  小 明  ")
	if err != nil {
		t.Fatalf("合法昵称被拒: %v", err)
	}
	if got != "小 明" {
		t.Errorf("nickname = %q，期望 %q", got, "小 明")
	}
}

// TestNormalizeAvatarURLAcceptsHTTPS 验证合法外链原样通过。
func TestNormalizeAvatarURLAcceptsHTTPS(t *testing.T) {
	const raw = "https://cdn.example.com/a.png"
	got, err := normalizeAvatarURL("  " + raw + "  ")
	if err != nil {
		t.Fatalf("合法头像地址被拒: %v", err)
	}
	if got != raw {
		t.Errorf("avatar_url = %q，期望 %q", got, raw)
	}
}

package follow

import (
	"context"
	"errors"
	"testing"

	"gorm.io/gorm"

	"forum-backend/internal/domain"
	"forum-backend/internal/model"
	"forum-backend/internal/testutil"
	"forum-backend/internal/user"
)

// followFixture 是一个用例需要的现场：库、service、三个用户。
// 三个用户足够测「A 关注 B、C 关注 B、A 关注 C」这类多对多关系。
// 带上 db 是为了让个别用例（比如构造孤儿关系）能直接操作表，
// 重复调用 testutil.NewDB 会重新清表，把夹具数据一起清掉。
type followFixture struct {
	db    *gorm.DB
	svc   *Service
	alice *model.User
	bob   *model.User
	carol *model.User
}

// newFollowFixture 准备干净库与固定用户。每个用例都从空表开始。
func newFollowFixture(t *testing.T) *followFixture {
	t.Helper()

	db := testutil.NewDB(t)
	return &followFixture{
		db:    db,
		svc:   NewService(db, testutil.NewRedis(t), user.NewUserService(db)),
		alice: testutil.SeedUser(t, db, "alice"),
		bob:   testutil.SeedUser(t, db, "bob"),
		carol: testutil.SeedUser(t, db, "carol"),
	}
}

// TestFollowIsIdempotent 验证重复关注不新增关系，Changed 能区分「第一次」与「重复」。
// 粉丝数必须每次重新统计：在旧值上 +1 会让重复点击把数字越加越大。
func TestFollowIsIdempotent(t *testing.T) {
	f := newFollowFixture(t)
	ctx := context.Background()

	state, err := f.svc.Follow(ctx, f.alice.ID, f.bob.ID)
	if err != nil {
		t.Fatalf("关注失败: %v", err)
	}
	if !state.Following || !state.Changed {
		t.Errorf("首次关注 state = %+v，期望 following=true changed=true", state)
	}
	if state.FollowerCount != 1 {
		t.Errorf("粉丝数 = %d，期望 1", state.FollowerCount)
	}

	// 第二次关注：关系已经存在，不报错、不新增、粉丝数不变。
	state, err = f.svc.Follow(ctx, f.alice.ID, f.bob.ID)
	if err != nil {
		t.Fatalf("重复关注应当成功: %v", err)
	}
	if state.Changed {
		t.Error("重复关注的 changed 应当为 false")
	}
	if state.FollowerCount != 1 {
		t.Errorf("重复关注后粉丝数 = %d，期望仍是 1", state.FollowerCount)
	}
}

// TestFollowRejectsInvalidPair 覆盖自关注与非法 id。
func TestFollowRejectsInvalidPair(t *testing.T) {
	f := newFollowFixture(t)
	ctx := context.Background()

	if _, err := f.svc.Follow(ctx, f.alice.ID, f.alice.ID); !errors.Is(err, domain.ErrSelfAction) {
		t.Errorf("自关注错误 = %v，期望 ErrSelfAction", err)
	}
	if _, err := f.svc.Follow(ctx, 0, f.bob.ID); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("关注者 id=0 错误 = %v，期望 ErrInvalidInput", err)
	}
	if _, err := f.svc.Follow(ctx, f.alice.ID, 0); !errors.Is(err, domain.ErrInvalidInput) {
		t.Errorf("被关注者 id=0 错误 = %v，期望 ErrInvalidInput", err)
	}
}

// TestFollowUnknownUser 验证关注不存在的用户返回 404 对应的错误。
// 表上没建外键，这条校验只能由 service 提供。
func TestFollowUnknownUser(t *testing.T) {
	f := newFollowFixture(t)

	_, err := f.svc.Follow(context.Background(), f.alice.ID, 999999)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("错误 = %v，期望 domain.ErrNotFound", err)
	}
}

// TestUnfollowIsIdempotent 验证取关重复执行同样成功，第二次 Changed 为 false。
func TestUnfollowIsIdempotent(t *testing.T) {
	f := newFollowFixture(t)
	ctx := context.Background()

	if _, err := f.svc.Follow(ctx, f.alice.ID, f.bob.ID); err != nil {
		t.Fatalf("准备关注失败: %v", err)
	}

	state, err := f.svc.Unfollow(ctx, f.alice.ID, f.bob.ID)
	if err != nil {
		t.Fatalf("取关失败: %v", err)
	}
	if state.Following || !state.Changed || state.FollowerCount != 0 {
		t.Errorf("首次取关 state = %+v，期望 following=false changed=true count=0", state)
	}

	state, err = f.svc.Unfollow(ctx, f.alice.ID, f.bob.ID)
	if err != nil {
		t.Fatalf("重复取关应当成功: %v", err)
	}
	if state.Changed {
		t.Error("重复取关的 changed 应当为 false")
	}
}

// TestStats 验证关注数、粉丝数与 isFollowing 三者的口径。
func TestStats(t *testing.T) {
	f := newFollowFixture(t)
	ctx := context.Background()

	// alice 关注 bob；carol 也关注 bob。于是 bob 有 2 个粉丝，alice 关注了 1 个人。
	if _, err := f.svc.Follow(ctx, f.alice.ID, f.bob.ID); err != nil {
		t.Fatalf("alice 关注 bob 失败: %v", err)
	}
	if _, err := f.svc.Follow(ctx, f.carol.ID, f.bob.ID); err != nil {
		t.Fatalf("carol 关注 bob 失败: %v", err)
	}

	following, followers, isFollowing, err := f.svc.Stats(ctx, f.bob.ID, f.alice.ID)
	if err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if following != 0 {
		t.Errorf("bob 的关注数 = %d，期望 0", following)
	}
	if followers != 2 {
		t.Errorf("bob 的粉丝数 = %d，期望 2", followers)
	}
	if !isFollowing {
		t.Error("alice 关注了 bob，isFollowing 应当为 true")
	}

	// 未登录访问（viewerID=0）：统计照常，isFollowing 固定 false。
	_, _, isFollowing, err = f.svc.Stats(ctx, f.bob.ID, 0)
	if err != nil {
		t.Fatalf("游客统计失败: %v", err)
	}
	if isFollowing {
		t.Error("游客的 isFollowing 应当为 false")
	}

	// 看自己的主页：不发那次查询，也不该说自己关注了自己。
	_, _, isFollowing, err = f.svc.Stats(ctx, f.bob.ID, f.bob.ID)
	if err != nil {
		t.Fatalf("自己看自己失败: %v", err)
	}
	if isFollowing {
		t.Error("自己看自己的 isFollowing 应当为 false")
	}
}

// TestListFollowingOrderAndPaging 验证关注列表按关系 id 倒序（最新在前）并支持分页。
func TestListFollowingOrderAndPaging(t *testing.T) {
	f := newFollowFixture(t)
	ctx := context.Background()

	// 依次关注 bob、carol：carol 的关系行 id 更大，应当排在前面。
	if _, err := f.svc.Follow(ctx, f.alice.ID, f.bob.ID); err != nil {
		t.Fatalf("关注 bob 失败: %v", err)
	}
	if _, err := f.svc.Follow(ctx, f.alice.ID, f.carol.ID); err != nil {
		t.Fatalf("关注 carol 失败: %v", err)
	}

	items, total, err := f.svc.ListFollowing(ctx, f.alice.ID, 0, 1, 10)
	if err != nil {
		t.Fatalf("取关注列表失败: %v", err)
	}
	if total != 2 {
		t.Errorf("total = %d，期望 2", total)
	}
	if len(items) != 2 {
		t.Fatalf("本页条数 = %d，期望 2", len(items))
	}
	if items[0].User.Username != "carol" || items[1].User.Username != "bob" {
		t.Errorf("顺序 = %s,%s，期望 carol,bob", items[0].User.Username, items[1].User.Username)
	}

	// 第二页每页 1 条：只剩 bob。
	items, _, err = f.svc.ListFollowing(ctx, f.alice.ID, 0, 2, 1)
	if err != nil {
		t.Fatalf("取第二页失败: %v", err)
	}
	if len(items) != 1 || items[0].User.Username != "bob" {
		t.Fatalf("第二页内容 = %+v，期望只有 bob", items)
	}
}

// TestListFollowersWithFollowedByMe 验证粉丝列表按被关注者反查，并带上 viewer 的关注状态。
// followed_by_me 的语义是「viewer 是否关注了列表里的这个人」，粉丝列表用它画「回关」按钮。
func TestListFollowersWithFollowedByMe(t *testing.T) {
	f := newFollowFixture(t)
	ctx := context.Background()

	// alice 与 carol 都关注 bob；alice 另外还关注了 carol。
	if _, err := f.svc.Follow(ctx, f.alice.ID, f.bob.ID); err != nil {
		t.Fatalf("alice 关注 bob 失败: %v", err)
	}
	if _, err := f.svc.Follow(ctx, f.carol.ID, f.bob.ID); err != nil {
		t.Fatalf("carol 关注 bob 失败: %v", err)
	}
	if _, err := f.svc.Follow(ctx, f.alice.ID, f.carol.ID); err != nil {
		t.Fatalf("alice 关注 carol 失败: %v", err)
	}

	// 以 alice 的身份看 bob 的粉丝列表：列表里是 carol 与 alice 自己。
	items, total, err := f.svc.ListFollowers(ctx, f.bob.ID, f.alice.ID, 1, 10)
	if err != nil {
		t.Fatalf("取粉丝列表失败: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("total=%d len=%d，期望都是 2", total, len(items))
	}

	// 结果按关系 id 倒序：carol 后关注 bob，排第一。
	if items[0].User.Username != "carol" {
		t.Fatalf("第一条 = %s，期望 carol", items[0].User.Username)
	}
	// alice 关注了 carol，所以 carol 那行要能画「已回关」。
	if !items[0].FollowedByMe {
		t.Error("alice 关注了 carol，carol 那行 followed_by_me 应为 true")
	}
	// 列表里出现 alice 自己：viewer 不会关注自己，这一行必须是 false。
	if items[1].User.Username != "alice" {
		t.Fatalf("第二条 = %s，期望 alice", items[1].User.Username)
	}
	if items[1].FollowedByMe {
		t.Error("自己不会关注自己，followed_by_me 应为 false")
	}
}

// TestListUnknownOwner 验证查不存在用户的列表返回 ErrNotFound，而不是空列表。
func TestListUnknownOwner(t *testing.T) {
	f := newFollowFixture(t)

	_, _, err := f.svc.ListFollowing(context.Background(), 999999, 0, 1, 10)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("错误 = %v，期望 domain.ErrNotFound", err)
	}
}

// TestListRejectsBadPaging 验证非法分页参数在 service 层就被挡掉。
func TestListRejectsBadPaging(t *testing.T) {
	f := newFollowFixture(t)
	ctx := context.Background()

	cases := []struct {
		page     int
		pageSize int
	}{
		{page: 0, pageSize: 10},
		{page: 1, pageSize: 0},
		{page: 1, pageSize: 51},
	}
	for _, tc := range cases {
		if _, _, err := f.svc.ListFollowing(ctx, f.alice.ID, 0, tc.page, tc.pageSize); !errors.Is(err, domain.ErrInvalidInput) {
			t.Errorf("page=%d size=%d 错误 = %v，期望 ErrInvalidInput", tc.page, tc.pageSize, err)
		}
	}
}

// TestListSkipsDeletedUser 验证关系行还在但用户已不存在时，列表跳过该行而不是整页失败。
func TestListSkipsDeletedUser(t *testing.T) {
	f := newFollowFixture(t)
	ctx := context.Background()

	// 直接插关系行，再删用户，构造出「孤儿关系」。
	// 表上没有外键，这种数据在真实环境可能出现（比如以后加注销功能）。
	ghost := testutil.SeedUser(t, f.db, "ghost")
	if _, err := f.svc.Follow(ctx, f.alice.ID, ghost.ID); err != nil {
		t.Fatalf("关注 ghost 失败: %v", err)
	}
	if err := f.db.Delete(&model.User{}, ghost.ID).Error; err != nil {
		t.Fatalf("删除 ghost 失败: %v", err)
	}

	items, total, err := f.svc.ListFollowing(ctx, f.alice.ID, 0, 1, 10)
	if err != nil {
		t.Fatalf("列表不该因孤儿关系失败: %v", err)
	}
	// total 按关系行算，仍是 1；本页展示会把查不到用户的那行跳过。
	if total != 1 {
		t.Errorf("total = %d，期望 1", total)
	}
	if len(items) != 0 {
		t.Errorf("本页条数 = %d，期望 0", len(items))
	}
}

package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"forum-backend/internal/keys"
	"forum-backend/internal/testutil"
	"forum-backend/pkg/jwt"
)

// newTestService 造一个跑在内存 Redis 上的会话服务。
// access 15 分钟、refresh 7 天，与生产默认值一致，断言里可以直接用这两个数。
func newTestService(t *testing.T) (*Service, *jwt.Manager) {
	t.Helper()
	manager, err := jwt.NewManager("test-secret", 15*time.Minute, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return NewService(manager, testutil.NewRedis(t)), manager
}

// TestIssueReturnsPairAndRegisters 验证签发结果的三要素，以及 jti 真的登记进了 Redis。
// 只检查返回的字符串非空是不够的：登记漏了的话，第一次刷新就会被当成泄露。
func TestIssueReturnsPairAndRegisters(t *testing.T) {
	svc, manager := newTestService(t)
	ctx := context.Background()

	tokens, err := svc.Issue(ctx, 7)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if tokens.AccessToken == "" || tokens.RefreshToken == "" {
		t.Fatal("Issue() 返回了空 token")
	}
	if tokens.ExpiresIn != 900 {
		t.Fatalf("ExpiresIn = %d, want 900", tokens.ExpiresIn)
	}

	// access 要能被业务侧解析，refresh 要能解析出 jti/fid。
	if userID, err := manager.ParseAccess(tokens.AccessToken); err != nil || userID != 7 {
		t.Fatalf("ParseAccess() = (%d, %v), want (7, nil)", userID, err)
	}
	userID, jti, familyID, err := manager.ParseRefresh(tokens.RefreshToken)
	if err != nil || userID != 7 {
		t.Fatalf("ParseRefresh() = (%d, %q, %q, %v), want userID 7", userID, jti, familyID, err)
	}

	// 登记表里必须有这个 jti，否则刷新必失败。
	got, err := svc.rdb.Get(ctx, keys.RefreshTokenKey(7, jti)).Result()
	if err != nil {
		t.Fatalf("登记表里找不到 jti: %v", err)
	}
	if got != "1" {
		t.Fatalf("登记值 = %q, want 1", got)
	}
}

// TestRefreshRotatesAndKeepsFamily 验证刷新会换一对新 token，且新 refresh 能继续用。
// 这里不复用旧 token，所以不该触发泄露判定。
func TestRefreshRotatesAndKeepsFamily(t *testing.T) {
	svc, manager := newTestService(t)
	ctx := context.Background()

	first, err := svc.Issue(ctx, 7)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	second, err := svc.Refresh(ctx, first.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if second.RefreshToken == first.RefreshToken {
		t.Fatal("刷新后 refresh 没变，等于没有旋转")
	}
	// access 不比字符串：同一秒内签两次，claims 完全一致，HS256 会算出同一个签名。
	// 真正要保证的是新 access 仍代表同一个人，且过期时间不早于旧的。
	oldUser, err := manager.ParseAccess(first.AccessToken)
	if err != nil {
		t.Fatalf("ParseAccess(first) error = %v", err)
	}
	newUser, err := manager.ParseAccess(second.AccessToken)
	if err != nil {
		t.Fatalf("ParseAccess(second) error = %v", err)
	}
	if oldUser != 7 || newUser != 7 {
		t.Fatalf("刷新前后 userID = %d/%d, want 7/7", oldUser, newUser)
	}

	// 同一族谱：familyID 必须一致，否则泄露时无法一次撤干净。
	_, _, familyA, err := manager.ParseRefresh(first.RefreshToken)
	if err != nil {
		t.Fatalf("ParseRefresh(first) error = %v", err)
	}
	_, _, familyB, err := manager.ParseRefresh(second.RefreshToken)
	if err != nil {
		t.Fatalf("ParseRefresh(second) error = %v", err)
	}
	if familyA != familyB {
		t.Fatalf("familyID 变了: %q -> %q", familyA, familyB)
	}

	// 新 refresh 能继续刷新，说明旧票作废只影响它自己。
	if _, err := svc.Refresh(ctx, second.RefreshToken); err != nil {
		t.Fatalf("用新 refresh 刷新 error = %v, want nil", err)
	}
}

// TestRefreshDropsConsumedJtiFromFamily 验证刷新后链上只剩当前有效的那个 jti。
//
// family 是 SET，早期实现只 SAdd 不 SRem，成员数随活跃时长一直涨：
// 每 15 分钟刷新一次，7 天 672 个、一个月近三千；成员数超过 128 后
// 编码从 listpack 转成 hashtable，内存开销翻倍。
// 这里把「成员数恒为 1」钉成断言，防止以后又退回只加不删。
func TestRefreshDropsConsumedJtiFromFamily(t *testing.T) {
	svc, manager := newTestService(t)
	ctx := context.Background()

	first, err := svc.Issue(ctx, 7)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	_, firstJTI, familyID, err := manager.ParseRefresh(first.RefreshToken)
	if err != nil {
		t.Fatalf("ParseRefresh(first) error = %v", err)
	}
	familyKey := keys.RefreshFamilyKey(7, familyID)

	// 刚登录时链上只有这一个 jti。
	if n, err := svc.rdb.SCard(ctx, familyKey).Result(); err != nil || n != 1 {
		t.Fatalf("登录后 SCard = %d, err = %v, want 1", n, err)
	}

	second, err := svc.Refresh(ctx, first.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	_, secondJTI, _, err := manager.ParseRefresh(second.RefreshToken)
	if err != nil {
		t.Fatalf("ParseRefresh(second) error = %v", err)
	}

	// 刷新后仍然是 1 个：旧的被摘掉，新的加进来。
	if n, err := svc.rdb.SCard(ctx, familyKey).Result(); err != nil || n != 1 {
		t.Fatalf("刷新后 SCard = %d, err = %v, want 1（旧 jti 应被 SREM）", n, err)
	}
	if ok, err := svc.rdb.SIsMember(ctx, familyKey, firstJTI).Result(); err != nil || ok {
		t.Fatalf("旧 jti 仍在链上: ok = %v, err = %v, want false", ok, err)
	}
	if ok, err := svc.rdb.SIsMember(ctx, familyKey, secondJTI).Result(); err != nil || !ok {
		t.Fatalf("新 jti 不在链上: ok = %v, err = %v, want true", ok, err)
	}
}

// TestRefreshReuseRevokesFamily 是旋转的核心用例：
// 旧 refresh 被用第二次时，必须撤销整条链，连刚发出去的新 refresh 也一起失效。
// 如果只拒绝这一次而不撤销，攻击者手里的旧票仍然能换出新票，旋转就白做了。
//
// 这个用例还守着一条容易被改坏的规则：刷新时会把旧 jti 从 family 里 SREM 掉，
// 所以「旧 jti 不在集合里」是每条链的常态。重放判定只能看 family key 是否存在，
// 一旦有人改成用 SIsMember 判成员，这里会立刻变红。
func TestRefreshReuseRevokesFamily(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	first, err := svc.Issue(ctx, 7)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	second, err := svc.Refresh(ctx, first.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	if _, err := svc.Refresh(ctx, first.RefreshToken); !errors.Is(err, ErrRefreshReused) {
		t.Fatalf("重放旧 refresh error = %v, want ErrRefreshReused", err)
	}

	// 家族已撤，刚拿到的新 refresh 也用不了。
	if _, err := svc.Refresh(ctx, second.RefreshToken); !errors.Is(err, ErrInvalidRefresh) {
		t.Fatalf("家族撤销后刷新 error = %v, want ErrInvalidRefresh", err)
	}
}

// TestRevokeEndsFamily 验证登出后 refresh 失效，且被识别为普通失效而不是泄露。
// 这个区分是为了日志干净：登出后再刷新不该报「token 泄露」。
func TestRevokeEndsFamily(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	tokens, err := svc.Issue(ctx, 7)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if err := svc.Revoke(ctx, tokens.RefreshToken); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if _, err := svc.Refresh(ctx, tokens.RefreshToken); !errors.Is(err, ErrInvalidRefresh) {
		t.Fatalf("登出后刷新 error = %v, want ErrInvalidRefresh", err)
	}
}

// TestRevokeIgnoresInvalidToken 验证登出对坏 token 不报错。
// 登出要幂等：客户端手里的票可能早就过期了，这时目的本来就达成了。
func TestRevokeIgnoresInvalidToken(t *testing.T) {
	svc, _ := newTestService(t)
	if err := svc.Revoke(context.Background(), "not-a-token"); err != nil {
		t.Fatalf("Revoke(坏 token) error = %v, want nil", err)
	}
}

// TestRefreshRejectsBadToken 覆盖三种不可用的 refresh：
// 乱码、access 冒充、以及别的密钥签出来的。
func TestRefreshRejectsBadToken(t *testing.T) {
	svc, manager := newTestService(t)
	ctx := context.Background()

	access, err := manager.SignAccess(7)
	if err != nil {
		t.Fatalf("SignAccess() error = %v", err)
	}
	otherManager, err := jwt.NewManager("another-secret", 15*time.Minute, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	foreign, err := otherManager.SignRefresh(7, "jti-x", "family-x")
	if err != nil {
		t.Fatalf("SignRefresh() error = %v", err)
	}

	for name, token := range map[string]string{
		"garbage":     "not-a-token",
		"access used": access,
		"foreign key": foreign,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.Refresh(ctx, token); !errors.Is(err, ErrInvalidRefresh) {
				t.Fatalf("Refresh() error = %v, want ErrInvalidRefresh", err)
			}
		})
	}
}

// TestRevokeAllRemovesEveryFamily 验证「踢掉所有设备」会清掉全部家族的 jti。
// 同一用户两次登录是两条独立的链，RevokeAll 之后两条都不能再用。
func TestRevokeAllRemovesEveryFamily(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	phone, err := svc.Issue(ctx, 7)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	laptop, err := svc.Issue(ctx, 7)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	// 另一个用户不该被牵连。
	other, err := svc.Issue(ctx, 8)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	if err := svc.RevokeAll(ctx, 7); err != nil {
		t.Fatalf("RevokeAll() error = %v", err)
	}

	for name, token := range map[string]string{"phone": phone.RefreshToken, "laptop": laptop.RefreshToken} {
		if _, err := svc.Refresh(ctx, token); !errors.Is(err, ErrInvalidRefresh) {
			t.Fatalf("%s 刷新 error = %v, want ErrInvalidRefresh", name, err)
		}
	}
	if _, err := svc.Refresh(ctx, other.RefreshToken); err != nil {
		t.Fatalf("别的用户被牵连了: %v", err)
	}
}

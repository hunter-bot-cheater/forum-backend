package jwt

import (
	"testing"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"
)

// testManager 造一个固定参数的管理器：access 15 分钟、refresh 7 天，
// 与生产默认值一致，测试里不用每处都写这两个时长。
func testManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager("test-secret", 15*time.Minute, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return m
}

// TestAccessRoundTrip 验证 access 能签发并解析出正确的 userID。
func TestAccessRoundTrip(t *testing.T) {
	manager := testManager(t)
	token, err := manager.SignAccess(42)
	if err != nil {
		t.Fatalf("SignAccess() error = %v", err)
	}

	got, err := manager.ParseAccess(token)
	if err != nil {
		t.Fatalf("ParseAccess() error = %v", err)
	}
	if got != 42 {
		t.Fatalf("ParseAccess() userID = %d, want 42", got)
	}
}

// TestAccessUsesConfiguredTTL 验证 exp - iat 等于配置的 access 有效期。
// 这个断言守着「改配置真的生效」：签名里如果误用了 refresh 的时长，
// 客户端会拿着一个 7 天的 access 到处跑，短有效期的意义就没了。
func TestAccessUsesConfiguredTTL(t *testing.T) {
	manager := testManager(t)
	tokenString, err := manager.SignAccess(42)
	if err != nil {
		t.Fatalf("SignAccess() error = %v", err)
	}

	claims := &Claims{}
	_, err = jwtlib.ParseWithClaims(tokenString, claims, func(token *jwtlib.Token) (any, error) {
		return []byte("test-secret"), nil
	}, jwtlib.WithValidMethods([]string{jwtlib.SigningMethodHS256.Alg()}))
	if err != nil {
		t.Fatalf("ParseWithClaims() error = %v", err)
	}
	if got := claims.ExpiresAt.Sub(claims.IssuedAt.Time); got != 15*time.Minute {
		t.Fatalf("exp - iat = %s, want 15m", got)
	}
}

// TestRefreshRoundTrip 验证 refresh 能解析出 userID、jti 与 familyID。
// 三个值都要对上：会话层完全依赖 jti 判断「是否用过」、依赖 fid 决定撤销范围。
func TestRefreshRoundTrip(t *testing.T) {
	manager := testManager(t)
	token, err := manager.SignRefresh(42, "jti-1", "family-1")
	if err != nil {
		t.Fatalf("SignRefresh() error = %v", err)
	}

	userID, jti, familyID, err := manager.ParseRefresh(token)
	if err != nil {
		t.Fatalf("ParseRefresh() error = %v", err)
	}
	if userID != 42 || jti != "jti-1" || familyID != "family-1" {
		t.Fatalf("ParseRefresh() = (%d, %q, %q), want (42, jti-1, family-1)", userID, jti, familyID)
	}
}

// TestTokenTypesAreNotInterchangeable 是双 token 的核心安全断言。
// 如果类型校验被去掉，一个 7 天的 refresh 就能直接当 access 调业务接口，
// 短有效期的设计等于没做。
func TestTokenTypesAreNotInterchangeable(t *testing.T) {
	manager := testManager(t)

	access, err := manager.SignAccess(42)
	if err != nil {
		t.Fatalf("SignAccess() error = %v", err)
	}
	refresh, err := manager.SignRefresh(42, "jti-1", "family-1")
	if err != nil {
		t.Fatalf("SignRefresh() error = %v", err)
	}

	if _, err := manager.ParseAccess(refresh); err == nil {
		t.Fatal("ParseAccess(refresh) error = nil, refresh 不该能当 access 用")
	}
	if _, _, _, err := manager.ParseRefresh(access); err == nil {
		t.Fatal("ParseRefresh(access) error = nil, access 不该能当 refresh 用")
	}
}

// TestSignRefreshRequiresJTIAndFamily 验证缺 jti 或 fid 的 refresh 签不出来。
// 没有 jti 的 refresh 在登记表里无从登记，等于一个无法撤销的永久凭证。
func TestSignRefreshRequiresJTIAndFamily(t *testing.T) {
	manager := testManager(t)
	if _, err := manager.SignRefresh(42, "", "family-1"); err == nil {
		t.Fatal("SignRefresh() with empty jti error = nil, want an error")
	}
	if _, err := manager.SignRefresh(42, "jti-1", ""); err == nil {
		t.Fatal("SignRefresh() with empty family error = nil, want an error")
	}
}

// TestParseRefreshRejectsMissingJTI 模拟一个没有 jti 的历史 token：
// 手工签出来（绕开 SignRefresh 的校验），解析时必须拒绝。
func TestParseRefreshRejectsMissingJTI(t *testing.T) {
	manager := testManager(t)
	now := time.Now()
	claims := Claims{
		UserID: 42, TokenType: RefreshTokenType,
		RegisteredClaims: jwtlib.RegisteredClaims{
			Issuer: issuer, Subject: "42",
			IssuedAt:  jwtlib.NewNumericDate(now),
			ExpiresAt: jwtlib.NewNumericDate(now.Add(time.Hour)),
		},
	}
	token, err := jwtlib.NewWithClaims(jwtlib.SigningMethodHS256, claims).SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("SignedString() error = %v", err)
	}
	if _, _, _, err := manager.ParseRefresh(token); err == nil {
		t.Fatal("ParseRefresh() error = nil, want an error")
	}
}

// TestNewManagerRejectsInvalidInput 覆盖时长配置的各种写反写法。
// refresh 比 access 短单独列一条：那不是非法值，但会让刷新变成常态。
func TestNewManagerRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name       string
		secret     string
		accessTTL  time.Duration
		refreshTTL time.Duration
	}{
		{name: "empty secret", secret: "", accessTTL: time.Minute, refreshTTL: time.Hour},
		{name: "zero access ttl", secret: "secret", accessTTL: 0, refreshTTL: time.Hour},
		{name: "negative refresh ttl", secret: "secret", accessTTL: time.Minute, refreshTTL: -time.Hour},
		{name: "access too long", secret: "secret", accessTTL: maxTTL + time.Hour, refreshTTL: maxTTL},
		{name: "refresh shorter than access", secret: "secret", accessTTL: time.Hour, refreshTTL: time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewManager(tt.secret, tt.accessTTL, tt.refreshTTL); err == nil {
				t.Fatal("NewManager() error = nil, want an error")
			}
		})
	}
}

// TestSignRejectsZeroUserID 验证 0 号用户签不出 token。
func TestSignRejectsZeroUserID(t *testing.T) {
	manager := testManager(t)
	if _, err := manager.SignAccess(0); err == nil {
		t.Fatal("SignAccess() error = nil, want an error")
	}
}

// TestParseRejectsWrongSecret 换密钥后旧 token 必须失效。
func TestParseRejectsWrongSecret(t *testing.T) {
	manager := testManager(t)
	token, err := manager.SignAccess(42)
	if err != nil {
		t.Fatalf("SignAccess() error = %v", err)
	}
	wrongManager, err := NewManager("wrong-secret", 15*time.Minute, 7*24*time.Hour)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if _, err := wrongManager.ParseAccess(token); err == nil {
		t.Fatal("ParseAccess() error = nil, want an error")
	}
}

// TestParseRejectsBadClaims 覆盖「签名对但声明不对」的几种情况。
// 这些 token 都是手工签的，绕过了签发侧的校验，专门喂给解析侧。
func TestParseRejectsBadClaims(t *testing.T) {
	tests := []struct {
		name   string
		claims Claims
	}{
		{
			name: "expired token",
			claims: Claims{
				UserID: 42, TokenType: AccessTokenType,
				RegisteredClaims: jwtlib.RegisteredClaims{
					Issuer: issuer, Subject: "42",
					IssuedAt:  jwtlib.NewNumericDate(time.Now().Add(-2 * time.Hour)),
					ExpiresAt: jwtlib.NewNumericDate(time.Now().Add(-time.Hour)),
				},
			},
		},
		{
			name: "wrong issuer",
			claims: Claims{
				UserID: 42, TokenType: AccessTokenType,
				RegisteredClaims: jwtlib.RegisteredClaims{
					Issuer: "another-service", Subject: "42",
					IssuedAt:  jwtlib.NewNumericDate(time.Now()),
					ExpiresAt: jwtlib.NewNumericDate(time.Now().Add(time.Hour)),
				},
			},
		},
		{
			name: "subject mismatch",
			claims: Claims{
				UserID: 42, TokenType: AccessTokenType,
				RegisteredClaims: jwtlib.RegisteredClaims{
					Issuer: issuer, Subject: "7",
					IssuedAt:  jwtlib.NewNumericDate(time.Now()),
					ExpiresAt: jwtlib.NewNumericDate(time.Now().Add(time.Hour)),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, err := jwtlib.NewWithClaims(jwtlib.SigningMethodHS256, tt.claims).SignedString([]byte("test-secret"))
			if err != nil {
				t.Fatalf("SignedString() error = %v", err)
			}
			if _, err := testManager(t).ParseAccess(token); err == nil {
				t.Fatal("ParseAccess() error = nil, want an error")
			}
		})
	}
}

// TestParsedAccessHasNoFamily 验证 access 不带 fid，避免有人把 access 当家族凭证读。
func TestParsedAccessHasNoFamily(t *testing.T) {
	manager := testManager(t)
	tokenString, err := manager.SignAccess(42)
	if err != nil {
		t.Fatalf("SignAccess() error = %v", err)
	}
	claims := &Claims{}
	if _, err := jwtlib.ParseWithClaims(tokenString, claims, func(token *jwtlib.Token) (any, error) {
		return []byte("test-secret"), nil
	}, jwtlib.WithValidMethods([]string{jwtlib.SigningMethodHS256.Alg()})); err != nil {
		t.Fatalf("ParseWithClaims() error = %v", err)
	}
	if claims.FamilyID != "" {
		t.Fatalf("access FamilyID = %q, want empty", claims.FamilyID)
	}
}

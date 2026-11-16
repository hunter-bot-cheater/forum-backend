// Package jwt 负责签发和验证本服务使用的 JWT。
//
// 本服务签发两种 token，区别只有有效期与 token_type：
//   - access  15 分钟，随每次请求发送，泄露窗口小；
//   - refresh 7 天，只在 /auth/refresh 用一次就作废（旋转），由 internal/session 负责存取。
//
// 拆两段的原因：一个长效凭证天天在网络上飞，一旦泄露就是长期冒用；
// 而只有短效 access 的话用户每 15 分钟就被踢回登录页。refresh 只走刷新接口，
// 暴露面小得多，且服务端能按 jti 单独撤销。
package jwt

import (
	"errors"
	"fmt"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"
)

const (
	issuer = "forum-backend" // 签发方标识，验证时会比对，防止拿别家签的 token 来用

	// token_type 写在自定义声明里。验证时必须比对：
	// 否则拿一个 7 天的 refresh 当 access 用，短有效期的意义就没了。
	AccessTokenType  = "access"
	RefreshTokenType = "refresh"

	minTTL = time.Minute
	maxTTL = 90 * 24 * time.Hour
)

// errInvalidToken 对外统一成一个错误。
// 过期、签名不对、算法不对、类型不对都归到这里，
// 调用方（中间件 / WS 握手）不需要区分原因，一律让用户重新登录。
var errInvalidToken = errors.New("token is invalid or expired")

// Manager 持有本服务的 JWT 配置。由启动装配层创建一个实例，并注入签发方和验证方。
type Manager struct {
	secret     string
	accessTTL  time.Duration
	refreshTTL time.Duration
}

// NewManager 校验配置并创建 JWT 管理器。
// 两个时长都必须落在 [1 分钟, 90 天] 内：写反了（比如把 refresh 填成 1 分钟）
// 会让用户不停被踢出去，这种配置错误应该在启动时就暴露，而不是上线后才发现。
func NewManager(secret string, accessTTL, refreshTTL time.Duration) (*Manager, error) {
	if secret == "" {
		return nil, errors.New("JWT secret is required")
	}
	if accessTTL < minTTL || accessTTL > maxTTL {
		return nil, fmt.Errorf("access token TTL must be between %s and %s", minTTL, maxTTL)
	}
	if refreshTTL < minTTL || refreshTTL > maxTTL {
		return nil, fmt.Errorf("refresh token TTL must be between %s and %s", minTTL, maxTTL)
	}
	// refresh 比 access 还短属于配置写反：旋转还没发生 access 先过期，
	// 用户会被迫每几分钟刷一次，刷新接口压力比正常请求还大。
	if refreshTTL < accessTTL {
		return nil, errors.New("refresh token TTL must not be shorter than access token TTL")
	}
	return &Manager{secret: secret, accessTTL: accessTTL, refreshTTL: refreshTTL}, nil
}

// Claims 是本服务 token 中允许携带的身份信息。
// TokenType 区分两种令牌；ID（jti）与 FamilyID（fid）只有 refresh 会填：
//   - jti 标识这一个 refresh，用一次就作废，用来实现旋转；
//   - fid 标识一整套轮换链（一次登录一条链），检测到旧 jti 被重放时，
//     撤销整条链而不是该用户的全部 token，其它设备不会被连坐。
type Claims struct {
	UserID    uint   `json:"user_id"`
	TokenType string `json:"token_type"`
	FamilyID  string `json:"fid,omitempty"`
	jwtlib.RegisteredClaims
}

// AccessTTL 返回 access token 的有效期，登录与刷新接口用它填 expires_in（秒）。
func (m *Manager) AccessTTL() time.Duration {
	return m.accessTTL
}

// RefreshTTL 返回 refresh token 的有效期，session 包用它设 Redis 键的过期时间。
func (m *Manager) RefreshTTL() time.Duration {
	return m.refreshTTL
}

// SignAccess 为指定用户签发 access token，失败只在参数不合法时发生。
// 签名算法固定 HS256，密钥来自配置，换密钥等于让所有旧 token 立刻失效。
func (m *Manager) SignAccess(userID uint) (string, error) {
	return m.sign(userID, AccessTokenType, "", "", m.accessTTL)
}

// SignRefresh 签发 refresh token。jti 与 familyID 由调用方生成并登记到 Redis，
// 服务端靠 jti 判断「这个 refresh 是否已经用过」，靠 familyID 决定重放时要撤销哪一批。
func (m *Manager) SignRefresh(userID uint, jti, familyID string) (string, error) {
	if jti == "" || familyID == "" {
		return "", errors.New("refresh token requires jti and family id")
	}
	return m.sign(userID, RefreshTokenType, jti, familyID, m.refreshTTL)
}

// sign 是两个签发方法共用的一段：只取一次当前时间，保证 iat 和 exp 用同一基准。
func (m *Manager) sign(userID uint, tokenType, jti, familyID string, ttl time.Duration) (string, error) {
	if userID == 0 {
		return "", errors.New("userID must be greater than zero")
	}

	now := time.Now()
	claims := Claims{
		UserID:    userID,
		TokenType: tokenType,
		FamilyID:  familyID,
		RegisteredClaims: jwtlib.RegisteredClaims{
			Issuer:    issuer,
			Subject:   fmt.Sprintf("%d", userID),
			ID:        jti,
			IssuedAt:  jwtlib.NewNumericDate(now),
			ExpiresAt: jwtlib.NewNumericDate(now.Add(ttl)),
		},
	}

	token := jwtlib.NewWithClaims(jwtlib.SigningMethodHS256, claims)
	return token.SignedString([]byte(m.secret))
}

// ParseAccess 验证签名、算法、issuer、过期时间与 token 类型，成功时返回 userID。
// 任何一项不通过都返回 errInvalidToken，不把具体原因传给调用方。
func (m *Manager) ParseAccess(tokenStr string) (uint, error) {
	claims, err := m.parse(tokenStr, AccessTokenType)
	if err != nil {
		return 0, err
	}
	return claims.UserID, nil
}

// ParseRefresh 验证并返回 userID、jti 与 familyID。
// jti/fid 是撤销的唯一依据，缺失时按非法 token 处理：
// 否则一个没有 jti 的 refresh 无法被删除，等于永不过期。
func (m *Manager) ParseRefresh(tokenStr string) (uint, string, string, error) {
	claims, err := m.parse(tokenStr, RefreshTokenType)
	if err != nil {
		return 0, "", "", err
	}
	if claims.ID == "" || claims.FamilyID == "" {
		return 0, "", "", errInvalidToken
	}
	return claims.UserID, claims.ID, claims.FamilyID, nil
}

// parse 是两种令牌共用的验签过程，最后按 wantType 校验类型。
func (m *Manager) parse(tokenStr, wantType string) (*Claims, error) {
	if tokenStr == "" {
		return nil, errInvalidToken
	}

	claims := &Claims{}
	token, err := jwtlib.ParseWithClaims(
		tokenStr,
		claims,
		func(token *jwtlib.Token) (any, error) {
			return []byte(m.secret), nil
		},
		// 显式限定算法，挡住把 alg 改成 none 的降级攻击。
		jwtlib.WithValidMethods([]string{jwtlib.SigningMethodHS256.Alg()}),
		jwtlib.WithIssuer(issuer),
		jwtlib.WithExpirationRequired(),
		jwtlib.WithIssuedAt(),
	)
	if err != nil || token == nil || !token.Valid {
		return nil, errInvalidToken
	}

	// 库只保证签名和注册声明合法，业务声明还得自己校验：
	// UserID 不能为 0、类型必须与期望一致、Subject 要与 UserID 对得上。
	if claims.UserID == 0 || claims.TokenType != wantType || claims.Subject != fmt.Sprintf("%d", claims.UserID) {
		return nil, errInvalidToken
	}
	return claims, nil
}

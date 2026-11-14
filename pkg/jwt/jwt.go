// Package jwt 负责签发和验证本服务使用的 JWT。
package jwt

import (
	"errors"
	"fmt"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"
)

const (
	issuer                = "forum-backend"
	accessTokenType       = "access"
	maxTokenLifetimeHours = 24 * 365
)

var errInvalidToken = errors.New("token is invalid or expired")

// Manager 持有本服务的 JWT 配置。由启动装配层创建一个实例，并注入签发方和验证方。
type Manager struct {
	secret      string
	expireHours int
}

// NewManager 校验配置并创建 JWT 管理器。
func NewManager(secret string, expireHours int) (*Manager, error) {
	if secret == "" {
		return nil, errors.New("JWT secret is required")
	}
	if expireHours <= 0 || expireHours > maxTokenLifetimeHours {
		return nil, fmt.Errorf("expireHours must be between 1 and %d", maxTokenLifetimeHours)
	}
	return &Manager{secret: secret, expireHours: expireHours}, nil
}

// Claims 是本服务 token 中允许携带的身份信息。
// TokenType 用于区分访问令牌；以后增加 refresh token 时不能把它当访问令牌使用。
type Claims struct {
	UserID    uint   `json:"user_id"`
	TokenType string `json:"token_type"`
	jwtlib.RegisteredClaims
}

// Sign 为指定用户签发 access token。  给Manager增加sign方法
func (m *Manager) Sign(userID uint) (string, error) {
	if userID == 0 {
		return "", errors.New("userID must be greater than zero")
	}

	// 只取一次当前时间，保证 iat 和 exp 使用同一个时间基准。
	now := time.Now()
	ttl := time.Duration(m.expireHours) * time.Hour
	claims := Claims{
		UserID:    userID,
		TokenType: accessTokenType,
		RegisteredClaims: jwtlib.RegisteredClaims{
			Issuer:    issuer,
			Subject:   fmt.Sprintf("%d", userID),
			IssuedAt:  jwtlib.NewNumericDate(now),
			ExpiresAt: jwtlib.NewNumericDate(now.Add(ttl)),
		},
	}

	token := jwtlib.NewWithClaims(jwtlib.SigningMethodHS256, claims)
	return token.SignedString([]byte(m.secret))
}

// Parse 验证签名、算法、issuer、过期时间和 token 类型，成功时返回 userID。
func (m *Manager) Parse(tokenStr string) (uint, error) {
	if tokenStr == "" {
		return 0, errInvalidToken
	}

	claims := &Claims{}
	token, err := jwtlib.ParseWithClaims(
		tokenStr,
		claims,
		func(token *jwtlib.Token) (any, error) {
			return []byte(m.secret), nil
		},
		jwtlib.WithValidMethods([]string{jwtlib.SigningMethodHS256.Alg()}),
		jwtlib.WithIssuer(issuer),
		jwtlib.WithExpirationRequired(),
		jwtlib.WithIssuedAt(),
	)
	if err != nil || token == nil || !token.Valid {
		return 0, errInvalidToken
	}
	if claims.UserID == 0 || claims.TokenType != accessTokenType || claims.Subject != fmt.Sprintf("%d", claims.UserID) {
		return 0, errInvalidToken
	}
	return claims.UserID, nil
}

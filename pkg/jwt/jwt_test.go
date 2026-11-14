package jwt

import (
	"testing"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"
)

func TestGenerateAndParse(t *testing.T) {
	manager, err := NewManager("test-secret", 24)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	token, err := manager.Sign(42)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	got, err := manager.Parse(token)
	if err != nil {
		t.Fatalf("Manager.Parse() error = %v", err)
	}
	if got != 42 {
		t.Fatalf("Parse() userID = %d, want 42", got)
	}
}

func TestGenerateUsesConsistentIssueAndExpirationTimes(t *testing.T) {
	manager, err := NewManager("test-secret", 24)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	tokenString, err := manager.Sign(42)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	claims := &Claims{}
	_, err = jwtlib.ParseWithClaims(tokenString, claims, func(token *jwtlib.Token) (any, error) {
		return []byte("test-secret"), nil
	}, jwtlib.WithValidMethods([]string{jwtlib.SigningMethodHS256.Alg()}))
	if err != nil {
		t.Fatalf("ParseWithClaims() error = %v", err)
	}
	if got := claims.ExpiresAt.Sub(claims.IssuedAt.Time); got != 24*time.Hour {
		t.Fatalf("exp - iat = %s, want 24h", got)
	}
}

func TestNewManagerRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name        string
		secret      string
		expireHours int
	}{
		{name: "empty secret", secret: "", expireHours: 1},
		{name: "zero expiration", secret: "secret", expireHours: 0},
		{name: "negative expiration", secret: "secret", expireHours: -1},
		{name: "expiration too large", secret: "secret", expireHours: maxTokenLifetimeHours + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewManager(tt.secret, tt.expireHours); err == nil {
				t.Fatal("NewManager() error = nil, want an error")
			}
		})
	}
}

func TestSignRejectsZeroUserID(t *testing.T) {
	manager, err := NewManager("test-secret", 1)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if _, err := manager.Sign(0); err == nil {
		t.Fatal("Sign() error = nil, want an error")
	}
}

func TestParseRejectsWrongSecret(t *testing.T) {
	manager, err := NewManager("correct-secret", 1)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	token, err := manager.Sign(42)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	wrongManager, err := NewManager("wrong-secret", 1)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if _, err := wrongManager.Parse(token); err == nil {
		t.Fatal("Manager.Parse() error = nil, want an error")
	}
}

func TestParseRejectsExpiredAndWrongTypeTokens(t *testing.T) {
	tests := []struct {
		name      string
		claims    Claims
		wantValid bool
	}{
		{
			name: "expired token",
			claims: Claims{
				UserID: 42, TokenType: accessTokenType,
				RegisteredClaims: jwtlib.RegisteredClaims{
					Issuer: issuer, Subject: "42",
					IssuedAt:  jwtlib.NewNumericDate(time.Now().Add(-2 * time.Hour)),
					ExpiresAt: jwtlib.NewNumericDate(time.Now().Add(-time.Hour)),
				},
			},
		},
		{
			name: "wrong token type",
			claims: Claims{
				UserID: 42, TokenType: "refresh",
				RegisteredClaims: jwtlib.RegisteredClaims{
					Issuer: issuer, Subject: "42",
					IssuedAt:  jwtlib.NewNumericDate(time.Now()),
					ExpiresAt: jwtlib.NewNumericDate(time.Now().Add(time.Hour)),
				},
			},
		},
		{
			name: "wrong issuer",
			claims: Claims{
				UserID: 42, TokenType: accessTokenType,
				RegisteredClaims: jwtlib.RegisteredClaims{
					Issuer: "another-service", Subject: "42",
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
			manager, err := NewManager("test-secret", 1)
			if err != nil {
				t.Fatalf("NewManager() error = %v", err)
			}
			if _, err := manager.Parse(token); err == nil {
				t.Fatal("Parse() error = nil, want an error")
			}
		})
	}
}

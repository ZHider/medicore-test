// Package jwt 负责 access_token 的签发与校验。
//
// 分工（spec §2.2）：
//   - access_token：JWT，无状态，2h，携带 uid/role，用于每个请求鉴权。
//   - refresh_token：不透明随机串，7d，存 Redis，可撤销（见 repository.TokenRepo）。
//
// 之所以 refresh_token 不用 JWT：无状态令牌无法撤销，而这正是刷新令牌最需要的性质。
package jwt

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// 令牌校验错误。handler 据此区分"未认证"与"已过期"。
var (
	ErrInvalidToken = errors.New("invalid token")
	ErrExpiredToken = errors.New("token expired")
)

// Claims 是 access_token 的载荷。
type Claims struct {
	UserID uint64 `json:"uid"`
	Role   string `json:"role"`
	jwt.RegisteredClaims
}

// Manager 签发与校验 access_token。
type Manager struct {
	secret    []byte
	issuer    string
	accessTTL time.Duration
}

// NewManager 构造 Manager。accessTTL 为 access_token 有效期。
func NewManager(secret, issuer string, accessTTL time.Duration) *Manager {
	return &Manager{
		secret:    []byte(secret),
		issuer:    issuer,
		accessTTL: accessTTL,
	}
}

// AccessTTL 返回 access_token 有效期（秒），用于响应体中的 expires_in。
func (m *Manager) AccessTTLSeconds() int64 {
	return int64(m.accessTTL.Seconds())
}

// IssueAccess 签发 access_token。
func (m *Manager) IssueAccess(userID uint64, role string) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID: userID,
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    m.issuer,
			Subject:   fmt.Sprintf("%d", userID),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(m.accessTTL)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(m.secret)
	if err != nil {
		return "", fmt.Errorf("sign access token: %w", err)
	}
	return signed, nil
}

// Parse 校验并解析 access_token。
// 显式限定 HS256，防止 alg 混淆攻击（例如伪造 alg=none 或换成 RS256 用公钥当密钥）。
func (m *Manager) Parse(tokenString string) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("%w: unexpected signing method %v", ErrInvalidToken, t.Header["alg"])
		}
		return m.secret, nil
	}, jwt.WithIssuer(m.issuer), jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpiredToken
		}
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	return claims, nil
}

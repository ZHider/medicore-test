package jwt

import (
	"testing"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSecret = "test-secret"

func TestIssueAndParse(t *testing.T) {
	m := NewManager(testSecret, "medicore", time.Hour)

	token, err := m.IssueAccess(42, "patient")
	require.NoError(t, err)
	assert.Equal(t, int64(3600), m.AccessTTLSeconds())

	claims, err := m.Parse(token)
	require.NoError(t, err)
	assert.Equal(t, uint64(42), claims.UserID)
	assert.Equal(t, "patient", claims.Role)
	assert.Equal(t, "medicore", claims.Issuer)
}

func TestParse_ExpiredToken(t *testing.T) {
	// 负数 TTL 直接产出已过期令牌
	m := NewManager(testSecret, "medicore", -time.Minute)

	token, err := m.IssueAccess(1, "patient")
	require.NoError(t, err)

	_, err = m.Parse(token)
	assert.ErrorIs(t, err, ErrExpiredToken)
}

func TestParse_WrongSecret(t *testing.T) {
	issuer := NewManager("secret-a", "medicore", time.Hour)
	token, err := issuer.IssueAccess(1, "patient")
	require.NoError(t, err)

	verifier := NewManager("secret-b", "medicore", time.Hour)
	_, err = verifier.Parse(token)
	assert.ErrorIs(t, err, ErrInvalidToken)
}

func TestParse_WrongIssuer(t *testing.T) {
	issuer := NewManager(testSecret, "issuer-a", time.Hour)
	token, err := issuer.IssueAccess(1, "patient")
	require.NoError(t, err)

	verifier := NewManager(testSecret, "issuer-b", time.Hour)
	_, err = verifier.Parse(token)
	assert.ErrorIs(t, err, ErrInvalidToken)
}

// 关键安全用例：alg=none 的伪造令牌必须被拒绝。
func TestParse_RejectsNoneAlgorithm(t *testing.T) {
	claims := Claims{
		UserID: 1,
		Role:   "admin",
		RegisteredClaims: jwtlib.RegisteredClaims{
			Issuer:    "medicore",
			ExpiresAt: jwtlib.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	forged, err := jwtlib.NewWithClaims(jwtlib.SigningMethodNone, claims).
		SignedString(jwtlib.UnsafeAllowNoneSignatureType)
	require.NoError(t, err)

	m := NewManager(testSecret, "medicore", time.Hour)
	_, err = m.Parse(forged)
	assert.ErrorIs(t, err, ErrInvalidToken)
}

func TestParse_Garbage(t *testing.T) {
	m := NewManager(testSecret, "medicore", time.Hour)
	for _, s := range []string{"", "not-a-jwt", "a.b.c"} {
		_, err := m.Parse(s)
		assert.ErrorIs(t, err, ErrInvalidToken)
	}
}

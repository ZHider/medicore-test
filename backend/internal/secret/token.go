package secret

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// RefreshTokenBytes 是刷新令牌的随机字节数（256 位熵）。
const RefreshTokenBytes = 32

// RandomToken 生成不可预测的随机令牌，用于刷新令牌等不透明凭证。
func RandomToken() (string, error) {
	b := make([]byte, RefreshTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

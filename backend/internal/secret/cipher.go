// Package secret 提供敏感字段的加密与摘要工具（spec §2.2）。
//
// 设计要点：
//   - 手机号/身份证用 AES-256-GCM 加密存储，密文格式为 nonce(12B) || ciphertext+tag。
//   - 同时保存确定性摘要用于唯一性校验与登录查找。
//   - 摘要用 HMAC-SHA256 而非 SHA256(明文+盐)：keyed hash 不会因为拼接方式引入长度扩展问题。
//     注意摘要必须是确定性的——若用逐行随机盐，登录时就无法由手机号反查用户。
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// 密钥长度常量。
const (
	aesKeyBytes  = 32 // AES-256
	aesKeyHexLen = aesKeyBytes * 2
)

// ErrCiphertextInvalid 表示密文格式非法或校验失败（被篡改 / 密钥不匹配）。
var ErrCiphertextInvalid = errors.New("ciphertext invalid")

// Cipher 封装 AES-GCM 加解密。
type Cipher struct {
	gcm cipher.AEAD
}

// NewCipher 由 64 位十六进制字符串构造 AES-256-GCM。
func NewCipher(keyHex string) (*Cipher, error) {
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, fmt.Errorf("aes key must be hex: %w", err)
	}
	if len(key) != aesKeyBytes {
		return nil, fmt.Errorf("aes key must be %d bytes (%d hex chars), got %d bytes",
			aesKeyBytes, aesKeyHexLen, len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}
	return &Cipher{gcm: gcm}, nil
}

// Encrypt 加密明文，返回 nonce || ciphertext。
func (c *Cipher) Encrypt(plaintext string) ([]byte, error) {
	nonce := make([]byte, c.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	// Seal 的第一个参数是 dst，传 nonce 让密文直接追加在 nonce 之后，省一次拷贝。
	return c.gcm.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

// Decrypt 解密 Encrypt 产生的密文。
func (c *Cipher) Decrypt(ciphertext []byte) (string, error) {
	nonceSize := c.gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", ErrCiphertextInvalid
	}
	plaintext, err := c.gcm.Open(nil, ciphertext[:nonceSize], ciphertext[nonceSize:], nil)
	if err != nil {
		return "", ErrCiphertextInvalid
	}
	return string(plaintext), nil
}

// Digest 计算确定性摘要，用于唯一约束与登录查找。
func Digest(value, pepper string) string {
	mac := hmac.New(sha256.New, []byte(pepper))
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

// Package traceid 负责链路追踪 ID 的生成与 context 传递。
// trace_id 为 32 位十六进制字符串，与 audit_logs.trace_id CHAR(32) 对齐。
package traceid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

type ctxKey struct{}

// New 生成一个新的 trace_id。
func New() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败属于不可恢复的环境问题，返回全零占位以保证请求不被阻断。
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(b)
}

// WithContext 将 trace_id 写入 context。
func WithContext(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// FromContext 从 context 读取 trace_id，不存在时返回空串。
func FromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		return v
	}
	return ""
}

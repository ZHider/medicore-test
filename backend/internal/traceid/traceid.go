// Package traceid 负责链路追踪 ID 的生成、校验与 context 传递。
// trace_id 为 32 位十六进制字符串，与 audit_logs.trace_id CHAR(32) 对齐。
package traceid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// TraceIDLen 是 trace_id 的固定长度，与 audit_logs.trace_id CHAR(32) 对齐。
const TraceIDLen = 32

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

// IsValid 判断 s 是否是本服务认可的 trace_id 形式：恰好 32 位十六进制（大小写均可）。
//
// 这个判断是**信任边界**，必须严格，有两个各自独立的理由：
//
//  1. 目标列是 `CHAR(32) CHARACTER SET ascii`。超过 32 字符时 MySQL 严格模式
//     （`STRICT_TRANS_TABLES`）会拒绝整条语句，而审计是**批量**写入的 ——
//     一条坏记录会让整批（BatchSize = 100）一起失败。实测：
//     `ERROR 1406 (22001): Data too long for column 'trace_id'`，
//     且同批里合法的那些行也不会落库。
//  2. 列排序规则是 `ascii_bin`（逐字节比较），大小写不同就是两个不同的值。
//     必须收敛到单一规范形式，否则"回显给用户的追踪号"与"能查到的追踪号"会对不上。
func IsValid(s string) bool {
	if len(s) != TraceIDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isHexDigit(s[i]) {
			return false
		}
	}
	return true
}

func isHexDigit(c byte) bool {
	switch {
	case c >= '0' && c <= '9':
		return true
	case c >= 'a' && c <= 'f':
		return true
	case c >= 'A' && c <= 'F':
		return true
	default:
		return false
	}
}

// Sanitize 在信任边界上处理**外部传入**的 trace_id（HTTP 头、消息属性等）：
// 形式合法则归一为小写后沿用（保住跨服务链路连续），否则丢弃并生成一个新的。
//
// 这里刻意**不做截断**：截断会让用户拿到的追踪号与实际落库的不是同一个值，
// 而且 33 个字符截成 32 个之后仍然"看起来合法"——那种假追踪号最误导人。
// 既然调用方能从响应头读到我们实际采用的值，丢弃重生成不会造成困惑。
func Sanitize(s string) string {
	if !IsValid(s) {
		return New()
	}
	return strings.ToLower(s)
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

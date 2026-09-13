package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/medicore/backend/internal/traceid"
)

const validTraceID = "ee33b8045a4cfb7a6f0ba98062ab25c6"

// traceProbe 挂上 TraceID 中间件，并把 handler 里看到的 trace_id 回显出来，
// 以便同时断言"响应头里的值"与"context 里的值"是一致的。
func traceProbe() (*gin.Engine, *string) {
	r := gin.New()
	r.Use(TraceID())

	var inContext string
	r.GET("/probe", func(c *gin.Context) {
		inContext = traceid.FromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})
	return r, &inContext
}

func probeTrace(t *testing.T, header string) (respHeader, inContext string) {
	t.Helper()

	r, ctxID := traceProbe()
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	if header != "" {
		req.Header.Set(HeaderTraceID, header)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	return w.Header().Get(HeaderTraceID), *ctxID
}

// 没有上游头时必须生成一个，且响应头与 context 用的是同一个值 ——
// 若两者不一致，用户截图上的追踪号在日志里根本搜不到。
func TestTraceID_GeneratesWhenAbsent(t *testing.T) {
	respHeader, inContext := probeTrace(t, "")

	assert.True(t, traceid.IsValid(respHeader), "生成的追踪号必须合法：%q", respHeader)
	assert.Equal(t, respHeader, inContext, "响应头与 context 必须是同一个值")
}

func TestTraceID_KeepsValidUpstreamHeader(t *testing.T) {
	respHeader, inContext := probeTrace(t, validTraceID)

	assert.Equal(t, validTraceID, respHeader, "合法的上游追踪号必须沿用，否则跨服务链路会断")
	assert.Equal(t, validTraceID, inContext)
}

// 列排序规则是 ascii_bin（逐字节比较），所以必须把大小写归一，
// 否则回显给用户的追踪号与落库的值是两个不同的字符串，查不到。
func TestTraceID_NormalizesUppercaseHeader(t *testing.T) {
	respHeader, inContext := probeTrace(t, strings.ToUpper(validTraceID))

	assert.Equal(t, validTraceID, respHeader)
	assert.Equal(t, validTraceID, inContext)
}

// 这是本次修复的核心用例。
//
// 不校验时，这个头会原样写进 `audit_logs.trace_id CHAR(32) ascii`；
// 严格模式下超长值让整条 INSERT 失败，而审计是批量写的 ——
// 一个未认证请求就能带走整批 100 条记录（含其他用户的）。
func TestTraceID_DiscardsInvalidUpstreamHeader(t *testing.T) {
	tests := []struct {
		name   string
		header string
	}{
		{"33 位（多一个字符）", validTraceID + "0"},
		{"31 位（少一个字符）", validTraceID[:31]},
		{"超长载荷", strings.Repeat("a", 4000)},
		{"UUID 形式（带连字符）", "550e8400-e29b-41d4-a716-446655440000"},
		{"非十六进制字符", strings.Repeat("z", 32)},
		{"非 ASCII", "中文中文中文中文中文中文中文中文中文中文中文中文中文中文中文中"},
		{"非法 UTF-8 字节", strings.Repeat("\xf0", 32)},
		{"换行注入", validTraceID[:31] + "\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			respHeader, inContext := probeTrace(t, tt.header)

			// 必须换成一个合法的值，而不是把非法值带下去
			assert.True(t, traceid.IsValid(respHeader),
				"非法头被放行了：%q -> %q", tt.header, respHeader)
			assert.NotEqual(t, tt.header, respHeader)
			assert.Equal(t, respHeader, inContext)

			// 回显给客户端的值必须就是我们实际使用/落库的值，
			// 否则用户拿到的追踪号查不到任何东西。
			assert.Len(t, respHeader, traceid.TraceIDLen)
		})
	}
}

// 空白的头等同于没带，不能被当成 32 字节值。
func TestTraceID_TrimsNothingButRejectsWhitespace(t *testing.T) {
	respHeader, _ := probeTrace(t, "   ")

	assert.True(t, traceid.IsValid(respHeader), "纯空白头必须被替换：%q", respHeader)
}

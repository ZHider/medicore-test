//go:build integration

package integration

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/medicore/backend/internal/traceid"
)

// 这一组用例覆盖"错误追踪号"这条链路的两端：
// 头部进入时必须过信任边界，出去之后必须能按它查回来。

type auditTraceItem struct {
	TraceID string `json:"trace_id"`
	Path    string `json:"path"`
}

type auditTracePage struct {
	Items []auditTraceItem `json:"items"`
}

// auditedRequest 发一个带**显式追踪号**的请求，并等它的审计记录真的落库。
//
// 显式指定追踪号：让用例不依赖随机值。
// 等落库：审计是异步批量写的，不等就可能断言在写入之前。
func auditedRequest(t *testing.T, token, traceID, path string) {
	t.Helper()

	resp, _ := call(t, request{
		method:  "GET",
		path:    path,
		token:   token,
		headers: map[string]string{"X-Trace-Id": traceID},
	})
	require.Equal(t, traceID, resp.Header.Get("X-Trace-Id"),
		"合法的追踪号必须被沿用（这是跨服务链路连续的前提）")

	waitForAudit(t, "trace_id = ?", traceID)
}

// HeaderTraceID 由客户端完全控制，而本中间件排在认证之前 —— 未认证请求也能设置它。
//
// 修复前它被原样写进 `audit_logs.trace_id CHAR(32) CHARACTER SET ascii`：
// 超长值在严格模式下让整条 INSERT 失败，而审计是**批量**写入的，
// 于是一个构造出来的请求就能带走整批 100 条记录（含其他用户的）。
//
// 这条用例断言两件事：头部被替换成合法值，且**审计确实落库了**。
func TestTrace_OversizedHeaderDoesNotBreakAudit(t *testing.T) {
	clearRateLimits(t)
	adminToken := mintToken(t, 910002, "admin")

	oversized := strings.Repeat("a", 4000)
	resp, _ := call(t, request{
		method:  "GET",
		path:    "/api/v1/departments",
		token:   adminToken,
		headers: map[string]string{"X-Trace-Id": oversized},
	})

	got := resp.Header.Get("X-Trace-Id")
	require.True(t, traceid.IsValid(got),
		"超长 X-Trace-Id 必须被替换成合法值，实际回显 %q（长度 %d）", got, len(got))
	assert.NotEqual(t, oversized, got)
	assert.NotEqual(t, oversized[:traceid.TraceIDLen], got,
		"回显值不能是攻击载荷的前缀——那是截断，会造出一个查不到的假追踪号")

	// 最要紧的一条：审计必须真的落库，而不是因为这一行超长而整批丢失
	row := waitForAudit(t, "trace_id = ?", got)
	assert.Equal(t, "/api/v1/departments", row.Path)
}

// 非 ASCII / 非法 UTF-8 的头同样不能进 ascii 列。
func TestTrace_NonASCIIHeaderIsRejected(t *testing.T) {
	clearRateLimits(t)
	token := mintToken(t, 910003, "admin")

	tests := []struct {
		name   string
		header string
	}{
		{"非 ASCII", "中文中文中文中文中文中文中文中文中文中文中文中文中文中文中文中"},
		{"非法 UTF-8 字节", strings.Repeat("\xf0", 32)},
		{"UUID 形式（带连字符）", "550e8400-e29b-41d4-a716-446655440000"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, _ := call(t, request{
				method:  "GET",
				path:    "/api/v1/departments",
				token:   token,
				headers: map[string]string{"X-Trace-Id": tt.header},
			})

			got := resp.Header.Get("X-Trace-Id")
			assert.True(t, traceid.IsValid(got), "回显值 %q 不合法", got)
			assert.NotEqual(t, tt.header, got)
		})
	}
}

// 合法的上游追踪号必须沿用 —— 这是跨服务链路连续的前提，别把校验写成一律重生成。
func TestTrace_ValidHeaderIsKept(t *testing.T) {
	clearRateLimits(t)
	token := mintToken(t, 910004, "admin")

	const want = "ee33b8045a4cfb7a6f0ba98062ab25c6"
	resp, _ := call(t, request{
		method:  "GET",
		path:    "/api/v1/departments",
		token:   token,
		headers: map[string]string{"X-Trace-Id": want},
	})

	assert.Equal(t, want, resp.Header.Get("X-Trace-Id"))

	row := waitForAudit(t, "trace_id = ?", want)
	assert.Equal(t, "/api/v1/departments", row.Path)
}

// 报错追踪号的用途是"用户截图 → 定位到那条请求"。
//
// 这条用例把整条闭环走一遍：拿到追踪号 → 等它落库 → 用它查审计 → 恰好命中那一条。
//
// 注意它刻意发了**两个**不同追踪号的请求。
// 只发一个是不够的：测试库在每次运行开始时从零建库，此时"过滤没生效"的查询
// 同样只返回那一条，`Len == 1` 恒真 —— 这个用例的第一版就是这么写的，
// 直到反向验证（把仓储层的 WHERE 删掉）时才发现它根本没在断言过滤。
func TestAudit_QueryByTraceIDClosesTheLoop(t *testing.T) {
	clearRateLimits(t)
	adminToken := mintToken(t, 910002, "admin")

	const (
		traceA = "aaaa0000000000000000000000000001"
		traceB = "bbbb0000000000000000000000000002"
	)

	auditedRequest(t, adminToken, traceA, "/api/v1/departments")
	auditedRequest(t, adminToken, traceB, "/api/v1/departments")

	resp, env := call(t, request{
		method: "GET",
		path:   "/api/v1/admin/audit-logs?trace_id=" + traceA,
		token:  adminToken,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode, "按追踪号查询失败：%s", env.Message)

	page := decode[auditTracePage](t, env)
	require.Len(t, page.Items, 1, "过滤未生效时会把 %s 的记录也带回来", traceB)
	assert.Equal(t, traceA, page.Items[0].TraceID)
	assert.Equal(t, "/api/v1/departments", page.Items[0].Path)
}

// 界面复制出来的追踪号可能被工具改成大写，列排序规则是 ascii_bin（逐字节比较）——
// 不归一化就查不到，用户会以为"这条记录不存在"。
func TestAudit_TraceIDQueryIsCaseInsensitive(t *testing.T) {
	clearRateLimits(t)
	adminToken := mintToken(t, 910002, "admin")

	const (
		traceA = "aaaa0000000000000000000000000003"
		traceB = "bbbb0000000000000000000000000004"
	)

	auditedRequest(t, adminToken, traceA, "/api/v1/departments")
	auditedRequest(t, adminToken, traceB, "/api/v1/departments")

	resp, env := call(t, request{
		method: "GET",
		path:   "/api/v1/admin/audit-logs?trace_id=" + strings.ToUpper(traceA),
		token:  adminToken,
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	page := decode[auditTracePage](t, env)
	require.Len(t, page.Items, 1)
	assert.Equal(t, traceA, page.Items[0].TraceID, "大小写归一后应命中同一个值")
}

// 格式不对的追踪号必须报 40001，**不能**静默忽略。
//
// 静默忽略会返回全部日志，管理员看到成百上千条会以为"这个追踪号影响面很大"——
// 一个比报错危险得多的误导。
func TestAudit_MalformedTraceIDIsRejected(t *testing.T) {
	clearRateLimits(t)
	adminToken := mintToken(t, 910002, "admin")

	for _, bad := range []string{
		"0123456789abcdef0123456789abcde",   // 31 位
		"0123456789abcdef0123456789abcdef0", // 33 位
		"550e8400-e29b-41d4-a716-446655440000",
		"not-a-trace-id",
	} {
		t.Run(bad, func(t *testing.T) {
			resp, env := call(t, request{
				method: "GET",
				path:   "/api/v1/admin/audit-logs?trace_id=" + url.QueryEscape(bad),
				token:  adminToken,
			})

			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
			assert.Equal(t, 40001, env.Code)
		})
	}
}

// 不传 trace_id 时行为不变：仍然是"不限"，不能被新参数误伤。
func TestAudit_EmptyTraceIDStillListsEverything(t *testing.T) {
	clearRateLimits(t)
	adminToken := mintToken(t, 910002, "admin")

	resp, env := call(t, request{
		method: "GET",
		path:   "/api/v1/admin/audit-logs?trace_id=",
		token:  adminToken,
	})

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	page := decode[auditTracePage](t, env)
	assert.NotEmpty(t, page.Items, "空 trace_id 应表示不限，而不是查不到任何东西")
}

// Package middleware 提供 HTTP 中间件。
//
// 链的顺序是**契约的一部分**，不是随手排的（spec §3.1）：
//
//	TraceID → Recovery → RequestLog → Metrics → [RateLimit → JWT → RBAC] → Audit
//
// 方括号内三段按**路由**挂载而非全局：探针与 /metrics 必须免认证，
// 否则 Prometheus 抓取会被 401，健康检查也无法在启动早期生效。
// 组装入口在 `internal/router`。
package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/medicore/backend/internal/traceid"
)

// HeaderTraceID 是链路追踪 ID 的响应头与上游透传头。
const HeaderTraceID = "X-Trace-Id"

// TraceID 为每个请求生成/透传 trace_id，写入 context、gin.Context 与响应头。
//
// 上游已带 X-Trace-Id 时沿用，保证跨服务链路连续 —— 但这个头**由客户端完全控制**，
// 而本中间件排在认证之前（未认证请求也会走到这里），所以必须先过信任边界再使用。
//
// 不校验会成为一个真实的审计旁路：HeaderTraceID 原样写进
// `audit_logs.trace_id CHAR(32) ascii`，超长值在严格模式下让整条 INSERT 失败，
// 而审计是批量写的 —— 一个构造出来的请求就能带走整批 100 条记录（含其他用户的）。
// 校验规则见 traceid.Sanitize：不合规就丢弃重生成，绝不落到数据库上。
func TraceID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := traceid.Sanitize(c.GetHeader(HeaderTraceID))

		c.Request = c.Request.WithContext(traceid.WithContext(c.Request.Context(), id))
		c.Header(HeaderTraceID, id)

		c.Next()
	}
}

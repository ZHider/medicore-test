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
// 若上游已带 X-Trace-Id 则沿用，保证跨服务链路连续。
func TraceID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(HeaderTraceID)
		if id == "" {
			id = traceid.New()
		}

		c.Request = c.Request.WithContext(traceid.WithContext(c.Request.Context(), id))
		c.Header(HeaderTraceID, id)

		c.Next()
	}
}

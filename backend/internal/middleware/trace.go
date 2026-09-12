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

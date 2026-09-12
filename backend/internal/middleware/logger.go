package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/medicore/backend/internal/authctx"
	"github.com/medicore/backend/internal/traceid"
	"go.uber.org/zap"
)

// RequestLog 输出结构化访问日志（spec §9.2：trace_id/user_id/method/path/status/latency）。
func RequestLog(log *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		// FullPath 为空说明未匹配到路由，此时用原始路径，便于排查扫描类请求。
		path := c.FullPath()
		if path == "" {
			path = c.Request.URL.Path
		}

		fields := []zap.Field{
			zap.String("trace_id", traceid.FromContext(c.Request.Context())),
			zap.String("method", c.Request.Method),
			zap.String("path", path),
			zap.Int("status", c.Writer.Status()),
			zap.Int64("latency_ms", time.Since(start).Milliseconds()),
			zap.String("client_ip", c.ClientIP()),
		}
		if uid, ok := authctx.UserID(c); ok {
			fields = append(fields, zap.Uint64("user_id", uid))
		}
		if role := authctx.Role(c); role != "" {
			fields = append(fields, zap.String("role", role))
		}

		// 5xx 用 Error 级别，便于告警规则直接基于 level 过滤。
		if c.Writer.Status() >= 500 {
			log.Error("http_request", fields...)
			return
		}
		log.Info("http_request", fields...)
	}
}

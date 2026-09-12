package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/response"
	"github.com/medicore/backend/internal/traceid"
	"go.uber.org/zap"
)

// Recovery 捕获 panic，记录完整堆栈，并返回统一的 500 响应。
// 放在 TraceID 之后，保证 panic 日志也带 trace_id。
func Recovery(log *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				log.Error("panic recovered",
					zap.String("trace_id", traceid.FromContext(c.Request.Context())),
					zap.String("method", c.Request.Method),
					zap.String("path", c.Request.URL.Path),
					zap.Any("error", err),
					zap.Stack("stack"),
				)
				response.Fail(c, errcode.ErrInternal)
				c.Abort()
			}
		}()

		c.Next()
	}
}

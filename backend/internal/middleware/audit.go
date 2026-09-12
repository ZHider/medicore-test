package middleware

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/medicore/backend/internal/audit"
	"github.com/medicore/backend/internal/authctx"
	"github.com/medicore/backend/internal/traceid"
)

// auditSkipPaths 是不记审计的运维端点。
//
// Prometheus 每 15 秒抓一次 /metrics，健康检查更频繁；
// 把它们记进审计表只会把真正的审计线索淹没。
var auditSkipPaths = map[string]struct{}{
	"/healthz": {},
	"/readyz":  {},
	"/metrics": {},
}

// Audit 记录请求的审计信息（spec §2.2 / §3.2 / §9.2）。
//
// 放在 RequestLog 之后、业务中间件之前，这样无论后续处理成功还是被 RBAC 拒绝，
// 都会被记录——"谁试图做什么但被拒绝了"恰恰是审计最关心的内容之一。
//
// 只记录方法、路径、状态码等元信息，**不记录请求体**：
// 请求体可能包含手机号、口令、病情描述，把它们复制进审计表
// 等于让前面所有的脱敏工作作废。
func Audit(recorder audit.Recorder) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		// 未匹配到路由时不记录：这类请求（扫描器、错误客户端）量大且无业务含义
		fullPath := c.FullPath()
		if fullPath == "" {
			return
		}
		if _, skip := auditSkipPaths[fullPath]; skip {
			return
		}

		resource, action := audit.Describe(fullPath, c.Request.Method)

		entry := audit.Entry{
			TraceID:    traceid.FromContext(c.Request.Context()),
			Method:     c.Request.Method,
			Path:       fullPath,
			Resource:   resource,
			Action:     action,
			StatusCode: c.Writer.Status(),
			LatencyMS:  int(time.Since(start).Milliseconds()),
			ClientIP:   c.ClientIP(),
			UserAgent:  c.Request.UserAgent(),
		}

		// 用户身份可能不存在（未登录或被 JWT 拒绝），此时留空而不是伪造
		if uid, ok := authctx.UserID(c); ok {
			entry.UserID = &uid
		}
		entry.Role = authctx.Role(c)

		// 资源 ID 解析失败不影响审计记录：能记下"谁在什么时候访问了哪个资源列表"
		// 已经足够，没必要因为一个参数格式问题丢掉整条记录。
		if id, err := strconv.ParseUint(c.Param("id"), 10, 64); err == nil && id > 0 {
			entry.ResourceID = &id
		}

		recorder.Record(entry)
	}
}

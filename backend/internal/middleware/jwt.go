package middleware

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/medicore/backend/internal/authctx"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/jwt"
	"github.com/medicore/backend/internal/response"
)

// bearerPrefix 是 Authorization 头的前缀。
const bearerPrefix = "Bearer "

// JWT 校验 access_token，并把 uid/role 写入请求上下文。
// 挂在业务路由组上，而非全局——探针与 /metrics 必须免认证。
func JWT(mgr *jwt.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if !strings.HasPrefix(header, bearerPrefix) {
			response.Fail(c, errcode.ErrUnauthorized)
			c.Abort()
			return
		}

		token := strings.TrimSpace(strings.TrimPrefix(header, bearerPrefix))
		if token == "" {
			response.Fail(c, errcode.ErrUnauthorized)
			c.Abort()
			return
		}

		claims, err := mgr.Parse(token)
		if err != nil {
			// 过期与非法分开返回：前端据此决定是"静默刷新"还是"跳登录页"。
			if errors.Is(err, jwt.ErrExpiredToken) {
				response.Fail(c, errcode.ErrTokenExpired)
			} else {
				response.Fail(c, errcode.ErrUnauthorized)
			}
			c.Abort()
			return
		}

		authctx.Set(c, claims.UserID, claims.Role)
		c.Next()
	}
}

package middleware

import (
	"github.com/gin-gonic/gin"

	"github.com/medicore/backend/internal/authctx"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/response"
)

// RequireRole 限定路由可访问的角色（spec §2.2 路由层校验）。
//
// 注意这只是**第一层**：资源归属仍需 service 层校验
// （例如患者只能看自己的病历），两者缺一不可。
func RequireRole(roles ...model.Role) gin.HandlerFunc {
	allowed := make(map[model.Role]struct{}, len(roles))
	for _, r := range roles {
		allowed[r] = struct{}{}
	}

	return func(c *gin.Context) {
		if _, ok := allowed[model.Role(authctx.Role(c))]; !ok {
			response.Fail(c, errcode.ErrForbidden)
			c.Abort()
			return
		}
		c.Next()
	}
}

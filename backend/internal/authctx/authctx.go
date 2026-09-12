// Package authctx 统一 gin.Context 中认证信息的读写键，避免各层各写一套字符串字面量。
package authctx

import "github.com/gin-gonic/gin"

// gin.Context 键名。
const (
	keyUserID = "auth_user_id"
	keyRole   = "auth_role"
)

// Set 写入认证信息，由 JWT 中间件调用。
func Set(c *gin.Context, userID uint64, role string) {
	c.Set(keyUserID, userID)
	c.Set(keyRole, role)
}

// UserID 读取当前用户 ID。
func UserID(c *gin.Context) (uint64, bool) {
	v, ok := c.Get(keyUserID)
	if !ok {
		return 0, false
	}
	id, ok := v.(uint64)
	return id, ok
}

// MustUserID 读取当前用户 ID，缺失时返回 0。
// 仅在已经过 JWT 中间件的路由中使用。
func MustUserID(c *gin.Context) uint64 {
	id, _ := UserID(c)
	return id
}

// Role 读取当前用户角色，缺失时返回空串。
func Role(c *gin.Context) string {
	v, ok := c.Get(keyRole)
	if !ok {
		return ""
	}
	role, _ := v.(string)
	return role
}

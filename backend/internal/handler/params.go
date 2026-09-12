package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/response"
)

// uintParam 解析路径参数中的正整数 ID。
// 失败时直接写出 400 响应并返回 false，调用方只需 `if !ok { return }`。
func uintParam(c *gin.Context, name string) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param(name), 10, 64)
	if err != nil || id == 0 {
		response.FailWithMessage(c, errcode.ErrInvalidParam, "路径参数 "+name+" 必须是正整数")
		return 0, false
	}
	return id, true
}

// intQuery 解析整数查询参数，缺失或非法时返回默认值。
//
// 非法的分页参数刻意不报错，而是回退默认值：
// 尺寸参数写错不值得让整个请求失败，且后续还有 NormalizePageSize 兜底。
func intQuery(c *gin.Context, name string, def int) int {
	raw := c.Query(name)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return v
}

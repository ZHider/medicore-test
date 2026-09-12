// Package response 提供统一响应封装（spec §3.3）。
//
//	成功: { "code": 0, "message": "ok", "data": {...}, "trace_id": "abc123" }
//	失败: { "code": 40002, "message": "号源不足", "data": null, "trace_id": "abc123" }
package response

import (
	"github.com/gin-gonic/gin"
	"github.com/medicore/backend/internal/apperr"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/traceid"
	"net/http"
)

// Body 是所有接口的统一响应体。
type Body struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data"`
	TraceID string `json:"trace_id"`
}

// OK 返回成功响应。
func OK(c *gin.Context, data any) {
	c.JSON(errcode.Success.HTTPStatus(), Body{
		Code:    int(errcode.Success),
		Message: errcode.Success.Message(),
		Data:    data,
		TraceID: traceID(c),
	})
}

// Fail 返回失败响应，使用错误码的默认文案。
func Fail(c *gin.Context, code errcode.Code) {
	FailWithMessage(c, code, code.Message())
}

// FailWithMessage 返回失败响应，并覆盖默认文案（用于携带具体原因的校验失败）。
func FailWithMessage(c *gin.Context, code errcode.Code, message string) {
	c.JSON(code.HTTPStatus(), Body{
		Code:    int(code),
		Message: message,
		Data:    nil,
		TraceID: traceID(c),
	})
}

// FailWithData 返回失败响应，但携带附加数据（如字段级校验详情、就绪检查明细）。
func FailWithData(c *gin.Context, code errcode.Code, data any) {
	c.JSON(code.HTTPStatus(), Body{
		Code:    int(code),
		Message: code.Message(),
		Data:    data,
		TraceID: traceID(c),
	})
}

// Accepted 返回 202 响应，用于"请求已受理但尚未完成"的异步操作。
//
// 单独提供这个 helper 而不是让调用方自己拼 Body：
// 手写 Body 很容易漏掉 trace_id，而 spec §9.2 要求全链路贯通——
// 一个空 trace_id 会让这条请求在日志与审计里彻底断链。
func Accepted(c *gin.Context, message string, data any) {
	c.JSON(http.StatusAccepted, Body{
		Code:    int(errcode.Success),
		Message: message,
		Data:    data,
		TraceID: traceID(c),
	})
}

// FailErr 直接把 error 转成响应。
// handler 层统一走这里，无需关心错误码/文案的提取细节（spec §3.2 铁律 2）。
func FailErr(c *gin.Context, err error) {
	code := apperr.Code(err)
	c.JSON(code.HTTPStatus(), Body{
		Code:    int(code),
		Message: apperr.Message(err),
		Data:    nil,
		TraceID: traceID(c),
	})
}

func traceID(c *gin.Context) string {
	return traceid.FromContext(c.Request.Context())
}

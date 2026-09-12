// Package errcode 定义全局错误码（spec §3.3）。
//
//	0      成功
//	400xx  业务校验
//	401xx  认证
//	403xx  权限
//	429xx  限流
//	500xx  服务端
//	502xx  LLM 上游
package errcode

import "net/http"

// Code 是业务错误码。
type Code int

const (
	Success Code = 0

	// 400xx 业务校验
	ErrInvalidParam     Code = 40001
	ErrNoStock          Code = 40002
	ErrNotFound         Code = 40003
	ErrConflict         Code = 40004
	ErrAlreadyDone      Code = 40005
	ErrMethodNotAllowed Code = 40006

	// 401xx 认证
	ErrUnauthorized   Code = 40101
	ErrTokenExpired   Code = 40102
	ErrRefreshInvalid Code = 40103

	// 403xx 权限
	ErrForbidden Code = 40301
	ErrNotOwner  Code = 40302

	// 429xx 限流
	ErrRateLimited Code = 42901

	// 500xx 服务端
	ErrInternal Code = 50001
	ErrNotReady Code = 50002

	// 502xx LLM 上游
	ErrLLMUpstream Code = 50201
)

var messages = map[Code]string{
	Success: "ok",

	ErrInvalidParam:     "参数校验失败",
	ErrNoStock:          "号源不足",
	ErrNotFound:         "资源不存在",
	ErrConflict:         "资源状态冲突",
	ErrAlreadyDone:      "操作已完成，请勿重复提交",
	ErrMethodNotAllowed: "请求方法不被支持",

	ErrUnauthorized:   "未认证或凭证无效",
	ErrTokenExpired:   "登录已过期",
	ErrRefreshInvalid: "刷新令牌无效",

	ErrForbidden: "无权限执行此操作",
	ErrNotOwner:  "无权访问他人资源",

	ErrRateLimited: "请求过于频繁，请稍后再试",

	ErrInternal: "服务器内部错误",
	ErrNotReady: "依赖服务未就绪",

	ErrLLMUpstream: "AI 服务暂时不可用",
}

var httpStatus = map[Code]int{
	Success: http.StatusOK,

	ErrInvalidParam:     http.StatusBadRequest,
	ErrNoStock:          http.StatusConflict,
	ErrNotFound:         http.StatusNotFound,
	ErrConflict:         http.StatusConflict,
	ErrAlreadyDone:      http.StatusConflict,
	ErrMethodNotAllowed: http.StatusMethodNotAllowed,

	ErrUnauthorized:   http.StatusUnauthorized,
	ErrTokenExpired:   http.StatusUnauthorized,
	ErrRefreshInvalid: http.StatusUnauthorized,

	ErrForbidden: http.StatusForbidden,
	ErrNotOwner:  http.StatusForbidden,

	ErrRateLimited: http.StatusTooManyRequests,

	ErrInternal: http.StatusInternalServerError,
	ErrNotReady: http.StatusServiceUnavailable,

	ErrLLMUpstream: http.StatusBadGateway,
}

// Message 返回错误码对应的用户可读文案。
func (c Code) Message() string {
	if m, ok := messages[c]; ok {
		return m
	}
	return "未知错误"
}

// HTTPStatus 返回错误码对应的 HTTP 状态码。
func (c Code) HTTPStatus() int {
	if s, ok := httpStatus[c]; ok {
		return s
	}
	return http.StatusInternalServerError
}

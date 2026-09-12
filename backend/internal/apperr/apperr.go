// Package apperr 定义可携带错误码的应用错误，落实 spec §3.2 第 2 条铁律：
// 错误必须包装上下文向上传递，由 handler 层统一转换为响应错误码。
package apperr

import (
	"errors"
	"fmt"

	"github.com/medicore/backend/internal/errcode"
)

// Error 是带错误码的应用错误。
type Error struct {
	Code    errcode.Code
	Message string
	cause   error
}

// New 创建应用错误。
func New(code errcode.Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// Wrap 包装底层错误并附加上下文；cause 保留给日志，不下发给客户端。
func Wrap(code errcode.Code, message string, cause error) *Error {
	return &Error{Code: code, Message: message, cause: cause}
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.cause)
	}
	return e.Message
}

// Unwrap 支持 errors.Is / errors.As 向下追溯。
func (e *Error) Unwrap() error { return e.cause }

// Code 提取错误码。非应用错误一律归为 500，避免把内部细节暴露给客户端。
func Code(err error) errcode.Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return errcode.ErrInternal
}

// Message 提取对客户端可见的文案。
func Message(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Message
	}
	return errcode.ErrInternal.Message()
}

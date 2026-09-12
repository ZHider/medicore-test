// Package llm 定义 LLM 客户端抽象，使业务代码与具体厂商解耦。
//
// 之所以要有这层抽象：spec §1.3 要求接入 OpenAI 兼容协议，
// 但开发与测试阶段不能依赖外部配额与网络。抽象之后，
// mock 与真实实现可以按配置切换，业务代码一行不改（决策 #3）。
package llm

import (
	"context"
	"errors"
	"time"
)

// 消息角色。
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// DefaultTimeout 是 LLM 调用的默认超时。LLM 调用是外部依赖，必须有上限，
// 否则一个卡住的请求会一直占着连接与 goroutine。
const DefaultTimeout = 30 * time.Second

// 上游错误分类。service 层据此决定是否重试以及返回哪个错误码。
var (
	// ErrUpstream 上游返回错误或连接失败。
	ErrUpstream = errors.New("llm upstream error")
	// ErrTimeout 调用超时。
	ErrTimeout = errors.New("llm request timeout")
)

// Message 是一条对话消息。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest 是一次补全请求。
type ChatRequest struct {
	// SystemPrompt 是系统提示词，其版本号需随结果一并入库以便追溯。
	SystemPrompt string
	// Messages 是历史对话，不含 SystemPrompt。
	Messages []Message
}

// Client 是 LLM 客户端。
//
// 流式接口刻意设计成**回调**而不是返回 channel：
// channel 方案必须额外起一个 goroutine，并在成功、出错、取消、调用方提前退出
// 这四条路径上都保证关闭，遗漏任何一条就是 goroutine 泄漏
// （spec §7.2 专门要求处理 ctx.Done() 正是为了这个）。
// 回调方案由调用方在自己的 goroutine 里驱动，不存在额外的生命周期问题。
type Client interface {
	// ChatStream 流式补全，每收到一段增量文本就调用 onDelta。
	// onDelta 返回 error 时立即中止并把该错误原样返回
	// （调用方据此感知"客户端已断开，不必继续"）。
	ChatStream(ctx context.Context, req ChatRequest, onDelta func(delta string) error) error

	// Complete 一次性补全，用于结构化总结等非流式场景。
	Complete(ctx context.Context, req ChatRequest) (string, error)

	// Model 返回模型标识，用于入库追溯。
	Model() string
}

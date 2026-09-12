package llm

import (
	"fmt"
	"time"
)

// 支持的 provider 名称。
const (
	ProviderMock = "mock"
	// 以下为 OpenAI 兼容协议的上游，尚未实现。
	ProviderOpenAI    = "openai"
	ProviderDashScope = "dashscope"
	ProviderDeepSeek  = "deepseek"
)

// NewClient 按 provider 构造客户端。
//
// 真实上游尚未实现：这里刻意返回明确错误而不是静默回退到 mock——
// 生产环境配了 dashscope 却悄悄用 mock 回复，比启动失败危险得多。
func NewClient(provider string, mockCfg MockConfig, timeout time.Duration) (Client, error) {
	switch provider {
	case "", ProviderMock:
		if timeout > 0 {
			mockCfg.DeltaDelay = min(mockCfg.DeltaDelay, timeout/4)
		}
		return NewMockClient(mockCfg), nil
	case ProviderOpenAI, ProviderDashScope, ProviderDeepSeek:
		return nil, fmt.Errorf(
			"llm provider %q 尚未实现（当前仅支持 %q）；"+
				"接入真实上游前请先确认 API Key 与配额", provider, ProviderMock)
	default:
		return nil, fmt.Errorf("未知的 llm provider: %q", provider)
	}
}

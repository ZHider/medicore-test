package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testClient(cfg MockConfig) *MockClient {
	if cfg.DeltaDelay == 0 {
		cfg.DeltaDelay = time.Millisecond
	}
	return NewMockClient(cfg)
}

func TestMock_ChatStreamDeliversFullReply(t *testing.T) {
	c := testClient(MockConfig{})

	var sb strings.Builder
	chunks := 0
	err := c.ChatStream(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "我肚子疼"}},
	}, func(delta string) error {
		chunks++
		sb.WriteString(delta)
		return nil
	})

	require.NoError(t, err)
	assert.Greater(t, chunks, 1, "流式输出应分成多段而不是一次给完")
	assert.NotEmpty(t, sb.String())
}

// 分片必须按 rune 切分：按 byte 会把中文截成半个字符，前端显示乱码。
func TestMock_ChunksDoNotSplitRunes(t *testing.T) {
	c := testClient(MockConfig{})

	var sb strings.Builder
	err := c.ChatStream(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "你好"}},
	}, func(delta string) error {
		// 每个分片都必须是合法 UTF-8
		assert.True(t, strings.ToValidUTF8(delta, "") == delta,
			"分片 %q 不是合法 UTF-8，说明按 byte 切分了多字节字符", delta)
		sb.WriteString(delta)
		return nil
	})

	require.NoError(t, err)
	assert.Contains(t, sb.String(), "不适", "中文内容应完整还原")
}

// 同一个对话无论重放多少次结果都一致——否则测试没法写断言。
func TestMock_ReplyIsDeterministic(t *testing.T) {
	c := testClient(MockConfig{})
	req := ChatRequest{Messages: []Message{{Role: RoleUser, Content: "x"}}}

	first := collect(t, c, req)
	second := collect(t, c, req)

	assert.Equal(t, first, second)
}

// 轮次不同回复不同：mock 应模拟多轮问诊，而不是复读同一句。
func TestMock_ReplyVariesByTurn(t *testing.T) {
	c := testClient(MockConfig{})

	turn1 := collect(t, c, ChatRequest{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	turn3 := collect(t, c, ChatRequest{Messages: []Message{
		{Role: RoleUser, Content: "a"}, {Role: RoleAssistant, Content: "b"},
		{Role: RoleUser, Content: "c"}, {Role: RoleAssistant, Content: "d"},
		{Role: RoleUser, Content: "e"},
	}})

	assert.NotEqual(t, turn1, turn3)
}

// 客户端断开（ctx 取消）必须立刻停止，否则就是连接与配额的浪费。
func TestMock_StopsOnContextCancel(t *testing.T) {
	c := testClient(MockConfig{DeltaDelay: 50 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	chunks := 0

	err := c.ChatStream(ctx, ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	}, func(string) error {
		chunks++
		if chunks == 2 {
			cancel()
		}
		return nil
	})

	assert.ErrorIs(t, err, context.Canceled)
	assert.LessOrEqual(t, chunks, 3, "取消后不应继续吐数据")
}

// onDelta 返回错误说明"写响应失败"（通常是客户端断开），应立即中止并原样返回。
func TestMock_StopsWhenOnDeltaFails(t *testing.T) {
	c := testClient(MockConfig{})
	sentinel := errors.New("client gone")

	err := c.ChatStream(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	}, func(string) error { return sentinel })

	assert.ErrorIs(t, err, sentinel)
}

func TestMock_InjectsStreamError(t *testing.T) {
	c := testClient(MockConfig{StreamErrorAt: 2})

	err := c.ChatStream(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	}, func(string) error { return nil })

	assert.ErrorIs(t, err, ErrUpstream)
}

func TestMock_CompleteModes(t *testing.T) {
	t.Run("valid 返回合法 JSON", func(t *testing.T) {
		out, err := testClient(MockConfig{SummaryMode: SummaryValid}).Complete(context.Background(), ChatRequest{})
		require.NoError(t, err)
		assert.Contains(t, out, "chief_complaint")
	})

	t.Run("invalid_json 返回带围栏的文本", func(t *testing.T) {
		out, err := testClient(MockConfig{SummaryMode: SummaryInvalidJSON}).Complete(context.Background(), ChatRequest{})
		require.NoError(t, err)
		assert.Contains(t, out, "```", "应模拟模型在 JSON 外包裹说明文字的真实故障")
	})

	t.Run("invalid_schema 是合法 JSON 但缺字段", func(t *testing.T) {
		out, err := testClient(MockConfig{SummaryMode: SummaryInvalidSchema}).Complete(context.Background(), ChatRequest{})
		require.NoError(t, err)
		assert.NotContains(t, out, "present_illness")
	})

	t.Run("error 返回上游错误", func(t *testing.T) {
		_, err := testClient(MockConfig{SummaryMode: SummaryError}).Complete(context.Background(), ChatRequest{})
		assert.ErrorIs(t, err, ErrUpstream)
	})

	t.Run("timeout 直到 ctx 超时", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()

		_, err := testClient(MockConfig{SummaryMode: SummaryTimeout}).Complete(ctx, ChatRequest{})
		assert.ErrorIs(t, err, ErrTimeout)
	})
}

func TestMock_Model(t *testing.T) {
	assert.Equal(t, "custom-model", NewMockClient(MockConfig{ModelName: "custom-model"}).Model())
	assert.NotEmpty(t, NewMockClient(MockConfig{}).Model(), "未指定时应回退到默认标识")
}

func TestNewClient(t *testing.T) {
	t.Run("mock 可用", func(t *testing.T) {
		c, err := NewClient(ProviderMock, MockConfig{}, time.Second)
		require.NoError(t, err)
		assert.NotNil(t, c)
	})

	t.Run("空 provider 回退 mock", func(t *testing.T) {
		c, err := NewClient("", MockConfig{}, time.Second)
		require.NoError(t, err)
		assert.NotNil(t, c)
	})

	// 配错了上游必须启动失败，而不是静默用 mock 回复——
	// 生产环境里"以为接了真模型其实返回的是假数据"比直接挂掉危险得多。
	t.Run("未实现的上游明确报错", func(t *testing.T) {
		_, err := NewClient(ProviderDashScope, MockConfig{}, time.Second)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "尚未实现")
	})

	t.Run("未知 provider 报错", func(t *testing.T) {
		_, err := NewClient("nonsense", MockConfig{}, time.Second)
		assert.Error(t, err)
	})
}

func collect(t *testing.T, c *MockClient, req ChatRequest) string {
	t.Helper()

	var sb strings.Builder
	require.NoError(t, c.ChatStream(context.Background(), req, func(d string) error {
		sb.WriteString(d)
		return nil
	}))
	return sb.String()
}

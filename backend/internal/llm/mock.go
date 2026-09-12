package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// SummaryMode 决定 mock 在结构化总结时返回什么，用于验证降级路径。
type SummaryMode string

// mock 的总结输出模式。
const (
	// SummaryValid 合法 JSON 且满足 schema。
	SummaryValid SummaryMode = "valid"
	// SummaryInvalidJSON 不是合法 JSON，用于验证"重试后降级"。
	SummaryInvalidJSON SummaryMode = "invalid_json"
	// SummaryInvalidSchema JSON 合法但缺少必填字段，同样应触发降级。
	SummaryInvalidSchema SummaryMode = "invalid_schema"
	// SummaryError 上游直接报错。
	SummaryError SummaryMode = "error"
	// SummaryTimeout 上游超时。
	SummaryTimeout SummaryMode = "timeout"
)

// MockConfig 是 mock 客户端的行为配置。
//
// 这些开关存在的意义不是"让 mock 更好玩"，而是让 G3 的验收标准
// （「返回 100% 通过 Schema 校验**或**触发优雅降级」）可以被真正验证：
// 没有故障注入就只能测到成功路径，降级分支永远是未经验证的代码。
type MockConfig struct {
	// DeltaDelay 是每段增量之间的延迟，用于模拟打字机效果。
	DeltaDelay time.Duration
	// SummaryMode 决定 Complete 的返回。
	SummaryMode SummaryMode
	// StreamErrorAt 为第几段之后注入错误（0 表示不注入）。
	StreamErrorAt int
	// ModelName 是入库追溯用的模型标识。
	ModelName string
}

// DefaultMockConfig 返回默认配置。
func DefaultMockConfig() MockConfig {
	return MockConfig{
		DeltaDelay:  20 * time.Millisecond,
		SummaryMode: SummaryValid,
		ModelName:   "mock-preconsult-v1",
	}
}

// MockClient 是不依赖任何外部服务的 LLM 实现。
type MockClient struct {
	cfg MockConfig
}

// NewMockClient 构造 MockClient。
func NewMockClient(cfg MockConfig) *MockClient {
	if cfg.ModelName == "" {
		cfg.ModelName = DefaultMockConfig().ModelName
	}
	return &MockClient{cfg: cfg}
}

// Model 返回模型标识。
func (c *MockClient) Model() string { return c.cfg.ModelName }

// chunkRunes 是每次吐出的字符数。按 rune 而非 byte 切分，
// 否则中文会被截断成半个字符，前端显示成乱码。
const chunkRunes = 3

// ChatStream 按打字机节奏吐出预设回复，并遵守 ctx 取消。
func (c *MockClient) ChatStream(
	ctx context.Context, req ChatRequest, onDelta func(delta string) error,
) error {
	reply := mockReply(req.Messages)
	runes := []rune(reply)

	emitted := 0
	for start := 0; start < len(runes); start += chunkRunes {
		// 客户端断开时必须立刻退出：这里是唯一能感知取消的位置。
		if err := ctx.Err(); err != nil {
			return err
		}

		end := min(start+chunkRunes, len(runes))
		if err := onDelta(string(runes[start:end])); err != nil {
			return err
		}
		emitted++

		if c.cfg.StreamErrorAt > 0 && emitted >= c.cfg.StreamErrorAt {
			return fmt.Errorf("%w: injected stream failure after %d chunks", ErrUpstream, emitted)
		}

		if c.cfg.DeltaDelay > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(c.cfg.DeltaDelay):
			}
		}
	}
	return nil
}

// Complete 按配置返回结构化总结。
func (c *MockClient) Complete(ctx context.Context, _ ChatRequest) (string, error) {
	switch c.cfg.SummaryMode {
	case SummaryError:
		return "", fmt.Errorf("%w: injected completion failure", ErrUpstream)

	case SummaryTimeout:
		// 等待 ctx 超时，真实上游卡住时就是这个表现
		<-ctx.Done()
		return "", fmt.Errorf("%w: %w", ErrTimeout, ctx.Err())

	case SummaryInvalidJSON:
		// 典型的真实故障：模型在 JSON 外面加了说明文字或代码块围栏
		return "好的，以下是总结：\n```json\n{\"chief_complaint\": \"腹痛\"\n```\n希望有帮助。", nil

	case SummaryInvalidSchema:
		// JSON 合法但缺字段：校验层必须能识别出来
		return `{"chief_complaint":"腹痛","note":"缺少其余必填字段"}`, nil

	case SummaryValid:
		fallthrough
	default:
		payload := validSummary()
		raw, err := json.Marshal(payload)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}
}

// validSummary 返回一份符合 schema 的总结。
func validSummary() map[string]any {
	return map[string]any{
		"chief_complaint":      "上腹隐痛 3 天",
		"present_illness":      "3 天前无明显诱因出现上腹隐痛，进食后加重，无发热、呕吐。",
		"past_history":         "既往有慢性胃炎病史 5 年。",
		"medications":          "奥美拉唑，每日一次。",
		"allergies":            "否认药物过敏史。",
		"red_flags":            []string{},
		"possible_departments": []string{"消化内科"},
		"urgency":              "routine",
	}
}

// mockReply 依据已发生的轮次给出下一句回复。
//
// 轮次由 user 消息数量推断，因此同一个对话无论重放多少次结果都一致——
// 这对测试很重要：随机的 mock 没法写断言。
func mockReply(messages []Message) string {
	turns := 0
	for _, m := range messages {
		if m.Role == RoleUser {
			turns++
		}
	}

	switch {
	case turns <= 1:
		return "好的，我来了解一下情况。请问您这次主要的不适是什么？大概持续多久了？"
	case turns == 2:
		return "明白了。这种不适是持续存在，还是间断出现的？有没有什么情况会加重或缓解？"
	case turns == 3:
		return "好的，记下了。您既往有没有慢性病史、做过手术，或者对什么药物过敏？"
	case turns == 4:
		return "了解了。目前有在服用什么药物吗？另外有没有自行处理过，效果怎么样？"
	default:
		return "信息我已经采集完整了，您可以提交给医生查看。如果还有补充，可以继续告诉我。"
	}
}

// Describe 返回配置摘要，供启动日志使用。
func (c MockClient) Describe() string {
	return fmt.Sprintf("mock(model=%s, summary_mode=%s, delta_delay=%s)",
		c.cfg.ModelName, c.cfg.SummaryMode, c.cfg.DeltaDelay)
}

var _ Client = (*MockClient)(nil)

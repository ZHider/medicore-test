package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/medicore/backend/internal/llm"
)

const testSummarySchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["chief_complaint", "possible_departments", "urgency"],
  "properties": {
    "chief_complaint": {"type": "string", "minLength": 2},
    "possible_departments": {"type": "array", "minItems": 1, "items": {"type": "string"}},
    "urgency": {"type": "string", "enum": ["routine", "urgent", "emergency"]}
  }
}`

const validSummaryJSON = `{"chief_complaint":"上腹痛 3 天","possible_departments":["消化内科"],"urgency":"routine"}`

func newTestValidator(t *testing.T) *SummaryValidator {
	t.Helper()

	v, err := NewSummaryValidator(testSummarySchema)
	require.NoError(t, err)
	return v
}

// --- Schema 编译 ---

// Schema 写错必须在启动时暴露，而不是等到第一次总结才发现。
func TestNewSummaryValidator_RejectsBrokenSchema(t *testing.T) {
	for _, bad := range []string{`{`, `{"type": "nonsense"}`, `[]`} {
		_, err := NewSummaryValidator(bad)
		assert.Error(t, err, "非法 Schema %q 应在编译阶段被拒绝", bad)
	}
}

// `{}` 是合法的 JSON Schema（什么都放行），编译不会报错——
// 而这正是危险所在：校验会静默失效。SelfCheck 负责把这种情况变成启动失败。
func TestSummaryValidator_SelfCheckCatchesTooPermissiveSchema(t *testing.T) {
	t.Run("空 Schema 被自检拦下", func(t *testing.T) {
		v, err := NewSummaryValidator(`{}`)
		require.NoError(t, err, "空 Schema 编译是成功的，问题不在这里")

		assert.Error(t, v.SelfCheck(), "空 Schema 必须被自检拦下，否则校验形同虚设")
	})

	t.Run("缺少必填约束的 Schema 被拦下", func(t *testing.T) {
		v, err := NewSummaryValidator(`{"type":"object","properties":{"a":{"type":"string"}}}`)
		require.NoError(t, err)

		assert.Error(t, v.SelfCheck())
	})

	t.Run("正常 Schema 通过自检", func(t *testing.T) {
		assert.NoError(t, newTestValidator(t).SelfCheck())
	})
}

func TestSummaryValidator(t *testing.T) {
	v := newTestValidator(t)

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"合法", validSummaryJSON, false},
		{"非法 JSON", "{not json", true},
		{"缺必填字段", `{"chief_complaint":"腹痛"}`, true},
		{"字段类型错误", `{"chief_complaint":123,"possible_departments":["内科"],"urgency":"routine"}`, true},
		{"枚举值非法", `{"chief_complaint":"腹痛","possible_departments":["内科"],"urgency":"sometime"}`, true},
		{"数组为空", `{"chief_complaint":"腹痛","possible_departments":[],"urgency":"routine"}`, true},
		{"字符串过短", `{"chief_complaint":"痛","possible_departments":["内科"],"urgency":"routine"}`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.Validate(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

// --- JSON 提取 ---

// 真实模型经常在 JSON 外面包一层说明文字或代码块，这是最常见的故障形态。
func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantOK   bool
		wantSame bool // 提取结果是否应等于原文（已 trim）
	}{
		{"纯 JSON", validSummaryJSON, true, true},
		{"带前后空白", "  \n" + validSummaryJSON + "\n  ", true, true},
		{"```json 围栏", "```json\n" + validSummaryJSON + "\n```", true, false},
		{"``` 围栏无语言标注", "```\n" + validSummaryJSON + "\n```", true, false},
		{"前后有说明文字", "以下是总结：\n" + validSummaryJSON + "\n希望有帮助。", true, false},
		{"围栏加说明文字", "好的：\n```json\n" + validSummaryJSON + "\n```\n完成", true, false},
		{"空串", "", false, false},
		{"纯文本无 JSON", "我不知道该怎么回答", false, false},
		{"只有左括号", "{\"chief_complaint\":", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := extractJSON(tt.input)
			assert.Equal(t, tt.wantOK, ok)
			if !tt.wantOK {
				return
			}
			if tt.wantSame {
				assert.JSONEq(t, validSummaryJSON, got)
			} else {
				// 无论怎么剥离，结果都必须是可直接反序列化的纯 JSON
				assert.JSONEq(t, validSummaryJSON, got)
			}
		})
	}
}

// 从围栏/说明文字里提取出来的内容必须能直接过 Schema 校验。
func TestExtractJSON_ResultPassesValidation(t *testing.T) {
	v := newTestValidator(t)

	for _, wrapped := range []string{
		"```json\n" + validSummaryJSON + "\n```",
		"以下是总结：\n" + validSummaryJSON,
	} {
		got, ok := extractJSON(wrapped)
		require.True(t, ok)
		assert.NoError(t, v.Validate(got))
	}
}

// --- 总结编排 ---

type summaryFixture struct {
	svc    *PreconsultService
	client *fakeLLMClient
}

func newSummaryFixture(t *testing.T, outputs ...string) *summaryFixture {
	t.Helper()

	client := &fakeLLMClient{completeOut: validSummaryJSON}
	if len(outputs) > 0 {
		client.completeOut = outputs[0]
	}

	svc := NewPreconsultService(PreconsultDeps{
		Client:        client,
		Validator:     newTestValidator(t),
		SummaryPrompt: "summary prompt",
	})
	return &summaryFixture{svc: svc, client: client}
}

func conversation() []llm.Message {
	return []llm.Message{
		{Role: llm.RoleUser, Content: "我肚子疼三天了"},
		{Role: llm.RoleAssistant, Content: "请问具体在哪个位置？"},
		{Role: llm.RoleUser, Content: "上腹部，饭后加重"},
	}
}

func TestSummarize_Success(t *testing.T) {
	f := newSummaryFixture(t)

	outcome := f.svc.summarize(context.Background(), conversation())

	assert.False(t, outcome.Fallback)
	assert.Equal(t, 1, outcome.Attempts, "一次成功不应重试")
	assert.JSONEq(t, validSummaryJSON, outcome.Structured)
}

// 首次输出带代码块围栏：应当被提取出来并通过校验，而不是直接降级。
func TestSummarize_RecoversFromCodeFence(t *testing.T) {
	f := newSummaryFixture(t, "好的，总结如下：\n```json\n"+validSummaryJSON+"\n```")

	outcome := f.svc.summarize(context.Background(), conversation())

	assert.False(t, outcome.Fallback, "带围栏的输出不应触发降级")
	assert.Equal(t, 1, outcome.Attempts)
}

// 校验失败应重试一次，第二次成功则不降级（spec §7.2）。
func TestSummarize_RetriesOnceThenSucceeds(t *testing.T) {
	f := newSummaryFixture(t)
	f.client.completeOut = `{"chief_complaint":"腹痛"}` // 缺必填字段

	// 第二次返回合法内容
	callCount := 0
	f.client.completeFunc = func() (string, error) {
		callCount++
		if callCount == 1 {
			return `{"chief_complaint":"腹痛"}`, nil
		}
		return validSummaryJSON, nil
	}

	outcome := f.svc.summarize(context.Background(), conversation())

	assert.False(t, outcome.Fallback)
	assert.Equal(t, 2, outcome.Attempts, "应在第二次尝试成功")
}

// 重试后仍不合法：降级，不再无限重试。
func TestSummarize_FallsBackAfterRetries(t *testing.T) {
	f := newSummaryFixture(t)
	f.client.completeOut = "完全不是 JSON"

	outcome := f.svc.summarize(context.Background(), conversation())

	assert.True(t, outcome.Fallback)
	assert.Empty(t, outcome.Structured, "降级时不应有结构化输出")
	assert.Equal(t, maxSummaryAttempts, outcome.Attempts)
	assert.Contains(t, outcome.Reason, "校验")
}

// 上游报错也应走降级路径：医生看到原始对话总好过一片空白。
func TestSummarize_FallsBackOnUpstreamError(t *testing.T) {
	f := newSummaryFixture(t)
	f.client.completeErr = llm.ErrUpstream

	outcome := f.svc.summarize(context.Background(), conversation())

	assert.True(t, outcome.Fallback)
	assert.Contains(t, outcome.Reason, "AI 服务返回错误")
}

func TestSummarize_FallsBackOnTimeout(t *testing.T) {
	f := newSummaryFixture(t)
	f.client.completeErr = llm.ErrTimeout

	outcome := f.svc.summarize(context.Background(), conversation())

	assert.True(t, outcome.Fallback)
	assert.Contains(t, outcome.Reason, "超时")
}

// 上下文已结束（超时/取消）时不应继续重试，否则只是白白多等一轮。
func TestSummarize_StopsRetryingWhenContextDone(t *testing.T) {
	f := newSummaryFixture(t)
	f.client.completeErr = context.DeadlineExceeded

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	outcome := f.svc.summarize(ctx, conversation())

	assert.True(t, outcome.Fallback)
	assert.Equal(t, 1, outcome.Attempts, "上下文已结束时不应再重试")
}

// 降级原因不能把上游原始错误直接抛给使用者（会写进报告给医生看）。
func TestDescribeSummaryFailure_DoesNotLeakInternals(t *testing.T) {
	reason := describeSummaryFailure(errors.New("dial tcp 10.0.0.5:443: connection refused"), 2)

	assert.NotContains(t, reason, "10.0.0.5")
	assert.NotContains(t, reason, "dial tcp")
	assert.True(t, strings.Contains(reason, "校验"), "应给出可理解的降级原因")
}

func TestTruncateRunes(t *testing.T) {
	assert.Equal(t, "abc", truncateRunes("abc", 5))
	// 按 rune 截断，不能把中文截成半个字符
	assert.Equal(t, "中文...", truncateRunes("中文字符测试", 2))
}

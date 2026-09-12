package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/medicore/backend/internal/apperr"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/llm"
	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/repository"
)

// --- 内存实现 ---

type fakeConversationStore struct {
	mu        sync.Mutex
	messages  map[uint64][]llm.Message
	appendErr error
	listErr   error
	cleared   int
}

func newFakeConversationStore() *fakeConversationStore {
	return &fakeConversationStore{messages: map[uint64][]llm.Message{}}
}

func (f *fakeConversationStore) Append(_ context.Context, appointmentID uint64, msg llm.Message) error {
	if f.appendErr != nil {
		return f.appendErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages[appointmentID] = append(f.messages[appointmentID], msg)
	return nil
}

func (f *fakeConversationStore) List(_ context.Context, appointmentID uint64) ([]llm.Message, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]llm.Message(nil), f.messages[appointmentID]...), nil
}

func (f *fakeConversationStore) Clear(_ context.Context, appointmentID uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.messages, appointmentID)
	f.cleared++
	return nil
}

func (f *fakeConversationStore) count(appointmentID uint64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.messages[appointmentID])
}

type fakeLLMClient struct {
	reply       string
	streamErr   error
	completeOut string
	completeErr error
	// completeFunc 用于需要按调用次数返回不同结果的场景（如重试后成功）
	completeFunc func() (string, error)

	mu      sync.Mutex
	calls   int
	lastReq llm.ChatRequest
}

func (f *fakeLLMClient) ChatStream(
	ctx context.Context, req llm.ChatRequest, onDelta func(string) error,
) error {
	f.mu.Lock()
	f.calls++
	f.lastReq = req
	f.mu.Unlock()

	if f.streamErr != nil {
		return f.streamErr
	}

	// 按 rune 分片，模拟真实流式行为
	runes := []rune(f.reply)
	for i := 0; i < len(runes); i += 4 {
		end := min(i+4, len(runes))
		if err := onDelta(string(runes[i:end])); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeLLMClient) Complete(context.Context, llm.ChatRequest) (string, error) {
	if f.completeFunc != nil {
		return f.completeFunc()
	}
	return f.completeOut, f.completeErr
}

func (f *fakeLLMClient) Model() string { return "fake-model" }

func (f *fakeLLMClient) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// --- 组装 ---

type preconsultFixture struct {
	svc       *PreconsultService
	appts     *fakeAppointmentStore
	conv      *fakeConversationStore
	llmClient *fakeLLMClient
}

func newPreconsultFixture(t *testing.T) *preconsultFixture {
	t.Helper()

	appts := &fakeAppointmentStore{
		item: &model.AppointmentItem{ID: 1, Status: model.AppointmentStatusPending},
	}
	conv := newFakeConversationStore()
	client := &fakeLLMClient{reply: "请问您主要的不适是什么？"}

	svc := NewPreconsultService(PreconsultDeps{
		Appointments:  appts,
		Conversation:  conv,
		Client:        client,
		SystemPrompt:  "system prompt",
		PromptVersion: "v1",
		Timeout:       time.Second,
	})
	return &preconsultFixture{svc: svc, appts: appts, conv: conv, llmClient: client}
}

func collectDeltas(t *testing.T, f *preconsultFixture, message string) string {
	t.Helper()

	var sb strings.Builder
	err := f.svc.Chat(context.Background(), 100, 1, message, func(d string) error {
		sb.WriteString(d)
		return nil
	})
	require.NoError(t, err)
	return sb.String()
}

// --- 正常路径 ---

func TestPreconsult_ChatSuccess(t *testing.T) {
	f := newPreconsultFixture(t)

	reply := collectDeltas(t, f, "我肚子疼")

	assert.Equal(t, f.llmClient.reply, reply, "拼起来的增量应等于完整回复")
	assert.Equal(t, 2, f.conv.count(1), "用户消息与助手回复都要入库")
	assert.Equal(t, 1, f.llmClient.callCount())
}

// 系统提示词必须随请求传给上游，否则模型就没有角色约束。
func TestPreconsult_PassesSystemPrompt(t *testing.T) {
	f := newPreconsultFixture(t)

	collectDeltas(t, f, "你好")

	assert.Equal(t, "system prompt", f.llmClient.lastReq.SystemPrompt)
}

// 第二轮必须带上历史，否则模型不记得上一句说过什么。
func TestPreconsult_IncludesHistoryOnLaterTurns(t *testing.T) {
	f := newPreconsultFixture(t)

	collectDeltas(t, f, "第一条")
	collectDeltas(t, f, "第二条")

	// 第二轮的历史应含：user1, assistant1, user2
	assert.Len(t, f.llmClient.lastReq.Messages, 3)
	assert.Equal(t, llm.RoleUser, f.llmClient.lastReq.Messages[0].Role)
	assert.Equal(t, llm.RoleAssistant, f.llmClient.lastReq.Messages[1].Role)
	assert.Equal(t, "第二条", f.llmClient.lastReq.Messages[2].Content)
}

// 用户消息先落库：即使上游失败，重试时也不必重新输入。
func TestPreconsult_SavesUserMessageBeforeCallingLLM(t *testing.T) {
	f := newPreconsultFixture(t)
	f.llmClient.streamErr = llm.ErrUpstream

	_ = f.svc.Chat(context.Background(), 100, 1, "我头疼", func(string) error { return nil })

	assert.Equal(t, 1, f.conv.count(1), "用户消息应已保存")
	msgs, _ := f.conv.List(context.Background(), 1)
	assert.Equal(t, llm.RoleUser, msgs[0].Role)
}

// 失败时不得保存半截助手回复：否则下一轮模型会看到残缺上下文。
func TestPreconsult_DoesNotSavePartialReplyOnFailure(t *testing.T) {
	f := newPreconsultFixture(t)
	f.llmClient.streamErr = llm.ErrUpstream

	err := f.svc.Chat(context.Background(), 100, 1, "我头疼", func(string) error { return nil })

	require.Error(t, err)
	msgs, _ := f.conv.List(context.Background(), 1)
	require.Len(t, msgs, 1)
	assert.Equal(t, llm.RoleUser, msgs[0].Role, "失败后不应出现助手消息")
}

// --- 参数与权限校验 ---

func TestPreconsult_RejectsEmptyOrOversizedMessage(t *testing.T) {
	tests := []struct {
		name    string
		message string
	}{
		{"空串", ""},
		{"纯空白", "   \n\t "},
		{"超长", strings.Repeat("疼", maxUserMessageRunes+1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPreconsultFixture(t)

			err := f.svc.Chat(context.Background(), 100, 1, tt.message, func(string) error { return nil })

			assert.Equal(t, errcode.ErrInvalidParam, apperr.Code(err))
			assert.Zero(t, f.llmClient.callCount(), "校验失败不应调用上游")
		})
	}
}

// 恰好等于上限应当放行，边界不能多一不可。
func TestPreconsult_AcceptsMessageAtLimit(t *testing.T) {
	f := newPreconsultFixture(t)

	reply := collectDeltas(t, f, strings.Repeat("疼", maxUserMessageRunes))

	assert.NotEmpty(t, reply)
	assert.Equal(t, 1, f.llmClient.callCount())
}

func TestPreconsult_RejectsUnknownAppointment(t *testing.T) {
	f := newPreconsultFixture(t)
	f.appts.itemErr = repository.ErrNotFound

	err := f.svc.Chat(context.Background(), 100, 999, "你好", func(string) error { return nil })

	assert.Equal(t, errcode.ErrNotFound, apperr.Code(err))
	assert.Zero(t, f.llmClient.callCount())
}

// 已取消/已过期的预约不应还能继续预问诊。
func TestPreconsult_RejectsInactiveAppointment(t *testing.T) {
	for _, status := range []string{
		model.AppointmentStatusCancelled,
		model.AppointmentStatusExpired,
	} {
		t.Run(status, func(t *testing.T) {
			f := newPreconsultFixture(t)
			f.appts.item.Status = status

			err := f.svc.Chat(context.Background(), 100, 1, "你好", func(string) error { return nil })

			assert.Equal(t, errcode.ErrConflict, apperr.Code(err))
			assert.Zero(t, f.llmClient.callCount())
		})
	}
}

// --- 上游错误映射 ---

func TestPreconsult_MapsUpstreamErrorsToLLMCode(t *testing.T) {
	tests := []struct {
		name    string
		llmErr  error
		wantMsg string
	}{
		{"上游错误", llm.ErrUpstream, "AI 对话服务暂时不可用"},
		{"超时", llm.ErrTimeout, "AI 服务响应超时"},
		{"上下文超时", context.DeadlineExceeded, "AI 服务响应超时"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPreconsultFixture(t)
			f.llmClient.streamErr = tt.llmErr

			err := f.svc.Chat(context.Background(), 100, 1, "你好", func(string) error { return nil })

			assert.Equal(t, errcode.ErrLLMUpstream, apperr.Code(err))
			assert.Contains(t, apperr.Message(err), tt.wantMsg)
		})
	}
}

// onDelta 返回错误（通常是写响应失败）必须原样向上传递，
// 让 handler 能判断"客户端已断开"，而不是当成上游故障。
func TestPreconsult_PropagatesOnDeltaError(t *testing.T) {
	f := newPreconsultFixture(t)
	sentinel := errors.New("write failed")

	err := f.svc.Chat(context.Background(), 100, 1, "你好", func(string) error { return sentinel })

	assert.ErrorIs(t, err, sentinel)
}

// 对话存储写入失败应阻断本轮：不能在没有历史的情况下继续问诊。
func TestPreconsult_AppendFailureAborts(t *testing.T) {
	f := newPreconsultFixture(t)
	f.conv.appendErr = errors.New("redis down")

	err := f.svc.Chat(context.Background(), 100, 1, "你好", func(string) error { return nil })

	assert.Equal(t, errcode.ErrInternal, apperr.Code(err))
	assert.Zero(t, f.llmClient.callCount())
}

// --- History ---

func TestPreconsult_History(t *testing.T) {
	f := newPreconsultFixture(t)
	collectDeltas(t, f, "第一条")

	messages, err := f.svc.History(context.Background(), 100, 1)

	require.NoError(t, err)
	assert.Len(t, messages, 2, "应返回完整的用户与助手消息")
	assert.Equal(t, "第一条", messages[0].Content)
}

func TestPreconsult_HistoryRespectsOwnership(t *testing.T) {
	f := newPreconsultFixture(t)
	f.appts.itemErr = repository.ErrNotFound

	_, err := f.svc.History(context.Background(), 100, 999)

	assert.Equal(t, errcode.ErrNotFound, apperr.Code(err))
}

func TestPreconsult_ExposesPromptVersionAndModel(t *testing.T) {
	f := newPreconsultFixture(t)

	assert.Equal(t, "v1", f.svc.PromptVersion())
	assert.Equal(t, "fake-model", f.svc.ModelName())
}

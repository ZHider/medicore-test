package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/medicore/backend/internal/apperr"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/llm"
	"github.com/medicore/backend/internal/metrics"
	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/repository"
)

// LLM 调用的用途与结果标签（spec §9.2 要求 llm_calls_total）。
const (
	llmPurposeChat    = "chat"
	llmPurposeSummary = "summary"

	llmResultSuccess  = "success"
	llmResultError    = "error"
	llmResultTimeout  = "timeout"
	llmResultCanceled = "canceled"
)

var llmCallsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "llm_calls_total",
	Help: "LLM 调用计数，按用途与结果分类。",
}, []string{"purpose", "result"})

func init() {
	metrics.Register(llmCallsTotal)
}

// maxUserMessageRunes 是单条用户消息的长度上限。
// 必须设上限：预问诊是流式接口，单条超长消息会长时间占用连接与上游配额。
const maxUserMessageRunes = 1000

// PreconsultDeps 是 PreconsultService 的依赖集合。
type PreconsultDeps struct {
	Appointments  appointmentStore
	Conversation  conversationStore
	Reports       reportStore
	SummaryQueue  summaryQueue
	Doctors       doctorLookup
	Client        llm.Client
	Validator     *SummaryValidator
	Log           *zap.Logger
	SystemPrompt  string
	SummaryPrompt string
	PromptVersion string
	Timeout       time.Duration
}

// PreconsultService 承载 AI 预问诊对话与结构化总结。
type PreconsultService struct {
	appointments  appointmentStore
	conversation  conversationStore
	reports       reportStore
	summaryQueue  summaryQueue
	doctors       doctorLookup
	client        llm.Client
	validator     *SummaryValidator
	log           *zap.Logger
	systemPrompt  string
	summaryPrompt string
	promptVersion string
	timeout       time.Duration
}

// NewPreconsultService 构造 PreconsultService。
func NewPreconsultService(d PreconsultDeps) *PreconsultService {
	log := d.Log
	if log == nil {
		log = zap.NewNop()
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = llm.DefaultTimeout
	}

	return &PreconsultService{
		appointments:  d.Appointments,
		conversation:  d.Conversation,
		reports:       d.Reports,
		summaryQueue:  d.SummaryQueue,
		doctors:       d.Doctors,
		client:        d.Client,
		validator:     d.Validator,
		log:           log,
		systemPrompt:  d.SystemPrompt,
		summaryPrompt: d.SummaryPrompt,
		promptVersion: d.PromptVersion,
		timeout:       timeout,
	}
}

// PromptVersion 返回当前使用的提示词版本，用于入库追溯（spec §7.2）。
func (s *PreconsultService) PromptVersion() string { return s.promptVersion }

// ModelName 返回当前模型标识。
func (s *PreconsultService) ModelName() string { return s.client.Model() }

// Chat 执行一轮流式对话。
//
// onDelta 每收到一段增量文本就被调用一次，由调用方（handler）写往 SSE。
// onDelta 返回错误时立即中止——这通常意味着客户端已断开。
//
// 失败时**不会**把助手回复写入对话历史：留下半截回复会让下一轮
// 模型看到不完整的上下文，用户重试时反而更混乱。
func (s *PreconsultService) Chat(
	ctx context.Context,
	userID, appointmentID uint64,
	userMessage string,
	onDelta func(delta string) error,
) error {
	message, err := s.validateMessage(userMessage)
	if err != nil {
		return err
	}
	if err := s.ensureChattable(ctx, userID, appointmentID); err != nil {
		return err
	}

	// 先落用户消息：即使后续 LLM 调用失败，用户也能看到自己说过的话，
	// 重试时不必重新输入。
	if err := s.conversation.Append(ctx, appointmentID, llm.Message{
		Role: llm.RoleUser, Content: message,
	}); err != nil {
		return apperr.Wrap(errcode.ErrInternal, "保存对话失败", err)
	}

	history, err := s.conversation.List(ctx, appointmentID)
	if err != nil {
		return apperr.Wrap(errcode.ErrInternal, "读取对话失败", err)
	}

	// 给上游调用单独设超时：客户端连接可能一直开着，
	// 不能让一个卡住的上游请求无限期占用连接与 goroutine。
	callCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	var reply strings.Builder
	streamErr := s.client.ChatStream(callCtx, llm.ChatRequest{
		SystemPrompt: s.systemPrompt,
		Messages:     history,
	}, func(delta string) error {
		reply.WriteString(delta)
		return onDelta(delta)
	})

	if streamErr != nil {
		llmCallsTotal.WithLabelValues(llmPurposeChat, classifyLLMError(streamErr)).Inc()

		// 客户端主动断开：不是服务端故障，也不该再尝试写响应
		if errors.Is(streamErr, context.Canceled) && ctx.Err() != nil {
			s.log.Info("preconsult chat interrupted by client",
				zap.Uint64("appointment_id", appointmentID))
			return streamErr
		}

		s.log.Warn("preconsult chat failed",
			zap.Uint64("appointment_id", appointmentID), zap.Error(streamErr))
		return mapLLMError(streamErr, "AI 对话服务暂时不可用")
	}

	// 用独立 context 落库：此时请求 context 可能已经接近超时，
	// 而这条写入是必须完成的（否则对话历史会缺一轮）。
	storeCtx, storeCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer storeCancel()

	if err := s.conversation.Append(storeCtx, appointmentID, llm.Message{
		Role: llm.RoleAssistant, Content: reply.String(),
	}); err != nil {
		// 回复已经发给用户了，保存失败只影响下一轮的上下文，不该让本次调用失败
		s.log.Warn("save assistant reply failed",
			zap.Uint64("appointment_id", appointmentID), zap.Error(err))
	}

	llmCallsTotal.WithLabelValues(llmPurposeChat, llmResultSuccess).Inc()
	return nil
}

// History 返回进行中的对话，供前端刷新页面后恢复上下文。
func (s *PreconsultService) History(
	ctx context.Context, userID, appointmentID uint64,
) ([]llm.Message, error) {
	if err := s.ensureChattable(ctx, userID, appointmentID); err != nil {
		return nil, err
	}

	messages, err := s.conversation.List(ctx, appointmentID)
	if err != nil {
		return nil, apperr.Wrap(errcode.ErrInternal, "读取对话失败", err)
	}
	return messages, nil
}

func (s *PreconsultService) validateMessage(raw string) (string, error) {
	message := strings.TrimSpace(raw)
	if message == "" {
		return "", apperr.New(errcode.ErrInvalidParam, "消息内容不能为空")
	}
	if len([]rune(message)) > maxUserMessageRunes {
		return "", apperr.New(errcode.ErrInvalidParam, "单条消息过长，请分批描述")
	}
	return message, nil
}

// ensureChattable 校验预约归属与状态。
//
// 用 FindItemByID 而不是单独的归属查询：它在 SQL 层就带了 user_id 条件，
// 因此"预约不存在"与"不是你的预约"返回同一结果，不会泄露他人预约是否存在。
func (s *PreconsultService) ensureChattable(ctx context.Context, userID, appointmentID uint64) error {
	item, err := s.appointments.FindItemByID(ctx, userID, appointmentID)
	if errors.Is(err, repository.ErrNotFound) {
		return apperr.New(errcode.ErrNotFound, "预约不存在")
	}
	if err != nil {
		return apperr.Wrap(errcode.ErrInternal, "查询预约失败", err)
	}

	// 已取消/已过期的预约不能再进行预问诊
	if !model.IsActiveAppointmentStatus(item.Status) {
		return apperr.New(errcode.ErrConflict, "当前预约状态不支持预问诊")
	}
	return nil
}

// classifyLLMError 把错误归类到指标标签。
func classifyLLMError(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return llmResultCanceled
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, llm.ErrTimeout):
		return llmResultTimeout
	default:
		return llmResultError
	}
}

// mapLLMError 把上游错误转成对客户端友好的业务错误码（spec §3.3 的 502xx）。
func mapLLMError(err error, message string) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, llm.ErrTimeout) {
		return apperr.Wrap(errcode.ErrLLMUpstream, "AI 服务响应超时，请稍后重试", err)
	}
	return apperr.Wrap(errcode.ErrLLMUpstream, message, err)
}

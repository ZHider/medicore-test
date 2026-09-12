package handler

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/medicore/backend/internal/apperr"
	"github.com/medicore/backend/internal/authctx"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/response"
	"github.com/medicore/backend/internal/service"
)

// SSE 事件名。前端按 event 分派处理逻辑。
const (
	sseEventDelta = "delta"
	sseEventDone  = "done"
	sseEventError = "error"
)

// PreconsultHandler 暴露 AI 预问诊端点。
type PreconsultHandler struct {
	svc *service.PreconsultService
}

// NewPreconsultHandler 构造 PreconsultHandler。
func NewPreconsultHandler(svc *service.PreconsultService) *PreconsultHandler {
	return &PreconsultHandler{svc: svc}
}

// Chat 是 SSE 流式对话端点。
//
// 用 POST + SSE 而非 EventSource：EventSource 只支持 GET，
// 无法携带消息体与 Authorization 头（spec §8.1 因此选择 fetch + ReadableStream）。
//
// 错误响应的形式取决于流是否已经开始：
//   - 流未开始：仍返回统一 JSON 信封，前端可按 code 分支处理；
//   - 流已开始：HTTP 状态码早已发出，只能通过 error 事件传递错误。
func (h *PreconsultHandler) Chat(c *gin.Context) {
	appointmentID, ok := uintParam(c, "id")
	if !ok {
		return
	}

	var req model.PreconsultChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.ErrInvalidParam)
		return
	}

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		response.FailWithMessage(c, errcode.ErrInternal, "当前服务不支持流式响应")
		return
	}

	streamStarted := false

	// startStream 在第一次真正要吐数据时才发响应头。
	// 这样前置校验失败时还能用统一信封，而不是先发 200 再报错。
	startStream := func() {
		if streamStarted {
			return
		}
		header := c.Writer.Header()
		header.Set("Content-Type", "text/event-stream")
		header.Set("Cache-Control", "no-cache")
		header.Set("Connection", "keep-alive")
		// 告诉反向代理不要缓冲，否则流式输出会被攒成一整块再下发。
		// 生产环境的 Nginx 同样需要 proxy_buffering off（spec §11.2）。
		header.Set("X-Accel-Buffering", "no")
		c.Writer.WriteHeader(http.StatusOK)
		flusher.Flush()
		streamStarted = true
	}

	writeEvent := func(event string, payload any) error {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, raw); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	err := h.svc.Chat(
		c.Request.Context(),
		authctx.MustUserID(c),
		appointmentID,
		req.Message,
		func(delta string) error {
			startStream()
			return writeEvent(sseEventDelta, gin.H{"delta": delta})
		},
	)

	if err != nil {
		// 客户端已断开：连接不可写，也没有必要再写任何东西
		if c.Request.Context().Err() != nil {
			return
		}
		if !streamStarted {
			response.FailErr(c, err)
			return
		}
		_ = writeEvent(sseEventError, gin.H{
			"code":    int(apperr.Code(err)),
			"message": apperr.Message(err),
		})
		return
	}

	if !streamStarted {
		startStream()
	}
	_ = writeEvent(sseEventDone, gin.H{"status": "ok"})
}

// History 返回进行中的对话，供前端刷新页面后恢复上下文。
func (h *PreconsultHandler) History(c *gin.Context) {
	appointmentID, ok := uintParam(c, "id")
	if !ok {
		return
	}

	messages, err := h.svc.History(c.Request.Context(), authctx.MustUserID(c), appointmentID)
	if err != nil {
		response.FailErr(c, err)
		return
	}

	items := make([]model.PreconsultMessage, 0, len(messages))
	for _, m := range messages {
		items = append(items, model.PreconsultMessage{Role: m.Role, Content: m.Content})
	}
	response.OK(c, gin.H{"items": items})
}

// Submit 结束对话并触发异步结构化总结。
//
// 返回 202 Accepted 而不是 200：总结在后台队列里跑，此刻还没有结果。
// 客户端应改为轮询 Report 端点。
func (h *PreconsultHandler) Submit(c *gin.Context) {
	appointmentID, ok := uintParam(c, "id")
	if !ok {
		return
	}

	result, err := h.svc.Submit(c.Request.Context(), authctx.MustUserID(c), appointmentID)
	if err != nil {
		response.FailErr(c, err)
		return
	}

	response.Accepted(c, "已提交，正在生成结构化总结", result)
}

// Report 返回结构化总结报告，供前端轮询状态与展示结果。
//
// 允许患者、接诊医生与管理员访问；具体归属校验在 service 层做
// （令牌只说明"你是医生"，不说明"这位患者归你接诊"）。
func (h *PreconsultHandler) Report(c *gin.Context) {
	appointmentID, ok := uintParam(c, "id")
	if !ok {
		return
	}

	report, err := h.svc.Report(c.Request.Context(), accessOf(c), appointmentID)
	if err != nil {
		response.FailErr(c, err)
		return
	}
	response.OK(c, report)
}

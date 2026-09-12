package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"go.uber.org/zap"

	"github.com/medicore/backend/internal/apperr"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/llm"
	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/repository"
)

// Submit 结束对话并触发异步结构化总结（spec §7.2 + 决策 #8）。
//
// 立即返回 202 语义而不是等 LLM 跑完：
//   - 用户提交后很可能刷新或关闭页面，同步等待会让这次总结随连接一起消失；
//   - LLM 有速率限制，队列天然提供背压；
//   - 总结耗时数秒到数十秒，同步会拖垮接口的 P99。
func (s *PreconsultService) Submit(
	ctx context.Context, userID, appointmentID uint64,
) (*model.SubmitResult, error) {
	if err := s.ensureChattable(ctx, userID, appointmentID); err != nil {
		return nil, err
	}

	messages, err := s.conversation.List(ctx, appointmentID)
	if err != nil {
		return nil, apperr.Wrap(errcode.ErrInternal, "读取对话失败", err)
	}
	if len(messages) == 0 {
		return nil, apperr.New(errcode.ErrInvalidParam, "尚未进行任何对话，无法提交")
	}

	conversationJSON, err := json.Marshal(messages)
	if err != nil {
		return nil, apperr.Wrap(errcode.ErrInternal, "序列化对话失败", err)
	}

	// 落库与去重：uk_appointment 保证一次预约只有一份报告
	report, created, err := s.reports.CreatePending(
		ctx, appointmentID, userID, string(conversationJSON), s.ModelName(), s.promptVersion,
	)
	if err != nil {
		return nil, apperr.Wrap(errcode.ErrInternal, "创建报告失败", err)
	}

	// 已经是终态：直接返回已有结果，不重复入队。
	// 这解决了"用户刷新后再次点击提交"的场景。
	if !created && report.IsTerminal() {
		return &model.SubmitResult{
			ReportID:         report.ID,
			Status:           report.Status,
			AlreadySubmitted: true,
		}, nil
	}

	// 重新入队是安全的：worker 用带状态条件的 UPDATE 抢占，
	// 重复消息只会被处理一次，其余直接确认并丢弃。
	if err := s.summaryQueue.Enqueue(ctx, report.ID); err != nil {
		return nil, apperr.Wrap(errcode.ErrInternal, "提交总结任务失败", err)
	}

	// 对话已转入报告，清掉 Redis 中的临时副本
	if err := s.conversation.Clear(ctx, appointmentID); err != nil {
		// 清理失败只影响内存占用，不影响正确性
		s.log.Warn("clear conversation after submit failed",
			zap.Uint64("appointment_id", appointmentID), zap.Error(err))
	}

	return &model.SubmitResult{
		ReportID:         report.ID,
		Status:           report.Status,
		AlreadySubmitted: !created,
	}, nil
}

// Report 按访问者身份返回某预约的结构化总结报告。
//
// spec §2.1 要求医生能"查看患者预问诊报告"，因此本方法不再只面向患者。
// 但它**不能**简单地放开给所有登录用户——必须做资源层归属校验：
//
//	患者：只能看自己的预约
//	医生：只能看**本人接诊**的预约（不是任意患者的）
//	管理员：全部读权限
//
// 少了医生那一层，任何医生改一下 URL 里的 ID 就能读到全院患者的病情描述。
func (s *PreconsultService) Report(
	ctx context.Context, access Access, appointmentID uint64,
) (*model.AIReportView, error) {
	if err := s.authorizeReport(ctx, access, appointmentID); err != nil {
		return nil, err
	}

	report, err := s.reports.FindByAppointment(ctx, appointmentID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.New(errcode.ErrNotFound, "尚未提交预问诊，暂无报告")
	}
	if err != nil {
		return nil, apperr.Wrap(errcode.ErrInternal, "查询报告失败", err)
	}
	return buildReportView(report), nil
}

// authorizeReport 实现报告的资源层归属校验。
func (s *PreconsultService) authorizeReport(
	ctx context.Context, access Access, appointmentID uint64,
) error {
	// 管理员：全部读权限（spec §2.1）
	if access.IsAdmin() {
		return nil
	}

	appt, err := s.appointments.FindByID(ctx, appointmentID)
	if errors.Is(err, repository.ErrNotFound) {
		return apperr.New(errcode.ErrNotFound, "预约不存在")
	}
	if err != nil {
		return apperr.Wrap(errcode.ErrInternal, "查询预约失败", err)
	}

	switch {
	case access.IsPatient():
		if appt.UserID != access.UserID {
			// 与"预约不存在"返回不同文案是有意的：患者只可能访问自己的 ID，
			// 出现不匹配说明是构造的请求，明确拒绝比含糊更合适。
			return apperr.New(errcode.ErrNotOwner, "无权查看他人预问诊报告")
		}
		return nil

	case access.IsDoctor():
		doctorID, dErr := resolveDoctorID(ctx, s.doctors, access.UserID)
		if dErr != nil {
			return dErr
		}
		if appt.DoctorID != doctorID {
			return apperr.New(errcode.ErrNotOwner, "无权查看非本人接诊患者的预问诊报告")
		}
		return nil

	default:
		return apperr.New(errcode.ErrForbidden, "当前角色无权查看预问诊报告")
	}
}

// SummarizeReport 处理一个总结任务，由 worker 调用。
//
// 返回值语义：
//   - nil：处理完成（或无需处理），调用方应确认消息；
//   - error：本轮处理失败，调用方应决定重投还是转入死信。
func (s *PreconsultService) SummarizeReport(ctx context.Context, reportID uint64) error {
	report, err := s.reports.FindByID(ctx, reportID)
	if errors.Is(err, repository.ErrNotFound) {
		// 报告已不存在（例如预约被清理）：没有可做的事，按成功处理避免无限重投
		s.log.Warn("summary task for missing report", zap.Uint64("report_id", reportID))
		return nil
	}
	if err != nil {
		return apperr.Wrap(errcode.ErrInternal, "查询报告失败", err)
	}

	// 已终态：幂等重放，直接成功
	if report.IsTerminal() {
		return nil
	}

	// 抢占。失败说明另一个 worker 已接手，直接成功退出。
	claimed, err := s.reports.MarkProcessing(ctx, reportID)
	if err != nil {
		return apperr.Wrap(errcode.ErrInternal, "标记报告处理中失败", err)
	}
	if !claimed {
		s.log.Info("summary task already claimed by another worker",
			zap.Uint64("report_id", reportID))
		return nil
	}

	var conversation []llm.Message
	if err := json.Unmarshal([]byte(report.Conversation), &conversation); err != nil {
		// 对话数据损坏：这是代码/数据问题，重投也不会变好，直接标记失败。
		// 落库失败时要返回错误，让 worker 把消息转入死信而不是当作已处理。
		if fErr := s.saveFailure(ctx, reportID, "对话数据损坏，无法生成总结"); fErr != nil {
			return fmt.Errorf("mark report failed: %w", fErr)
		}
		return nil
	}

	callCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	outcome := s.summarize(callCtx, conversation)

	row := repository.SummaryOutcomeRow{
		Status:        model.AIReportStatusDone,
		LLMModel:      s.ModelName(),
		PromptVersion: s.promptVersion,
		Fallback:      outcome.Fallback,
		Attempts:      outcome.Attempts,
	}
	if !outcome.Fallback {
		row.Structured = &outcome.Structured
	} else {
		// 降级：structured_json 置 null，医生端展示原始对话（spec §7.2）
		row.ErrorMessage = &outcome.Reason
	}

	if err := s.reports.SaveOutcome(ctx, reportID, row); err != nil {
		return apperr.Wrap(errcode.ErrInternal, "保存报告失败", err)
	}

	if outcome.Fallback {
		s.log.Warn("preconsult summary fell back to raw conversation",
			zap.Uint64("report_id", reportID), zap.String("reason", outcome.Reason))
	}
	return nil
}

// FailReport 把报告标记为失败。重试超限时由 worker 调用。
//
// 必须做这一步：否则报告会永远停在 processing，
// 对客户端来说与"正在处理"无法区分，医生会一直等一个不会来的结果。
//
// 必须**把错误返回出去**：worker 的死信路径要靠它判断报告是否真的被标记。
// 曾经这里无条件 return nil，落库失败只写日志，于是 worker 以为标记成功，
// 报告却留在 processing —— 恰好是这段代码要避免的那个状态。
func (s *PreconsultService) FailReport(ctx context.Context, reportID uint64, reason string) error {
	return s.saveFailure(ctx, reportID, reason)
}

func (s *PreconsultService) saveFailure(ctx context.Context, reportID uint64, reason string) error {
	row := repository.SummaryOutcomeRow{
		Status:       model.AIReportStatusFailed,
		Attempts:     1,
		ErrorMessage: &reason,
	}
	if err := s.reports.SaveOutcome(ctx, reportID, row); err != nil {
		s.log.Error("save summary failure state failed",
			zap.Uint64("report_id", reportID), zap.Error(err))
		return fmt.Errorf("save summary failure state: %w", err)
	}
	return nil
}

// buildReportView 把报告实体转成客户端视图。
func buildReportView(report *model.AIReport) *model.AIReportView {
	view := &model.AIReportView{
		ID:            report.ID,
		AppointmentID: report.AppointmentID,
		Status:        report.Status,
		Fallback:      report.Fallback,
		LLMModel:      report.LLMModel,
		PromptVersion: report.PromptVersion,
		CreatedAt:     report.CreatedAt.Format(model.APITimeLayout),
	}

	if report.StructuredRaw != nil && *report.StructuredRaw != "" {
		view.Structured = json.RawMessage(*report.StructuredRaw)
	}
	if report.Conversation != "" {
		view.Conversation = json.RawMessage(report.Conversation)
	}
	if report.ErrorMessage != nil {
		view.ErrorMessage = *report.ErrorMessage
	}
	if report.FinishedAt != nil {
		view.FinishedAt = report.FinishedAt.Format(model.APITimeLayout)
	}
	return view
}

package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/medicore/backend/internal/authctx"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/response"
	"github.com/medicore/backend/internal/service"
)

// AppointmentHandler 暴露抢号、取消、签到与候诊队列端点。
type AppointmentHandler struct {
	svc *service.AppointmentService
}

// NewAppointmentHandler 构造 AppointmentHandler。
func NewAppointmentHandler(svc *service.AppointmentService) *AppointmentHandler {
	return &AppointmentHandler{svc: svc}
}

// Grab 抢号。
//
// 用户身份一律取自令牌，绝不接受请求体里的 user_id，
// 否则任何人都能替他人抢号。
func (h *AppointmentHandler) Grab(c *gin.Context) {
	var req model.GrabRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.ErrInvalidParam)
		return
	}

	item, err := h.svc.Grab(c.Request.Context(), authctx.MustUserID(c), req)
	if err != nil {
		response.FailErr(c, err)
		return
	}
	response.OK(c, item)
}

// ListMine 游标分页返回当前用户的预约。
func (h *AppointmentHandler) ListMine(c *gin.Context) {
	page, err := h.svc.ListMine(
		c.Request.Context(),
		authctx.MustUserID(c),
		c.Query("cursor"),
		intQuery(c, "size", model.DefaultPageSize),
	)
	if err != nil {
		response.FailErr(c, err)
		return
	}
	response.OK(c, page)
}

// Cancel 取消预约。
func (h *AppointmentHandler) Cancel(c *gin.Context) {
	appointmentID, ok := uintParam(c, "id")
	if !ok {
		return
	}

	var req model.CancelRequest
	// 允许空 body：仅带 id 取消也应可用
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			response.Fail(c, errcode.ErrInvalidParam)
			return
		}
	}

	if err := h.svc.Cancel(
		c.Request.Context(), authctx.MustUserID(c), appointmentID, req.Reason,
	); err != nil {
		response.FailErr(c, err)
		return
	}
	response.OK(c, gin.H{"status": model.AppointmentStatusCancelled})
}

// CheckIn 签到并领取就诊序号。
func (h *AppointmentHandler) CheckIn(c *gin.Context) {
	appointmentID, ok := uintParam(c, "id")
	if !ok {
		return
	}

	appt, err := h.svc.CheckIn(c.Request.Context(), authctx.MustUserID(c), appointmentID)
	if err != nil {
		response.FailErr(c, err)
		return
	}

	// checked_in_at 必须与其他接口保持同一种时间格式：
	// 直接塞 time.Time 会序列化成 RFC3339（带时区偏移），
	// 前端就得为这一个字段单独写解析分支。未签到时为 null。
	var checkedInAt *string
	if appt.CheckedInAt != nil {
		s := appt.CheckedInAt.Format(model.APITimeLayout)
		checkedInAt = &s
	}

	response.OK(c, gin.H{
		"id":            appt.ID,
		"queue_no":      appt.QueueNo,
		"checked_in_at": checkedInAt,
		"status":        appt.Status,
	})
}

// Queue 返回候诊队列（医生端）。
//
// 归属校验在 service 层：路由只说明"你是医生"，不说明"这个排班归你"。
// 队列里是**未脱敏**的患者姓名与 user_id，缺了这一层，任何医生换一个排班 ID
// 就能拿到全院患者的身份信息。
func (h *AppointmentHandler) Queue(c *gin.Context) {
	scheduleID, ok := uintParam(c, "id")
	if !ok {
		return
	}

	items, err := h.svc.Queue(c.Request.Context(), accessOf(c), scheduleID)
	if err != nil {
		response.FailErr(c, err)
		return
	}
	response.OK(c, gin.H{"items": model.NonNil(items)})
}

// ListForDoctor 返回医生本人接诊的预约列表（管理员可查全部）。
//
// 与患者侧的 ListMine 是两个端点而非一个：两者响应形态不同
// （患者看医生姓名，医生看患者姓名与病历状态），混在一起会让契约模糊。
func (h *AppointmentHandler) ListForDoctor(c *gin.Context) {
	filter := model.DoctorAppointmentFilter{
		Date:   c.Query("date"),
		Status: c.Query("status"),
	}

	page, err := h.svc.ListForDoctor(
		c.Request.Context(),
		accessOf(c),
		filter,
		c.Query("cursor"),
		intQuery(c, "size", model.DefaultPageSize),
	)
	if err != nil {
		response.FailErr(c, err)
		return
	}
	response.OK(c, page)
}

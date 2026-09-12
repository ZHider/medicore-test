package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/medicore/backend/internal/authctx"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/response"
	"github.com/medicore/backend/internal/service"
)

// RecordHandler 暴露病历与处方端点。
type RecordHandler struct {
	svc *service.RecordService
}

// NewRecordHandler 构造 RecordHandler。
func NewRecordHandler(svc *service.RecordService) *RecordHandler {
	return &RecordHandler{svc: svc}
}

// access 从请求上下文取出访问者身份。
//
// 角色取自 JWT（路由层已验证），但**归属校验仍在 service 层做**：
// 令牌只说明"你是医生"，不说明"这份病历归你"。
func accessOf(c *gin.Context) service.RecordAccess {
	return service.RecordAccess{
		UserID: authctx.MustUserID(c),
		Role:   model.Role(authctx.Role(c)),
	}
}

// Write 由接诊医生书写病历与处方。
func (h *RecordHandler) Write(c *gin.Context) {
	appointmentID, ok := uintParam(c, "id")
	if !ok {
		return
	}

	var req model.WriteRecordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.ErrInvalidParam)
		return
	}

	record, err := h.svc.Write(c.Request.Context(), accessOf(c), appointmentID, req)
	if err != nil {
		response.FailErr(c, err)
		return
	}
	response.OK(c, record)
}

// Get 查看某次就诊的病历。患者本人、接诊医生与管理员可见。
func (h *RecordHandler) Get(c *gin.Context) {
	appointmentID, ok := uintParam(c, "id")
	if !ok {
		return
	}

	record, err := h.svc.Get(c.Request.Context(), accessOf(c), appointmentID)
	if err != nil {
		response.FailErr(c, err)
		return
	}
	response.OK(c, record)
}

// ListMine 游标分页返回当前身份可见的病历列表。
func (h *RecordHandler) ListMine(c *gin.Context) {
	page, err := h.svc.ListMine(
		c.Request.Context(),
		accessOf(c),
		c.Query("cursor"),
		intQuery(c, "size", model.DefaultPageSize),
	)
	if err != nil {
		response.FailErr(c, err)
		return
	}
	response.OK(c, page)
}

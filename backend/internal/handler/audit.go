package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/response"
	"github.com/medicore/backend/internal/service"
)

// AuditHandler 暴露审计日志查询（仅管理员）。
type AuditHandler struct {
	svc *service.AuditService
}

// NewAuditHandler 构造 AuditHandler。
func NewAuditHandler(svc *service.AuditService) *AuditHandler {
	return &AuditHandler{svc: svc}
}

// List 游标分页返回审计日志。
func (h *AuditHandler) List(c *gin.Context) {
	query := service.AuditQuery{
		Resource: c.Query("resource"),
		Action:   c.Query("action"),
		From:     c.Query("from"),
		To:       c.Query("to"),
	}

	// user_id 非法时不报错，而是忽略该条件——
	// 让一个可选的过滤参数写错就整条查询失败，体验很差。
	if raw := c.Query("user_id"); raw != "" {
		if id, err := strconv.ParseUint(raw, 10, 64); err == nil && id > 0 {
			query.UserID = &id
		}
	}

	page, err := h.svc.List(
		c.Request.Context(), query,
		c.Query("cursor"),
		intQuery(c, "size", model.DefaultPageSize),
	)
	if err != nil {
		response.FailErr(c, err)
		return
	}
	response.OK(c, page)
}

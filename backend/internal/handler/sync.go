package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/response"
	"github.com/medicore/backend/internal/service"
)

// SyncHandler 暴露 HIS 同步端点（仅管理员）。
type SyncHandler struct {
	svc *service.SyncService
}

// NewSyncHandler 构造 SyncHandler。
func NewSyncHandler(svc *service.SyncService) *SyncHandler {
	return &SyncHandler{svc: svc}
}

// Trigger 手动触发一次同步。
//
// 刻意做成同步返回：模拟数据源很快，管理员点完就能看到结果，
// 比"返回 202 再自己轮询"体验好得多。
// 接入真实 HIS 后若单次同步可能耗时很久，应改为异步 + 轮询（与预问诊总结同一模式）。
func (h *SyncHandler) Trigger(c *gin.Context) {
	source := c.Param("source")
	if source != service.SourceName {
		response.FailWithMessage(c, errcode.ErrNotFound,
			"未知的数据源（当前仅支持 "+service.SourceName+"）")
		return
	}

	logView, err := h.svc.Sync(c.Request.Context(), model.SyncTriggerManual)
	if err != nil {
		response.FailErr(c, err)
		return
	}
	response.OK(c, logView)
}

// ListLogs 游标分页返回同步记录，供排查历史同步结果。
func (h *SyncHandler) ListLogs(c *gin.Context) {
	page, err := h.svc.ListLogs(
		c.Request.Context(),
		c.Query("source"),
		c.Query("cursor"),
		intQuery(c, "size", model.DefaultPageSize),
	)
	if err != nil {
		response.FailErr(c, err)
		return
	}
	response.OK(c, page)
}

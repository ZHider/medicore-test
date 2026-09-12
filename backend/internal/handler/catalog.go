package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/response"
	"github.com/medicore/backend/internal/service"
)

// CatalogHandler 暴露科室、医生、排班的查询端点。
type CatalogHandler struct {
	svc *service.CatalogService
}

// NewCatalogHandler 构造 CatalogHandler。
func NewCatalogHandler(svc *service.CatalogService) *CatalogHandler {
	return &CatalogHandler{svc: svc}
}

// ListDepartments 返回启用中的科室列表。
func (h *CatalogHandler) ListDepartments(c *gin.Context) {
	items, err := h.svc.ListDepartments(c.Request.Context())
	if err != nil {
		response.FailErr(c, err)
		return
	}
	response.OK(c, gin.H{"items": model.NonNil(items)})
}

// ListDoctorsByDepartment 返回科室下的医生列表（服务端走 Redis 缓存）。
func (h *CatalogHandler) ListDoctorsByDepartment(c *gin.Context) {
	departmentID, ok := uintParam(c, "id")
	if !ok {
		return
	}

	items, err := h.svc.ListDoctorsByDepartment(c.Request.Context(), departmentID)
	if err != nil {
		response.FailErr(c, err)
		return
	}
	response.OK(c, gin.H{"items": model.NonNil(items)})
}

// DoctorDetail 返回医生详情及其所属科室。
func (h *CatalogHandler) DoctorDetail(c *gin.Context) {
	doctorID, ok := uintParam(c, "id")
	if !ok {
		return
	}

	detail, err := h.svc.DoctorDetail(c.Request.Context(), doctorID)
	if err != nil {
		response.FailErr(c, err)
		return
	}
	response.OK(c, detail)
}

// ListSchedulesByDepartment 按科室 + 就诊日查询号源，游标分页。
func (h *CatalogHandler) ListSchedulesByDepartment(c *gin.Context) {
	departmentID, ok := uintParam(c, "id")
	if !ok {
		return
	}

	page, err := h.svc.ListSchedulesByDepartmentDate(
		c.Request.Context(),
		departmentID,
		c.Query("date"),
		c.Query("cursor"),
		intQuery(c, "size", model.DefaultPageSize),
	)
	if err != nil {
		response.FailErr(c, err)
		return
	}
	response.OK(c, page)
}

// ListSchedulesByDoctor 按医生 + 日期区间查询排班，游标分页。
func (h *CatalogHandler) ListSchedulesByDoctor(c *gin.Context) {
	doctorID, ok := uintParam(c, "id")
	if !ok {
		return
	}

	page, err := h.svc.ListSchedulesByDoctor(
		c.Request.Context(),
		doctorID,
		c.Query("from"),
		c.Query("to"),
		c.Query("cursor"),
		intQuery(c, "size", model.DefaultPageSize),
	)
	if err != nil {
		response.FailErr(c, err)
		return
	}
	response.OK(c, page)
}

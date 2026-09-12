// Package handler 负责参数绑定与校验、调用 service、返回统一响应。
// 铁律：本层禁止出现 SQL 与 GORM（spec §3.2）。
package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/response"
	"github.com/medicore/backend/internal/service"
)

// HealthHandler 暴露存活与就绪探针。
type HealthHandler struct {
	svc *service.HealthService
}

// NewHealthHandler 构造 HealthHandler。
func NewHealthHandler(svc *service.HealthService) *HealthHandler {
	return &HealthHandler{svc: svc}
}

// Live 是存活探针：进程活着即返回 ok，不探测外部依赖。
// 刻意不检查依赖——依赖故障时容器不应被重启，那是 Ready 的职责。
func (h *HealthHandler) Live(c *gin.Context) {
	response.OK(c, gin.H{"status": "ok"})
}

// Ready 是就绪探针：任一依赖不可用即返回 503，让流量摘除本实例。
func (h *HealthHandler) Ready(c *gin.Context) {
	result := h.svc.Ready(c.Request.Context())
	for _, r := range result {
		if r.Status != "ok" {
			response.FailWithData(c, errcode.ErrNotReady, result)
			return
		}
	}
	response.OK(c, result)
}

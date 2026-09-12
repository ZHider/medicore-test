package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/medicore/backend/internal/authctx"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/response"
	"github.com/medicore/backend/internal/service"
)

// AuthHandler 暴露认证相关端点。
type AuthHandler struct {
	svc *service.AuthService
}

// NewAuthHandler 构造 AuthHandler。
func NewAuthHandler(svc *service.AuthService) *AuthHandler {
	return &AuthHandler{svc: svc}
}

// Register 处理患者注册。
func (h *AuthHandler) Register(c *gin.Context) {
	var req model.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.ErrInvalidParam)
		return
	}

	profile, err := h.svc.Register(c.Request.Context(), req)
	if err != nil {
		response.FailErr(c, err)
		return
	}
	response.OK(c, profile)
}

// Login 处理登录。该路由挂有限流中间件（spec §9.3）。
func (h *AuthHandler) Login(c *gin.Context) {
	var req model.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.ErrInvalidParam)
		return
	}

	tokens, err := h.svc.Login(c.Request.Context(), req)
	if err != nil {
		response.FailErr(c, err)
		return
	}
	response.OK(c, tokens)
}

// Refresh 用刷新令牌换取新的双令牌。
func (h *AuthHandler) Refresh(c *gin.Context) {
	var req model.RefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.ErrInvalidParam)
		return
	}

	tokens, err := h.svc.Refresh(c.Request.Context(), req.RefreshToken)
	if err != nil {
		response.FailErr(c, err)
		return
	}
	response.OK(c, tokens)
}

// Logout 撤销当前刷新令牌。
func (h *AuthHandler) Logout(c *gin.Context) {
	var req model.RefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, errcode.ErrInvalidParam)
		return
	}

	if err := h.svc.Logout(c.Request.Context(), req.RefreshToken); err != nil {
		response.FailErr(c, err)
		return
	}
	response.OK(c, gin.H{"status": "logged_out"})
}

// Me 返回当前登录用户的脱敏资料。
// 用户 ID 一律取自令牌，绝不接受客户端传入，否则就是越权读任意用户。
func (h *AuthHandler) Me(c *gin.Context) {
	profile, err := h.svc.Profile(c.Request.Context(), authctx.MustUserID(c))
	if err != nil {
		response.FailErr(c, err)
		return
	}
	response.OK(c, profile)
}

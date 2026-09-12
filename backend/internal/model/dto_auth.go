package model

// 认证相关 DTO。
//
// 校验规则放在 binding tag 上，handler 负责触发；service 只信任已绑定的输入，
// 但仍会做一次业务级校验（例如手机号是否已注册）。

// RegisterRequest 是患者注册请求。
// 说明：注册只开放患者角色；医生/管理员账号由管理端创建，避免自助提权。
type RegisterRequest struct {
	Phone    string `json:"phone" binding:"required,len=11,numeric"`
	Name     string `json:"name" binding:"required,min=2,max=32"`
	Password string `json:"password" binding:"required,min=8,max=64"`
	IDCard   string `json:"id_card" binding:"omitempty,len=18"`
}

// LoginRequest 是登录请求。
type LoginRequest struct {
	Phone    string `json:"phone" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// RefreshRequest 是刷新令牌请求。
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// TokenPair 是登录/刷新成功后返回的双令牌。
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
}

// UserProfile 是返回给客户端的用户信息，敏感字段已脱敏。
type UserProfile struct {
	ID    uint64 `json:"id"`
	Role  Role   `json:"role"`
	Name  string `json:"name"`
	Phone string `json:"phone"`
}

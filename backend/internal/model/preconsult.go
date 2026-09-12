package model

// PreconsultChatRequest 是预问诊对话请求。
type PreconsultChatRequest struct {
	Message string `json:"message" binding:"required"`
}

// PreconsultMessage 是返回给前端的单条对话消息。
type PreconsultMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

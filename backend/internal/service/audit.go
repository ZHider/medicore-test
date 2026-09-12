package service

import (
	"context"
	"time"

	"github.com/medicore/backend/internal/apperr"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/repository"
)

// AuditDeps 是 AuditService 的依赖集合。
type AuditDeps struct {
	Logs auditStore
}

// AuditService 提供审计日志查询（spec §2.1：管理员可查审计日志）。
type AuditService struct {
	logs auditStore
}

// NewAuditService 构造 AuditService。
func NewAuditService(d AuditDeps) *AuditService {
	return &AuditService{logs: d.Logs}
}

// AuditQuery 是审计查询条件（来自请求参数，全部可选）。
type AuditQuery struct {
	UserID   *uint64
	Resource string
	Action   string
	From     string
	To       string
}

// List 游标分页查询审计日志。
func (s *AuditService) List(
	ctx context.Context, query AuditQuery, cursorRaw string, size int,
) (model.CursorPage[model.AuditLogView], error) {
	var empty model.CursorPage[model.AuditLogView]

	filter := repository.AuditFilter{
		UserID:   query.UserID,
		Resource: query.Resource,
		Action:   query.Action,
	}

	var err error
	if filter.From, err = parseOptionalTime(query.From); err != nil {
		return empty, err
	}
	if filter.To, err = parseOptionalTime(query.To); err != nil {
		return empty, err
	}
	if !filter.From.IsZero() && !filter.To.IsZero() && filter.To.Before(filter.From) {
		return empty, apperr.New(errcode.ErrInvalidParam, "结束时间不能早于开始时间")
	}

	cursor, err := parseCursor(cursorRaw)
	if err != nil {
		return empty, err
	}
	limit := model.NormalizePageSize(size)

	rows, err := s.logs.ListByCursor(ctx, filter, cursor, limit)
	if err != nil {
		return empty, apperr.Wrap(errcode.ErrInternal, "查询审计日志失败", err)
	}
	return model.NewCursorPage(rows, limit, model.AuditLogView.GetID), nil
}

// parseOptionalTime 解析可选的日期时间参数，空串表示不限。
func parseOptionalTime(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	// 允许只给日期（2026-09-12）或完整时间（2026-09-12 10:00:00）
	for _, layout := range []string{time.DateOnly, "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, apperr.New(errcode.ErrInvalidParam,
		"时间格式应为 YYYY-MM-DD 或 YYYY-MM-DD HH:MM:SS")
}

package repository

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/medicore/backend/internal/model"
)

// AuditRepo 是 audit_logs 表的数据访问。
type AuditRepo struct {
	db *gorm.DB
}

// NewAuditRepo 构造 AuditRepo。
func NewAuditRepo(db *gorm.DB) *AuditRepo {
	return &AuditRepo{db: db}
}

// InsertBatch 批量写入审计记录。
//
// 用批量插入而不是逐条：审计写入在每个请求上都会发生，
// 逐条往返会把数据库连接池和网络 RTT 变成吞吐瓶颈。
func (r *AuditRepo) InsertBatch(ctx context.Context, entries []model.AuditLog) error {
	if len(entries) == 0 {
		return nil
	}
	if err := r.db.WithContext(ctx).CreateInBatches(entries, len(entries)).Error; err != nil {
		return fmt.Errorf("insert audit logs: %w", err)
	}
	return nil
}

// AuditFilter 是审计日志的查询条件。
type AuditFilter struct {
	UserID   *uint64
	Resource string
	Action   string
	// From / To 为时间范围，零值表示不限。
	From time.Time
	To   time.Time
}

// ListByCursor 游标分页查询审计日志，供管理员使用。
//
// 审计表是写入最频繁的表，因此这条查询必须走索引：
// idx_user_cursor (user_id, id DESC)、idx_resource (resource, resource_id)、
// idx_created (created_at)。默认按主键倒序（最新在前）最省事。
func (r *AuditRepo) ListByCursor(
	ctx context.Context, filter AuditFilter, cursor uint64, limit int,
) ([]model.AuditLogView, error) {
	q := r.db.WithContext(ctx).
		Table("audit_logs AS a").
		Select(`a.id, a.trace_id, a.user_id, COALESCE(a.role, '') AS role,
		        a.method, a.path,
		        COALESCE(a.resource, '') AS resource, a.resource_id,
		        COALESCE(a.action, '') AS action,
		        a.status_code, a.latency_ms,
		        COALESCE(a.client_ip, '') AS client_ip,
		        DATE_FORMAT(a.created_at, '%Y-%m-%d %H:%i:%s') AS created_at`)

	if filter.UserID != nil {
		q = q.Where("a.user_id = ?", *filter.UserID)
	}
	if filter.Resource != "" {
		q = q.Where("a.resource = ?", filter.Resource)
	}
	if filter.Action != "" {
		q = q.Where("a.action = ?", filter.Action)
	}
	if !filter.From.IsZero() {
		q = q.Where("a.created_at >= ?", filter.From)
	}
	if !filter.To.IsZero() {
		q = q.Where("a.created_at <= ?", filter.To)
	}
	if cursor > 0 {
		q = q.Where("a.id < ?", cursor)
	}

	var items []model.AuditLogView
	if err := q.Order("a.id DESC").Limit(limit + 1).Scan(&items).Error; err != nil {
		return nil, fmt.Errorf("list audit logs: %w", err)
	}
	return items, nil
}

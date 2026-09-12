package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/medicore/backend/internal/model"
)

// SyncLogRepo 是 sync_logs 表的数据访问。
type SyncLogRepo struct {
	db *gorm.DB
}

// NewSyncLogRepo 构造 SyncLogRepo。
func NewSyncLogRepo(db *gorm.DB) *SyncLogRepo {
	return &SyncLogRepo{db: db}
}

// Start 创建一条 running 状态的同步记录，返回其 ID。
//
// 先写 running 再执行同步，是为了让"同步中途崩溃"也能在表里留下痕迹——
// 若只在结束时写一条记录，崩溃的那次同步会完全不可见。
func (r *SyncLogRepo) Start(ctx context.Context, source, trigger string) (uint64, error) {
	log := model.SyncLog{
		Source:      source,
		TriggerType: trigger,
		Status:      model.SyncStatusRunning,
		StartedAt:   time.Now(),
	}
	if err := r.db.WithContext(ctx).Create(&log).Error; err != nil {
		return 0, fmt.Errorf("create sync log: %w", err)
	}
	return log.ID, nil
}

// Finish 写入同步结果。
func (r *SyncLogRepo) Finish(
	ctx context.Context, id uint64, result SyncResult,
) error {
	now := time.Now()
	if err := r.db.WithContext(ctx).
		Model(&model.SyncLog{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":      result.Status,
			"total":       result.Total,
			"succeeded":   result.Succeeded,
			"failed":      result.Failed,
			"error_msg":   result.ErrorMessage,
			"finished_at": now,
		}).Error; err != nil {
		return fmt.Errorf("finish sync log: %w", err)
	}
	return nil
}

// SyncResult 是一次同步的结果统计。
type SyncResult struct {
	Total        int
	Succeeded    int
	Failed       int
	Status       string
	ErrorMessage *string
}

// FindViewByID 按主键返回同步记录视图。
func (r *SyncLogRepo) FindViewByID(ctx context.Context, id uint64) (*model.SyncLogView, error) {
	var view model.SyncLogView

	err := r.db.WithContext(ctx).
		Table("sync_logs").
		Select(`id, source, trigger_type, total, succeeded, failed, status,
		        COALESCE(error_msg, '') AS error_msg,
		        DATE_FORMAT(started_at, '%Y-%m-%d %H:%i:%s') AS started_at,
		        COALESCE(DATE_FORMAT(finished_at, '%Y-%m-%d %H:%i:%s'), '') AS finished_at`).
		Where("id = ?", id).
		Scan(&view).Error
	if err != nil {
		return nil, fmt.Errorf("find sync log: %w", err)
	}
	if view.ID == 0 {
		return nil, ErrNotFound
	}
	return &view, nil
}

// ListByCursor 游标分页返回同步记录，供管理端排查。
func (r *SyncLogRepo) ListByCursor(
	ctx context.Context, source string, cursor uint64, limit int,
) ([]model.SyncLogView, error) {
	q := r.db.WithContext(ctx).
		Table("sync_logs").
		Select(`id, source, trigger_type, total, succeeded, failed, status,
		        COALESCE(error_msg, '') AS error_msg,
		        DATE_FORMAT(started_at, '%Y-%m-%d %H:%i:%s') AS started_at,
		        COALESCE(DATE_FORMAT(finished_at, '%Y-%m-%d %H:%i:%s'), '') AS finished_at`)

	if source != "" {
		q = q.Where("source = ?", source)
	}
	if cursor > 0 {
		q = q.Where("id < ?", cursor)
	}

	var items []model.SyncLogView
	if err := q.Order("id DESC").Limit(limit + 1).Scan(&items).Error; err != nil {
		return nil, fmt.Errorf("list sync logs: %w", err)
	}
	return items, nil
}

// LastFinished 返回某数据源最近一次已完成的同步记录。
func (r *SyncLogRepo) LastFinished(ctx context.Context, source string) (*model.SyncLog, error) {
	var log model.SyncLog

	err := r.db.WithContext(ctx).
		Where("source = ? AND status <> ?", source, model.SyncStatusRunning).
		Order("id DESC").First(&log).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find last sync log: %w", err)
	}
	return &log, nil
}

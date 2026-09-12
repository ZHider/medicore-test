package service

import (
	"context"
	"errors"
	"time"

	"github.com/medicore/backend/internal/apperr"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/repository"
)

// 医生排班查询的默认时间窗。
const defaultScheduleRangeDays = 7

// CatalogDeps 是 CatalogService 的依赖集合。
type CatalogDeps struct {
	Departments departmentStore
	Doctors     doctorStore
	Schedules   scheduleStore
}

// CatalogService 提供科室、医生、排班的只读查询。
type CatalogService struct {
	departments departmentStore
	doctors     doctorStore
	schedules   scheduleStore
}

// NewCatalogService 构造 CatalogService。
func NewCatalogService(d CatalogDeps) *CatalogService {
	return &CatalogService{
		departments: d.Departments,
		doctors:     d.Doctors,
		schedules:   d.Schedules,
	}
}

// ListDepartments 返回启用中的科室列表。
func (s *CatalogService) ListDepartments(ctx context.Context) ([]model.DepartmentBrief, error) {
	items, err := s.departments.ListActive(ctx)
	if err != nil {
		return nil, apperr.Wrap(errcode.ErrInternal, "查询科室失败", err)
	}
	return items, nil
}

// ListDoctorsByDepartment 返回科室下的医生（走 Redis 缓存）。
//
// 先校验科室是否存在：否则不存在的科室会返回空列表，
// 客户端无法区分"科室不存在"与"该科室暂无医生"。
func (s *CatalogService) ListDoctorsByDepartment(ctx context.Context, departmentID uint64) ([]model.DoctorListItem, error) {
	if err := s.ensureDepartment(ctx, departmentID); err != nil {
		return nil, err
	}

	items, err := s.doctors.ListByDepartment(ctx, departmentID)
	if err != nil {
		return nil, apperr.Wrap(errcode.ErrInternal, "查询医生列表失败", err)
	}
	return items, nil
}

// DoctorDetail 返回医生详情及其所属科室（多对多）。
func (s *CatalogService) DoctorDetail(ctx context.Context, doctorID uint64) (*model.DoctorDetail, error) {
	detail, err := s.doctors.FindDetail(ctx, doctorID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.New(errcode.ErrNotFound, "医生不存在")
	}
	if err != nil {
		return nil, apperr.Wrap(errcode.ErrInternal, "查询医生详情失败", err)
	}

	departments, err := s.doctors.ListDepartmentsOfDoctor(ctx, doctorID)
	if err != nil {
		return nil, apperr.Wrap(errcode.ErrInternal, "查询医生所属科室失败", err)
	}
	detail.Departments = departments
	return detail, nil
}

// ListSchedulesByDepartmentDate 按"科室 + 就诊日"查询可挂号源（游标分页）。
func (s *CatalogService) ListSchedulesByDepartmentDate(
	ctx context.Context,
	departmentID uint64,
	dateRaw, cursorRaw string,
	size int,
) (model.CursorPage[model.ScheduleListItem], error) {
	var empty model.CursorPage[model.ScheduleListItem]

	if err := s.ensureDepartment(ctx, departmentID); err != nil {
		return empty, err
	}

	day, err := parseDateOrToday(dateRaw)
	if err != nil {
		return empty, err
	}
	cursor, err := parseCursor(cursorRaw)
	if err != nil {
		return empty, err
	}
	limit := model.NormalizePageSize(size)

	rows, err := s.schedules.ListByDepartmentDate(ctx, departmentID, day, cursor, limit)
	if err != nil {
		return empty, apperr.Wrap(errcode.ErrInternal, "查询排班失败", err)
	}
	return model.NewCursorPage(rows, limit, model.ScheduleListItem.GetID), nil
}

// ListSchedulesByDoctor 按"医生 + 日期区间"查询排班（游标分页）。
// 区间留空时默认"今天起 7 天"。
func (s *CatalogService) ListSchedulesByDoctor(
	ctx context.Context,
	doctorID uint64,
	fromRaw, toRaw, cursorRaw string,
	size int,
) (model.CursorPage[model.ScheduleListItem], error) {
	var empty model.CursorPage[model.ScheduleListItem]

	from, err := parseDateOrToday(fromRaw)
	if err != nil {
		return empty, err
	}
	to := from.AddDate(0, 0, defaultScheduleRangeDays)
	if toRaw != "" {
		to, err = parseDateOrToday(toRaw)
		if err != nil {
			return empty, err
		}
	}
	// 区间反向时直接拒绝，否则会静默返回空列表，让人误以为"没有排班"。
	if to.Before(from) {
		return empty, apperr.New(errcode.ErrInvalidParam, "结束日期不能早于开始日期")
	}

	cursor, err := parseCursor(cursorRaw)
	if err != nil {
		return empty, err
	}
	limit := model.NormalizePageSize(size)

	rows, err := s.schedules.ListByDoctorRange(ctx, doctorID, from, to, cursor, limit)
	if err != nil {
		return empty, apperr.Wrap(errcode.ErrInternal, "查询医生排班失败", err)
	}
	return model.NewCursorPage(rows, limit, model.ScheduleListItem.GetID), nil
}

func (s *CatalogService) ensureDepartment(ctx context.Context, departmentID uint64) error {
	ok, err := s.departments.Exists(ctx, departmentID)
	if err != nil {
		return apperr.Wrap(errcode.ErrInternal, "校验科室失败", err)
	}
	if !ok {
		return apperr.New(errcode.ErrNotFound, "科室不存在")
	}
	return nil
}

// parseDateOrToday 解析 YYYY-MM-DD，留空表示今天。
func parseDateOrToday(raw string) (time.Time, error) {
	if raw == "" {
		return time.Now(), nil
	}
	day, err := time.ParseInLocation(time.DateOnly, raw, time.Local)
	if err != nil {
		return time.Time{}, apperr.New(errcode.ErrInvalidParam, "日期格式应为 YYYY-MM-DD")
	}
	return day, nil
}

// parseCursor 把游标解析错误统一转成 400，而不是让内部错误暴露成 500。
func parseCursor(raw string) (uint64, error) {
	cursor, err := model.ParseCursor(raw)
	if err != nil {
		return 0, apperr.New(errcode.ErrInvalidParam, "游标格式不正确")
	}
	return cursor, nil
}

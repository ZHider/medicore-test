package service

import (
	"context"
	"errors"
	"strings"

	"github.com/medicore/backend/internal/apperr"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/repository"
)

// Access 是资源层归属校验所需的访问者身份。
//
// 为什么需要它：路由层的 RBAC 只能回答"这个角色能不能访问这类接口"，
// 无法回答"这个患者能不能看**这一份**病历"、"这个医生是不是**这位**患者的接诊医生"。
// spec §2.2 要求双层校验——缺了资源层校验，
// 任何人只要改一下 URL 里的 ID 就能读到别人的病历与预问诊报告。
//
// 三个 service（病历、预约、预问诊）共用它，因此放在 service 包顶层而非某个模块文件里。
type Access struct {
	UserID uint64
	Role   model.Role
}

// IsAdmin 判断是否为管理员。管理员拥有全部读权限（spec §2.1）。
func (a Access) IsAdmin() bool { return a.Role == model.RoleAdmin }

// IsDoctor 判断是否为医生。
func (a Access) IsDoctor() bool { return a.Role == model.RoleDoctor }

// IsPatient 判断是否为患者。
func (a Access) IsPatient() bool { return a.Role == model.RolePatient }

// resolveDoctorID 由用户 ID 解析医生档案 ID。
//
// appointments / medical_records / ai_pre_reports 三张表存的都是 doctors.id，
// 直接拿 users.id 比较会**永远不相等**，所以这一步在所有涉及医生的归属校验里都躲不掉。
//
// 收敛成包级函数而不是三个 service 各写一份：错误码与文案必须一致，
// 否则同一个"没有医生档案"的情况会在不同接口返回不同的 code，前端无法统一处理。
// 曾经这段逻辑在 appointment.go、record.go、preconsult_submit.go 里各有一份。
func resolveDoctorID(ctx context.Context, doctors doctorLookup, userID uint64) (uint64, error) {
	doctorID, err := doctors.FindIDByUserID(ctx, userID)
	if errors.Is(err, repository.ErrNotFound) {
		return 0, apperr.New(errcode.ErrForbidden, "当前账号没有医生档案")
	}
	if err != nil {
		return 0, apperr.Wrap(errcode.ErrInternal, "查询医生档案失败", err)
	}
	return doctorID, nil
}

// optionalTrimmedString 把空串（含纯空白）转成 nil，避免数据库里存一堆空字符串。
//
// 名字里的 Trimmed 是刻意的：internal/audit 有一个不做 Trim 的同名概念，
// 因为审计要原样保留 User-Agent / ClientIP。两者不可互换。
func optionalTrimmedString(s string) *string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

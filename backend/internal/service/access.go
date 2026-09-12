package service

import "github.com/medicore/backend/internal/model"

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

// Package his 定义与 HIS（医院信息系统）对接的数据契约。
//
// 为什么单独成包，而不是把这些类型留在 mockhis 里：
// service 层的同步编排要按这套契约编写。若契约类型定义在 mockhis
// ——一个名字里就写着"模拟"的包——依赖方向就反了：
// 将来接真实 HIS 的适配器将不得不 import 一个 mock 包来拿类型。
// 契约单独成包后，mockhis 与真实适配器都只是它的实现方之一。
//
// 本包**只放数据类型，不放接口**：按本项目一贯的 Go 惯例
// （见 service/deps.go 的说明），接口由使用方声明，这里只提供它所需要的形状。
package his

import "time"

// Department 是 HIS 侧的科室记录。
type Department struct {
	ExternalID  string `json:"external_id"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
	SortOrder   int    `json:"sort_order"`
}

// Doctor 是 HIS 侧的医生记录。
type Doctor struct {
	ExternalID     string `json:"external_id"`
	Name           string `json:"name"`
	Phone          string `json:"phone"`
	Title          string `json:"title"`
	Specialty      string `json:"specialty"`
	DepartmentCode string `json:"department_code"`
	ConsultFee     string `json:"consult_fee"`
	IsPrimary      bool   `json:"is_primary"`
}

// Schedule 是 HIS 侧的排班记录。
//
// SchedDate 是**绝对日期**：真实 HIS 给的就是日期本身。
// "相对今天多少天"是演示数据文件的一种写法，属于 mockhis 的内部细节，
// 由它在读取时换算成绝对日期，不泄漏到契约里。
type Schedule struct {
	ExternalID     string    `json:"external_id"`
	DoctorExternal string    `json:"doctor_external_id"`
	DepartmentCode string    `json:"department_code"`
	SchedDate      time.Time `json:"sched_date"`
	TimeSlot       string    `json:"time_slot"`
	Total          int       `json:"total"`
}

// Dataset 是一次同步拉取到的全部数据。
type Dataset struct {
	Departments []Department `json:"departments"`
	Doctors     []Doctor     `json:"doctors"`
	Schedules   []Schedule   `json:"schedules"`
}

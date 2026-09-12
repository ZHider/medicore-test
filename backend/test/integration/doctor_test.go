//go:build integration

package integration

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 本文件覆盖两个 spec §2.1 要求的医生能力：
//   1. 医生查看本人预约列表（GET /api/v1/doctor/appointments）
//   2. 医生查看患者预问诊报告（GET /api/v1/appointments/:id/preconsult/report）
//
// 两者都是"资源层归属校验"的典型场景：仅凭角色无法判断
// "这位患者是否归你接诊"，必须在 service 层再校验一次。

// --- 医生接诊列表 ---

func TestDoctorAppointments_ListsOwnPatients(t *testing.T) {
	doc := seedDoctor(t, 30)
	token := mintToken(t, doc.UserID, "doctor")
	clearRateLimits(t)

	// 患者抢一个号
	patientToken, _ := registerPatient(t)
	_, grab := call(t, request{
		method: "POST", path: "/api/v1/appointments", token: patientToken,
		body: map[string]any{
			"schedule_id":     doc.ScheduleID,
			"idempotency_key": fmt.Sprintf("doc-list-%d", doc.ScheduleID),
		},
	})
	require.Equal(t, 0, grab.Code, "抢号失败: %s", grab.Message)

	_, env := call(t, request{
		method: "GET", path: "/api/v1/doctor/appointments", token: token,
	})
	require.Equal(t, 0, env.Code, "查询接诊列表失败: %s", env.Message)

	page := decode[struct {
		Items []struct {
			ID           uint64 `json:"id"`
			PatientID    uint64 `json:"patient_id"`
			PatientName  string `json:"patient_name"`
			Status       string `json:"status"`
			ScheduleID   uint64 `json:"schedule_id"`
			DepartmentID uint64 `json:"department_id"`
			HasRecord    bool   `json:"has_record"`
		} `json:"items"`
		HasMore bool `json:"has_more"`
	}](t, env)

	require.Len(t, page.Items, 1)
	// 医生视角必须带患者信息与病历状态——这正是它不复用患者侧结构的原因
	assert.Equal(t, doc.ScheduleID, page.Items[0].ScheduleID)
	assert.NotEmpty(t, page.Items[0].PatientName, "医生需要患者姓名来识别")
	assert.False(t, page.Items[0].HasRecord, "尚未写病历时应为 false")
}

// 医生只能看到**本人接诊**的预约，不能看到其他医生的。
func TestDoctorAppointments_ExcludesOtherDoctorsPatients(t *testing.T) {
	mine := seedDoctor(t, 30)
	other := seedDoctor(t, 30)
	clearRateLimits(t)

	patientToken, _ := registerPatient(t)
	_, grab := call(t, request{
		method: "POST", path: "/api/v1/appointments", token: patientToken,
		body: map[string]any{
			"schedule_id":     other.ScheduleID, // 抢的是**别的医生**的号
			"idempotency_key": fmt.Sprintf("other-doc-%d", other.ScheduleID),
		},
	})
	require.Equal(t, 0, grab.Code, "抢号失败: %s", grab.Message)

	_, env := call(t, request{
		method: "GET", path: "/api/v1/doctor/appointments",
		token: mintToken(t, mine.UserID, "doctor"),
	})
	require.Equal(t, 0, env.Code)

	page := decode[struct {
		Items []struct {
			ScheduleID uint64 `json:"schedule_id"`
		} `json:"items"`
	}](t, env)

	for _, item := range page.Items {
		assert.NotEqual(t, other.ScheduleID, item.ScheduleID,
			"不得看到其他医生的患者的预约")
	}
}

// 患者不能访问医生工作台端点。
func TestDoctorAppointments_PatientIsForbidden(t *testing.T) {
	patientToken, _ := registerPatient(t)

	_, env := call(t, request{
		method: "GET", path: "/api/v1/doctor/appointments", token: patientToken,
	})

	assert.Equal(t, 40301, env.Code, "患者访问医生端点应被 403 拒绝")
}

func TestDoctorAppointments_RequiresAuth(t *testing.T) {
	_, env := call(t, request{method: "GET", path: "/api/v1/doctor/appointments"})

	assert.Equal(t, 40101, env.Code)
}

// 非法筛选条件必须被拒绝：拼错的状态会静默返回空列表，极具误导性。
func TestDoctorAppointments_RejectsInvalidFilter(t *testing.T) {
	doc := seedDoctor(t, 5)
	token := mintToken(t, doc.UserID, "doctor")

	tests := []struct {
		name string
		path string
	}{
		{"日期格式错误", "/api/v1/doctor/appointments?date=2026/09/12"},
		{"非法状态", "/api/v1/doctor/appointments?status=not-a-status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, env := call(t, request{method: "GET", path: tt.path, token: token})
			assert.Equal(t, 40001, env.Code)
		})
	}
}

func TestDoctorAppointments_AcceptsValidFilter(t *testing.T) {
	doc := seedDoctor(t, 5)
	token := mintToken(t, doc.UserID, "doctor")

	_, env := call(t, request{
		method: "GET",
		path:   "/api/v1/doctor/appointments?date=2026-01-01&status=pending&size=10",
		token:  token,
	})

	assert.Equal(t, 0, env.Code, "合法筛选不应报错")
}

// --- 医生查看预问诊报告 ---

// preconsultReport 让患者走完"抢号 → 对话 → 提交"并返回 appointmentID。
func preconsultSubmit(t *testing.T, scheduleID uint64, idemSuffix string) (uint64, string) {
	t.Helper()

	patientToken, _ := registerPatient(t)

	_, grab := call(t, request{
		method: "POST", path: "/api/v1/appointments", token: patientToken,
		body: map[string]any{
			"schedule_id":     scheduleID,
			"idempotency_key": "rx-" + idemSuffix,
		},
	})
	require.Equal(t, 0, grab.Code, "抢号失败: %s", grab.Message)

	appt := decode[struct {
		ID uint64 `json:"id"`
	}](t, grab)

	// 一轮对话，让报告里有内容
	_, chat := call(t, request{
		method: "POST", path: fmt.Sprintf("/api/v1/appointments/%d/preconsult/chat", appt.ID),
		token: patientToken,
		body:  map[string]any{"message": "我上腹隐痛三天了"},
	})
	require.Equal(t, 0, chat.Code, "对话失败: %s", chat.Message)

	resp, submit := call(t, request{
		method: "POST", path: fmt.Sprintf("/api/v1/appointments/%d/preconsult/submit", appt.ID),
		token: patientToken,
	})
	require.Equal(t, 202, resp.StatusCode, "提交是异步操作，应返回 202 而非 200")
	require.Equal(t, 0, submit.Code, "提交失败: %s", submit.Message)

	return appt.ID, patientToken
}

// 接诊医生可以查看患者已提交的预问诊报告（spec §2.1）。
func TestPreconsultReport_AttendingDoctorCanRead(t *testing.T) {
	doc := seedDoctor(t, 30)
	apptID, _ := preconsultSubmit(t, doc.ScheduleID, fmt.Sprintf("doc-read-%d", doc.ScheduleID))

	doctorToken := mintToken(t, doc.UserID, "doctor")
	_, env := call(t, request{
		method: "GET",
		path:   fmt.Sprintf("/api/v1/appointments/%d/preconsult/report", apptID),
		token:  doctorToken,
	})

	require.Equal(t, 0, env.Code, "接诊医生应能读取报告: %s", env.Message)

	report := decode[struct {
		Status       string `json:"status"`
		Conversation []struct {
			Role string `json:"role"`
		} `json:"conversation"`
	}](t, env)

	assert.NotEmpty(t, report.Status)
	assert.NotEmpty(t, report.Conversation, "报告应带出对话记录供医生查看")
}

// 非接诊医生不得查看他人患者的报告——这是本次补齐的关键安全边界。
func TestPreconsultReport_OtherDoctorIsForbidden(t *testing.T) {
	mine := seedDoctor(t, 30)
	other := seedDoctor(t, 30)
	apptID, _ := preconsultSubmit(t, mine.ScheduleID, fmt.Sprintf("owner-%d", mine.ScheduleID))

	// other 医生不是这个预约的接诊医生
	otherToken := mintToken(t, other.UserID, "doctor")
	_, env := call(t, request{
		method: "GET",
		path:   fmt.Sprintf("/api/v1/appointments/%d/preconsult/report", apptID),
		token:  otherToken,
	})

	assert.Equal(t, 40302, env.Code,
		"非接诊医生不得查看他人患者的预问诊报告（40302 无权访问他人资源）")
}

// 患者仍可读自己的报告（不能因为放开给医生而破坏原有行为）。
func TestPreconsultReport_PatientStillReadsOwn(t *testing.T) {
	doc := seedDoctor(t, 30)
	apptID, patientToken := preconsultSubmit(t, doc.ScheduleID, fmt.Sprintf("self-%d", doc.ScheduleID))

	_, env := call(t, request{
		method: "GET",
		path:   fmt.Sprintf("/api/v1/appointments/%d/preconsult/report", apptID),
		token:  patientToken,
	})

	assert.Equal(t, 0, env.Code)
}

// 患者不得读他人的报告。
func TestPreconsultReport_OtherPatientIsForbidden(t *testing.T) {
	doc := seedDoctor(t, 30)
	apptID, _ := preconsultSubmit(t, doc.ScheduleID, fmt.Sprintf("p2-%d", doc.ScheduleID))

	otherToken, _ := registerPatient(t)
	_, env := call(t, request{
		method: "GET",
		path:   fmt.Sprintf("/api/v1/appointments/%d/preconsult/report", apptID),
		token:  otherToken,
	})

	assert.Equal(t, 40302, env.Code)
}

// 管理员拥有全部读权限（spec §2.1）。
func TestPreconsultReport_AdminCanReadAny(t *testing.T) {
	doc := seedDoctor(t, 30)
	apptID, _ := preconsultSubmit(t, doc.ScheduleID, fmt.Sprintf("adm-%d", doc.ScheduleID))

	adminToken := mintToken(t, 900001, "admin")
	_, env := call(t, request{
		method: "GET",
		path:   fmt.Sprintf("/api/v1/appointments/%d/preconsult/report", apptID),
		token:  adminToken,
	})

	assert.Equal(t, 0, env.Code, "管理员应能读取任意报告")
}

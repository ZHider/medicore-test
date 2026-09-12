//go:build integration

package integration

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 候诊队列的归属校验。
//
// 这个端点返回的是**未脱敏**的患者姓名与 user_id（医生要能叫号，脱敏会让它失去意义）。
// 正因为不脱敏，它必须像病历、预问诊报告一样在 service 层校验排班归属——
// 否则任何医生换一个排班 ID 就能枚举全院患者的身份信息。
//
// 此前该端点只有路由层 RequireRole(doctor)，且没有任何测试覆盖。

// queueAppointment 抢号并签到，返回预约 ID——只有签到后的患者才会进队列。
func queueAppointment(t *testing.T, scheduleID uint64, idemKey string) (appointmentID uint64, patientUserID uint64) {
	t.Helper()

	patientToken, userID := registerPatient(t)
	_, grab := call(t, request{
		method: "POST", path: "/api/v1/appointments", token: patientToken,
		body: map[string]any{"schedule_id": scheduleID, "idempotency_key": idemKey},
	})
	require.Equal(t, 0, grab.Code, "抢号失败: %s", grab.Message)

	appt := decode[struct {
		ID uint64 `json:"id"`
	}](t, grab)

	_, checkin := call(t, request{
		method: "POST", path: fmt.Sprintf("/api/v1/appointments/%d/checkin", appt.ID),
		token: patientToken,
	})
	require.Equal(t, 0, checkin.Code, "签到失败: %s", checkin.Message)

	return appt.ID, userID
}

// 接诊医生能读到自己排班的队列，且能看到未打码的患者姓名。
func TestQueue_AttendingDoctorCanRead(t *testing.T) {
	doc := seedDoctor(t, 30)
	clearRateLimits(t)

	_, patientID := queueAppointment(t, doc.ScheduleID, fmt.Sprintf("queue-own-%d", doc.ScheduleID))

	_, env := call(t, request{
		method: "GET", path: fmt.Sprintf("/api/v1/schedules/%d/queue", doc.ScheduleID),
		token: mintToken(t, doc.UserID, "doctor"),
	})
	require.Equal(t, 0, env.Code, "接诊医生应能读取本人排班的队列: %s", env.Message)

	items := decode[struct {
		Items []struct {
			AppointmentID uint64 `json:"appointment_id"`
			PatientID     uint64 `json:"patient_id"`
			PatientName   string `json:"patient_name"`
		} `json:"items"`
	}](t, env)

	require.Len(t, items.Items, 1)
	assert.Equal(t, patientID, items.Items[0].PatientID)
	// 刻意不脱敏：医生必须能识别面前的患者
	assert.NotEmpty(t, items.Items[0].PatientName)
	assert.NotContains(t, items.Items[0].PatientName, "*", "队列姓名不应被打码")
}

// 非接诊医生不能读到别人排班的队列 —— 这是本轮修掉的信息泄露点。
func TestQueue_OtherDoctorIsForbidden(t *testing.T) {
	attending := seedDoctor(t, 30)
	other := seedDoctor(t, 30)
	clearRateLimits(t)

	queueAppointment(t, attending.ScheduleID, fmt.Sprintf("queue-other-%d", attending.ScheduleID))

	_, env := call(t, request{
		method: "GET", path: fmt.Sprintf("/api/v1/schedules/%d/queue", attending.ScheduleID),
		token: mintToken(t, other.UserID, "doctor"), // 另一位医生
	})

	assert.Equal(t, 40302, env.Code, "非接诊医生必须被拒绝，而不是拿到患者姓名")
	assert.NotContains(t, string(env.Data), "patient_name",
		"越权响应里不应带出任何队列内容")
}

// 管理员拥有全部读权限（spec §2.1）。
func TestQueue_AdminCanReadAny(t *testing.T) {
	doc := seedDoctor(t, 30)
	clearRateLimits(t)

	_, env := call(t, request{
		method: "GET", path: fmt.Sprintf("/api/v1/schedules/%d/queue", doc.ScheduleID),
		token: mintToken(t, 910001, "admin"),
	})
	assert.Equal(t, 0, env.Code, "管理员应能读取任意排班的队列: %s", env.Message)
}

// 患者完全不能访问该端点。
func TestQueue_PatientIsForbidden(t *testing.T) {
	doc := seedDoctor(t, 30)
	clearRateLimits(t)

	patientToken, _ := registerPatient(t)
	_, env := call(t, request{
		method: "GET", path: fmt.Sprintf("/api/v1/schedules/%d/queue", doc.ScheduleID),
		token: patientToken,
	})
	assert.Equal(t, 40301, env.Code)
}

// 未登录必须 401，而不是 403 —— 两者对前端的含义不同（该不该跳登录页）。
func TestQueue_RequiresAuth(t *testing.T) {
	doc := seedDoctor(t, 30)
	clearRateLimits(t)

	_, env := call(t, request{
		method: "GET", path: fmt.Sprintf("/api/v1/schedules/%d/queue", doc.ScheduleID),
	})
	assert.Equal(t, 40101, env.Code)
}

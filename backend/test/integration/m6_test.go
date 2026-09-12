//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/medicore/backend/internal/secret"
)

// ---------------------------------------------------------------------------
// 病历与处方
// ---------------------------------------------------------------------------

func writeRecordBody() map[string]any {
	return map[string]any{
		"chief_complaint":  "上腹隐痛 3 天",
		"present_illness":  "3 天前无明显诱因出现上腹隐痛，进食后加重。",
		"diagnosis":        "慢性胃炎急性发作",
		"treatment_advice": "清淡饮食，两周后复诊。",
		"prescriptions": []map[string]any{
			{"drug_name": "奥美拉唑肠溶胶囊", "spec": "20mg", "quantity": 1, "usage_desc": "每日一次", "days": 14},
			{"drug_name": "铝碳酸镁咀嚼片", "spec": "500mg", "quantity": 3, "usage_desc": "每日三次", "days": 7},
		},
	}
}

// grabFor 以指定患者身份抢号，返回预约 ID。
func grabFor(t *testing.T, patientToken string, scheduleID uint64, idemKey string) uint64 {
	t.Helper()

	_, env := call(t, request{
		method: "POST", path: "/api/v1/appointments", token: patientToken,
		body: map[string]any{"schedule_id": scheduleID, "idempotency_key": idemKey},
	})
	require.Equal(t, 0, env.Code, "抢号失败: %s", env.Message)

	got := decode[struct {
		ID uint64 `json:"id"`
	}](t, env)
	require.NotZero(t, got.ID)
	return got.ID
}

// recordView 是 MedicalRecordView 在测试侧的镜像，只声明断言用到的字段。
type recordView struct {
	ID            uint64 `json:"id"`
	AppointmentID uint64 `json:"appointment_id"`
	PatientName   string `json:"patient_name"`
	DoctorName    string `json:"doctor_name"`
	Department    string `json:"department_name"`
	Diagnosis     string `json:"diagnosis"`
	Prescriptions []struct {
		DrugName string `json:"drug_name"`
		Quantity int    `json:"quantity"`
		Days     *int   `json:"days"`
	} `json:"prescriptions"`
}

func TestRecord_WriteAndRead(t *testing.T) {
	doc := seedDoctor(t, 5)
	doctorToken := mintToken(t, doc.UserID, "doctor")
	patientToken, _ := registerPatient(t)
	appointmentID := grabFor(t, patientToken, doc.ScheduleID, "itest-record-0001")

	recordPath := fmt.Sprintf("/api/v1/appointments/%d/record", appointmentID)

	t.Run("医生书写病历与处方", func(t *testing.T) {
		_, env := call(t, request{
			method: "POST", path: recordPath, token: doctorToken, body: writeRecordBody(),
		})
		require.Equal(t, 0, env.Code, env.Message)

		record := decode[recordView](t, env)

		require.NotZero(t, record.ID)
		assert.Equal(t, appointmentID, record.AppointmentID)
		assert.Equal(t, "慢性胃炎急性发作", record.Diagnosis)
		assert.NotEmpty(t, record.PatientName, "应 JOIN 出患者姓名")
		assert.NotEmpty(t, record.DoctorName, "应 JOIN 出医生姓名")
		assert.NotEmpty(t, record.Department, "应 JOIN 出科室名称")

		require.Len(t, record.Prescriptions, 2, "处方明细必须随病历一并返回")
		assert.Equal(t, "奥美拉唑肠溶胶囊", record.Prescriptions[0].DrugName)
		assert.Equal(t, 1, record.Prescriptions[0].Quantity)
		require.NotNil(t, record.Prescriptions[0].Days)
		assert.Equal(t, 14, *record.Prescriptions[0].Days)
	})

	t.Run("同一预约不可重复书写", func(t *testing.T) {
		resp, env := call(t, request{
			method: "POST", path: recordPath, token: doctorToken, body: writeRecordBody(),
		})

		// uk_appointment 唯一索引把它转成业务冲突，而不是 500
		assert.Equal(t, 40004, env.Code)
		assert.Equal(t, 409, resp.StatusCode)
	})

	t.Run("患者本人可查看且看到处方", func(t *testing.T) {
		_, env := call(t, request{method: "GET", path: recordPath, token: patientToken})
		require.Equal(t, 0, env.Code, env.Message)

		record := decode[recordView](t, env)
		assert.Len(t, record.Prescriptions, 2, "Read 路径同样要带出处方明细")
	})

	t.Run("患者列表可见自己的病历", func(t *testing.T) {
		_, env := call(t, request{method: "GET", path: "/api/v1/records", token: patientToken})
		require.Equal(t, 0, env.Code)

		page := decode[struct {
			Items []struct {
				Diagnosis string `json:"diagnosis"`
			} `json:"items"`
		}](t, env)
		require.NotEmpty(t, page.Items, "刚写好的病历应出现在本人列表里")
		assert.Equal(t, "慢性胃炎急性发作", page.Items[0].Diagnosis)
	})

	t.Run("诊断为空被拒", func(t *testing.T) {
		// 换一个患者：uk_active 是 (user_id, schedule_id)，同一患者无法在同排班上开两个预约
		otherPatient, _ := registerPatient(t)
		otherAppt := grabFor(t, otherPatient, doc.ScheduleID, "itest-record-0002")

		body := writeRecordBody()
		delete(body, "diagnosis")

		_, env := call(t, request{
			method: "POST", path: fmt.Sprintf("/api/v1/appointments/%d/record", otherAppt),
			token: doctorToken, body: body,
		})
		assert.Equal(t, 40001, env.Code, "诊断是必填项")
	})
}

// 资源层归属校验——本文件里安全相关性最高的一组断言。
// 路由层只能判断"你是医生"，判断不了"这份病历是不是你的"。
func TestRecord_OwnershipIsEnforced(t *testing.T) {
	doc := seedDoctor(t, 5)
	doctorToken := mintToken(t, doc.UserID, "doctor")
	patientToken, _ := registerPatient(t)
	appointmentID := grabFor(t, patientToken, doc.ScheduleID, "itest-owner-0001")

	recordPath := fmt.Sprintf("/api/v1/appointments/%d/record", appointmentID)

	_, env := call(t, request{
		method: "POST", path: recordPath, token: doctorToken, body: writeRecordBody(),
	})
	require.Equal(t, 0, env.Code, env.Message)

	t.Run("其他患者不可查看", func(t *testing.T) {
		otherToken, _ := registerPatient(t)

		resp, env := call(t, request{method: "GET", path: recordPath, token: otherToken})

		assert.Equal(t, 40302, env.Code, "必须返回 NOT_OWNER")
		assert.Equal(t, 403, resp.StatusCode)
	})

	t.Run("非接诊医生不可查看", func(t *testing.T) {
		other := seedDoctor(t, 1)
		otherToken := mintToken(t, other.UserID, "doctor")

		_, env := call(t, request{method: "GET", path: recordPath, token: otherToken})

		assert.Equal(t, 40302, env.Code)
	})

	t.Run("非接诊医生不可书写", func(t *testing.T) {
		other := seedDoctor(t, 1)
		otherToken := mintToken(t, other.UserID, "doctor")

		_, env := call(t, request{
			method: "POST", path: recordPath, token: otherToken, body: writeRecordBody(),
		})

		assert.Equal(t, 40302, env.Code, "越权书写等于冒名下诊断")
	})

	t.Run("患者不可书写病历", func(t *testing.T) {
		resp, env := call(t, request{
			method: "POST", path: recordPath, token: patientToken, body: writeRecordBody(),
		})

		assert.Equal(t, 40301, env.Code, "路由层就应拦下患者")
		assert.Equal(t, 403, resp.StatusCode)
	})

	t.Run("管理员可查看任意病历", func(t *testing.T) {
		adminToken := mintToken(t, 999001, "admin")

		_, env := call(t, request{method: "GET", path: recordPath, token: adminToken})

		require.Equal(t, 0, env.Code, "spec §2.1 管理员拥有全部读权限: %s", env.Message)
	})

	t.Run("医生的待办列表包含自己的病历", func(t *testing.T) {
		_, env := call(t, request{method: "GET", path: "/api/v1/records", token: doctorToken})
		require.Equal(t, 0, env.Code)

		page := decode[struct {
			Items []struct {
				Diagnosis string `json:"diagnosis"`
			} `json:"items"`
		}](t, env)
		require.NotEmpty(t, page.Items)
		assert.Equal(t, "慢性胃炎急性发作", page.Items[0].Diagnosis)
	})
}

func TestRecord_UnknownAppointment(t *testing.T) {
	patientToken, _ := registerPatient(t)

	resp, env := call(t, request{
		method: "GET", path: "/api/v1/appointments/99999999/record", token: patientToken,
	})

	assert.Equal(t, 40003, env.Code)
	assert.Equal(t, 404, resp.StatusCode)
}

// ---------------------------------------------------------------------------
// HIS 同步
// ---------------------------------------------------------------------------

func countCatalog(t *testing.T) (depts, doctors, schedules int64) {
	t.Helper()

	db := mustDB(t)
	db.Raw("SELECT COUNT(*) FROM departments").Scan(&depts)
	db.Raw("SELECT COUNT(*) FROM doctors").Scan(&doctors)
	db.Raw("SELECT COUNT(*) FROM schedules").Scan(&schedules)
	return
}

func syncOnce(t *testing.T, adminToken string) (status string, total, succeeded, failed int) {
	t.Helper()

	resp, env := call(t, request{
		method: "POST", path: "/api/v1/admin/sync/mockhis", token: adminToken,
	})
	require.Equal(t, 0, env.Code, env.Message)
	require.Equal(t, 200, resp.StatusCode)

	got := decode[struct {
		Status    string `json:"status"`
		Total     int    `json:"total"`
		Succeeded int    `json:"succeeded"`
		Failed    int    `json:"failed"`
	}](t, env)

	return got.Status, got.Total, got.Succeeded, got.Failed
}

func TestSync_ManualTriggerIsIdempotent(t *testing.T) {
	adminToken := mintToken(t, 999002, "admin")

	beforeDepts, beforeDocs, beforeScheds := countCatalog(t)

	status, total, succeeded, failed := syncOnce(t, adminToken)
	assert.Equal(t, "success", status)
	assert.Equal(t, succeeded+failed, total, "总数应等于成功加失败")
	assert.Greater(t, total, 0)
	assert.Zero(t, failed)

	midDepts, midDocs, midScheds := countCatalog(t)
	assert.Greater(t, midDepts, beforeDepts, "应新增科室")
	assert.Greater(t, midDocs, beforeDocs, "应新增医生")
	assert.Greater(t, midScheds, beforeScheds, "应新增排班")

	// 第二次同步必须幂等：数据量不变
	syncOnce(t, adminToken)

	afterDepts, afterDocs, afterScheds := countCatalog(t)
	assert.Equal(t, midDepts, afterDepts, "重复同步不应重复建科室")
	assert.Equal(t, midDocs, afterDocs, "重复同步不应重复建医生")
	assert.Equal(t, midScheds, afterScheds, "重复同步不应重复建排班")
}

// 最关键的一条：重复同步绝不能重置 remaining。
//
// 若把 total 与 remaining 一起覆盖，HIS 每次同步都会把已预约的号源"还回去"，
// 直接造成超卖——而这恰恰是同步功能最容易写错的地方。
func TestSync_DoesNotResetRemaining(t *testing.T) {
	adminToken := mintToken(t, 999003, "admin")
	db := mustDB(t)

	syncOnce(t, adminToken)

	var scheduleID uint64
	require.NoError(t, db.Raw(
		`SELECT id FROM schedules
		 WHERE external_id LIKE 'HIS-SCH-%' AND remaining > 0
		 ORDER BY id LIMIT 1`).Scan(&scheduleID).Error)
	require.NotZero(t, scheduleID, "同步后应存在可用排班")

	// 抢一个号，制造 remaining < total 的状态
	patientToken, _ := registerPatient(t)
	grabFor(t, patientToken, scheduleID, "itest-sync-remain-1")

	var afterGrab, totalOf int
	require.NoError(t, db.Raw("SELECT remaining, total FROM schedules WHERE id = ?", scheduleID).
		Row().Scan(&afterGrab, &totalOf))
	require.Less(t, afterGrab, totalOf, "抢号后剩余应少于总量")

	syncOnce(t, adminToken)

	var afterSync int
	require.NoError(t, db.Raw("SELECT remaining FROM schedules WHERE id = ?", scheduleID).
		Scan(&afterSync).Error)

	assert.Equal(t, afterGrab, afterSync,
		"重复同步重置了号源，已预约的号会被重复放出（超卖）")
}

// 演示数据必须用相对日期，否则过几天全部排班都成了过去时，
// 整个演示环境会"莫名其妙地"抢不到号。
func TestSync_ScheduleDatesAreFresh(t *testing.T) {
	adminToken := mintToken(t, 999004, "admin")
	db := mustDB(t)

	syncOnce(t, adminToken)

	var upcoming, synced int64
	require.NoError(t, db.Raw(
		`SELECT COUNT(*) FROM schedules WHERE external_id LIKE 'HIS-SCH-%'`).
		Scan(&synced).Error)
	require.NoError(t, db.Raw(
		`SELECT COUNT(*) FROM schedules
		 WHERE external_id LIKE 'HIS-SCH-%' AND sched_date >= CURDATE()`).
		Scan(&upcoming).Error)

	assert.Equal(t, synced, upcoming, "同步来的排班应全部落在今天或之后")
}

func TestSync_PatientIsForbidden(t *testing.T) {
	patientToken, _ := registerPatient(t)

	resp, env := call(t, request{
		method: "POST", path: "/api/v1/admin/sync/mockhis", token: patientToken,
	})

	assert.Equal(t, 403, resp.StatusCode)
	assert.Equal(t, 40301, env.Code)
}

func TestSync_UnknownSourceIsRejected(t *testing.T) {
	adminToken := mintToken(t, 999005, "admin")

	resp, env := call(t, request{
		method: "POST", path: "/api/v1/admin/sync/nosuchsource", token: adminToken,
	})

	assert.Equal(t, 404, resp.StatusCode)
	assert.Equal(t, 40003, env.Code)
}

func TestSync_LogsAreQueryable(t *testing.T) {
	adminToken := mintToken(t, 999006, "admin")

	syncOnce(t, adminToken)

	_, env := call(t, request{
		method: "GET", path: "/api/v1/admin/sync-logs?size=5", token: adminToken,
	})
	require.Equal(t, 0, env.Code)

	page := decode[struct {
		Items []struct {
			Source      string `json:"source"`
			Status      string `json:"status"`
			TriggerType string `json:"trigger_type"`
			Total       int    `json:"total"`
		} `json:"items"`
	}](t, env)

	require.NotEmpty(t, page.Items)
	assert.Equal(t, "mockhis", page.Items[0].Source)
	assert.Equal(t, "manual", page.Items[0].TriggerType)
	assert.Equal(t, "success", page.Items[0].Status)
	assert.Greater(t, page.Items[0].Total, 0)
}

// 同步来的医生账号若共用一个可猜的口令，就是最典型的入侵入口。
// 这里不猜口令，而是直接验证散列本身：算法正确、逐账号独立。
func TestSync_DoctorPasswordsAreUniqueAndHashed(t *testing.T) {
	adminToken := mintToken(t, 999007, "admin")
	db := mustDB(t)

	syncOnce(t, adminToken)

	var hashes []string
	require.NoError(t, db.Raw(
		`SELECT u.password_hash FROM users u
		 JOIN doctors d ON d.user_id = u.id
		 WHERE d.external_id LIKE 'HIS-DOC-%'`).Scan(&hashes).Error)

	require.Greater(t, len(hashes), 1, "应同步到多名医生")

	seen := make(map[string]struct{}, len(hashes))
	for _, h := range hashes {
		require.True(t, strings.HasPrefix(h, "$2"),
			"口令必须用 bcrypt 散列，实际为 %q", h)

		cost, err := bcrypt.Cost([]byte(h))
		require.NoError(t, err, "口令散列格式不合法: %q", h)
		assert.GreaterOrEqual(t, cost, secret.MinBcryptCost,
			"散列强度低于项目下限，离线爆破成本过低")

		// 常见弱口令一个都不能通过
		for _, weak := range []string{"123456", "password", "doctor", "admin", "12345678"} {
			assert.Error(t, bcrypt.CompareHashAndPassword([]byte(h), []byte(weak)),
				"同步医生使用了可猜口令 %q", weak)
		}

		seen[h] = struct{}{}
	}
	assert.Len(t, seen, len(hashes), "同步医生共用了同一个口令散列（等同于默认口令）")
}

// 外部数据必须经过校验，不能整条写进库。
// 病历/科室名都是中文，落库后读出来不能是乱码或问号。
func TestSync_ChineseRoundTrips(t *testing.T) {
	adminToken := mintToken(t, 999008, "admin")
	db := mustDB(t)

	syncOnce(t, adminToken)

	var name string
	require.NoError(t, db.Raw(
		`SELECT name FROM departments WHERE external_id = 'HIS-DEPT-01'`).Scan(&name).Error)

	assert.Equal(t, "心血管内科", name, "字符集应为 utf8mb4 且往返无损")
}

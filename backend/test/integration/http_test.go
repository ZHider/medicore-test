//go:build integration

package integration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestOps_HealthAndReadiness(t *testing.T) {
	resp, env := call(t, request{method: "GET", path: "/healthz"})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 0, env.Code)
	assert.NotEmpty(t, env.TraceID, "运维端点也应带 trace_id")

	resp, env = call(t, request{method: "GET", path: "/readyz"})
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 0, env.Code)

	// 就绪探针必须真的逐个探测依赖，而不是笼统返回 ok
	var data map[string]struct {
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(env.Data, &data))
	assert.Equal(t, "ok", data["mysql"].Status)
	assert.Equal(t, "ok", data["redis"].Status)
}

func TestOps_MetricsExposed(t *testing.T) {
	resp, err := testServer.Client().Get(testServer.URL + "/metrics")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// 未匹配路由必须返回统一信封，而不是 gin 默认的纯文本 404，
// 否则客户端无法按 code 字段分支处理（spec §3.3）。
func TestUnifiedEnvelope_UnknownRouteAndMethod(t *testing.T) {
	t.Run("未知路由返回 404 信封", func(t *testing.T) {
		resp, env := call(t, request{method: "GET", path: "/api/v1/definitely-not-here"})
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		assert.Equal(t, 40003, env.Code)
		assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")
	})

	t.Run("方法不允许返回 405 信封", func(t *testing.T) {
		resp, env := call(t, request{method: "DELETE", path: "/healthz"})
		assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
		assert.Equal(t, 40006, env.Code)
	})

	t.Run("参数非法返回 400 信封", func(t *testing.T) {
		token, _ := registerPatient(t)
		resp, env := call(t, request{
			method: "GET", path: "/api/v1/departments/abc/doctors", token: token,
		})
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		assert.Equal(t, 40001, env.Code)
	})
}

func TestAuth_FullFlow(t *testing.T) {
	// 本用例会多次调用登录接口，先清掉限流键，避免受其它用例影响
	clearRateLimits(t)

	phone := fmt.Sprintf("138%08d", time.Now().UnixNano()%100000000)

	_, env := call(t, request{
		method: "POST", path: "/api/v1/auth/register",
		body: map[string]any{"phone": phone, "name": "张三", "password": "Passw0rd!"},
	})
	require.Equal(t, 0, env.Code, env.Message)

	// 重复注册应被唯一索引拦下并转成业务冲突，而不是 500
	_, env = call(t, request{
		method: "POST", path: "/api/v1/auth/register",
		body: map[string]any{"phone": phone, "name": "张三", "password": "Passw0rd!"},
	})
	assert.Equal(t, 40004, env.Code)

	_, env = call(t, request{
		method: "POST", path: "/api/v1/auth/login",
		body: map[string]any{"phone": phone, "password": "Passw0rd!"},
	})
	require.Equal(t, 0, env.Code)
	tokens := decode[struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}](t, env)
	assert.NotEmpty(t, tokens.AccessToken)
	assert.Equal(t, int64(7200), tokens.ExpiresIn)

	t.Run("me 返回脱敏资料", func(t *testing.T) {
		_, env := call(t, request{method: "GET", path: "/api/v1/auth/me", token: tokens.AccessToken})
		require.Equal(t, 0, env.Code)
		profile := decode[struct {
			Phone string `json:"phone"`
		}](t, env)
		assert.Contains(t, profile.Phone, "****", "手机号必须脱敏后返回")
		assert.NotContains(t, profile.Phone, phone)
	})

	t.Run("无令牌被拒", func(t *testing.T) {
		resp, env := call(t, request{method: "GET", path: "/api/v1/auth/me"})
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		assert.Equal(t, 40101, env.Code)
	})

	t.Run("刷新轮换且旧令牌失效", func(t *testing.T) {
		_, env := call(t, request{
			method: "POST", path: "/api/v1/auth/refresh",
			body: map[string]any{"refresh_token": tokens.RefreshToken},
		})
		require.Equal(t, 0, env.Code)
		next := decode[struct {
			RefreshToken string `json:"refresh_token"`
		}](t, env)
		assert.NotEqual(t, tokens.RefreshToken, next.RefreshToken)

		// 旧令牌重放必须失败
		_, env = call(t, request{
			method: "POST", path: "/api/v1/auth/refresh",
			body: map[string]any{"refresh_token": tokens.RefreshToken},
		})
		assert.Equal(t, 40103, env.Code)
	})

	t.Run("错误密码与未知账号返回相同错误", func(t *testing.T) {
		_, wrongPw := call(t, request{
			method: "POST", path: "/api/v1/auth/login",
			body: map[string]any{"phone": phone, "password": "wrong-password"},
		})
		_, unknown := call(t, request{
			method: "POST", path: "/api/v1/auth/login",
			body: map[string]any{"phone": "13000000000", "password": "wrong-password"},
		})
		assert.Equal(t, wrongPw.Code, unknown.Code)
		assert.Equal(t, wrongPw.Message, unknown.Message, "不得泄露账号是否存在")
	})
}

func TestRBAC_PatientCannotReachAdmin(t *testing.T) {
	token, _ := registerPatient(t)

	resp, env := call(t, request{method: "GET", path: "/api/v1/admin/audit-logs", token: token})

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Equal(t, 40301, env.Code)
}

// 被拒绝的请求同样要进审计——"谁试图做什么但被拒绝"正是审计的价值所在。
func TestAudit_DeniedRequestIsRecorded(t *testing.T) {
	token, _ := registerPatient(t)
	call(t, request{method: "GET", path: "/api/v1/admin/audit-logs", token: token})

	// 资源名必须是 audit-logs 而不是 admin。
	// 曾经的 bug：Describe 取版本段之后的第 1 段，/api/v1/admin/* 全被记成
	// resource=admin，三个管理端点按资源聚合后混成一条。
	// 这个用例原先断言的正是那个错误值，属"测试把 bug 固化下来"。
	row := waitForAudit(t, "resource = ? AND status_code = ?", "audit-logs", http.StatusForbidden)
	assert.Equal(t, "view", row.Action)
}

// 三个管理端点必须能被审计按资源区分开。
//
// 仅断言"有一条记录"是不够的：resource 全塌成 admin 时也满足。
func TestAudit_AdminRoutesAreDistinguishableByResource(t *testing.T) {
	clearRateLimits(t)
	adminToken := mintToken(t, 910002, "admin")

	// 触发三条路径不同的管理请求。
	//
	// 这里刻意用一个**不存在的**数据源：本用例只关心审计怎么推导资源名，
	// 真跑一次同步会有副作用——它会创建排班，让后面 TestSync_ManualTriggerIsIdempotent
	// 的"应新增排班"断言失效（踩过一次）。
	call(t, request{method: "GET", path: "/api/v1/admin/audit-logs", token: adminToken})
	call(t, request{method: "GET", path: "/api/v1/admin/sync-logs", token: adminToken})
	call(t, request{method: "POST", path: "/api/v1/admin/sync/unknown-source", token: adminToken})

	for _, want := range []string{"audit-logs", "sync-logs", "sync"} {
		row := waitForAudit(t, "resource = ?", want)
		assert.Equal(t, want, row.Resource)
	}

	// 不应存在 resource=admin 的记录：它是权限域，不是资源
	var n int64
	require.NoError(t, mustDB(t).Raw(
		"SELECT COUNT(*) FROM audit_logs WHERE resource = 'admin'").Scan(&n).Error)
	assert.Zero(t, n, "权限域不应被当成资源名")
}

func TestCatalog_DepartmentsDoctorsSchedules(t *testing.T) {
	token, _ := registerPatient(t)
	seedDoctorSchedule(t, 8)

	t.Run("科室列表", func(t *testing.T) {
		_, env := call(t, request{method: "GET", path: "/api/v1/departments", token: token})
		require.Equal(t, 0, env.Code)
	})

	t.Run("排班可查询且能取到刚插入的那条", func(t *testing.T) {
		_, env := call(t, request{
			method: "GET", path: "/api/v1/departments", token: token,
		})
		depts := decode[struct {
			Items []struct {
				ID uint64 `json:"id"`
			} `json:"items"`
		}](t, env)
		require.NotEmpty(t, depts.Items)

		_, env = call(t, request{
			method: "GET",
			path:   fmt.Sprintf("/api/v1/departments/%d/schedules?size=50", depts.Items[0].ID),
			token:  token,
		})
		require.Equal(t, 0, env.Code, env.Message)
	})

	t.Run("不存在的科室返回 404 而不是空列表", func(t *testing.T) {
		resp, env := call(t, request{
			method: "GET", path: "/api/v1/departments/999999/doctors", token: token,
		})
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		assert.Equal(t, 40003, env.Code)
	})

	t.Run("游标非法返回 400", func(t *testing.T) {
		_, env := call(t, request{
			method: "GET", path: "/api/v1/departments/1/schedules?cursor=abc", token: token,
		})
		assert.Equal(t, 40001, env.Code)
	})
}

func TestAppointment_GrabIdempotencyAndCancel(t *testing.T) {
	token, _ := registerPatient(t)
	scheduleID := seedDoctorSchedule(t, 3)

	grab := func(key string) response {
		_, env := call(t, request{
			method: "POST", path: "/api/v1/appointments", token: token,
			body: map[string]any{"schedule_id": scheduleID, "idempotency_key": key},
		})
		return env
	}

	env := grab("itest-idem-0001")
	require.Equal(t, 0, env.Code, env.Message)
	first := decode[struct {
		ID     uint64 `json:"id"`
		Status string `json:"status"`
	}](t, env)
	assert.Equal(t, "pending", first.Status)

	t.Run("同一幂等键重放返回同一预约", func(t *testing.T) {
		env := grab("itest-idem-0001")
		require.Equal(t, 0, env.Code)
		again := decode[struct {
			ID uint64 `json:"id"`
		}](t, env)
		assert.Equal(t, first.ID, again.ID, "幂等重放不应创建新预约")
	})

	t.Run("不同幂等键抢同一号源被 uk_active 拒绝", func(t *testing.T) {
		env := grab("itest-idem-0002")
		assert.Equal(t, 40004, env.Code)
	})

	t.Run("号源只被扣了一次", func(t *testing.T) {
		var remaining int
		db := mustDB(t)
		require.NoError(t, db.Raw("SELECT remaining FROM schedules WHERE id = ?", scheduleID).
			Scan(&remaining).Error)
		assert.Equal(t, 2, remaining, "3 个号源抢 1 次后应剩 2")
	})

	t.Run("签到分配序号", func(t *testing.T) {
		_, env := call(t, request{
			method: "POST", path: fmt.Sprintf("/api/v1/appointments/%d/checkin", first.ID), token: token,
		})
		require.Equal(t, 0, env.Code, env.Message)
		got := decode[struct {
			QueueNo int `json:"queue_no"`
		}](t, env)
		assert.Equal(t, 1, got.QueueNo)
	})

	t.Run("取消回补号源", func(t *testing.T) {
		_, env := call(t, request{
			method: "POST", path: fmt.Sprintf("/api/v1/appointments/%d/cancel", first.ID), token: token,
			body: map[string]any{"reason": "集成测试取消"},
		})
		require.Equal(t, 0, env.Code, env.Message)

		var remaining int
		db := mustDB(t)
		require.NoError(t, db.Raw("SELECT remaining FROM schedules WHERE id = ?", scheduleID).
			Scan(&remaining).Error)
		assert.Equal(t, 3, remaining, "取消后号源应回到 3")
	})

	t.Run("分页返回本人预约", func(t *testing.T) {
		_, env := call(t, request{method: "GET", path: "/api/v1/appointments?size=10", token: token})
		require.Equal(t, 0, env.Code)
	})
}

// SSE 是 HTTP 层最容易出错的地方：响应头时机、分块传输、错误传递方式。
func TestPreconsult_SSEChatAndReport(t *testing.T) {
	token, _ := registerPatient(t)
	scheduleID := seedDoctorSchedule(t, 2)

	_, env := call(t, request{
		method: "POST", path: "/api/v1/appointments", token: token,
		body: map[string]any{"schedule_id": scheduleID, "idempotency_key": "itest-sse-0001"},
	})
	require.Equal(t, 0, env.Code)
	appt := decode[struct {
		ID uint64 `json:"id"`
	}](t, env)

	t.Run("SSE 流式对话", func(t *testing.T) {
		body := strings.NewReader(`{"message":"我上腹隐痛三天了"}`)
		req, err := http.NewRequest("POST",
			testServer.URL+fmt.Sprintf("/api/v1/appointments/%d/preconsult/chat", appt.ID), body)
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := testServer.Client().Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
		assert.Equal(t, "no", resp.Header.Get("X-Accel-Buffering"),
			"必须告诉反向代理不要缓冲，否则流式会被攒成一整块")

		// 逐行读事件，确认收到 delta 与 done
		var (
			deltas  int
			sawDone bool
			scanner = bufio.NewScanner(resp.Body)
		)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "event: delta" {
				deltas++
			}
			if line == "event: done" {
				sawDone = true
			}
		}
		require.NoError(t, scanner.Err())
		assert.Greater(t, deltas, 1, "应分多段推送，而不是一次性返回")
		assert.True(t, sawDone, "流结束应有 done 事件")
	})

	t.Run("history 可恢复上下文", func(t *testing.T) {
		_, env := call(t, request{
			method: "GET",
			path:   fmt.Sprintf("/api/v1/appointments/%d/preconsult/history", appt.ID),
			token:  token,
		})
		require.Equal(t, 0, env.Code)
		history := decode[struct {
			Items []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"items"`
		}](t, env)
		require.NotEmpty(t, history.Items)
		assert.Equal(t, "user", history.Items[0].Role)
	})

	t.Run("提交返回 202 且带 trace_id", func(t *testing.T) {
		resp, env := call(t, request{
			method: "POST",
			path:   fmt.Sprintf("/api/v1/appointments/%d/preconsult/submit", appt.ID),
			token:  token,
		})
		assert.Equal(t, http.StatusAccepted, resp.StatusCode, "异步任务应返回 202 而不是 200")
		require.Equal(t, 0, env.Code)
		assert.NotEmpty(t, env.TraceID, "202 响应同样必须带 trace_id")

		submitted := decode[struct {
			ReportID uint64 `json:"report_id"`
			Status   string `json:"status"`
		}](t, env)
		assert.NotZero(t, submitted.ReportID)
		assert.Equal(t, "pending", submitted.Status, "此刻尚未处理完")
	})

	t.Run("worker 异步生成报告", func(t *testing.T) {
		require.Eventually(t, func() bool {
			_, env := call(t, request{
				method: "GET",
				path:   fmt.Sprintf("/api/v1/appointments/%d/preconsult/report", appt.ID),
				token:  token,
			})
			if env.Code != 0 {
				return false
			}
			report := decode[struct {
				Status string `json:"status"`
			}](t, env)
			return report.Status == "done" || report.Status == "failed"
		}, 15*time.Second, 300*time.Millisecond, "worker 未在预期时间内处理完总结任务")

		_, env := call(t, request{
			method: "GET",
			path:   fmt.Sprintf("/api/v1/appointments/%d/preconsult/report", appt.ID),
			token:  token,
		})
		report := decode[struct {
			Status     string          `json:"status"`
			Fallback   bool            `json:"fallback"`
			Structured json.RawMessage `json:"structured"`
			PromptVer  string          `json:"prompt_version"`
		}](t, env)

		assert.Equal(t, "done", report.Status)
		assert.False(t, report.Fallback, "mock 的 valid 模式不应降级")
		assert.NotEmpty(t, report.Structured)
		assert.Equal(t, "v1", report.PromptVer, "提示词版本必须入库以便追溯")
	})

	t.Run("空消息被拒", func(t *testing.T) {
		_, env := call(t, request{
			method: "POST",
			path:   fmt.Sprintf("/api/v1/appointments/%d/preconsult/chat", appt.ID),
			token:  token,
			body:   map[string]any{"message": "   "},
		})
		assert.Equal(t, 40001, env.Code)
	})
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func mustDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(mysql.Open(testDSN()), &gorm.Config{})
	require.NoError(t, err)
	return db
}

// waitForAudit 轮询等待一条符合条件的审计记录落库。
// 审计是异步写入的，测试必须容忍这个延迟。
func waitForAudit(t *testing.T, where string, args ...any) auditRow {
	t.Helper()

	db := mustDB(t)
	var row auditRow

	require.Eventually(t, func() bool {
		err := db.Table("audit_logs").Where(where, args...).
			Select("method, path, COALESCE(resource,'') AS resource, COALESCE(action,'') AS action, status_code").
			Order("id DESC").Limit(1).Scan(&row).Error
		if err != nil || row.Path == "" {
			return false
		}
		return true
	}, 5*time.Second, 100*time.Millisecond, "未等到符合条件的审计记录: %s", where)

	return row
}

type auditRow struct {
	Method     string
	Path       string
	Resource   string
	Action     string
	StatusCode int
}

//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 限流是 spec §9.3 要求的安全控制，也是**登录爆破防护的唯一手段**。
//
// 此前它从未被任何测试执行过：所有用例都在开头 clearRateLimits 把桶清空，
// 好让自己能反复登录。于是"限流到底有没有生效"这件事一直没人验证 ——
// 一个静默失效的限流器不会有任何症状，直到有人把口令字典跑完。
//
// 代价是不对称的：漏测限流的后果远大于测它多花的十几毫秒。

// 登录连续尝试超过桶容量后必须被拒，且带 Retry-After 供客户端退避。
func TestRateLimit_LoginIsThrottledAfterCapacity(t *testing.T) {
	// 桶容量 5（router 里的 Capacity），先把历史计数清掉，
	// 否则本用例的结果取决于此前跑过多少用例。
	clearRateLimits(t)
	t.Cleanup(func() { clearRateLimits(t) })

	// 用不存在的账号：限流器在 handler 之前执行，
	// 所以无论登录成功与否都会消耗令牌。
	body := map[string]any{"phone": "13000000000", "password": "WrongPassw0rd!"}

	for i := 1; i <= 5; i++ {
		resp, env := call(t, request{method: "POST", path: "/api/v1/auth/login", body: body})
		assert.NotEqual(t, http.StatusTooManyRequests, resp.StatusCode,
			"第 %d 次尝试在桶容量（5）之内，不应被限流", i)
		assert.NotEqual(t, 42901, env.Code)
	}

	// 第 6 次必须被拒
	resp, env := call(t, request{method: "POST", path: "/api/v1/auth/login", body: body})

	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Equal(t, 42901, env.Code, "应返回统一的限流错误码")
	assert.NotEmpty(t, resp.Header.Get("Retry-After"),
		"必须告知客户端何时可以重试，否则客户端只能盲目重试")
}

// 被限流的响应也必须是统一信封，而不是 gin 的裸文本。
//
// 前端按 {code,message} 判断错误类型；若这里退化成纯文本，
// 前端会把它当成"解析失败"而不是"请求过于频繁"。
func TestRateLimit_ThrottledResponseUsesUnifiedEnvelope(t *testing.T) {
	clearRateLimits(t)
	t.Cleanup(func() { clearRateLimits(t) })

	body := map[string]any{"phone": "13000000001", "password": "WrongPassw0rd!"}
	for i := 0; i < 5; i++ {
		call(t, request{method: "POST", path: "/api/v1/auth/login", body: body})
	}

	_, env := call(t, request{method: "POST", path: "/api/v1/auth/login", body: body})

	require.Equal(t, 42901, env.Code)
	assert.NotEmpty(t, env.Message, "限流响应必须带可读文案")
	assert.NotEmpty(t, env.TraceID, "限流响应同样要带 trace_id，否则排查时链路是断的")
}

// 限流按**主体**隔离：清掉键之后应立刻恢复可用。
//
// 这条同时验证了"桶确实记在 Redis 里且键名正确"——
// 若键名算错（例如漏了前缀），限流要么永不生效，要么永远清不掉。
func TestRateLimit_ClearingBucketRestoresAccess(t *testing.T) {
	clearRateLimits(t)
	t.Cleanup(func() { clearRateLimits(t) })

	body := map[string]any{"phone": "13000000002", "password": "WrongPassw0rd!"}

	// 先打满
	for i := 0; i < 6; i++ {
		call(t, request{method: "POST", path: "/api/v1/auth/login", body: body})
	}
	_, throttled := call(t, request{method: "POST", path: "/api/v1/auth/login", body: body})
	require.Equal(t, 42901, throttled.Code, "打满后应处于限流状态")

	// 清空后应恢复
	clearRateLimits(t)
	_, env := call(t, request{method: "POST", path: "/api/v1/auth/login", body: body})
	assert.NotEqual(t, 42901, env.Code,
		"清空限流键后应恢复可用，否则说明桶的键名与清理逻辑不一致")
}

// 抢号限流是按**用户**隔离的，不是按 IP。
//
// 按 IP 限流会让同一个 NAT 后面的多个患者互相影响；
// 这条用例用两个不同患者验证"一个被限流不影响另一个"。
func TestRateLimit_GrabIsPerUserNotPerIP(t *testing.T) {
	clearRateLimits(t)
	t.Cleanup(func() { clearRateLimits(t) })

	doc := seedDoctor(t, 30)

	// 患者 A 打满自己的桶（容量 10）
	firstToken, _ := registerPatient(t)
	for i := 0; i < 10; i++ {
		call(t, request{
			method: "POST", path: "/api/v1/appointments", token: firstToken,
			body: map[string]any{
				"schedule_id":     doc.ScheduleID,
				"idempotency_key": fmt.Sprintf("throttle-a-%d-%d", doc.ScheduleID, i),
			},
		})
	}

	// 患者 B 是独立主体，不应受影响
	secondToken, _ := registerPatient(t)
	_, env := call(t, request{
		method: "POST", path: "/api/v1/appointments", token: secondToken,
		body: map[string]any{
			"schedule_id":     doc.ScheduleID,
			"idempotency_key": fmt.Sprintf("throttle-b-%d", doc.ScheduleID),
		},
	})

	assert.NotEqual(t, 42901, env.Code,
		"限流必须按用户隔离，否则同网段的其他患者会被无辜牵连")
}

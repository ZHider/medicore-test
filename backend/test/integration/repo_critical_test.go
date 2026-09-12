//go:build integration

package integration

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/repository"
)

// 本文件直接测 repository 层的关键不变量。
//
// 为什么需要这一层，而不是继续加 HTTP 集成测试：
//
//	service 单测全部用内存 fake，验证的是"编排逻辑"；
//	HTTP 集成测试验证的是"端到端行为"。
//	两者之间有一块空白 —— **真正执行 SQL 与 Lua 的那段代码**。
//
// 而超卖、重复回补、幂等失效这三类问题**恰恰只存在于那一层**：
// fake 里一行 Go 代码就实现的行为，在 Lua 脚本和带条件的 UPDATE 里
// 是完全不同的语义。端到端测试能发现"表现不对"，但很难指出是哪条语句错了。
//
// 这里只挑**后果最严重且最难排查**的不变量，不做覆盖率罗列。

// --- 号源（Redis Lua）：整个项目最贵的一条防线 ---

// stockSchedule 造一个只用于 Redis 键的排班。
// 号源键只依赖 ID，其余字段仅预热时读 Remaining 与 SchedDate。
func stockSchedule(id uint64, remaining int) *model.Schedule {
	return &model.Schedule{
		ID:        id,
		Total:     remaining,
		Remaining: remaining,
		SchedDate: time.Now().AddDate(0, 0, 1),
	}
}

// 用例之间用互不相同的排班 ID，避免通过 Redis 键互相干扰。
var stockIDSeq atomic.Uint64

func nextStockID() uint64 { return 900_000_000 + stockIDSeq.Add(1) }

// 键不存在必须报"未预热"，不能报"号源不足"。
//
// spec 的原始脚本在键不存在时返回 0（=号源不足）。后果是 Redis 冷启动或
// 键过期之后**所有抢号请求全部失败**，而错误信息还写着"号源不足"，
// 排查时会一路去查排班余量，方向完全错。
func TestStock_DecrementOnMissingKeyReportsNotLoaded(t *testing.T) {
	ctx := context.Background()
	stock := repository.NewStockRepo(mustRedis(t))

	got, err := stock.Decrement(ctx, nextStockID(), 1)
	require.NoError(t, err)

	assert.Equal(t, repository.StockNotLoaded, got,
		"键不存在必须与号源不足区分开")
	assert.NotEqual(t, repository.StockInsufficient, got,
		"把'未预热'报成'号源不足'会让冷启动伪装成余量耗尽")
}

// 预热绝不能覆盖已扣减的号源 —— 这是整个抢号模块最重要的一条不变量。
//
// 用 SET 覆盖已存在的键，会把并发请求刚扣掉的号源"还回去"，直接超卖。
// 这条只有打真实 Redis 才测得出来：fake 里"SetNX"就是一行 map 赋值，
// 写错成 Set 也不会有人发现。
func TestStock_EnsureLoadedNeverClobbersDecrementedValue(t *testing.T) {
	ctx := context.Background()
	stock := repository.NewStockRepo(mustRedis(t))
	id := nextStockID()

	require.NoError(t, stock.EnsureLoaded(ctx, stockSchedule(id, 10)))

	got, err := stock.Decrement(ctx, id, 1)
	require.NoError(t, err)
	require.Equal(t, repository.StockOK, got)

	// 模拟另一个请求带着"数据库里的旧余量 10"来做预热
	require.NoError(t, stock.EnsureLoaded(ctx, stockSchedule(id, 10)))

	current, exists, err := stock.Current(ctx, id)
	require.NoError(t, err)
	require.True(t, exists)
	assert.Equal(t, 9, current, "预热把已扣减的号源覆盖回去 = 直接超卖")
}

// 余量不足时必须拒绝，且不得把余量扣成负数。
func TestStock_DecrementRefusesToGoBelowZero(t *testing.T) {
	ctx := context.Background()
	stock := repository.NewStockRepo(mustRedis(t))
	id := nextStockID()

	require.NoError(t, stock.EnsureLoaded(ctx, stockSchedule(id, 1)))

	got, err := stock.Decrement(ctx, id, 1)
	require.NoError(t, err)
	require.Equal(t, repository.StockOK, got)

	got, err = stock.Decrement(ctx, id, 1)
	require.NoError(t, err)
	assert.Equal(t, repository.StockInsufficient, got)

	current, _, err := stock.Current(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, 0, current, "余量不得变成负数")
}

// 回补必须以容量为上限。
//
// 重复回补（补偿逻辑与 cron 释放同时命中）若不做上限，
// 会把余量推高到超过总号源 —— 同样是超卖。
func TestStock_ReleaseIsCappedAtCapacity(t *testing.T) {
	ctx := context.Background()
	stock := repository.NewStockRepo(mustRedis(t))
	id := nextStockID()

	require.NoError(t, stock.EnsureLoaded(ctx, stockSchedule(id, 5)))
	_, err := stock.Decrement(ctx, id, 5)
	require.NoError(t, err)

	// 一次回补 100 个，远超容量 5
	got, err := stock.Release(ctx, id, 100, 5)
	require.NoError(t, err)

	assert.Equal(t, 5, got, "回补必须以容量为上限")
	current, _, err := stock.Current(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, 5, current)
}

// 键不存在时回补不得创建键。
//
// "键不存在"意味着未预热或已过期，而不是"余量为 0"。
// 此处若 INCRBY 出一个新键，就等于凭空造出号源。
func TestStock_ReleaseNeverCreatesMissingKey(t *testing.T) {
	ctx := context.Background()
	stock := repository.NewStockRepo(mustRedis(t))
	id := nextStockID()

	got, err := stock.Release(ctx, id, 1, 10)
	require.NoError(t, err)
	assert.Equal(t, -1, got, "键不存在应返回 -1 供调用方跳过")

	_, exists, err := stock.Current(ctx, id)
	require.NoError(t, err)
	assert.False(t, exists, "键不存在时回补不得凭空创建键")
}

// 钳位不得清掉 TTL。
//
// 裸 SET 会抹掉 EnsureLoaded 用 SETNX 设的过期时间，号源键就此变成永久键；
// SETNX 也不会再刷新它。对历史排班更糟：ListReconcilable 只扫
// sched_date >= today，这个被永久钉住的错值再也不会被对账修正。
func TestStock_ReleaseClampPreservesTTL(t *testing.T) {
	ctx := context.Background()
	rdb := mustRedis(t)
	stock := repository.NewStockRepo(rdb)
	id := nextStockID()

	require.NoError(t, stock.EnsureLoaded(ctx, stockSchedule(id, 5)))

	before, err := rdb.TTL(ctx, repository.StockKey(id)).Result()
	require.NoError(t, err)
	require.Greater(t, before, time.Duration(0), "预热应设置 TTL")

	_, err = stock.Decrement(ctx, id, 5)
	require.NoError(t, err)

	// 触发钳位分支
	got, err := stock.Release(ctx, id, 100, 5)
	require.NoError(t, err)
	require.Equal(t, 5, got, "应走到钳位分支")

	after, err := rdb.TTL(ctx, repository.StockKey(id)).Result()
	require.NoError(t, err)
	assert.Greater(t, after, time.Duration(0),
		"钳位后 TTL 不应被清掉（-1 表示永不过期），否则号源键再也不会被对账修正")
}

// 对账必须"值没变才写"。
//
// 对账读 MySQL 与写 Redis 之间存在时间窗，期间若有并发扣减，
// 无条件 SET 会把刚扣掉的号源还回去 —— 对账本身制造超卖。
func TestStock_CompareAndSetOnlyWritesWhenValueMatches(t *testing.T) {
	ctx := context.Background()
	stock := repository.NewStockRepo(mustRedis(t))
	id := nextStockID()

	require.NoError(t, stock.EnsureLoaded(ctx, stockSchedule(id, 10)))

	ok, err := stock.CompareAndSet(ctx, id, 10, 8)
	require.NoError(t, err)
	require.True(t, ok, "值一致时应完成修正")

	current, _, err := stock.Current(ctx, id)
	require.NoError(t, err)
	require.Equal(t, 8, current)

	// 值已被并发改动（不再是 10），本次修正必须放弃
	ok, err = stock.CompareAndSet(ctx, id, 10, 3)
	require.NoError(t, err)
	assert.False(t, ok, "值已变化时必须放弃，绝不能强行覆盖")

	current, _, err = stock.Current(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, 8, current, "放弃的修正不得改动余量")
}

// --- 幂等键（Redis Lua）：抢号幂等的第一道防线 ---

// 幂等键的前缀是 idem:grab:{uid}:，用例之间用独立的 key 名避免互相干扰。
func idemKey(t *testing.T) string { return "itest-" + t.Name() }

func TestIdempotency_AcquireCompleteThenReplayReturnsSameAppointment(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewIdempotencyRepo(mustRedis(t))
	userID, key := uint64(880001), idemKey(t)

	acquired, id, err := repo.Acquire(ctx, userID, key)
	require.NoError(t, err)
	assert.True(t, acquired, "首次请求应占用成功")
	assert.Zero(t, id)

	// 处理期间重复提交：必须告知"在途"，而不是当成一次新请求继续走
	acquired, _, err = repo.Acquire(ctx, userID, key)
	assert.False(t, acquired)
	assert.ErrorIs(t, err, repository.ErrInFlight)

	require.NoError(t, repo.Complete(ctx, userID, key, 4242))

	acquired, id, err = repo.Acquire(ctx, userID, key)
	require.NoError(t, err)
	assert.False(t, acquired)
	assert.Equal(t, uint64(4242), id, "重放必须返回上次创建的预约")
}

// 释放只应清掉"处理中"的键，绝不能删掉"已完成"的记录。
//
// 这是本项目修过的一个 spec 缺陷：原设计用无条件 DEL 做失败清理。
// 设想请求 A 成功写入预约 ID 后，请求 B 因超时执行清理 ——
// 一次无条件 DEL 就让后续重放再也查不到结果，幂等保护形同虚设。
func TestIdempotency_ReleaseMustNotDeleteCompletedRecord(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewIdempotencyRepo(mustRedis(t))
	userID, key := uint64(880002), idemKey(t)

	acquired, _, err := repo.Acquire(ctx, userID, key)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NoError(t, repo.Complete(ctx, userID, key, 7777))

	// 模拟一个超时请求执行清理
	require.NoError(t, repo.Release(ctx, userID, key))

	_, id, err := repo.Acquire(ctx, userID, key)
	require.NoError(t, err)
	assert.Equal(t, uint64(7777), id,
		"无条件 DEL 会删掉已完成记录，让幂等保护形同虚设")
}

// 处理中的键被释放后，客户端应能重试。
func TestIdempotency_ReleaseAllowsRetryWhileInFlight(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewIdempotencyRepo(mustRedis(t))
	userID, key := uint64(880003), idemKey(t)

	acquired, _, err := repo.Acquire(ctx, userID, key)
	require.NoError(t, err)
	require.True(t, acquired)

	require.NoError(t, repo.Release(ctx, userID, key))

	acquired, _, err = repo.Acquire(ctx, userID, key)
	require.NoError(t, err)
	assert.True(t, acquired, "释放后应能重新占用，否则用户永远重试不了")
}

// Complete 用 SET ... XX：键已消失时不得凭空创建。
//
// 否则一个已经关闭的幂等窗口会重新生效，且里面存着一条"上次的预约"。
func TestIdempotency_CompleteOnVanishedKeyDoesNotRecreateIt(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewIdempotencyRepo(mustRedis(t))
	userID, key := uint64(880004), idemKey(t)

	err := repo.Complete(ctx, userID, key, 1)
	assert.Error(t, err, "键不存在时 Complete 应报错而不是创建键")

	acquired, _, err := repo.Acquire(ctx, userID, key)
	require.NoError(t, err)
	assert.True(t, acquired, "失败后应仍是首次请求，说明键没被凭空创建")
}

// --- 预约：超时释放的幂等闸门 ---

// 同一批超时订单被重复处理时，只有第一次能真正回补号源。
//
// 闸门是"带状态条件的 UPDATE"返回的 RowsAffected。多实例部署时
// 两个实例会扫到同一批订单，若这里写成无条件 UPDATE，号源会被重复加回 → 超卖。
func TestAppointmentExpire_IsIdempotentAndRefundsOnlyOnce(t *testing.T) {
	ctx := context.Background()
	db := mustDB(t)
	repo := repository.NewAppointmentRepo(db)
	doc := seedDoctor(t, 5)

	apptID := grabAndExpire(t, doc.ScheduleID, fmt.Sprintf("expire-%d", doc.ScheduleID))

	before := scheduleRemaining(t, doc.ScheduleID)

	first, err := repo.Expire(ctx, apptID)
	require.NoError(t, err)
	assert.True(t, first.Changed, "首次处理应真正发生状态跃迁")
	assert.Equal(t, before+1, scheduleRemaining(t, doc.ScheduleID), "应回补 1 个号源")

	// 第二个实例扫到同一条订单
	second, err := repo.Expire(ctx, apptID)
	require.NoError(t, err)
	assert.False(t, second.Changed, "已被处理过的订单不得重复回补")
	assert.Equal(t, before+1, scheduleRemaining(t, doc.ScheduleID),
		"重复处理不得把号源再加一次（否则号源会凭空变多）")
}

// ListExpiredPending 只应捞出"仍待确认且已超时"的订单。
//
// 若把 confirmed / cancelled 的也捞出来，定时任务会对它们做无效处理，
// 甚至把已经取消的订单号源再加一次。
func TestAppointmentListExpiredPending_OnlyPendingAndPastDue(t *testing.T) {
	ctx := context.Background()
	db := mustDB(t)
	repo := repository.NewAppointmentRepo(db)
	doc := seedDoctor(t, 5)

	expiredID := grabAndExpire(t, doc.ScheduleID, fmt.Sprintf("listexp-%d", doc.ScheduleID))

	rows, err := repo.ListExpiredPending(ctx, time.Now(), 200)
	require.NoError(t, err)

	var found bool
	for _, r := range rows {
		require.Equal(t, model.AppointmentStatusPending, r.Status,
			"只应返回仍为 pending 的订单")
		require.True(t, r.ExpireAt.Before(time.Now()), "只应返回已超时的订单")
		if r.ID == expiredID {
			found = true
		}
	}
	assert.True(t, found, "刚制造的超时订单应被捞出")
}

// --- 预约：签到序号 ---

// 序号按实际签到顺序分配，且重复签到返回原序号。
func TestAppointmentCheckIn_SequentialAndIdempotent(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewAppointmentRepo(mustDB(t))
	doc := seedDoctor(t, 5)
	clearRateLimits(t)

	firstID, firstUser := grabAsPatient(t, doc.ScheduleID, fmt.Sprintf("checkin-first-%d", doc.ScheduleID))
	secondID, secondUser := grabAsPatient(t, doc.ScheduleID, fmt.Sprintf("checkin-second-%d", doc.ScheduleID))

	a, err := repo.CheckIn(ctx, firstUser, firstID)
	require.NoError(t, err)
	require.NotNil(t, a.QueueNo)
	assert.Equal(t, 1, *a.QueueNo, "第一位签到者取 1")

	b, err := repo.CheckIn(ctx, secondUser, secondID)
	require.NoError(t, err)
	require.NotNil(t, b.QueueNo)
	assert.Equal(t, 2, *b.QueueNo, "第二位签到者取 2")

	// 重复签到：必须返回原序号，而不是重新排队
	again, err := repo.CheckIn(ctx, firstUser, firstID)
	require.NoError(t, err)
	require.NotNil(t, again.QueueNo)
	assert.Equal(t, 1, *again.QueueNo, "重复签到不得重新分配序号")
}

// 不能替别人签到（归属校验下沉在 SQL 层：WHERE id = ? AND user_id = ?）。
func TestAppointmentCheckIn_RejectsOtherUsersAppointment(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewAppointmentRepo(mustDB(t))
	doc := seedDoctor(t, 5)
	clearRateLimits(t)

	apptID, _ := grabAsPatient(t, doc.ScheduleID, fmt.Sprintf("checkin-owner-%d", doc.ScheduleID))
	_, otherUser := grabAsPatient(t, doc.ScheduleID, fmt.Sprintf("checkin-other-%d", doc.ScheduleID))

	_, err := repo.CheckIn(ctx, otherUser, apptID)

	assert.ErrorIs(t, err, repository.ErrNotFound,
		"他人的预约应表现为'不存在'，不泄露它是否存在")
}

// --- 刷新令牌（Redis）：登出必须真的能撤销 ---

// 刷新令牌存 Redis 而不是 JWT 的**唯一理由**就是"必须可撤销"。
// 这三个方法都用同一个键前缀拼 key，一旦哪一处的拼法不一致，
// 症状是"登出静默失败"——用户以为退出了，令牌却还能换新。
//
// service 层已有 TestLogout_RevokesRefreshToken，但那里用的是内存 fake，
// fake 里 `delete(m, k)` 不存在拼错 key 的可能。这里打真实 Redis。
func TestTokenRefresh_SaveConsumeDeleteRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewTokenRepo(mustRedis(t))
	token := "itest-refresh-" + t.Name()
	const userID = uint64(880010)

	require.NoError(t, repo.SaveRefresh(ctx, token, userID, time.Hour))

	// 取出即删除：刷新令牌是一次性凭证（GETDEL）
	got, err := repo.ConsumeRefresh(ctx, token)
	require.NoError(t, err)
	assert.Equal(t, userID, got, "取出的必须是同一个用户")

	// 同一令牌不能再用第二次（重放窗口）
	_, err = repo.ConsumeRefresh(ctx, token)
	assert.ErrorIs(t, err, repository.ErrNotFound,
		"刷新令牌必须一次性；能重复消费等于给重放留了口子")
}

func TestTokenRefresh_DeleteRevokesToken(t *testing.T) {
	ctx := context.Background()
	repo := repository.NewTokenRepo(mustRedis(t))
	token := "itest-logout-" + t.Name()

	require.NoError(t, repo.SaveRefresh(ctx, token, 880011, time.Hour))
	require.NoError(t, repo.DeleteRefresh(ctx, token))

	_, err := repo.ConsumeRefresh(ctx, token)
	assert.ErrorIs(t, err, repository.ErrNotFound,
		"登出后令牌必须失效；这里若拼错 key，登出会静默失败")
}

// 登出幂等：删一个不存在的令牌不应报错，否则客户端重试会拿到 500。
func TestTokenRefresh_DeleteMissingTokenIsNotAnError(t *testing.T) {
	repo := repository.NewTokenRepo(mustRedis(t))

	assert.NoError(t, repo.DeleteRefresh(context.Background(), "never-existed-"+t.Name()))
}

// 未保存过的令牌不能被消费。
func TestTokenRefresh_ConsumeUnknownTokenFails(t *testing.T) {
	repo := repository.NewTokenRepo(mustRedis(t))

	_, err := repo.ConsumeRefresh(context.Background(), "unknown-"+t.Name())

	assert.ErrorIs(t, err, repository.ErrNotFound)
}

// --- 辅助 ---

// grabAndExpire 抢一个号并把 expire_at 拨到过去，制造"待确认且已超时"的订单。
func grabAndExpire(t *testing.T, scheduleID uint64, idemKey string) uint64 {
	t.Helper()

	apptID, _ := grabAsPatient(t, scheduleID, idemKey)

	require.NoError(t, mustDB(t).Exec(
		"UPDATE appointments SET expire_at = ? WHERE id = ?",
		time.Now().Add(-time.Minute), apptID,
	).Error)

	return apptID
}

// grabAsPatient 以一个新患者身份抢号，返回预约 ID 与该患者 ID。
func grabAsPatient(t *testing.T, scheduleID uint64, idemKey string) (appointmentID, userID uint64) {
	t.Helper()

	token, uid := registerPatient(t)
	_, env := call(t, request{
		method: "POST", path: "/api/v1/appointments", token: token,
		body: map[string]any{"schedule_id": scheduleID, "idempotency_key": idemKey},
	})
	require.Equal(t, 0, env.Code, "抢号失败: %s", env.Message)

	appt := decode[struct {
		ID uint64 `json:"id"`
	}](t, env)
	require.NotZero(t, appt.ID)

	return appt.ID, uid
}

func scheduleRemaining(t *testing.T, scheduleID uint64) int {
	t.Helper()

	var remaining int
	require.NoError(t, mustDB(t).Raw(
		"SELECT remaining FROM schedules WHERE id = ?", scheduleID).Scan(&remaining).Error)
	return remaining
}

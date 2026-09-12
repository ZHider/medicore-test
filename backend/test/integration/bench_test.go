//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/repository"
)

// 这些基准跑在**真实 MySQL / Redis** 上，因为被测的正是 SQL 与 Lua 本身。
//
// 它们守护的不是"某段代码有多快"，而是**两条已经写进文档的性能结论**：
//
//  1. 游标分页的代价与页码无关，OFFSET 随页码线性增长（手册里那个"284 倍"）；
//  2. 号源扣减是单次 Lua 往返，可以扛住并发抢号。
//
// 没有基准时，这两条只是"某次手工测量的历史事实"——重构之后没人知道它们还成不成立。

// ---------------------------------------------------------------------------
// 号源扣减（Redis Lua）：抢号热路径
// ---------------------------------------------------------------------------

// 单次扣减就是一次 Redis 往返（EVALSHA）。
//
// 关注点：应当在几十微秒级。若涨到毫秒级，说明有人把 Lua 拆成了多次调用 ——
// 那不仅慢，更会**破坏原子性**，直接打开超卖的口子。
func BenchmarkStockDecrement(b *testing.B) {
	ctx := context.Background()
	stock := repository.NewStockRepo(mustRedis(b))

	// 容量给足，避免基准中途因为"号源耗尽"而走到拒绝分支
	scheduleID := nextStockID()
	if err := stock.EnsureLoaded(ctx, benchSchedule(scheduleID, 1_000_000)); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := stock.Decrement(ctx, scheduleID, 1)
		if err != nil {
			b.Fatal(err)
		}
		if res != repository.StockOK {
			b.Fatalf("扣减失败：%v（基准中途耗尽了号源？）", res)
		}
	}
}

// 并发扣减：这是抢号的真实形态，也是 Redis 单线程模型的价值所在。
//
// 跑 -cpu=1,4,8 可以看到吞吐随连接数扩展；若并发上升后明显退化，
// 说明存在不必要的往返或热点竞争。
func BenchmarkStockDecrementParallel(b *testing.B) {
	ctx := context.Background()
	stock := repository.NewStockRepo(mustRedis(b))

	scheduleID := nextStockID()
	if err := stock.EnsureLoaded(ctx, benchSchedule(scheduleID, 1_000_000)); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := stock.Decrement(ctx, scheduleID, 1); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// 对账用的比对更新：只在值未变时才写。它比扣减多了 GET 与分支，但仍在一次往返内。
func BenchmarkStockCompareAndSet(b *testing.B) {
	ctx := context.Background()
	stock := repository.NewStockRepo(mustRedis(b))

	// 初始值就设为 100，才能保证每次都走"命中"分支 ——
	// 否则测的是"比对失败后直接返回"的短路路径，白测。
	scheduleID := nextStockID()
	if err := stock.EnsureLoaded(ctx, benchSchedule(scheduleID, 100)); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if ok, err := stock.CompareAndSet(ctx, scheduleID, 100, 100); err != nil || !ok {
			b.Fatalf("CompareAndSet 未命中：ok=%v err=%v", ok, err)
		}
	}
}

func benchSchedule(id uint64, remaining int) *model.Schedule {
	return &model.Schedule{
		ID:        id,
		Total:     remaining,
		Remaining: remaining,
		SchedDate: time.Now().AddDate(0, 0, 1),
	}
}

// ---------------------------------------------------------------------------
// 基线：裸 Redis 往返
// ---------------------------------------------------------------------------

// 没有基线，上面那些"多少微秒"的数字就无法解读 ——
// 是 Lua 慢？还是本机到 Docker 的网络慢？
//
// 实测结论（见 docs/benchmark/）：裸 PING 与一次 EVALSHA 扣减耗时同量级，
// 说明**时间几乎全花在传输上，Lua 本身的开销可忽略**。
// 这也是为什么"把 Lua 拆成多次调用"是纯粹的损失：往返次数直接翻倍。
//
// 绝对值高度依赖运行环境（Windows + Docker Desktop 的端口转发尤其慢），
// 所以这些数字只用于**同机对比与回归检测**，不应对外当作吞吐承诺。
func BenchmarkRedisPing(b *testing.B) {
	ctx := context.Background()
	rdb := mustRedis(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := rdb.Ping(ctx).Err(); err != nil {
			b.Fatal(err)
		}
	}
}

// ---------------------------------------------------------------------------
// 游标分页 vs OFFSET：手册里那条"284 倍"
// ---------------------------------------------------------------------------

// benchAuditRows 是基准灌入的审计记录条数。
//
// 手册里的 284 倍来自 23 万行。这里默认用同量级；
// 想快速迭代时用 MEDICORE_BENCH_ROWS 调小（例如 20000），
// 定性结论（游标恒定 / OFFSET 线性增长）在小数据量上同样成立。
func benchAuditRows() int {
	if v := os.Getenv("MEDICORE_BENCH_ROWS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 200_000
}

// 分批 INSERT 而不是递归 CTE：MySQL 的 cte_max_recursion_depth 默认只有 1000，
// 而它是**会话级**变量 —— 用 SET SESSION 改它，后续语句可能落到连接池里的另一条连接上，
// 于是时好时坏。分批写则与连接、与 MySQL 版本都无关。
//
// 5000 行一批是权衡：太少则往返次数多，太多会撞 max_allowed_packet。
const benchInsertBatch = 5000

func seedAuditRows(b *testing.B, db *gorm.DB, rows int) {
	b.Helper()

	var existing int64
	if err := db.Raw("SELECT COUNT(*) FROM audit_logs").Scan(&existing).Error; err != nil {
		b.Fatal(err)
	}
	if existing >= int64(rows) {
		return // 同一轮 -bench 里多个基准共用，不重复灌
	}

	b.StopTimer() // 灌数据的时间不计入基准
	defer b.StartTimer()

	for done := 0; done < rows; done += benchInsertBatch {
		n := benchInsertBatch
		if done+n > rows {
			n = rows - done
		}

		var sb strings.Builder
		sb.WriteString("INSERT INTO audit_logs " +
			"(trace_id, method, path, status_code, latency_ms, created_at) VALUES ")

		args := make([]any, 0, n)
		for i := 0; i < n; i++ {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString("(?,'GET','/api/v1/bench',200,1,NOW(3))")
			args = append(args, fmt.Sprintf("%032d", done+i))
		}

		if err := db.Exec(sb.String(), args...).Error; err != nil {
			b.Fatalf("灌入基准数据失败（第 %d 批）: %v", done/benchInsertBatch, err)
		}
	}
}

// 游标分页：代价与页码**无关**，恒为"走主键索引取 20 行"。
//
// 浅页 = 从最新一条开始；深页 = 翻到很久以前（游标很小）。
// 两者应当接近 —— 若深页明显更慢，说明游标条件没走主键索引。
func BenchmarkAuditList_Cursor(b *testing.B) {
	db := mustDB(b)
	seedAuditRows(b, db, benchAuditRows())

	var maxID uint64
	if err := db.Raw("SELECT MAX(id) FROM audit_logs").Scan(&maxID).Error; err != nil {
		b.Fatal(err)
	}

	cases := []struct {
		name   string
		cursor uint64
	}{
		{"浅页", maxID}, // 从最新往旧翻
		{"深页", 1000},  // 翻到很久以前：游标很小
	}

	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			repo := repository.NewAuditRepo(db)
			ctx := context.Background()

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := repo.ListByCursor(ctx, repository.AuditFilter{}, c.cursor, 20); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// OFFSET 分页：代价随 offset 线性增长。
//
// 这个基准存在的意义是**作为对照**：它把"为什么禁止 OFFSET"从一句口号
// 变成每次都能重新测出来的数字。直接查表而不走 repository，
// 是因为项目里根本不存在 OFFSET 的实现 —— 这里测的是"如果那样写会怎样"。
func BenchmarkAuditList_Offset(b *testing.B) {
	db := mustDB(b)
	rows := benchAuditRows()
	seedAuditRows(b, db, rows)

	// 浅页 = OFFSET 0；深页 = 翻到最后一页（OFFSET 接近总行数），
	// 这正是 OFFSET 最贵的形态。
	cases := []struct {
		name   string
		offset int
	}{
		{"浅页", 0},
		{"深页", rows - 20},
	}

	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var out []model.AuditLogView
				if err := db.Raw(
					`SELECT id, trace_id, method, path, status_code, latency_ms, created_at
					 FROM audit_logs ORDER BY id DESC LIMIT 20 OFFSET ?`, c.offset,
				).Scan(&out).Error; err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

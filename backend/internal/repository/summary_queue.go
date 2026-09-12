package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis Stream 相关常量。
const (
	// SummaryStreamKey 是预问诊总结任务的队列。
	SummaryStreamKey = "stream:preconsult.summary"
	// SummaryGroup 是消费者组名。
	SummaryGroup = "cg:summary"
	// summaryDeadLetterKey 是死信队列：重试超限的任务转到这里，不再无限重投。
	summaryDeadLetterKey = "stream:preconsult.summary.dlq"
	// summaryMaxLen 限制队列长度（MAXLEN ~），防止 Redis 内存被无限增长的消息吃掉。
	summaryMaxLen = 10000
	// reportIDField 是消息体中的字段名。
	reportIDField = "report_id"
)

// SummaryQueueRepo 是预问诊总结任务的 Redis Stream 队列。
//
// 为什么用 Stream 而不是直接起 goroutine（spec §1.3 与决策 #8）：
//   - 用户提交后刷新/关闭页面不等于任务取消，任务必须与请求生命周期解耦；
//   - LLM 有速率限制，队列天然提供背压与削峰；
//   - 消费者组 + XPENDING/XAUTOCLAIM 让崩溃后的任务能被重新认领，
//     而裸 goroutine 一旦进程退出任务就永久丢失。
type SummaryQueueRepo struct {
	rdb *redis.Client
}

// NewSummaryQueueRepo 构造 SummaryQueueRepo。
func NewSummaryQueueRepo(rdb *redis.Client) *SummaryQueueRepo {
	return &SummaryQueueRepo{rdb: rdb}
}

// EnsureGroup 创建消费者组。用 MKSTREAM 保证 Redis 全新时也能成功。
func (r *SummaryQueueRepo) EnsureGroup(ctx context.Context) error {
	err := r.rdb.XGroupCreateMkStream(ctx, SummaryStreamKey, SummaryGroup, "0").Err()
	if err != nil && !redis.HasErrorPrefix(err, "BUSYGROUP") {
		return fmt.Errorf("create summary consumer group: %w", err)
	}
	return nil
}

// Enqueue 投递一个总结任务。
func (r *SummaryQueueRepo) Enqueue(ctx context.Context, reportID uint64) error {
	_, err := r.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: SummaryStreamKey,
		// Approx 对应 MAXLEN ~，让 Redis 可以按整节点裁剪，比精确裁剪高效得多
		MaxLen: summaryMaxLen,
		Approx: true,
		Values: map[string]any{reportIDField: reportID},
	}).Result()
	if err != nil {
		return fmt.Errorf("enqueue summary task: %w", err)
	}
	return nil
}

// Read 从消费者组读取新消息。无消息时（阻塞超时）返回空切片而非错误。
func (r *SummaryQueueRepo) Read(
	ctx context.Context, consumer string, count int, block time.Duration,
) ([]redis.XMessage, error) {
	res, err := r.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    SummaryGroup,
		Consumer: consumer,
		// ">" 表示只读从未投递给本组的新消息
		Streams: []string{SummaryStreamKey, ">"},
		Count:   int64(count),
		Block:   block,
	}).Result()

	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read summary stream: %w", err)
	}
	if len(res) == 0 {
		return nil, nil
	}
	return res[0].Messages, nil
}

// ClaimStale 认领长时间未确认的消息。
//
// 这是"至少一次投递"的关键：worker 处理到一半崩溃时，消息会停留在
// pending 列表里；没有认领机制，这条任务就永远没人处理，
// 报告会一直卡在 processing 状态。
func (r *SummaryQueueRepo) ClaimStale(
	ctx context.Context, consumer string, minIdle time.Duration, count int,
) ([]redis.XMessage, error) {
	msgs, _, err := r.rdb.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream:   SummaryStreamKey,
		Group:    SummaryGroup,
		Consumer: consumer,
		MinIdle:  minIdle,
		Start:    "0-0",
		Count:    int64(count),
	}).Result()

	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim stale summary tasks: %w", err)
	}
	return msgs, nil
}

// Ack 确认消息已处理完毕。
func (r *SummaryQueueRepo) Ack(ctx context.Context, messageID string) error {
	if err := r.rdb.XAck(ctx, SummaryStreamKey, SummaryGroup, messageID).Err(); err != nil {
		return fmt.Errorf("ack summary task: %w", err)
	}
	return nil
}

// DeadLetter 把重试超限的任务转入死信队列，避免无限重投堵塞主队列。
func (r *SummaryQueueRepo) DeadLetter(ctx context.Context, reportID uint64, reason string) error {
	_, err := r.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: summaryDeadLetterKey,
		MaxLen: summaryMaxLen,
		Approx: true,
		Values: map[string]any{
			reportIDField: reportID,
			"reason":      reason,
		},
	}).Result()
	if err != nil {
		return fmt.Errorf("dead letter summary task: %w", err)
	}
	return nil
}

// DeadLetterLen 返回死信队列长度，供运维排查。
func (r *SummaryQueueRepo) DeadLetterLen(ctx context.Context) (int64, error) {
	n, err := r.rdb.XLen(ctx, summaryDeadLetterKey).Result()
	if err != nil {
		return 0, fmt.Errorf("read dead letter length: %w", err)
	}
	return n, nil
}

// PendingCount 返回消费者组中未确认的消息数，可用于告警"任务积压"。
func (r *SummaryQueueRepo) PendingCount(ctx context.Context) (int64, error) {
	info, err := r.rdb.XPending(ctx, SummaryStreamKey, SummaryGroup).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return 0, nil
		}
		return 0, fmt.Errorf("read pending summary tasks: %w", err)
	}
	return info.Count, nil
}

// DeliveryCount 返回某条未确认消息已被投递的次数。
//
// 用它而不是报告行里的 attempt_count 来判断重试次数：报告计数器只在
// 成功抢占后才递增，而"抢占前就失败"（例如数据库连接断了）也需要计数，
// 否则那类失败会永远重试下去。Redis 的投递计数是更可靠的依据。
func (r *SummaryQueueRepo) DeliveryCount(ctx context.Context, messageID string) (int64, error) {
	pendings, err := r.rdb.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: SummaryStreamKey,
		Group:  SummaryGroup,
		Start:  messageID,
		End:    messageID,
		Count:  1,
	}).Result()
	if err != nil {
		return 0, fmt.Errorf("read delivery count: %w", err)
	}
	if len(pendings) == 0 {
		// 消息已被确认或不存在，按 1 次处理
		return 1, nil
	}
	return pendings[0].RetryCount, nil
}

// ParseReportID 从消息体中取出报告 ID。
func ParseReportID(msg redis.XMessage) (uint64, error) {
	raw, ok := msg.Values[reportIDField]
	if !ok {
		return 0, fmt.Errorf("消息 %s 缺少字段 %s", msg.ID, reportIDField)
	}

	switch v := raw.(type) {
	case string:
		id, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("消息 %s 的 %s 不是数字: %w", msg.ID, reportIDField, err)
		}
		return id, nil
	case int64:
		return uint64(v), nil
	case uint64:
		return v, nil
	default:
		return 0, fmt.Errorf("消息 %s 的 %s 类型不支持: %T", msg.ID, reportIDField, raw)
	}
}

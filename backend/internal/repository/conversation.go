package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/medicore/backend/internal/llm"
)

const (
	conversationKeyPrefix = "preconsult:conv:"
	// 对话保留 24 小时：足够覆盖一次就诊，又不会长期占用内存。
	conversationTTL = 24 * time.Hour
	// 上限防止恶意客户端无限追加把内存打满。
	// 40 条足够覆盖一次预问诊（约 20 轮问答）。
	maxConversationMessages = 40
)

// ConversationRepo 保存进行中的预问诊对话。
//
// 放 Redis 而不是直接写 ai_pre_reports.conversation：
//   - 对话过程中每轮都要追加，写 JSON 列是"读-改-写"，多标签页并发会互相覆盖；
//   - 报告行应该在**提交**时才产生，避免出现大量 status=pending 的空壳记录。
//
// 提交时再把完整对话一次性落到报告行。
type ConversationRepo struct {
	rdb *redis.Client
}

// NewConversationRepo 构造 ConversationRepo。
func NewConversationRepo(rdb *redis.Client) *ConversationRepo {
	return &ConversationRepo{rdb: rdb}
}

// Append 追加一条消息，并裁剪与续期。
func (r *ConversationRepo) Append(ctx context.Context, appointmentID uint64, msg llm.Message) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal conversation message: %w", err)
	}

	key := conversationKey(appointmentID)
	pipe := r.rdb.Pipeline()
	pipe.RPush(ctx, key, raw)
	// 只保留最近 N 条，防止无限增长
	pipe.LTrim(ctx, key, -maxConversationMessages, -1)
	pipe.Expire(ctx, key, conversationTTL)

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("append conversation message: %w", err)
	}
	return nil
}

// List 返回完整对话，顺序为时间先后。
func (r *ConversationRepo) List(ctx context.Context, appointmentID uint64) ([]llm.Message, error) {
	raws, err := r.rdb.LRange(ctx, conversationKey(appointmentID), 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("list conversation: %w", err)
	}

	messages := make([]llm.Message, 0, len(raws))
	for i, raw := range raws {
		var msg llm.Message
		if err := json.Unmarshal([]byte(raw), &msg); err != nil {
			// 单条损坏不影响其余内容，跳过并留痕即可
			return nil, fmt.Errorf("unmarshal conversation message #%d: %w", i, err)
		}
		messages = append(messages, msg)
	}
	return messages, nil
}

// Clear 清空对话，用于提交后或重新开始。
func (r *ConversationRepo) Clear(ctx context.Context, appointmentID uint64) error {
	if err := r.rdb.Del(ctx, conversationKey(appointmentID)).Err(); err != nil {
		return fmt.Errorf("clear conversation: %w", err)
	}
	return nil
}

// Exists 判断是否存在进行中的对话。
func (r *ConversationRepo) Exists(ctx context.Context, appointmentID uint64) (bool, error) {
	n, err := r.rdb.Exists(ctx, conversationKey(appointmentID)).Result()
	if err != nil {
		return false, fmt.Errorf("check conversation: %w", err)
	}
	return n > 0, nil
}

// ErrConversationEmpty 表示尚无任何对话内容。
var ErrConversationEmpty = errors.New("conversation empty")

func conversationKey(appointmentID uint64) string {
	return fmt.Sprintf("%s%d", conversationKeyPrefix, appointmentID)
}

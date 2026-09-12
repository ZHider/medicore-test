package middleware

import (
	"fmt"
	"math"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/response"
)

// tokenBucketScript 是令牌桶的 Lua 实现（spec §9.3）。
//
// 必须在 Redis 侧原子执行：若拆成"读余量 → 计算 → 写回"三次往返，
// 并发请求会读到同一个旧值，导致限流被击穿。
//
// KEYS[1] 桶键
// ARGV[1] 容量          ARGV[2] 每秒补充速率
// ARGV[3] 当前时间(ms)  ARGV[4] 本次请求令牌数
// 返回 1 允许 / 0 拒绝
const tokenBucketScript = `
local key       = KEYS[1]
local capacity  = tonumber(ARGV[1])
local rate      = tonumber(ARGV[2])
local now       = tonumber(ARGV[3])
local requested = tonumber(ARGV[4])

local data   = redis.call('HMGET', key, 'tokens', 'ts')
local tokens = tonumber(data[1])
local ts     = tonumber(data[2])

if tokens == nil then
  tokens = capacity
  ts     = now
end

-- 按流逝时间补充令牌，上限为容量
local delta = math.max(0, now - ts) / 1000
tokens = math.min(capacity, tokens + delta * rate)

local allowed = 0
if tokens >= requested then
  tokens = tokens - requested
  allowed = 1
end

redis.call('HSET', key, 'tokens', tokens, 'ts', now)
-- 桶空闲到满所需时间即可安全过期，避免无界占用内存
redis.call('PEXPIRE', key, math.ceil(capacity / rate * 1000) + 1000)

return allowed
`

var tokenBucket = redis.NewScript(tokenBucketScript)

// RateLimitOptions 是限流中间件配置。
type RateLimitOptions struct {
	// KeyPrefix 用于区分不同限流维度，如 "rl:login"。
	KeyPrefix string
	// Capacity 是桶容量，即突发上限。
	Capacity int
	// RefillPerSecond 是每秒补充令牌数。
	// spec §9.3 的"登录 5 次/分钟"即 Capacity=5、RefillPerSecond=5.0/60。
	RefillPerSecond float64
	// KeyFunc 决定限流主体，默认按客户端 IP。
	KeyFunc func(*gin.Context) string
}

// RateLimit 基于 Redis 令牌桶的限流中间件。
func RateLimit(rdb *redis.Client, opts RateLimitOptions) gin.HandlerFunc {
	keyFunc := opts.KeyFunc
	if keyFunc == nil {
		keyFunc = func(c *gin.Context) string { return c.ClientIP() }
	}

	return func(c *gin.Context) {
		key := opts.KeyPrefix + ":" + keyFunc(c)

		res, err := tokenBucket.Run(
			c.Request.Context(), rdb, []string{key},
			opts.Capacity, opts.RefillPerSecond, time.Now().UnixMilli(), 1,
		).Int()
		if err != nil {
			// 限流器故障时放行（fail-open）：让登录整体不可用比短暂放开限流更严重。
			// 该路径必须可观测，否则等于静默失去防护。
			rateLimitErrorsTotal.Inc()
			c.Next()
			return
		}

		if res == 0 {
			retryAfter := 1
			if opts.RefillPerSecond > 0 {
				retryAfter = int(math.Ceil(1 / opts.RefillPerSecond))
			}
			c.Header("Retry-After", fmt.Sprintf("%d", retryAfter))
			response.Fail(c, errcode.ErrRateLimited)
			c.Abort()
			return
		}

		c.Next()
	}
}

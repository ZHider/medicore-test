package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func newTestCache(t *testing.T) *Cache {
	t.Helper()

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	return NewCache(rdb, zap.NewNop())
}

func TestCacheLoad_HitAfterFirstLoad(t *testing.T) {
	c := newTestCache(t)
	loads := 0
	loader := func(context.Context) (string, error) {
		loads++
		return "value", nil
	}

	for i := 0; i < 3; i++ {
		got, err := Load(context.Background(), c, "k", time.Minute, loader)
		require.NoError(t, err)
		assert.Equal(t, "value", got)
	}

	assert.Equal(t, 1, loads, "第二次起应命中缓存，不再回源")
}

// 这是技术债修复的核心断言：
// singleflight 的跟随者共享 leader 的 context，若 leader 的请求被取消，
// 所有健康跟随者会一起收到 context.Canceled——拿到一个本不该属于它们的错误。
// 修复方式是回源时使用脱离调用方取消信号的 context。
func TestCacheLoad_LoaderContextSurvivesCallerCancellation(t *testing.T) {
	c := newTestCache(t)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, cancelled.Err())

	got, err := Load(cancelled, c, "k", time.Minute, func(loadCtx context.Context) (string, error) {
		// 回源 context 不应带着调用方的取消状态
		if err := loadCtx.Err(); err != nil {
			return "", fmt.Errorf("回源 context 已被取消: %w", err)
		}
		return "value", nil
	})

	require.NoError(t, err, "调用方已取消不应导致回源失败")
	assert.Equal(t, "value", got)
}

// 并发场景：leader 的请求被取消后，已经排在它后面的跟随者仍应拿到结果。
func TestCacheLoad_LeaderCancellationDoesNotFailFollower(t *testing.T) {
	c := newTestCache(t)

	const loadDelay = 80 * time.Millisecond
	started := make(chan struct{})
	var startOnce sync.Once

	loader := func(ctx context.Context) (string, error) {
		startOnce.Do(func() { close(started) })
		select {
		case <-time.After(loadDelay):
			return "loaded", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}

	var (
		wg          sync.WaitGroup
		leaderErr   error
		followerErr error
		followerVal string
	)

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()

	wg.Add(1)
	go func() {
		defer wg.Done()
		// leader：结果本身不重要（它的请求已被取消），
		// 关键是它不能把取消状态传染给跟随者
		_, leaderErr = Load(leaderCtx, c, "k", time.Minute, loader)
	}()

	// 等 leader 真正进入回源，再取消它并让 follower 加入
	<-started
	cancelLeader()

	wg.Add(1)
	go func() {
		defer wg.Done()
		followerVal, followerErr = Load(context.Background(), c, "k", time.Minute, loader)
	}()

	wg.Wait()

	require.NoError(t, followerErr,
		"跟随者的请求是健康的，不应因为 leader 被取消而失败")
	assert.Equal(t, "loaded", followerVal)
	assert.NoError(t, leaderErr, "修复后 leader 的回源也应完成（顺带预热了缓存）")
}

// 回源必须有独立超时上界：调用方早早离开后，这次回源不能无限期挂着。
// 直接断言 loader 拿到的 context 带 deadline，比等满超时更快也更精确。
func TestCacheLoad_DetachedLoadHasDeadline(t *testing.T) {
	c := newTestCache(t)

	var sawDeadline bool
	var deadline time.Time

	_, err := Load(context.Background(), c, "k", time.Minute, func(loadCtx context.Context) (string, error) {
		deadline, sawDeadline = loadCtx.Deadline()
		return "v", nil
	})

	require.NoError(t, err)
	require.True(t, sawDeadline, "回源 context 必须有独立超时上界，否则调用方离开后回源会永久挂住")
	assert.WithinDuration(t, time.Now().Add(loadTimeout), deadline, 2*time.Second)
}

func TestCacheLoad_PropagatesLoaderError(t *testing.T) {
	c := newTestCache(t)
	sentinel := fmt.Errorf("db down")

	_, err := Load(context.Background(), c, "k", time.Minute, func(context.Context) (string, error) {
		return "", sentinel
	})

	assert.ErrorIs(t, err, sentinel)
}

func TestCache_GetJSONCorruptedPayloadIsEvicted(t *testing.T) {
	c := newTestCache(t)

	require.NoError(t, c.rdb.Set(context.Background(), "k", "{not json", time.Minute).Err())

	var dest map[string]any
	hit, err := c.GetJSON(context.Background(), "k", &dest)

	require.NoError(t, err, "脏数据应按未命中处理，而不是把错误抛给调用方")
	assert.False(t, hit)

	exists, err := c.rdb.Exists(context.Background(), "k").Result()
	require.NoError(t, err)
	assert.Zero(t, exists, "脏键应被删除，避免反复触发反序列化错误")
}

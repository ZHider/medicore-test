// Package service 承载业务逻辑、事务边界与权限归属校验（spec §3.2）。
package service

import (
	"context"
	"sync"
)

// HealthService 聚合各依赖的健康状态。
type HealthService struct {
	repo healthChecker
}

// NewHealthService 构造 HealthService。
func NewHealthService(repo healthChecker) *HealthService {
	return &HealthService{repo: repo}
}

// CheckResult 是单个依赖的健康状态。
type CheckResult struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type probe struct {
	name string
	fn   func(context.Context) error
}

// Ready 并发探测全部依赖，返回逐项结果。
// 无论成功失败都返回明细，便于运维直接定位是哪个依赖不可用。
func (s *HealthService) Ready(ctx context.Context) map[string]CheckResult {
	probes := []probe{
		{"mysql", s.repo.PingMySQL},
		{"redis", s.repo.PingRedis},
	}

	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		result = make(map[string]CheckResult, len(probes))
	)

	for _, p := range probes {
		wg.Add(1)
		go func(p probe) {
			defer wg.Done()

			res := CheckResult{Status: "ok"}
			if err := p.fn(ctx); err != nil {
				res.Status = "down"
				res.Error = err.Error()
			}

			mu.Lock()
			result[p.name] = res
			mu.Unlock()
		}(p)
	}

	wg.Wait()
	return result
}

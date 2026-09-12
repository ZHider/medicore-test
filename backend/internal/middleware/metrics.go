package middleware

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// 独立 registry 而非 prometheus.DefaultRegisterer：
// 避免测试或多次初始化时重复注册 panic，也让 /metrics 只暴露本项目关心的指标。
var (
	registry = prometheus.NewRegistry()

	httpRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "HTTP 请求总数。",
	}, []string{"method", "path", "status"})

	httpRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP 请求耗时（秒）。",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
	}, []string{"method", "path"})

	// 限流器 fail-open 计数。该路径一旦上涨，说明 Redis 不可用、
	// 当前限流实际已失效——必须能被告警发现，否则等于静默失去防护。
	rateLimitErrorsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "ratelimit_errors_total",
		Help: "限流器自身故障（Redis 不可用）而放行请求的次数。",
	})
)

func init() {
	registry.MustRegister(
		httpRequestsTotal,
		httpRequestDuration,
		rateLimitErrorsTotal,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
}

// Registry 暴露注册表，供后续模块注册业务指标
// （spec §9.2：appointment_grab_total、llm_calls_total 等）。
func Registry() prometheus.Registerer {
	return registry
}

// Metrics 采集请求计数与耗时。path 使用路由模板（FullPath）而非真实路径，避免高基数。
func Metrics() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		path := c.FullPath()
		if path == "" {
			path = "unmatched"
		}
		httpRequestsTotal.WithLabelValues(c.Request.Method, path, strconv.Itoa(c.Writer.Status())).Inc()
		httpRequestDuration.WithLabelValues(c.Request.Method, path).Observe(time.Since(start).Seconds())
	}
}

// MetricsHandler 返回 /metrics 的 HTTP handler。
func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}

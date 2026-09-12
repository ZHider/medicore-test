// Package metrics 持有 Prometheus 注册表。
//
// 独立成包的原因：指标需要由 middleware（HTTP 层）与 service（业务层）
// 共同注册。若注册表放在 middleware 中，service 就得反向依赖它。
// 下沉到这里后，各层都只依赖本包，不存在环。
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// registry 是独立注册表，而非 prometheus.DefaultRegisterer：
// 避免测试或重复初始化时 panic，也让 /metrics 只暴露本项目关心的指标。
var registry = prometheus.NewRegistry()

func init() {
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
}

// Register 注册指标收集器。重复注册会 panic，因此只在包的 init 中调用一次。
func Register(collectors ...prometheus.Collector) {
	registry.MustRegister(collectors...)
}

// Handler 返回 /metrics 的 HTTP handler。
func Handler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}

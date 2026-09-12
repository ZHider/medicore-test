// Package router 组装 HTTP 路由与中间件链（spec §3.1）。
package router

import (
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/medicore/backend/internal/config"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/handler"
	"github.com/medicore/backend/internal/jwt"
	"github.com/medicore/backend/internal/middleware"
	"github.com/medicore/backend/internal/response"
	"github.com/medicore/backend/internal/service"
)

// Deps 是路由装配所需的依赖。
// 用结构体承载：后续模块只需在此加字段，不必改 New 的签名。
type Deps struct {
	Config     *config.Config
	Logger     *zap.Logger
	Redis      *redis.Client
	Health     *service.HealthService
	Auth       *service.AuthService
	Catalog    *service.CatalogService
	JWTManager *jwt.Manager
}

// New 构建 gin.Engine。
//
// 中间件链顺序（spec §3.1，固定不可调换）：
//
//	TraceID → Recovery → RequestLog → Metrics → [RateLimit → JWT → RBAC]
//
// 方括号内三段按路由挂载而非全局：探针与 /metrics 必须免认证，
// 否则 Prometheus 抓取会被 401，健康检查也无法在启动早期生效。
func New(d Deps) *gin.Engine {
	if d.Config.App.Env == "prod" {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.New()
	r.HandleMethodNotAllowed = true

	// 基础设施中间件：对全部请求生效
	r.Use(
		middleware.TraceID(),
		middleware.Recovery(d.Logger),
		middleware.RequestLog(d.Logger),
		middleware.Metrics(),
	)

	// 运维端点：不鉴权、不限流
	healthHandler := handler.NewHealthHandler(d.Health)
	r.GET("/healthz", healthHandler.Live)
	r.GET("/readyz", healthHandler.Ready)
	r.GET("/metrics", gin.WrapH(middleware.MetricsHandler()))

	registerAuthRoutes(r, d)
	registerCatalogRoutes(r, d)

	// 未匹配路由/方法也必须返回统一信封，否则客户端会拿到 gin 的纯文本 404，
	// 无法按 code 字段分支处理（spec §3.3）。
	r.NoRoute(func(c *gin.Context) {
		response.Fail(c, errcode.ErrNotFound)
	})
	r.NoMethod(func(c *gin.Context) {
		response.Fail(c, errcode.ErrMethodNotAllowed)
	})

	return r
}

func registerAuthRoutes(r *gin.Engine, d Deps) {
	h := handler.NewAuthHandler(d.Auth)

	auth := r.Group("/api/v1/auth")

	// 公开端点
	auth.POST("/register", h.Register)
	auth.POST("/refresh", h.Refresh)
	// 登录限流：spec §9.3 要求 5 次/分钟。
	// capacity=5 允许瞬时 5 次突发，refill=5/60 每秒即每分钟补满。
	auth.POST("/login", middleware.RateLimit(d.Redis, middleware.RateLimitOptions{
		KeyPrefix:       "rl:login",
		Capacity:        5,
		RefillPerSecond: 5.0 / 60.0,
	}), h.Login)

	// 需要登录的端点
	secured := auth.Group("")
	secured.Use(middleware.JWT(d.JWTManager))
	secured.POST("/logout", h.Logout)
	secured.GET("/me", h.Me)
}

// registerCatalogRoutes 挂载科室 / 医生 / 排班查询（spec §6.1，角色=登录）。
//
// 整个分组统一要求登录：这些是院内数据，不应匿名可见。
func registerCatalogRoutes(r *gin.Engine, d Deps) {
	h := handler.NewCatalogHandler(d.Catalog)

	v1 := r.Group("/api/v1")
	v1.Use(middleware.JWT(d.JWTManager))

	v1.GET("/departments", h.ListDepartments)
	v1.GET("/departments/:id/doctors", h.ListDoctorsByDepartment)
	v1.GET("/departments/:id/schedules", h.ListSchedulesByDepartment)
	v1.GET("/doctors/:id", h.DoctorDetail)
	v1.GET("/doctors/:id/schedules", h.ListSchedulesByDoctor)
}

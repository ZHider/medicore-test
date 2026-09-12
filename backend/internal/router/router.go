// Package router 组装 HTTP 路由与中间件链（spec §3.1）。
package router

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/medicore/backend/internal/audit"
	"github.com/medicore/backend/internal/authctx"
	"github.com/medicore/backend/internal/config"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/handler"
	"github.com/medicore/backend/internal/jwt"
	"github.com/medicore/backend/internal/middleware"
	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/response"
	"github.com/medicore/backend/internal/service"
)

// Deps 是路由装配所需的依赖。
// 用结构体承载：后续模块只需在此加字段，不必改 New 的签名。
type Deps struct {
	Config       *config.Config
	Logger       *zap.Logger
	Redis        *redis.Client
	Audit        audit.Recorder
	Health       *service.HealthService
	Auth         *service.AuthService
	Catalog      *service.CatalogService
	Appointments *service.AppointmentService
	Preconsult   *service.PreconsultService
	AuditLogs    *service.AuditService
	Records      *service.RecordService
	Sync         *service.SyncService
	JWTManager   *jwt.Manager
}

// New 构建 gin.Engine。
//
// 中间件链顺序（spec §3.1，固定不可调换）：
//
//	TraceID → Recovery → RequestLog → Metrics → Audit → [RateLimit → JWT → RBAC]
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
		// Audit 在 c.Next() 之后记录，因此能包住后面所有中间件与 handler——
		// 被 RBAC 拒绝的请求同样会留下记录，而那恰恰是审计最关心的内容之一。
		middleware.Audit(d.Audit),
	)

	registerOpsRoutes(r, d)
	registerPublicRoutes(r, d)
	registerSecuredRoutes(r, d)

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

// registerOpsRoutes 挂载运维端点：不鉴权、不限流。
func registerOpsRoutes(r *gin.Engine, d Deps) {
	h := handler.NewHealthHandler(d.Health)
	r.GET("/healthz", h.Live)
	r.GET("/readyz", h.Ready)
	r.GET("/metrics", gin.WrapH(middleware.MetricsHandler()))
}

// registerPublicRoutes 挂载无需登录的端点。
func registerPublicRoutes(r *gin.Engine, d Deps) {
	h := handler.NewAuthHandler(d.Auth)
	auth := r.Group("/api/v1/auth")

	auth.POST("/register", h.Register)
	auth.POST("/refresh", h.Refresh)
	// 登录限流：spec §9.3 要求 5 次/分钟。
	// capacity=5 允许瞬时 5 次突发，refill=5/60 每秒即每分钟补满。
	auth.POST("/login", middleware.RateLimit(d.Redis, middleware.RateLimitOptions{
		KeyPrefix:       "rl:login",
		Capacity:        5,
		RefillPerSecond: 5.0 / 60.0,
	}), h.Login)
}

// registerSecuredRoutes 挂载需要登录的端点。
// 统一在一个分组上加 JWT，避免逐条路由重复声明。
func registerSecuredRoutes(r *gin.Engine, d Deps) {
	secured := r.Group("/api/v1")
	secured.Use(middleware.JWT(d.JWTManager))

	registerAuthSecured(secured, d)
	registerCatalog(secured, d)
	registerAppointments(secured, d)
	registerPreconsult(secured, d)
	registerAdmin(secured, d)
	registerRecords(secured, d)
	registerSync(secured, d)
}

func registerAuthSecured(g *gin.RouterGroup, d Deps) {
	h := handler.NewAuthHandler(d.Auth)
	auth := g.Group("/auth")
	auth.POST("/logout", h.Logout)
	auth.GET("/me", h.Me)
}

// registerCatalog 挂载科室 / 医生 / 排班查询（spec §6.1，角色=登录）。
func registerCatalog(g *gin.RouterGroup, d Deps) {
	h := handler.NewCatalogHandler(d.Catalog)

	g.GET("/departments", h.ListDepartments)
	g.GET("/departments/:id/doctors", h.ListDoctorsByDepartment)
	g.GET("/departments/:id/schedules", h.ListSchedulesByDepartment)
	g.GET("/doctors/:id", h.DoctorDetail)
	g.GET("/doctors/:id/schedules", h.ListSchedulesByDoctor)
}

// registerPreconsult 挂载 AI 预问诊端点。
//
// 对话走 SSE：POST + text/event-stream，而不是 EventSource
// （后者只支持 GET，无法携带消息体与 Authorization 头）。
func registerPreconsult(g *gin.RouterGroup, d Deps) {
	h := handler.NewPreconsultHandler(d.Preconsult)
	patient := middleware.RequireRole(model.RolePatient)

	g.POST("/appointments/:id/preconsult/chat", patient, h.Chat)
	g.GET("/appointments/:id/preconsult/history", patient, h.History)
	g.POST("/appointments/:id/preconsult/submit", patient, h.Submit)
	// 报告允许患者、接诊医生与管理员访问；
	// 具体归属校验在 service 层（仅凭角色无法判断"这位患者是否归你接诊"）。
	g.GET("/appointments/:id/preconsult/report",
		middleware.RequireRole(model.RolePatient, model.RoleDoctor, model.RoleAdmin), h.Report)
}

// registerAppointments 挂载抢号相关端点。
func registerAppointments(g *gin.RouterGroup, d Deps) {
	h := handler.NewAppointmentHandler(d.Appointments)

	// 抢号限流：spec §9.3 要求 10 次/分钟。
	// 按用户维度限流（此时 JWT 已执行），而不是按 IP——
	// 同一 NAT 后的多名患者不应互相影响。
	grabLimit := middleware.RateLimit(d.Redis, middleware.RateLimitOptions{
		KeyPrefix:       "rl:grab",
		Capacity:        10,
		RefillPerSecond: 10.0 / 60.0,
		KeyFunc: func(c *gin.Context) string {
			return strconv.FormatUint(authctx.MustUserID(c), 10)
		},
	})

	patient := middleware.RequireRole(model.RolePatient)

	g.POST("/appointments", patient, grabLimit, h.Grab)
	g.GET("/appointments", patient, h.ListMine)
	g.POST("/appointments/:id/cancel", patient, h.Cancel)
	g.POST("/appointments/:id/checkin", patient, h.CheckIn)

	// 医生接诊列表：挂在 /doctor 下与患者侧的 /appointments 区分开，
	// 两者响应形态不同（患者看医生，医生看患者），混用端点会让契约模糊。
	clinical := middleware.RequireRole(model.RoleDoctor, model.RoleAdmin)
	g.GET("/doctor/appointments", clinical, h.ListForDoctor)

	// 候诊队列供医生与管理员查看
	g.GET("/schedules/:id/queue", clinical, h.Queue)
}

// registerAdmin 挂载仅管理员可用的端点（spec §2.1）。
func registerAdmin(g *gin.RouterGroup, d Deps) {
	h := handler.NewAuditHandler(d.AuditLogs)

	admin := g.Group("/admin")
	admin.Use(middleware.RequireRole(model.RoleAdmin))
	admin.GET("/audit-logs", h.List)
}

// registerRecords 挂载病历与处方端点。
//
// 角色限制只放到"医生/患者/管理员均可见"这一层：
// 更细的归属校验（这份病历是不是你的）必须由 service 层完成，
// 路由层拿不到资源归属信息。
func registerRecords(g *gin.RouterGroup, d Deps) {
	h := handler.NewRecordHandler(d.Records)

	doctor := middleware.RequireRole(model.RoleDoctor)
	anyRole := middleware.RequireRole(model.RolePatient, model.RoleDoctor, model.RoleAdmin)

	g.POST("/appointments/:id/record", doctor, h.Write)
	g.GET("/appointments/:id/record", anyRole, h.Get)
	g.GET("/records", anyRole, h.ListMine)
}

// registerSync 挂载 HIS 同步端点（仅管理员）。
func registerSync(g *gin.RouterGroup, d Deps) {
	h := handler.NewSyncHandler(d.Sync)

	admin := g.Group("/admin")
	admin.Use(middleware.RequireRole(model.RoleAdmin))
	admin.POST("/sync/:source", h.Trigger)
	admin.GET("/sync-logs", h.ListLogs)
}

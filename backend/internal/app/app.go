// Package app 负责装配全部依赖。
//
// 之所以独立成包而不是写在 main 里：集成测试需要复用**同一套装配**。
// 若测试自己重新搭一遍，测的就是"测试搭的东西"，而真实启动路径
// （中间件顺序、依赖注入、后台任务注册）仍然没有被覆盖。
package app

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/medicore/backend/internal/audit"
	"github.com/medicore/backend/internal/config"
	"github.com/medicore/backend/internal/jwt"
	"github.com/medicore/backend/internal/llm"
	"github.com/medicore/backend/internal/mockhis"
	"github.com/medicore/backend/internal/repository"
	"github.com/medicore/backend/internal/router"
	"github.com/medicore/backend/internal/scheduler"
	"github.com/medicore/backend/internal/secret"
	"github.com/medicore/backend/internal/service"
	"github.com/medicore/backend/internal/worker"
	"github.com/medicore/backend/prompts"
)

// hisSource 是模拟 HIS 数据源。
//
// 单独定义而不是直接用函数：真实 HIS 接入时只需替换这个实现，
// service 层的同步编排逻辑一行不用改。
type hisSource struct{}

func (hisSource) Load(context.Context) (*mockhis.Dataset, error) {
	return mockhis.Load()
}

// App 持有已装配的运行单元。
type App struct {
	// Engine 是装配完成的 HTTP 路由，可直接交给 httptest 使用。
	Engine *gin.Engine
	// Config 是生效的配置，供调用方读取端口等。
	Config *config.Config

	sqlDB         *sql.DB
	rdb           *redis.Client
	auditWriter   *audit.Writer
	scheduler     *scheduler.Scheduler
	summaryWorker *worker.SummaryWorker
	log           *zap.Logger
}

// Build 装配全部依赖并返回 App。此时后台任务尚未启动。
func Build(cfg *config.Config, log *zap.Logger) (*App, error) {
	if log == nil {
		log = zap.NewNop()
	}

	db, err := gorm.Open(mysql.Open(cfg.MySQL.DSN()), &gorm.Config{
		// 只记录慢查询与错误，避免业务日志被 SQL 淹没
		Logger: gormlogger.Default.LogMode(gormlogger.Warn),
		// 把驱动的方言错误转成 GORM 的通用错误（如唯一键冲突 -> ErrDuplicatedKey），
		// 这样 repository 层无需解析驱动错误文本。
		TranslateError: true,
	})
	if err != nil {
		return nil, fmt.Errorf("connect mysql: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql.DB: %w", err)
	}
	sqlDB.SetMaxOpenConns(cfg.MySQL.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MySQL.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.MySQL.ConnMaxLifetime)

	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
		PoolSize: cfg.Redis.PoolSize,
	})

	// 加密组件在启动时构造：密钥格式错误必须立刻失败，而不是等第一个注册请求才暴露。
	cipher, err := secret.NewCipher(cfg.Security.AESKey)
	if err != nil {
		_ = rdb.Close()
		_ = sqlDB.Close()
		return nil, fmt.Errorf("init aes cipher: %w", err)
	}
	jwtManager := jwt.NewManager(
		cfg.Security.JWTSecret,
		cfg.Security.JWTIssuer,
		cfg.Security.AccessTokenTTL,
	)

	// ---- 提示词与输出 Schema ----
	systemPrompt, err := prompts.PreconsultSystemPrompt(prompts.PreconsultLatest)
	if err != nil {
		_ = rdb.Close()
		_ = sqlDB.Close()
		return nil, fmt.Errorf("加载预问诊提示词: %w", err)
	}
	summaryPrompt, err := prompts.PreconsultSummaryPrompt(prompts.PreconsultLatest)
	if err != nil {
		_ = rdb.Close()
		_ = sqlDB.Close()
		return nil, fmt.Errorf("加载总结提示词: %w", err)
	}
	summarySchema, err := prompts.PreconsultSummarySchema(prompts.PreconsultLatest)
	if err != nil {
		_ = rdb.Close()
		_ = sqlDB.Close()
		return nil, fmt.Errorf("加载总结 Schema: %w", err)
	}
	// Schema 在启动时编译：写错却跑到线上才发现，代价是整条降级路径静默生效
	summaryValidator, err := service.NewSummaryValidator(summarySchema)
	if err != nil {
		_ = rdb.Close()
		_ = sqlDB.Close()
		return nil, fmt.Errorf("编译总结 Schema: %w", err)
	}
	// 反向自检：确认 Schema 真会拒绝内容，而不是被改成 {}
	if err := summaryValidator.SelfCheck(); err != nil {
		_ = rdb.Close()
		_ = sqlDB.Close()
		return nil, fmt.Errorf("总结 Schema 自检失败: %w", err)
	}

	llmClient, err := llm.NewClient(cfg.LLM.Provider, llm.MockConfig{
		DeltaDelay:    time.Duration(cfg.LLM.Mock.DeltaDelayMS) * time.Millisecond,
		SummaryMode:   llm.SummaryMode(cfg.LLM.Mock.SummaryMode),
		StreamErrorAt: cfg.LLM.Mock.StreamErrorAt,
	}, cfg.LLM.Timeout)
	if err != nil {
		_ = rdb.Close()
		_ = sqlDB.Close()
		return nil, fmt.Errorf("初始化 LLM 客户端: %w", err)
	}

	// ---- 依赖装配：handler → service → repository（依赖方向只能向下）----
	healthRepo := repository.NewHealthRepo(db, rdb)
	healthSvc := service.NewHealthService(healthRepo)

	authSvc := service.NewAuthService(service.AuthDeps{
		Users:       repository.NewUserRepo(db),
		Tokens:      repository.NewTokenRepo(rdb),
		Cipher:      cipher,
		JWT:         jwtManager,
		Log:         log,
		PhonePepper: cfg.Security.PhonePepper,
		BcryptCost:  cfg.Security.BcryptCost,
		RefreshTTL:  cfg.Security.RefreshTokenTTL,
	})

	cache := repository.NewCache(rdb, log)
	catalogSvc := service.NewCatalogService(service.CatalogDeps{
		Departments: repository.NewDepartmentRepo(db),
		Doctors:     repository.NewDoctorRepo(db, cache),
		Schedules:   repository.NewScheduleRepo(db),
	})

	appointmentRepo := repository.NewAppointmentRepo(db)
	doctorRepo := repository.NewDoctorRepo(db, cache)
	appointmentSvc := service.NewAppointmentService(service.AppointmentDeps{
		Appointments:       appointmentRepo,
		DoctorAppointments: appointmentRepo,
		Doctors:            doctorRepo,
		Janitor:            appointmentRepo,
		Schedules:          repository.NewScheduleRepo(db),
		Stock:              repository.NewStockRepo(rdb),
		Idempotency:        repository.NewIdempotencyRepo(rdb),
		Log:                log,
	})

	summaryQueueRepo := repository.NewSummaryQueueRepo(rdb)
	preconsultSvc := service.NewPreconsultService(service.PreconsultDeps{
		Appointments:  appointmentRepo,
		Conversation:  repository.NewConversationRepo(rdb),
		Reports:       repository.NewAIReportRepo(db),
		SummaryQueue:  summaryQueueRepo,
		Doctors:       doctorRepo,
		Client:        llmClient,
		Validator:     summaryValidator,
		Log:           log,
		SystemPrompt:  systemPrompt,
		SummaryPrompt: summaryPrompt,
		PromptVersion: prompts.PreconsultLatest,
		Timeout:       cfg.LLM.Timeout,
	})

	auditSvc := service.NewAuditService(service.AuditDeps{
		Logs: repository.NewAuditRepo(db),
	})

	// 病历与处方
	recordSvc := service.NewRecordService(service.RecordDeps{
		Records:      repository.NewMedicalRecordRepo(db),
		Appointments: appointmentRepo,
		Doctors:      repository.NewDoctorRepo(db, cache),
		Log:          log,
	})

	// HIS 同步
	syncLogRepo := repository.NewSyncLogRepo(db)
	syncSvc := service.NewSyncService(service.SyncDeps{
		Source:      hisSource{},
		Sync:        repository.NewSyncRepo(db),
		Logs:        syncLogRepo,
		Cipher:      cipher,
		PhonePepper: cfg.Security.PhonePepper,
		Log:         log,
	})

	// 审计写入器：异步批量落库，请求路径只做一次非阻塞入队
	auditWriter := audit.NewWriter(repository.NewAuditRepo(db), log)

	engine := router.New(router.Deps{
		Config:       cfg,
		Logger:       log,
		Redis:        rdb,
		Audit:        auditWriter,
		Health:       healthSvc,
		Auth:         authSvc,
		Catalog:      catalogSvc,
		Appointments: appointmentSvc,
		Preconsult:   preconsultSvc,
		AuditLogs:    auditSvc,
		Records:      recordSvc,
		Sync:         syncSvc,
		JWTManager:   jwtManager,
	})

	sched := scheduler.New(log, appointmentSvc, syncSvc, scheduler.Options{
		ExpireScan:     cfg.Scheduler.ExpireScan,
		StockReconcile: cfg.Scheduler.StockReconcile,
		HISSync:        cfg.Scheduler.HISSync,
	})
	summaryWorker := worker.NewSummaryWorker(summaryQueueRepo, preconsultSvc, log)

	return &App{
		Engine:        engine,
		Config:        cfg,
		sqlDB:         sqlDB,
		rdb:           rdb,
		auditWriter:   auditWriter,
		scheduler:     sched,
		summaryWorker: summaryWorker,
		log:           log,
	}, nil
}

// Start 启动后台任务。任何一项启动失败都会回滚已启动的部分。
func (a *App) Start() error {
	a.auditWriter.Start()

	if err := a.scheduler.Start(); err != nil {
		a.scheduler.Stop()
		a.auditWriter.Stop()
		return fmt.Errorf("start scheduler: %w", err)
	}

	if err := a.summaryWorker.Start(); err != nil {
		a.scheduler.Stop()
		a.auditWriter.Stop()
		return fmt.Errorf("start summary worker: %w", err)
	}
	return nil
}

// Stop 按依赖的逆序优雅关闭。
func (a *App) Stop() {
	a.summaryWorker.Stop()
	a.scheduler.Stop()
	// 审计写入器最后停：让前面停机过程中产生的记录也尽量落库
	a.auditWriter.Stop()

	if a.rdb != nil {
		_ = a.rdb.Close()
	}
	if a.sqlDB != nil {
		_ = a.sqlDB.Close()
	}
}

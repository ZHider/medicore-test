// Package main 是 MediCore 后端入口：加载配置、初始化依赖、启动 HTTP 服务并优雅关闭。
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/medicore/backend/internal/config"
	"github.com/medicore/backend/internal/jwt"
	"github.com/medicore/backend/internal/llm"
	"github.com/medicore/backend/internal/logger"
	"github.com/medicore/backend/internal/repository"
	"github.com/medicore/backend/internal/router"
	"github.com/medicore/backend/internal/scheduler"
	"github.com/medicore/backend/internal/secret"
	"github.com/medicore/backend/internal/service"
	"github.com/medicore/backend/internal/worker"
	"github.com/medicore/backend/prompts"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := os.Getenv("MEDICORE_CONFIG")
	if cfgPath == "" {
		cfgPath = "configs/config.yaml"
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	log, err := logger.New(cfg.Log.Level)
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	defer func() { _ = log.Sync() }()

	db, err := gorm.Open(mysql.Open(cfg.MySQL.DSN()), &gorm.Config{
		// 只记录慢查询与错误，避免业务日志被 SQL 淹没
		Logger: gormlogger.Default.LogMode(gormlogger.Warn),
		// 把驱动的方言错误转成 GORM 的通用错误（如唯一键冲突 -> ErrDuplicatedKey），
		// 这样 repository 层无需解析驱动错误文本。
		TranslateError: true,
	})
	if err != nil {
		return fmt.Errorf("connect mysql: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get sql.DB: %w", err)
	}
	sqlDB.SetMaxOpenConns(cfg.MySQL.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MySQL.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.MySQL.ConnMaxLifetime)
	defer func() { _ = sqlDB.Close() }()

	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
		PoolSize: cfg.Redis.PoolSize,
	})
	defer func() { _ = rdb.Close() }()

	// 加密组件在启动时构造：密钥格式错误必须立刻失败，而不是等第一个注册请求才暴露。
	cipher, err := secret.NewCipher(cfg.Security.AESKey)
	if err != nil {
		return fmt.Errorf("init aes cipher: %w", err)
	}
	jwtManager := jwt.NewManager(
		cfg.Security.JWTSecret,
		cfg.Security.JWTIssuer,
		cfg.Security.AccessTokenTTL,
	)

	// 依赖装配：handler → service → repository（依赖方向只能向下）
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

	// 缓存与目录查询
	cache := repository.NewCache(rdb, log)
	catalogSvc := service.NewCatalogService(service.CatalogDeps{
		Departments: repository.NewDepartmentRepo(db),
		Doctors:     repository.NewDoctorRepo(db, cache),
		Schedules:   repository.NewScheduleRepo(db),
	})

	// 预约与抢号
	appointmentRepo := repository.NewAppointmentRepo(db)
	appointmentSvc := service.NewAppointmentService(service.AppointmentDeps{
		Appointments: appointmentRepo,
		Janitor:      appointmentRepo,
		Schedules:    repository.NewScheduleRepo(db),
		Stock:        repository.NewStockRepo(rdb),
		Idempotency:  repository.NewIdempotencyRepo(rdb),
		Log:          log,
	})

	// AI 预问诊：提示词与输出 Schema 都编进二进制，版本号随结果入库以便追溯
	systemPrompt, err := prompts.PreconsultSystemPrompt(prompts.PreconsultLatest)
	if err != nil {
		return fmt.Errorf("加载预问诊提示词: %w", err)
	}
	summaryPrompt, err := prompts.PreconsultSummaryPrompt(prompts.PreconsultLatest)
	if err != nil {
		return fmt.Errorf("加载总结提示词: %w", err)
	}
	summarySchema, err := prompts.PreconsultSummarySchema(prompts.PreconsultLatest)
	if err != nil {
		return fmt.Errorf("加载总结 Schema: %w", err)
	}
	// Schema 在启动时编译：写错却跑到线上才发现，代价是整条降级路径静默生效
	summaryValidator, err := service.NewSummaryValidator(summarySchema)
	if err != nil {
		return fmt.Errorf("编译总结 Schema: %w", err)
	}
	// 反向自检：确认 Schema 真会拒绝内容，而不是被改成 {}
	if err := summaryValidator.SelfCheck(); err != nil {
		return fmt.Errorf("总结 Schema 自检失败: %w", err)
	}

	llmClient, err := llm.NewClient(cfg.LLM.Provider, llm.MockConfig{
		DeltaDelay:    time.Duration(cfg.LLM.Mock.DeltaDelayMS) * time.Millisecond,
		SummaryMode:   llm.SummaryMode(cfg.LLM.Mock.SummaryMode),
		StreamErrorAt: cfg.LLM.Mock.StreamErrorAt,
	}, cfg.LLM.Timeout)
	if err != nil {
		return fmt.Errorf("初始化 LLM 客户端: %w", err)
	}

	summaryQueueRepo := repository.NewSummaryQueueRepo(rdb)

	preconsultSvc := service.NewPreconsultService(service.PreconsultDeps{
		Appointments:  appointmentRepo,
		Conversation:  repository.NewConversationRepo(rdb),
		Reports:       repository.NewAIReportRepo(db),
		SummaryQueue:  summaryQueueRepo,
		Client:        llmClient,
		Validator:     summaryValidator,
		Log:           log,
		SystemPrompt:  systemPrompt,
		SummaryPrompt: summaryPrompt,
		PromptVersion: prompts.PreconsultLatest,
		Timeout:       cfg.LLM.Timeout,
	})

	engine := router.New(router.Deps{
		Config:       cfg,
		Logger:       log,
		Redis:        rdb,
		Health:       healthSvc,
		Auth:         authSvc,
		Catalog:      catalogSvc,
		Appointments: appointmentSvc,
		Preconsult:   preconsultSvc,
		JWTManager:   jwtManager,
	})

	// 定时任务：释放超时未确认的预约
	sched := scheduler.New(log, appointmentSvc, scheduler.Options{
		ExpireScan:     cfg.Scheduler.ExpireScan,
		StockReconcile: cfg.Scheduler.StockReconcile,
	})
	if err := sched.Start(); err != nil {
		return fmt.Errorf("start scheduler: %w", err)
	}
	defer sched.Stop()

	// 预问诊总结 worker：从 Redis Stream 消费任务。
	// 与请求生命周期解耦，用户提交后关掉页面也不会丢任务。
	summaryWorker := worker.NewSummaryWorker(summaryQueueRepo, preconsultSvc, log)
	if err := summaryWorker.Start(); err != nil {
		return fmt.Errorf("start summary worker: %w", err)
	}
	defer summaryWorker.Stop()

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.App.Port),
		Handler:           engine,
		ReadHeaderTimeout: 10 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Info("server starting",
			zap.String("addr", srv.Addr),
			zap.String("env", cfg.App.Env),
			zap.String("app", cfg.App.Name),
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		return fmt.Errorf("listen: %w", err)
	case sig := <-quit:
		log.Info("shutdown signal received", zap.String("signal", sig.String()))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	log.Info("server stopped")
	return nil
}

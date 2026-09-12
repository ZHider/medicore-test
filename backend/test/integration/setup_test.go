//go:build integration

// Package integration 是 HTTP 层的集成测试。
//
// 与单元测试的分工：
//   - 单元测试（service/worker/audit 等包内）用内存实现验证业务分支；
//   - 本包用**真实的 app.Build 装配** + 真实 MySQL/Redis，验证
//     路由、中间件顺序、参数绑定、状态码、响应信封这些只有跑起来才能确认的东西。
//
// handler 层原本覆盖率 0%，正是靠这一层补上。
//
// 运行方式：
//
//	nub run backend:test:integration
//
// 需要 dev compose 的 mysql/redis 已启动（nub run deps:up）。
// 测试会创建一个独立的 medicore_test 库并使用 Redis DB 1，
// 不会碰开发库的数据。
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/medicore/backend/internal/app"
	"github.com/medicore/backend/internal/config"
	"github.com/medicore/backend/internal/jwt"
)

const (
	testDatabase = "medicore_test"
	testRedisDB  = 1
)

var (
	testApp    *app.App
	testServer *httptest.Server
	testConfig *config.Config
	testJWT    *jwt.Manager
)

func TestMain(m *testing.M) {
	// gin 的调试输出会淹没测试日志
	gin.SetMode(gin.TestMode)

	code, err := setup()
	if err != nil {
		fmt.Fprintf(os.Stderr, "集成测试环境准备失败: %v\n", err)
		os.Exit(1)
	}
	if code != 0 {
		os.Exit(code)
	}

	rc := m.Run()

	teardown()
	os.Exit(rc)
}

func setup() (int, error) {
	root, err := repoRoot()
	if err != nil {
		return 1, err
	}

	cfg, err := config.Load(filepath.Join(root, "backend", "configs", "config.yaml"))
	if err != nil {
		return 1, fmt.Errorf("加载配置: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return 1, fmt.Errorf("配置无效（是否已执行 go run ./cmd/genlocal？）: %w", err)
	}

	if err := prepareTestDatabase(cfg, root); err != nil {
		return 1, err
	}

	// 切到测试库与独立 Redis DB
	cfg.MySQL.Database = testDatabase
	cfg.Redis.DB = testRedisDB
	// 关掉定时任务对测试的干扰：让测试自己控制何时触发
	cfg.Scheduler.ExpireScan = "@every 24h"
	cfg.Scheduler.StockReconcile = "@every 24h"

	// 先清空 Redis 测试库，再启动应用：
	// 反过来的话会把 EnsureGroup 刚创建的消费者组一起删掉，
	// worker 从此收不到任何任务（这个顺序问题正是集成测试第一次跑就抓出来的）。
	rdb := redis.NewClient(&redis.Options{
		Addr: cfg.Redis.Addr, Password: cfg.Redis.Password, DB: testRedisDB,
	})
	_ = rdb.FlushDB(context.Background()).Err()
	_ = rdb.Close()

	application, err := app.Build(cfg, zap.NewNop())
	if err != nil {
		return 1, fmt.Errorf("装配应用: %w", err)
	}
	if err := application.Start(); err != nil {
		return 1, fmt.Errorf("启动应用: %w", err)
	}

	testApp = application
	testServer = httptest.NewServer(application.Engine)
	testConfig = cfg
	testJWT = jwt.NewManager(cfg.Security.JWTSecret, cfg.Security.JWTIssuer, cfg.Security.AccessTokenTTL)

	return 0, nil
}

func teardown() {
	if testServer != nil {
		testServer.Close()
	}
	if testApp != nil {
		testApp.Stop()
	}
}

// prepareTestDatabase 创建独立的测试库并执行 docs/schema.sql。
func prepareTestDatabase(cfg *config.Config, root string) error {
	dsn := cfg.MySQL.DSN()
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		return fmt.Errorf("连接 MySQL 失败。请确认：\n"+
			"  1. 依赖已启动：nub run deps:up\n"+
			"  2. 已设置数据库口令：MEDICORE_MYSQL_PASSWORD=medicore123（与 deploy/compose.dev.yml 一致）\n"+
			"  原因: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer func() { _ = sqlDB.Close() }()

	if err := db.Exec("CREATE DATABASE IF NOT EXISTS " + testDatabase +
		" CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci").Error; err != nil {
		return fmt.Errorf("创建测试库: %w", err)
	}
	if err := db.Exec("USE " + testDatabase).Error; err != nil {
		return fmt.Errorf("切换测试库: %w", err)
	}
	// 每次从零开始，保证测试之间互不影响
	if err := db.Exec("DROP DATABASE " + testDatabase).Error; err != nil {
		return fmt.Errorf("清理测试库: %w", err)
	}
	if err := db.Exec("CREATE DATABASE " + testDatabase +
		" CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci").Error; err != nil {
		return fmt.Errorf("重建测试库: %w", err)
	}

	schema, err := os.ReadFile(filepath.Join(root, "docs", "schema.sql"))
	if err != nil {
		return fmt.Errorf("读取 schema.sql: %w", err)
	}
	if err := db.Exec("USE " + testDatabase).Error; err != nil {
		return err
	}
	// 逐条执行：schema.sql 里有多条语句，驱动默认不允许一次发送多条
	for _, stmt := range splitSQL(string(schema)) {
		if err := db.Exec(stmt).Error; err != nil {
			return fmt.Errorf("执行 schema 语句失败:\n%s\n%w", truncate(stmt, 200), err)
		}
	}
	return nil
}

// splitSQL 按分号切分 SQL，跳过注释行。
// 够用即可：schema.sql 是我们自己维护的，不含存储过程等复杂语法。
func splitSQL(raw string) []string {
	var (
		out []string
		cur strings.Builder
	)
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") || trimmed == "" {
			continue
		}
		cur.WriteString(line)
		cur.WriteString("\n")
		if strings.HasSuffix(trimmed, ";") {
			stmt := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(cur.String()), ";"))
			if stmt != "" {
				out = append(out, stmt)
			}
			cur.Reset()
		}
	}
	return out
}

// repoRoot 从当前测试文件向上找到仓库根（以 spec.md 为标志）。
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "spec.md")); err == nil {
			return dir, nil
		}
		dir = filepath.Dir(dir)
	}
	return "", fmt.Errorf("未能从 %s 向上找到仓库根（缺少 spec.md）", dir)
}

// ---------------------------------------------------------------------------
// HTTP 辅助
// ---------------------------------------------------------------------------

// response 是统一响应信封。
type response struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
	TraceID string          `json:"trace_id"`
}

type request struct {
	method  string
	path    string
	token   string
	body    any
	headers map[string]string
}

func call(t *testing.T, r request) (*http.Response, response) {
	t.Helper()

	var bodyReader io.Reader
	if r.body != nil {
		raw, err := json.Marshal(r.body)
		require.NoError(t, err)
		bodyReader = strings.NewReader(string(raw))
	}

	req, err := http.NewRequest(r.method, testServer.URL+r.path, bodyReader)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	for k, v := range r.headers {
		req.Header.Set(k, v)
	}

	resp, err := testServer.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var env response
	if len(raw) > 0 && strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		_ = json.Unmarshal(raw, &env)
	} else {
		env.Message = string(raw)
	}
	return resp, env
}

// decode 把 data 字段反序列化到目标结构。
func decode[T any](t *testing.T, env response) T {
	t.Helper()
	var out T
	require.NoError(t, json.Unmarshal(env.Data, &out), "data 字段解析失败: %s", string(env.Data))
	return out
}

// ---------------------------------------------------------------------------
// 测试数据辅助
// ---------------------------------------------------------------------------

var phoneSeq int

// registerPatient 注册一个患者并返回其访问令牌。
//
// 刻意**不走登录接口**签发令牌，而是本地签发：
// 登录有 5 次/分钟/用户的限流（spec §9.3），所有测试请求都来自同一个 IP，
// 逐个登录会让测试在跑到第五个用例时因限流而失败——
// 变成一个与被测行为无关的脆弱依赖。
// 登录本身的行为由 TestAuth_FullFlow 专门覆盖。
func registerPatient(t *testing.T) (token string, userID uint64) {
	t.Helper()

	phoneSeq++
	phone := fmt.Sprintf("139%08d", phoneSeq)

	_, env := call(t, request{
		method: "POST", path: "/api/v1/auth/register",
		body: map[string]any{"phone": phone, "name": "集成测试患者", "password": "Passw0rd!"},
	})
	require.Equal(t, 0, env.Code, "注册失败: %s", env.Message)

	profile := decode[struct {
		ID uint64 `json:"id"`
	}](t, env)
	require.NotZero(t, profile.ID)

	return mintToken(t, profile.ID, "patient"), profile.ID
}

// mintToken 用与服务端相同的密钥本地签发访问令牌。
func mintToken(t *testing.T, userID uint64, role string) string {
	t.Helper()

	token, err := testJWT.IssueAccess(userID, role)
	require.NoError(t, err)
	return token
}

// clearRateLimits 清掉限流键，供需要反复登录的用例使用。
func clearRateLimits(t *testing.T) {
	t.Helper()

	rdb := redis.NewClient(&redis.Options{
		Addr: testConfig.Redis.Addr, Password: testConfig.Redis.Password, DB: testRedisDB,
	})
	defer func() { _ = rdb.Close() }()

	ctx := context.Background()
	keys, err := rdb.Keys(ctx, "rl:*").Result()
	require.NoError(t, err)
	if len(keys) > 0 {
		require.NoError(t, rdb.Del(ctx, keys...).Err())
	}
}

// seedDoctorSchedule 直接写库插入一个医生与排班，返回排班 ID。
//
// 走 SQL 而不是 API：教师的写入路径尚未实现（属于 M6），
// 但查询与抢号路径需要数据。
func seedDoctorSchedule(t *testing.T, total int) uint64 {
	t.Helper()

	db, err := gorm.Open(mysql.Open(testDSN()), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer func() { _ = sqlDB.Close() }()

	deptID := insertAndLastID(t, db,
		`INSERT INTO departments (code, name, sort_order, status) VALUES (?, ?, ?, 1)`,
		fmt.Sprintf("IT%d", time.Now().UnixNano()%100000), "集成测试科室", 1)

	userID := insertAndLastID(t, db,
		`INSERT INTO users (role, name, phone_enc, phone_hash, password_hash, status)
		 VALUES ('doctor', ?, ?, ?, ?, 1)`,
		"集成测试医生", []byte{0x01}, fmt.Sprintf("%064d", time.Now().UnixNano()%1e18), "x")

	doctorID := insertAndLastID(t, db,
		`INSERT INTO doctors (user_id, title, consult_fee, status) VALUES (?, ?, '10.00', 1)`,
		userID, "主治医师")

	require.NoError(t, db.Exec(
		`INSERT INTO doctor_departments (doctor_id, department_id, is_primary) VALUES (?, ?, 1)`,
		doctorID, deptID).Error)

	scheduleID := insertAndLastID(t, db,
		`INSERT INTO schedules (doctor_id, department_id, sched_date, time_slot, total, remaining, status)
		 VALUES (?, ?, CURDATE(), 'am', ?, ?, 'open')`,
		doctorID, deptID, total, total)

	return scheduleID
}

func insertAndLastID(t *testing.T, db *gorm.DB, query string, args ...any) uint64 {
	t.Helper()

	require.NoError(t, db.Exec(query, args...).Error)
	var id uint64
	require.NoError(t, db.Raw("SELECT LAST_INSERT_ID()").Scan(&id).Error)
	require.NotZero(t, id)
	return id
}

func testDSN() string {
	cfg, err := config.Load(filepath.Join(mustRepoRoot(), "backend", "configs", "config.yaml"))
	if err != nil {
		panic(err)
	}
	cfg.MySQL.Database = testDatabase
	return cfg.MySQL.DSN()
}

func mustRepoRoot() string {
	root, err := repoRoot()
	if err != nil {
		panic(err)
	}
	return root
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

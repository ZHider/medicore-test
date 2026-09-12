// Command loadtest 对抢号接口做并发压测，验证「零超卖」。
//
// 为什么不直接用 hey/wrk：这两个工具对每次请求发送同一个 token，
// 而同一用户对同一号源只能持有一个进行中预约（uk_active 约束），
// 重复请求只会得到冲突，根本触达不到并发扣减路径。
// 本工具先批量准备患者账号，再为每个账号签发自有权杖，最后并发抢同一个号源。
//
//	go run ./cmd/loadtest -users 200 -concurrency 100 -schedule 1
//
// 结束后会核对三方数据是否一致：
//
//	MySQL remaining、Redis 号源键、以及"成功次数"三者必须自洽。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/medicore/backend/internal/config"
	"github.com/medicore/backend/internal/jwt"
	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/repository"
	"github.com/medicore/backend/internal/secret"
)

type options struct {
	baseURL     string
	users       int
	concurrency int
	scheduleID  uint64
}

func main() {
	opts := options{}
	flag.StringVar(&opts.baseURL, "base", "http://127.0.0.1:8080", "服务地址")
	flag.IntVar(&opts.users, "users", 200, "并发患者数量")
	flag.IntVar(&opts.concurrency, "concurrency", 100, "并发度")
	flag.Uint64Var(&opts.scheduleID, "schedule", 0, "目标排班 ID（0 表示自动选第一个有余号的未来排班）")
	flag.Parse()

	if err := run(opts); err != nil {
		fmt.Fprintf(os.Stderr, "压测失败: %v\n", err)
		os.Exit(1)
	}
}

func run(opts options) error {
	cfgPath := os.Getenv("MEDICORE_CONFIG")
	if cfgPath == "" {
		cfgPath = "configs/config.yaml"
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("加载配置: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("配置无效: %w", err)
	}

	db, err := gorm.Open(mysql.Open(cfg.MySQL.DSN()), &gorm.Config{TranslateError: true})
	if err != nil {
		return fmt.Errorf("连接数据库: %w", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: cfg.Redis.Addr, Password: cfg.Redis.Password, DB: cfg.Redis.DB})
	defer func() { _ = rdb.Close() }()

	cipher, err := secret.NewCipher(cfg.Security.AESKey)
	if err != nil {
		return fmt.Errorf("初始化加密器: %w", err)
	}
	tokens := jwt.NewManager(cfg.Security.JWTSecret, cfg.Security.JWTIssuer, cfg.Security.AccessTokenTTL)

	target, err := pickSchedule(db, opts.scheduleID)
	if err != nil {
		return err
	}
	fmt.Printf("目标排班 #%d：医生 %d 科室 %d %s %s，总号源 %d，当前余号 %d\n",
		target.ID, target.DoctorID, target.DepartmentID,
		target.SchedDate.Format(time.DateOnly), target.TimeSlot, target.Total, target.Remaining)

	userIDs, err := ensurePatients(db, cipher, cfg.Security.PhonePepper, opts.users)
	if err != nil {
		return err
	}

	authTokens := make([]string, 0, len(userIDs))
	for _, id := range userIDs {
		token, err := tokens.IssueAccess(id, string(model.RolePatient))
		if err != nil {
			return fmt.Errorf("签发压测令牌: %w", err)
		}
		authTokens = append(authTokens, token)
	}

	// 清掉可能残留的号源键，确保从数据库真实值重新预热
	if err := rdb.Del(context.Background(), repository.StockKey(target.ID)).Err(); err != nil {
		return fmt.Errorf("清理号源缓存: %w", err)
	}

	before := readRemaining(db, target.ID)
	fmt.Printf("压测开始：%d 个患者并发抢 %d 个号源（并发度 %d）\n", len(authTokens), before, opts.concurrency)

	result := fire(opts, target.ID, authTokens)

	after := readRemaining(db, target.ID)
	redisStock, redisExists, err := repository.NewStockRepo(rdb).Current(context.Background(), target.ID)
	if err != nil {
		return fmt.Errorf("读取 Redis 号源: %w", err)
	}

	fmt.Println()
	fmt.Println("=== 结果 ===")
	fmt.Printf("成功抢到      : %d\n", result.success)
	fmt.Printf("号源不足      : %d\n", result.noStock)
	fmt.Printf("重复/冲突     : %d\n", result.conflict)
	fmt.Printf("限流          : %d\n", result.rateLimited)
	fmt.Printf("其他错误      : %d\n", result.other)
	if len(result.samples) > 0 {
		fmt.Printf("错误样本      : %v\n", result.samples)
	}
	fmt.Printf("耗时          : %s\n", result.elapsed.Round(time.Millisecond))
	fmt.Println()
	fmt.Printf("MySQL 余号    : %d -> %d\n", before, after)
	fmt.Printf("Redis 号源键  : 存在=%v 值=%d\n", redisExists, redisStock)

	fmt.Println()
	fmt.Println("=== 一致性校验 ===")
	ok := true

	if after < 0 {
		fmt.Printf("❌ 出现负号源：%d\n", after)
		ok = false
	}
	if result.success != int64(before-after) {
		fmt.Printf("❌ 成功次数(%d) 与 MySQL 号源消耗(%d) 不一致\n", result.success, before-after)
		ok = false
	}
	if after < 0 || after > target.Total {
		fmt.Printf("❌ MySQL 余号越界：%d\n", after)
		ok = false
	}
	if redisExists && redisStock != after {
		fmt.Printf("❌ Redis(%d) 与 MySQL(%d) 不一致\n", redisStock, after)
		ok = false
	}
	if ok {
		fmt.Println("✅ 无超卖：成功次数、MySQL 余号、Redis 号源三者自洽")
	} else {
		return fmt.Errorf("一致性校验未通过")
	}
	return nil
}

type tally struct {
	success     int64
	noStock     int64
	conflict    int64
	rateLimited int64
	other       int64
	elapsed     time.Duration

	mu      sync.Mutex
	samples []string
}

func (t *tally) addSample(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.samples) < 5 {
		t.samples = append(t.samples, s)
	}
}

func fire(opts options, scheduleID uint64, tokens []string) *tally {
	var (
		success, noStock, conflict, rateLimited, other atomic.Int64
		wg                                             sync.WaitGroup
	)
	t := &tally{}
	sem := make(chan struct{}, opts.concurrency)
	client := &http.Client{Timeout: 30 * time.Second}

	start := time.Now()
	for i, token := range tokens {
		wg.Add(1)
		sem <- struct{}{}

		go func(i int, token string) {
			defer wg.Done()
			defer func() { <-sem }()

			code, body := grab(client, opts.baseURL, scheduleID, token, fmt.Sprintf("load-%d-%d", scheduleID, i))
			switch code {
			case 0:
				success.Add(1)
			case 40002:
				noStock.Add(1)
			case 40004:
				conflict.Add(1)
			case 42901:
				rateLimited.Add(1)
			default:
				other.Add(1)
				t.addSample(fmt.Sprintf("code=%d body=%s", code, truncate(body, 160)))
			}
		}(i, token)
	}
	wg.Wait()
	t.elapsed = time.Since(start)

	t.success = success.Load()
	t.noStock = noStock.Load()
	t.conflict = conflict.Load()
	t.rateLimited = rateLimited.Load()
	t.other = other.Load()
	return t
}

// grab 发起一次抢号请求，返回业务错误码与原始响应体。
func grab(client *http.Client, baseURL string, scheduleID uint64, token, idemKey string) (int, string) {
	payload, _ := json.Marshal(map[string]any{
		"schedule_id":     scheduleID,
		"idempotency_key": idemKey,
	})

	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/v1/appointments", bytes.NewReader(payload))
	if err != nil {
		return -1, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return -1, err.Error()
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	var envelope struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return -1, string(raw)
	}
	return envelope.Code, envelope.Message
}

// pickSchedule 选择压测目标排班。
func pickSchedule(db *gorm.DB, id uint64) (*model.Schedule, error) {
	var s model.Schedule

	q := db.Where("status = ? AND remaining > 0", model.ScheduleStatusOpen)
	if id > 0 {
		q = q.Where("id = ?", id)
	} else {
		q = q.Where("sched_date >= ?", time.Now().Format(time.DateOnly)).Order("id ASC")
	}

	if err := q.First(&s).Error; err != nil {
		return nil, fmt.Errorf("选择目标排班（是否已执行 go run ./cmd/seed？）: %w", err)
	}
	return &s, nil
}

// ensurePatients 批量准备压测用患者账号。
//
// 口令散列只算一次再复用：压测关心的是抢号并发，
// 而不是 bcrypt 的吞吐——每个账号单独算会把准备阶段拖到几分钟。
func ensurePatients(db *gorm.DB, cipher *secret.Cipher, pepper string, count int) ([]uint64, error) {
	hash, err := secret.HashPassword("LoadTest@123", secret.MinBcryptCost)
	if err != nil {
		return nil, fmt.Errorf("生成压测口令散列: %w", err)
	}

	ids := make([]uint64, 0, count)
	for i := 0; i < count; i++ {
		phone := fmt.Sprintf("137%08d", i+1)
		phoneEnc, err := cipher.Encrypt(phone)
		if err != nil {
			return nil, fmt.Errorf("加密压测手机号: %w", err)
		}

		user := model.User{
			Role:         model.RolePatient,
			Name:         fmt.Sprintf("压测患者%04d", i+1),
			PhoneEnc:     phoneEnc,
			PhoneHash:    secret.Digest(phone, pepper),
			PasswordHash: hash,
			Status:       model.StatusActive,
		}
		if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&user).Error; err != nil {
			return nil, fmt.Errorf("写入压测账号: %w", err)
		}
		if user.ID == 0 {
			if err := db.Where("phone_hash = ?", user.PhoneHash).First(&user).Error; err != nil {
				return nil, fmt.Errorf("查询压测账号: %w", err)
			}
		}
		ids = append(ids, user.ID)
	}
	return ids, nil
}

func readRemaining(db *gorm.DB, scheduleID uint64) int {
	var remaining int
	db.Model(&model.Schedule{}).Where("id = ?", scheduleID).Select("remaining").Scan(&remaining)
	return remaining
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

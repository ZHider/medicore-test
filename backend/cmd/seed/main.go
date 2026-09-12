// Command seed 向本地数据库灌入演示数据（医生档案与排班）。
//
// 幂等：全部写入都用"冲突即忽略"，可反复执行而不会产生重复数据。
// 密码散列与手机号密文使用当前配置中的密钥，因此必须先执行 cmd/genlocal。
//
//	go run ./cmd/seed
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/medicore/backend/internal/config"
	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/repository"
	"github.com/medicore/backend/internal/secret"
)

// 演示医生的分配：科室编码 -> 该科室的医生数量。
var doctorPlan = []struct {
	departmentCode string
	count          int
	titles         []string
}{
	{"IM", 5, []string{"主任医师", "副主任医师", "主治医师"}},
	{"SURG", 3, []string{"主任医师", "主治医师"}},
	{"PED", 2, []string{"副主任医师", "主治医师"}},
	{"OBGY", 2, []string{"主任医师", "主治医师"}},
}

const (
	scheduleDaysAhead = 7
	seatsPerSchedule  = 20
	demoPassword      = "Doctor@123"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "seed 失败: %v\n", err)
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
		return fmt.Errorf("加载配置: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("配置无效（是否已执行 go run ./cmd/genlocal？）: %w", err)
	}

	db, err := gorm.Open(mysql.Open(cfg.MySQL.DSN()), &gorm.Config{TranslateError: true})
	if err != nil {
		return fmt.Errorf("连接数据库: %w", err)
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})
	defer func() { _ = rdb.Close() }()

	cipher, err := secret.NewCipher(cfg.Security.AESKey)
	if err != nil {
		return fmt.Errorf("初始化加密器: %w", err)
	}

	departments, err := loadDepartments(db)
	if err != nil {
		return err
	}
	if len(departments) == 0 {
		return fmt.Errorf("科室表为空：请先确认 docs/schema.sql 的初始数据已执行")
	}

	admin, err := ensureAdmin(db, cipher, cfg.Security.PhonePepper)
	if err != nil {
		return err
	}

	doctors, err := seedDoctors(db, cipher, cfg.Security.PhonePepper, departments)
	if err != nil {
		return err
	}

	scheduleCount, err := seedSchedules(db, doctors)
	if err != nil {
		return err
	}

	deptIDs := make([]uint64, 0, len(departments))
	for _, d := range departments {
		deptIDs = append(deptIDs, d.ID)
	}
	if err := clearDoctorCache(rdb, deptIDs); err != nil {
		return err
	}

	fmt.Printf("完成：科室 %d 个，医生 %d 名，排班 %d 条（新增）\n",
		len(departments), len(doctors), scheduleCount)
	fmt.Printf("管理员账号：手机号 %s\n", admin.PhoneHash[:8]+"...")
	return nil
}

// 管理员账号的演示参数。
const (
	adminPhone       = "13800000000"
	defaultAdminPass = "Admin@12345"
	adminPasswordEnv = "MEDICORE_ADMIN_PASSWORD"
)

// ensureAdmin 创建管理员账号。
//
// 密码优先从环境变量读取；缺省时用演示口令并**显式打印警告**：
// 管理端能查审计日志，用一个众所周知的默认口令是真实风险，不能悄悄带过。
func ensureAdmin(db *gorm.DB, cipher *secret.Cipher, pepper string) (*model.User, error) {
	password := os.Getenv(adminPasswordEnv)
	if password == "" {
		password = defaultAdminPass
		fmt.Printf("警告：%s 未设置，管理员使用默认口令（仅限本地演示，切勿用于真实环境）\n",
			adminPasswordEnv)
	}

	phoneEnc, err := cipher.Encrypt(adminPhone)
	if err != nil {
		return nil, fmt.Errorf("加密管理员手机号: %w", err)
	}
	passwordHash, err := secret.HashPassword(password, secret.MinBcryptCost)
	if err != nil {
		return nil, fmt.Errorf("生成管理员口令散列: %w", err)
	}

	admin := model.User{
		Role:         model.RoleAdmin,
		Name:         "系统管理员",
		PhoneEnc:     phoneEnc,
		PhoneHash:    secret.Digest(adminPhone, pepper),
		PasswordHash: passwordHash,
		Status:       model.StatusActive,
	}

	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&admin).Error; err != nil {
		return nil, fmt.Errorf("写入管理员账号: %w", err)
	}
	if admin.ID == 0 {
		if err := db.Where("phone_hash = ?", admin.PhoneHash).First(&admin).Error; err != nil {
			return nil, fmt.Errorf("查询已存在的管理员账号: %w", err)
		}
	}
	return &admin, nil
}

// loadDepartments 读取已由 schema.sql 初始化的科室。
func loadDepartments(db *gorm.DB) (map[string]model.Department, error) {
	var rows []model.Department
	if err := db.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("查询科室: %w", err)
	}

	byCode := make(map[string]model.Department, len(rows))
	for _, d := range rows {
		byCode[d.Code] = d
	}
	return byCode, nil
}

// seedDoctors 创建医生账号、医生档案与科室关联（多对多）。
func seedDoctors(
	db *gorm.DB,
	cipher *secret.Cipher,
	pepper string,
	departments map[string]model.Department,
) ([]model.Doctor, error) {
	var doctors []model.Doctor
	seq := 0

	for _, plan := range doctorPlan {
		dept, ok := departments[plan.departmentCode]
		if !ok {
			return nil, fmt.Errorf("科室 %s 不存在", plan.departmentCode)
		}

		for i := 0; i < plan.count; i++ {
			seq++
			externalID := fmt.Sprintf("seed-doc-%03d", seq)
			name := fmt.Sprintf("%s医生%02d", dept.Name, i+1)
			phone := fmt.Sprintf("139%08d", seq)

			user, err := ensureDoctorUser(db, cipher, pepper, name, phone)
			if err != nil {
				return nil, err
			}

			doctor, err := ensureDoctor(db, user.ID, externalID, plan.titles[i%len(plan.titles)])
			if err != nil {
				return nil, err
			}

			if err := ensureDoctorDepartment(db, doctor.ID, dept.ID, true); err != nil {
				return nil, err
			}
			doctors = append(doctors, *doctor)
		}
	}
	return doctors, nil
}

func ensureDoctorUser(
	db *gorm.DB,
	cipher *secret.Cipher,
	pepper, name, phone string,
) (*model.User, error) {
	phoneEnc, err := cipher.Encrypt(phone)
	if err != nil {
		return nil, fmt.Errorf("加密手机号: %w", err)
	}
	passwordHash, err := secret.HashPassword(demoPassword, secret.MinBcryptCost)
	if err != nil {
		return nil, fmt.Errorf("生成口令散列: %w", err)
	}

	user := model.User{
		Role:         model.RoleDoctor,
		Name:         name,
		PhoneEnc:     phoneEnc,
		PhoneHash:    secret.Digest(phone, pepper),
		PasswordHash: passwordHash,
		Status:       model.StatusActive,
	}

	// phone_hash 上有唯一索引：已存在则忽略，保证可重复执行。
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&user).Error; err != nil {
		return nil, fmt.Errorf("写入医生账号 %s: %w", phone, err)
	}

	// DoNothing 时不会回填 ID，需要按 phone_hash 重新查一次。
	if user.ID == 0 {
		if err := db.Where("phone_hash = ?", user.PhoneHash).First(&user).Error; err != nil {
			return nil, fmt.Errorf("查询已存在的医生账号 %s: %w", phone, err)
		}
	}
	return &user, nil
}

func ensureDoctor(db *gorm.DB, userID uint64, externalID, title string) (*model.Doctor, error) {
	doctor := model.Doctor{
		UserID:     userID,
		ExternalID: &externalID,
		Title:      title,
		ConsultFee: "15.00",
		Status:     model.StatusActive,
	}

	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&doctor).Error; err != nil {
		return nil, fmt.Errorf("写入医生档案 %s: %w", externalID, err)
	}
	if doctor.ID == 0 {
		if err := db.Where("user_id = ?", userID).First(&doctor).Error; err != nil {
			return nil, fmt.Errorf("查询已存在的医生档案 %s: %w", externalID, err)
		}
	}
	return &doctor, nil
}

func ensureDoctorDepartment(db *gorm.DB, doctorID, departmentID uint64, primary bool) error {
	rel := model.DoctorDepartment{
		DoctorID:     doctorID,
		DepartmentID: departmentID,
		IsPrimary:    primary,
	}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&rel).Error; err != nil {
		return fmt.Errorf("写入医生科室关联: %w", err)
	}
	return nil
}

// seedSchedules 为每位医生生成"今天起 7 天 × 上午/下午"的排班。
func seedSchedules(db *gorm.DB, doctors []model.Doctor) (int, error) {
	// 必须用本地时区的当天零点，不能 time.Now().Truncate(24*time.Hour)：
	// 后者截的是 UTC 零点，在 UTC+8 下本地时间 08:00 之前算出来的日期会**早一天**，
	// 于是刚灌入的演示排班从"昨天"开始，看起来像是数据错了。
	// 项目其余部分（DSN 的 loc=Local、排班的 sched_date）统一用本地时间。
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	created := 0

	for _, doctor := range doctors {
		// 排班必须绑定科室；这里取该医生的全部科室关联，为每个科室各排一份。
		var rels []model.DoctorDepartment
		if err := db.Where("doctor_id = ?", doctor.ID).Find(&rels).Error; err != nil {
			return 0, fmt.Errorf("查询医生科室关联: %w", err)
		}

		for _, rel := range rels {
			for day := 0; day < scheduleDaysAhead; day++ {
				date := today.AddDate(0, 0, day)
				for _, slot := range []string{model.TimeSlotAM, model.TimeSlotPM} {
					s := model.Schedule{
						DoctorID:     doctor.ID,
						DepartmentID: rel.DepartmentID,
						SchedDate:    date,
						TimeSlot:     slot,
						Total:        seatsPerSchedule,
						Remaining:    seatsPerSchedule,
						Status:       model.ScheduleStatusOpen,
					}
					res := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&s)
					if res.Error != nil {
						return 0, fmt.Errorf("写入排班: %w", res.Error)
					}
					created += int(res.RowsAffected)
				}
			}
		}
	}
	return created, nil
}

// clearDoctorCache 清空科室医生列表缓存。
//
// 必须做：种子数据写入后若不清理，之前查询缓存下的空列表会一直掩盖新数据，
// 直到 5 分钟 TTL 过期——这期间的接口表现会让人以为数据没写进去。
func clearDoctorCache(rdb *redis.Client, departmentIDs []uint64) error {
	keys := repository.DepartmentDoctorsCacheKeys(departmentIDs...)
	if err := rdb.Del(context.Background(), keys...).Err(); err != nil {
		return fmt.Errorf("清理科室医生缓存: %w", err)
	}
	return nil
}

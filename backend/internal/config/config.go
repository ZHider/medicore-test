// Package config 负责配置加载：YAML 文件 + 环境变量覆盖（spec §9.3：敏感配置走环境变量）。
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config 是应用完整配置。
type Config struct {
	App       App       `mapstructure:"app"`
	Log       Log       `mapstructure:"log"`
	MySQL     MySQL     `mapstructure:"mysql"`
	Redis     Redis     `mapstructure:"redis"`
	Security  Security  `mapstructure:"security"`
	Scheduler Scheduler `mapstructure:"scheduler"`
}

// Scheduler 是定时任务配置。用 cron 表达式而非固定常量，
// 便于按环境调整（例如压测时把对账周期缩短，或临时关闭某项任务）。
type Scheduler struct {
	// ExpireScan 超时订单释放周期，空串表示用默认值。
	ExpireScan string `mapstructure:"expire_scan"`
	// StockReconcile 号源对账周期，空串表示用默认值。
	StockReconcile string `mapstructure:"stock_reconcile"`
}

// Security 是认证与加密相关配置。
//
// 三个密钥项都必须由环境变量注入生产值：AESKey 泄露等于全部病历明文泄露，
// PhonePepper 泄露则可用彩虹表反推手机号，JWTSecret 泄露可直接伪造任意用户令牌。
type Security struct {
	AESKey          string        `mapstructure:"aes_key"` // 64 位十六进制 = 32 字节
	PhonePepper     string        `mapstructure:"phone_pepper"`
	JWTSecret       string        `mapstructure:"jwt_secret"`
	JWTIssuer       string        `mapstructure:"jwt_issuer"`
	AccessTokenTTL  time.Duration `mapstructure:"access_token_ttl"`
	RefreshTokenTTL time.Duration `mapstructure:"refresh_token_ttl"`
	BcryptCost      int           `mapstructure:"bcrypt_cost"`
}

// App 应用级配置。
type App struct {
	Name string `mapstructure:"name"`
	Env  string `mapstructure:"env"` // dev / prod
	Port int    `mapstructure:"port"`
}

// Log 日志配置。
type Log struct {
	Level string `mapstructure:"level"`
}

// MySQL 数据库配置。
type MySQL struct {
	Host            string        `mapstructure:"host"`
	Port            int           `mapstructure:"port"`
	User            string        `mapstructure:"user"`
	Password        string        `mapstructure:"password"`
	Database        string        `mapstructure:"database"`
	MaxOpenConns    int           `mapstructure:"max_open_conns"`
	MaxIdleConns    int           `mapstructure:"max_idle_conns"`
	ConnMaxLifetime time.Duration `mapstructure:"conn_max_lifetime"`
}

// DSN 组装 MySQL 连接串。
// parseTime=true 让 DATETIME 直接扫描进 time.Time；loc=Local 保证时区与写入端一致。
func (m MySQL) DSN() string {
	return fmt.Sprintf(
		"%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=true&loc=Local",
		m.User, m.Password, m.Host, m.Port, m.Database,
	)
}

// Redis 配置。
type Redis struct {
	Addr     string `mapstructure:"addr"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
	PoolSize int    `mapstructure:"pool_size"`
}

// Load 从指定文件加载配置，并允许环境变量覆盖。
//
// 注意：viper 的 AutomaticEnv 对嵌套键的 Unmarshal 支持不完整，
// 因此这里对需要覆盖的键显式调用 BindEnv（同时保持行为可预测）。
func Load(path string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetEnvPrefix("MEDICORE")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	v.SetDefault("app.name", "medicore")
	v.SetDefault("app.env", "dev")
	v.SetDefault("app.port", 8080)
	v.SetDefault("log.level", "info")
	v.SetDefault("mysql.host", "127.0.0.1")
	v.SetDefault("mysql.port", 3306)
	v.SetDefault("mysql.database", "medicore")
	v.SetDefault("mysql.max_open_conns", 50)
	v.SetDefault("mysql.max_idle_conns", 10)
	v.SetDefault("mysql.conn_max_lifetime", "1h")
	v.SetDefault("redis.addr", "127.0.0.1:6379")
	v.SetDefault("redis.pool_size", 20)
	v.SetDefault("security.jwt_issuer", "medicore")
	v.SetDefault("security.access_token_ttl", "2h")
	v.SetDefault("security.refresh_token_ttl", "168h") // 7 天
	v.SetDefault("security.bcrypt_cost", 10)
	v.SetDefault("scheduler.expire_scan", "@every 1m")
	v.SetDefault("scheduler.stock_reconcile", "@every 5m")

	envBindings := map[string]string{
		"app.env":        "MEDICORE_APP_ENV",
		"app.port":       "MEDICORE_APP_PORT",
		"log.level":      "MEDICORE_LOG_LEVEL",
		"mysql.host":     "MEDICORE_MYSQL_HOST",
		"mysql.port":     "MEDICORE_MYSQL_PORT",
		"mysql.user":     "MEDICORE_MYSQL_USER",
		"mysql.password": "MEDICORE_MYSQL_PASSWORD",
		"mysql.database": "MEDICORE_MYSQL_DATABASE",
		"redis.addr":     "MEDICORE_REDIS_ADDR",
		"redis.password": "MEDICORE_REDIS_PASSWORD",
		"redis.db":       "MEDICORE_REDIS_DB",

		"security.aes_key":      "MEDICORE_SECURITY_AES_KEY",
		"security.phone_pepper": "MEDICORE_SECURITY_PHONE_PEPPER",
		"security.jwt_secret":   "MEDICORE_SECURITY_JWT_SECRET",
		"security.bcrypt_cost":  "MEDICORE_SECURITY_BCRYPT_COST",

		"scheduler.expire_scan":     "MEDICORE_SCHEDULER_EXPIRE_SCAN",
		"scheduler.stock_reconcile": "MEDICORE_SCHEDULER_STOCK_RECONCILE",
	}
	for key, env := range envBindings {
		if err := v.BindEnv(key, env); err != nil {
			return nil, fmt.Errorf("bind env %s: %w", env, err)
		}
	}

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	// 合并同目录下的 <name>.local.<ext>（如 configs/config.local.yaml）。
	// 该文件存放本地开发密钥且已被 .gitignore 排除，使仓库中不含任何密钥。
	// 文件不存在属正常情况，不作为错误。
	localPath := localConfigPath(path)
	if _, statErr := os.Stat(localPath); statErr == nil {
		v.SetConfigFile(localPath)
		if err := v.MergeInConfig(); err != nil {
			return nil, fmt.Errorf("merge local config %s: %w", localPath, err)
		}
	}

	var c Config
	if err := v.Unmarshal(&c); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	return &c, nil
}

// localConfigPath 由基础配置路径推导本地覆盖文件路径：
// configs/config.yaml -> configs/config.local.yaml
func localConfigPath(path string) string {
	ext := filepath.Ext(path)
	return strings.TrimSuffix(path, ext) + ".local" + ext
}

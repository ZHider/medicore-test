package service

import (
	"context"
	"time"

	"github.com/medicore/backend/internal/model"
)

// 本包按 Go 的惯例「接口由使用方定义」声明所需的最小能力接口，
// 而不是直接依赖 repository 的具体类型。
//
// 这样做的好处：
//   - service 与 repository 的耦合面被显式写出来，一眼可见它到底用了哪些能力；
//   - service 的单测可注入内存实现，无需真实 MySQL/Redis（见 auth_test.go）；
//   - repository 可以自由增加方法而不影响 service 的编译。
//
// 注意接口保持**最小**：只声明 service 真正调用的方法。
// 若按实现方的全部方法声明接口，就退化成"接口只是为了抽象"的反模式。

// userStore 是用户读写能力。
type userStore interface {
	Create(ctx context.Context, u *model.User) error
	FindByPhoneHash(ctx context.Context, phoneHash string) (*model.User, error)
	FindByID(ctx context.Context, id uint64) (*model.User, error)
	TouchLastLogin(ctx context.Context, id uint64, at time.Time) error
}

// tokenStore 是刷新令牌存取能力。
type tokenStore interface {
	SaveRefresh(ctx context.Context, token string, userID uint64, ttl time.Duration) error
	ConsumeRefresh(ctx context.Context, token string) (uint64, error)
	DeleteRefresh(ctx context.Context, token string) error
}

// healthChecker 是依赖健康探测能力。
type healthChecker interface {
	PingMySQL(ctx context.Context) error
	PingRedis(ctx context.Context) error
}

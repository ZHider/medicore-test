package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/medicore/backend/internal/apperr"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/jwt"
	"github.com/medicore/backend/internal/mask"
	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/repository"
	"github.com/medicore/backend/internal/secret"
)

// AuthDeps 是 AuthService 的依赖集合。
// 用结构体而非长参数列表，新增依赖时不必改调用点。
// Users/Tokens 声明为接口（见 deps.go），使本服务可在无数据库的情况下单测。
type AuthDeps struct {
	Users       userStore
	Tokens      tokenStore
	Cipher      *secret.Cipher
	JWT         *jwt.Manager
	Log         *zap.Logger
	PhonePepper string
	BcryptCost  int
	RefreshTTL  time.Duration
}

// AuthService 承载注册、登录、刷新、登出与个人信息查询。
type AuthService struct {
	users       userStore
	tokens      tokenStore
	cipher      *secret.Cipher
	jwt         *jwt.Manager
	log         *zap.Logger
	phonePepper string
	bcryptCost  int
	refreshTTL  time.Duration
	// dummyHash 是占位口令的 bcrypt 散列，用于登录时序防护。
	// 在构造时用与真实口令**相同的 cost** 预生成，保证比对耗时一致。
	dummyHash string
}

// NewAuthService 构造 AuthService。
func NewAuthService(d AuthDeps) *AuthService {
	// 允许调用方不传日志（例如单测），避免空指针。
	log := d.Log
	if log == nil {
		log = zap.NewNop()
	}

	// 预生成占位散列。这里失败几乎不可能（仅当 bcrypt 本身出错），
	// 但仍显式记录：一旦缺失，登录时序防护会静默失效。
	dummyHash, err := secret.HashPassword(secret.DummyPassword, d.BcryptCost)
	if err != nil {
		log.Warn("failed to precompute dummy password hash; login timing protection is disabled",
			zap.Error(err))
	}

	return &AuthService{
		users:       d.Users,
		tokens:      d.Tokens,
		cipher:      d.Cipher,
		jwt:         d.JWT,
		log:         log,
		phonePepper: d.PhonePepper,
		bcryptCost:  d.BcryptCost,
		refreshTTL:  d.RefreshTTL,
		dummyHash:   dummyHash,
	}
}

// errInvalidCredentials 是登录失败的统一文案。
// 刻意不区分"手机号不存在"和"密码错误"：区分会变成用户枚举漏洞。
func errInvalidCredentials() error {
	return apperr.New(errcode.ErrUnauthorized, "手机号或密码错误")
}

// equalizeLoginTiming 在账号不存在时消耗与真实校验等量的时间。
// 结果被刻意丢弃——它的唯一目的就是耗时。
func (s *AuthService) equalizeLoginTiming(password string) {
	if s.dummyHash == "" {
		return
	}
	secret.VerifyPassword(s.dummyHash, password)
}

// Register 注册患者账号。
func (s *AuthService) Register(ctx context.Context, req model.RegisterRequest) (*model.UserProfile, error) {
	if !validPhone(req.Phone) {
		return nil, apperr.New(errcode.ErrInvalidParam, "手机号格式不正确")
	}

	phoneHash := secret.Digest(req.Phone, s.phonePepper)

	phoneEnc, err := s.cipher.Encrypt(req.Phone)
	if err != nil {
		return nil, apperr.Wrap(errcode.ErrInternal, "加密手机号失败", err)
	}

	passwordHash, err := secret.HashPassword(req.Password, s.bcryptCost)
	if err != nil {
		return nil, apperr.Wrap(errcode.ErrInternal, "生成口令散列失败", err)
	}

	u := &model.User{
		Role:         model.RolePatient,
		Name:         strings.TrimSpace(req.Name),
		PhoneEnc:     phoneEnc,
		PhoneHash:    phoneHash,
		PasswordHash: passwordHash,
		Status:       model.UserStatusActive,
	}

	if req.IDCard != "" {
		idEnc, err := s.cipher.Encrypt(req.IDCard)
		if err != nil {
			return nil, apperr.Wrap(errcode.ErrInternal, "加密身份证失败", err)
		}
		hash := secret.Digest(req.IDCard, s.phonePepper)
		u.IDCardEnc = idEnc
		u.IDCardHash = &hash
	}

	if err := s.users.Create(ctx, u); err != nil {
		// 直接依赖唯一索引判定重复，而不是"先查再插"：
		// 软删除行会被 GORM 的查询过滤掉，但 uk_phone_hash 依然会拦住插入，
		// 先查再插反而会给出与事实相反的提示。
		if errors.Is(err, repository.ErrDuplicateKey) {
			return nil, apperr.New(errcode.ErrConflict, "该手机号已注册")
		}
		return nil, apperr.Wrap(errcode.ErrInternal, "创建用户失败", err)
	}

	profile, err := s.profile(u)
	if err != nil {
		return nil, err
	}
	return profile, nil
}

// Login 校验凭证并签发双令牌。
func (s *AuthService) Login(ctx context.Context, req model.LoginRequest) (*model.TokenPair, error) {
	u, err := s.users.FindByPhoneHash(ctx, secret.Digest(req.Phone, s.phonePepper))
	if errors.Is(err, repository.ErrNotFound) {
		// 时序防护：账号不存在时也执行一次代价相同的口令比对。
		// 否则"立即返回"与"比对后返回"相差约一个 bcrypt 周期（cost=10 时约 60ms），
		// 攻击者据此即可枚举已注册手机号。
		s.equalizeLoginTiming(req.Password)
		return nil, errInvalidCredentials()
	}
	if err != nil {
		return nil, apperr.Wrap(errcode.ErrInternal, "查询用户失败", err)
	}

	if !u.IsActive() {
		return nil, apperr.New(errcode.ErrForbidden, "账号已被禁用")
	}
	if !secret.VerifyPassword(u.PasswordHash, req.Password) {
		return nil, errInvalidCredentials()
	}

	// 最近登录时间属于审计信息，写失败不应阻断登录。
	if err := s.users.TouchLastLogin(ctx, u.ID, time.Now()); err != nil {
		s.log.Warn("touch last login failed",
			zap.Uint64("user_id", u.ID),
			zap.Error(err),
		)
	}

	return s.issueTokens(ctx, u)
}

// Refresh 用一次性刷新令牌换取新的双令牌（轮换）。
// 旧令牌在 ConsumeRefresh 中已被原子删除，因此同一个刷新令牌无法重放。
func (s *AuthService) Refresh(ctx context.Context, refreshToken string) (*model.TokenPair, error) {
	userID, err := s.tokens.ConsumeRefresh(ctx, refreshToken)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.New(errcode.ErrRefreshInvalid, "刷新令牌无效或已过期，请重新登录")
	}
	if err != nil {
		return nil, apperr.Wrap(errcode.ErrInternal, "读取刷新令牌失败", err)
	}

	u, err := s.users.FindByID(ctx, userID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.New(errcode.ErrRefreshInvalid, "用户不存在或已注销")
	}
	if err != nil {
		return nil, apperr.Wrap(errcode.ErrInternal, "查询用户失败", err)
	}
	if !u.IsActive() {
		return nil, apperr.New(errcode.ErrForbidden, "账号已被禁用")
	}

	return s.issueTokens(ctx, u)
}

// Logout 撤销刷新令牌。幂等：令牌不存在也返回成功。
func (s *AuthService) Logout(ctx context.Context, refreshToken string) error {
	if err := s.tokens.DeleteRefresh(ctx, refreshToken); err != nil {
		return apperr.Wrap(errcode.ErrInternal, "登出失败", err)
	}
	return nil
}

// Profile 返回当前用户的脱敏资料。
func (s *AuthService) Profile(ctx context.Context, userID uint64) (*model.UserProfile, error) {
	u, err := s.users.FindByID(ctx, userID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, apperr.New(errcode.ErrNotFound, "用户不存在")
	}
	if err != nil {
		return nil, apperr.Wrap(errcode.ErrInternal, "查询用户失败", err)
	}
	return s.profile(u)
}

// issueTokens 签发 access_token 与 refresh_token。
// 先签 JWT 再写 Redis：若 Redis 写失败则不返回令牌，避免出现"无法刷新的登录态"。
func (s *AuthService) issueTokens(ctx context.Context, u *model.User) (*model.TokenPair, error) {
	accessToken, err := s.jwt.IssueAccess(u.ID, string(u.Role))
	if err != nil {
		return nil, apperr.Wrap(errcode.ErrInternal, "签发访问令牌失败", err)
	}

	refreshToken, err := secret.RandomToken()
	if err != nil {
		return nil, apperr.Wrap(errcode.ErrInternal, "生成刷新令牌失败", err)
	}
	if err := s.tokens.SaveRefresh(ctx, refreshToken, u.ID, s.refreshTTL); err != nil {
		return nil, apperr.Wrap(errcode.ErrInternal, "保存刷新令牌失败", err)
	}

	return &model.TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		TokenType:    "Bearer",
		ExpiresIn:    s.jwt.AccessTTLSeconds(),
	}, nil
}

// profile 解密并脱敏用户资料。解密失败说明密钥不匹配或数据被篡改，属于服务端问题。
func (s *AuthService) profile(u *model.User) (*model.UserProfile, error) {
	phone, err := s.cipher.Decrypt(u.PhoneEnc)
	if err != nil {
		return nil, apperr.Wrap(errcode.ErrInternal, "解密手机号失败", err)
	}
	return &model.UserProfile{
		ID:    u.ID,
		Role:  u.Role,
		Name:  u.Name,
		Phone: mask.Phone(phone),
	}, nil
}

// validPhone 校验中国大陆手机号：11 位数字且以 1 开头。
func validPhone(p string) bool {
	if len(p) != 11 || p[0] != '1' {
		return false
	}
	for i := 0; i < len(p); i++ {
		if p[i] < '0' || p[i] > '9' {
			return false
		}
	}
	return true
}

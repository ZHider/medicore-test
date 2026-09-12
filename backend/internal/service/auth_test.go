package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/medicore/backend/internal/apperr"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/jwt"
	"github.com/medicore/backend/internal/model"
	"github.com/medicore/backend/internal/repository"
	"github.com/medicore/backend/internal/secret"
)

const (
	testPhone = "13812345678"
	testPass  = "Passw0rd!"
)

// randomHex 生成 n 字节的随机十六进制串。
// 测试刻意不使用固定的密钥常量：固定密钥字面量会被密钥扫描工具误报，
// 也会让测试无谓地依赖某个特定值。
func randomHex(t *testing.T, n int) string {
	t.Helper()

	b := make([]byte, n)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return hex.EncodeToString(b)
}

// --- 内存实现：service 的单测因此完全不依赖 MySQL / Redis ---

type fakeUserStore struct {
	mu     sync.Mutex
	byID   map[uint64]*model.User
	nextID uint64
	// createErr 用于模拟唯一键冲突等写入失败。
	createErr error
}

func newFakeUserStore() *fakeUserStore {
	return &fakeUserStore{byID: make(map[uint64]*model.User)}
}

func (f *fakeUserStore) Create(_ context.Context, u *model.User) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	f.nextID++
	u.ID = f.nextID
	clone := *u
	f.byID[u.ID] = &clone
	return nil
}

func (f *fakeUserStore) FindByPhoneHash(_ context.Context, phoneHash string) (*model.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, u := range f.byID {
		if u.PhoneHash == phoneHash {
			clone := *u
			return &clone, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (f *fakeUserStore) FindByID(_ context.Context, id uint64) (*model.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	u, ok := f.byID[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	clone := *u
	return &clone, nil
}

func (f *fakeUserStore) TouchLastLogin(_ context.Context, id uint64, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if u, ok := f.byID[id]; ok {
		u.LastLoginAt = &at
	}
	return nil
}

type fakeTokenStore struct {
	mu     sync.Mutex
	tokens map[string]uint64
}

func newFakeTokenStore() *fakeTokenStore {
	return &fakeTokenStore{tokens: make(map[string]uint64)}
}

func (f *fakeTokenStore) SaveRefresh(_ context.Context, token string, userID uint64, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens[token] = userID
	return nil
}

// ConsumeRefresh 复刻真实实现的一次性语义（GETDEL）：取出即删除。
func (f *fakeTokenStore) ConsumeRefresh(_ context.Context, token string) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	userID, ok := f.tokens[token]
	if !ok {
		return 0, repository.ErrNotFound
	}
	delete(f.tokens, token)
	return userID, nil
}

func (f *fakeTokenStore) DeleteRefresh(_ context.Context, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.tokens, token)
	return nil
}

func newTestAuthService(t *testing.T) (*AuthService, *fakeUserStore, *fakeTokenStore) {
	t.Helper()

	cipher, err := secret.NewCipher(randomHex(t, 32))
	require.NoError(t, err)

	users := newFakeUserStore()
	tokens := newFakeTokenStore()

	svc := NewAuthService(AuthDeps{
		Users:       users,
		Tokens:      tokens,
		Cipher:      cipher,
		JWT:         jwt.NewManager(randomHex(t, 32), "medicore", time.Hour),
		PhonePepper: randomHex(t, 16),
		BcryptCost:  secret.MinBcryptCost,
		RefreshTTL:  time.Hour,
	})
	return svc, users, tokens
}

// registerPatient 注册一个可用账号并返回其 ID。
func registerPatient(t *testing.T, svc *AuthService) uint64 {
	t.Helper()

	profile, err := svc.Register(context.Background(), model.RegisterRequest{
		Phone:    testPhone,
		Name:     "张三",
		Password: testPass,
	})
	require.NoError(t, err)
	return profile.ID
}

// --- Register ---

func TestRegister_Success(t *testing.T) {
	svc, _, _ := newTestAuthService(t)

	profile, err := svc.Register(context.Background(), model.RegisterRequest{
		Phone:    testPhone,
		Name:     "  张三  ",
		Password: testPass,
		IDCard:   "110101199001011234",
	})

	require.NoError(t, err)
	assert.Equal(t, model.RolePatient, profile.Role, "注册只应产生患者角色")
	assert.Equal(t, "张三", profile.Name, "姓名应去除首尾空白")
	assert.Equal(t, "138****5678", profile.Phone, "手机号必须脱敏后才可返回")
}

// 注册接口不得允许自助选择角色，否则任何人都能注册成管理员。
func TestRegister_AlwaysAssignsPatientRole(t *testing.T) {
	svc, users, _ := newTestAuthService(t)

	_, err := svc.Register(context.Background(), model.RegisterRequest{
		Phone: testPhone, Name: "张三", Password: testPass,
	})
	require.NoError(t, err)

	var stored *model.User
	for _, u := range users.byID {
		stored = u
	}
	require.NotNil(t, stored)
	assert.Equal(t, model.RolePatient, stored.Role)
}

func TestRegister_RejectsInvalidPhone(t *testing.T) {
	svc, _, _ := newTestAuthService(t)

	for _, phone := range []string{"1381234567", "23812345678", "138123456789", "1381234567a"} {
		_, err := svc.Register(context.Background(), model.RegisterRequest{
			Phone: phone, Name: "张三", Password: testPass,
		})
		assert.Equal(t, errcode.ErrInvalidParam, apperr.Code(err), "手机号 %q 应被拒绝", phone)
	}
}

// 唯一键冲突必须转成 40004 业务冲突，而不是 500。
func TestRegister_DuplicatePhoneBecomesConflict(t *testing.T) {
	svc, users, _ := newTestAuthService(t)
	registerPatient(t, svc)

	users.createErr = repository.ErrDuplicateKey

	_, err := svc.Register(context.Background(), model.RegisterRequest{
		Phone: testPhone, Name: "李四", Password: testPass,
	})
	assert.Equal(t, errcode.ErrConflict, apperr.Code(err))
}

// --- Login ---

func TestLogin_Success(t *testing.T) {
	svc, _, tokens := newTestAuthService(t)
	registerPatient(t, svc)

	pair, err := svc.Login(context.Background(), model.LoginRequest{
		Phone: testPhone, Password: testPass,
	})

	require.NoError(t, err)
	assert.NotEmpty(t, pair.AccessToken)
	assert.NotEmpty(t, pair.RefreshToken)
	assert.Equal(t, "Bearer", pair.TokenType)
	assert.Equal(t, int64(3600), pair.ExpiresIn)

	// 刷新令牌必须落库，否则后续无法刷新。
	assert.Len(t, tokens.tokens, 1)
}

// 账号不存在与密码错误必须返回**完全相同**的错误，否则可枚举手机号。
func TestLogin_WrongPasswordAndUnknownUserAreIndistinguishable(t *testing.T) {
	svc, _, _ := newTestAuthService(t)
	registerPatient(t, svc)

	_, wrongPw := svc.Login(context.Background(), model.LoginRequest{
		Phone: testPhone, Password: "definitely-wrong",
	})
	_, unknown := svc.Login(context.Background(), model.LoginRequest{
		Phone: "13900000000", Password: "definitely-wrong",
	})

	require.Error(t, wrongPw)
	require.Error(t, unknown)
	assert.Equal(t, apperr.Code(wrongPw), apperr.Code(unknown))
	assert.Equal(t, apperr.Message(wrongPw), apperr.Message(unknown))
}

// 时序防护回归测试：账号不存在时也必须消耗一个 bcrypt 周期。
// 若去掉 equalizeLoginTiming，未知账号会立即返回，比值将趋近 0。
func TestLogin_UnknownUserTimingIsEqualized(t *testing.T) {
	svc, _, _ := newTestAuthService(t)
	registerPatient(t, svc)

	measure := func(phone string) time.Duration {
		start := time.Now()
		_, _ = svc.Login(context.Background(), model.LoginRequest{
			Phone: phone, Password: "definitely-wrong",
		})
		return time.Since(start)
	}

	// 两种路径都应接近一个 bcrypt 周期，绝对值抖动较大，因此比较比值。
	const rounds = 3
	var unknownTotal, wrongPwTotal time.Duration
	for i := 0; i < rounds; i++ {
		unknownTotal += measure("13900000000")
		wrongPwTotal += measure(testPhone)
	}

	ratio := float64(unknownTotal) / float64(wrongPwTotal)
	assert.Greater(t, ratio, 0.5,
		"账号不存在的响应过快（比值 %.2f），说明时序防护失效，可被用于枚举手机号", ratio)
	assert.Less(t, ratio, 2.0, "账号不存在的响应异常慢（比值 %.2f）", ratio)
}

func TestLogin_DisabledAccountIsForbidden(t *testing.T) {
	svc, users, _ := newTestAuthService(t)
	id := registerPatient(t, svc)

	users.mu.Lock()
	users.byID[id].Status = model.UserStatusDisabled
	users.mu.Unlock()

	_, err := svc.Login(context.Background(), model.LoginRequest{
		Phone: testPhone, Password: testPass,
	})
	assert.Equal(t, errcode.ErrForbidden, apperr.Code(err))
}

// 即使口令正确，也应为禁用账号预先拦截（顺序：状态先于口令）。
func TestLogin_DisabledAccountDoesNotIssueTokens(t *testing.T) {
	svc, users, tokens := newTestAuthService(t)
	id := registerPatient(t, svc)

	users.mu.Lock()
	users.byID[id].Status = model.UserStatusDisabled
	users.mu.Unlock()

	_, err := svc.Login(context.Background(), model.LoginRequest{
		Phone: testPhone, Password: testPass,
	})
	require.Error(t, err)
	assert.Empty(t, tokens.tokens, "被拒的登录不得留下刷新令牌")
}

// --- Refresh ---

func TestRefresh_RotatesAndInvalidatesOldToken(t *testing.T) {
	svc, _, _ := newTestAuthService(t)
	registerPatient(t, svc)

	first, err := svc.Login(context.Background(), model.LoginRequest{
		Phone: testPhone, Password: testPass,
	})
	require.NoError(t, err)

	second, err := svc.Refresh(context.Background(), first.RefreshToken)
	require.NoError(t, err)
	assert.NotEqual(t, first.RefreshToken, second.RefreshToken, "刷新必须轮换令牌")
	assert.NotEmpty(t, second.AccessToken)

	// 旧令牌重放必须失败——这是轮换的核心目的。
	_, err = svc.Refresh(context.Background(), first.RefreshToken)
	assert.Equal(t, errcode.ErrRefreshInvalid, apperr.Code(err))
}

func TestRefresh_UnknownToken(t *testing.T) {
	svc, _, _ := newTestAuthService(t)

	_, err := svc.Refresh(context.Background(), "not-a-real-token")
	assert.Equal(t, errcode.ErrRefreshInvalid, apperr.Code(err))
}

func TestRefresh_DisabledAccountCannotRefresh(t *testing.T) {
	svc, users, _ := newTestAuthService(t)
	id := registerPatient(t, svc)

	pair, err := svc.Login(context.Background(), model.LoginRequest{
		Phone: testPhone, Password: testPass,
	})
	require.NoError(t, err)

	users.mu.Lock()
	users.byID[id].Status = model.UserStatusDisabled
	users.mu.Unlock()

	_, err = svc.Refresh(context.Background(), pair.RefreshToken)
	assert.Equal(t, errcode.ErrForbidden, apperr.Code(err))
}

// --- Logout ---

func TestLogout_RevokesRefreshToken(t *testing.T) {
	svc, _, _ := newTestAuthService(t)
	registerPatient(t, svc)

	pair, err := svc.Login(context.Background(), model.LoginRequest{
		Phone: testPhone, Password: testPass,
	})
	require.NoError(t, err)

	require.NoError(t, svc.Logout(context.Background(), pair.RefreshToken))

	_, err = svc.Refresh(context.Background(), pair.RefreshToken)
	assert.Equal(t, errcode.ErrRefreshInvalid, apperr.Code(err))
}

func TestLogout_IsIdempotent(t *testing.T) {
	svc, _, _ := newTestAuthService(t)

	// 登出未知令牌不应报错，保证客户端重试安全。
	assert.NoError(t, svc.Logout(context.Background(), "never-existed"))
}

// --- Profile ---

func TestProfile_DecryptsAndMasks(t *testing.T) {
	svc, _, _ := newTestAuthService(t)
	id := registerPatient(t, svc)

	profile, err := svc.Profile(context.Background(), id)

	require.NoError(t, err)
	assert.Equal(t, "138****5678", profile.Phone)
	assert.NotContains(t, profile.Phone, testPhone, "响应中不得出现完整手机号")
}

func TestProfile_NotFound(t *testing.T) {
	svc, _, _ := newTestAuthService(t)

	_, err := svc.Profile(context.Background(), 999)
	assert.Equal(t, errcode.ErrNotFound, apperr.Code(err))
}

// 手机号在存储层必须是密文，且能被正确解回。
func TestRegister_PhoneIsEncryptedAtRest(t *testing.T) {
	svc, users, _ := newTestAuthService(t)
	registerPatient(t, svc)

	for _, u := range users.byID {
		assert.NotContains(t, string(u.PhoneEnc), testPhone, "存储层不得出现明文手机号")
		assert.NotEmpty(t, u.PasswordHash, "口令散列必须已写入")
		assert.NotEqual(t, testPass, u.PasswordHash, "口令绝不能明文存储")
		assert.Len(t, u.PhoneHash, 64, "摘要需匹配 phone_hash CHAR(64)")
	}
}

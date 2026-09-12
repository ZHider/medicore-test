package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/medicore/backend/internal/authctx"
	"github.com/medicore/backend/internal/errcode"
	"github.com/medicore/backend/internal/jwt"
	"github.com/medicore/backend/internal/model"
)

func init() {
	gin.SetMode(gin.TestMode)
}

const testJWTSecret = "test-secret"

func newJWTManager(ttl time.Duration) *jwt.Manager {
	return jwt.NewManager(testJWTSecret, "medicore", ttl)
}

func doRequest(r *gin.Engine, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// bodyCode 解析统一响应信封中的 code 字段。
func bodyCode(t *testing.T, w *httptest.ResponseRecorder) int {
	t.Helper()
	var body struct {
		Code int `json:"code"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body.Code
}

// --- JWT 中间件 ---

func TestJWT_RejectsMissingOrMalformedHeader(t *testing.T) {
	m := newJWTManager(time.Hour)
	r := gin.New()
	r.GET("/probe", JWT(m), func(c *gin.Context) { c.Status(http.StatusOK) })

	tests := []struct {
		name    string
		headers map[string]string
	}{
		{"无 Authorization", nil},
		{"缺少 Bearer 前缀", map[string]string{"Authorization": "abc.def.ghi"}},
		{"Bearer 后为空", map[string]string{"Authorization": "Bearer "}},
		{"令牌非法", map[string]string{"Authorization": "Bearer not-a-jwt"}},
		{"错误密钥签发", map[string]string{
			"Authorization": "Bearer " + mustToken(t, jwt.NewManager("other", "medicore", time.Hour)),
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := doRequest(r, tt.headers)
			assert.Equal(t, http.StatusUnauthorized, w.Code)
			assert.Equal(t, int(errcode.ErrUnauthorized), bodyCode(t, w))
		})
	}
}

// 过期与非法必须返回不同错误码：前端据此决定静默刷新还是跳登录页。
func TestJWT_ExpiredReturnsDistinctCode(t *testing.T) {
	m := newJWTManager(-time.Minute)
	r := gin.New()
	r.GET("/probe", JWT(m), func(c *gin.Context) { c.Status(http.StatusOK) })

	w := doRequest(r, map[string]string{"Authorization": "Bearer " + mustToken(t, m)})

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, int(errcode.ErrTokenExpired), bodyCode(t, w))
}

func TestJWT_ValidTokenInjectsIdentity(t *testing.T) {
	m := newJWTManager(time.Hour)

	var gotID uint64
	var gotRole string
	r := gin.New()
	r.GET("/probe", JWT(m), func(c *gin.Context) {
		gotID = authctx.MustUserID(c)
		gotRole = authctx.Role(c)
		c.Status(http.StatusOK)
	})

	w := doRequest(r, map[string]string{"Authorization": "Bearer " + mustTokenFor(t, m, 7, "doctor")})

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, uint64(7), gotID)
	assert.Equal(t, "doctor", gotRole)
}

// --- RBAC 中间件 ---

func TestRequireRole(t *testing.T) {
	m := newJWTManager(time.Hour)

	newSecuredRouter := func(allowed ...model.Role) *gin.Engine {
		r := gin.New()
		r.GET("/probe", JWT(m), RequireRole(allowed...), func(c *gin.Context) {
			c.Status(http.StatusOK)
		})
		return r
	}

	tests := []struct {
		name       string
		allowed    []model.Role
		tokenRole  model.Role
		wantStatus int
		wantCode   int
	}{
		{"患者访问患者端点", []model.Role{model.RolePatient}, model.RolePatient, http.StatusOK, 0},
		{"患者访问管理端点", []model.Role{model.RoleAdmin}, model.RolePatient, http.StatusForbidden, int(errcode.ErrForbidden)},
		{"医生访问多角色端点", []model.Role{model.RoleDoctor, model.RoleAdmin}, model.RoleDoctor, http.StatusOK, 0},
		{"管理员访问多角色端点", []model.Role{model.RoleDoctor, model.RoleAdmin}, model.RoleAdmin, http.StatusOK, 0},
		{"未列入的角色被拒", []model.Role{model.RoleDoctor}, model.RoleAdmin, http.StatusForbidden, int(errcode.ErrForbidden)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := mustTokenFor(t, m, 1, string(tt.tokenRole))
			w := doRequest(newSecuredRouter(tt.allowed...), map[string]string{
				"Authorization": "Bearer " + token,
			})
			assert.Equal(t, tt.wantStatus, w.Code)
			if tt.wantCode != 0 {
				assert.Equal(t, tt.wantCode, bodyCode(t, w))
			}
		})
	}
}

// 无令牌访问受保护端点必须由 JWT 中间件拦为 401，而不是落到 RBAC 报 403。
func TestRequireRole_WithoutTokenIsUnauthorized(t *testing.T) {
	m := newJWTManager(time.Hour)
	r := gin.New()
	r.GET("/probe", JWT(m), RequireRole(model.RoleAdmin), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	w := doRequest(r, nil)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Equal(t, int(errcode.ErrUnauthorized), bodyCode(t, w))
}

func mustToken(t *testing.T, m *jwt.Manager) string {
	t.Helper()
	return mustTokenFor(t, m, 1, "patient")
}

func mustTokenFor(t *testing.T, m *jwt.Manager, uid uint64, role string) string {
	t.Helper()
	token, err := m.IssueAccess(uid, role)
	require.NoError(t, err)
	return token
}

package integrationtest

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dujiao-next/internal/app/httpserver/middleware"
	"github.com/dujiao-next/internal/authz"
	"github.com/dujiao-next/internal/config"
	admincontract "github.com/dujiao-next/internal/modules/identity/admin/contract"
	adminauthapp "github.com/dujiao-next/internal/modules/identity/adminauth/application"
	adminauthhttp "github.com/dujiao-next/internal/modules/identity/adminauth/transport/http"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type logoutTransport struct {
	adminauthhttp.LoginAuthService
	auth *adminauthapp.Service
}

func (a logoutTransport) Logout(id uint, version uint64) error { return a.auth.Logout(id, version) }

type failingLogoutStore struct{ admincontract.Store }

func (f failingLogoutStore) RevokeSessions(uint, uint64) (bool, error) {
	return false, errors.New("database unavailable")
}
func TestLogoutRevokesOldSessionsAndPreservesOtherAccounts(t *testing.T) {
	auth, _, repo := newAuthTestService(t)
	a := createAuthTestAdmin(t, repo, "logout-a", "secret123")
	createAuthTestAdmin(t, repo, "logout-b", "secret123")
	first, err := auth.Login("logout-a", "secret123")
	if err != nil {
		t.Fatal(err)
	}
	second, err := auth.Login("logout-a", "secret123")
	if err != nil {
		t.Fatal(err)
	}
	other, err := auth.Login("logout-b", "secret123")
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(middleware.JWTAuthMiddleware("jwt-secret-for-test", repo))
	h := adminauthhttp.NewAdminLoginHandler(logoutTransport{auth: auth}, nil, nil)
	router.POST("/logout", h.Logout)
	router.GET("/protected", func(c *gin.Context) { c.Status(204) })
	call := func(method, path, token string) int {
		r := httptest.NewRequest(method, path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			return w.Code
		}
		var body struct {
			Code int `json:"status_code"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Code != 0 {
			return body.Code
		}
		return http.StatusOK
	}
	if got := call("POST", "/logout", ""); got != http.StatusUnauthorized {
		t.Fatalf("anonymous logout %d", got)
	}
	if got := call("POST", "/logout", first.Token); got != http.StatusOK {
		t.Fatalf("logout %d", got)
	}
	for _, token := range []string{first.Token, second.Token} {
		if got := call("GET", "/protected", token); got != http.StatusUnauthorized {
			t.Fatalf("old token %d", got)
		}
	}
	if got := call("GET", "/protected", other.Token); got != http.StatusNoContent {
		t.Fatalf("other account %d", got)
	}
	if err := auth.Logout(a.ID, a.TokenVersion); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetByID(a.ID)
	if err != nil || stored.TokenVersion != a.TokenVersion+1 {
		t.Fatalf("stale logout changed version: %+v %v", stored, err)
	}
	fresh, err := auth.Login("logout-a", "secret123")
	if err != nil {
		t.Fatal(err)
	}
	if got := call("GET", "/protected", fresh.Token); got != http.StatusNoContent {
		t.Fatalf("fresh token %d", got)
	}
}

func TestLogoutFailureDoesNotPretendToRevoke(t *testing.T) {
	auth, _, repo := newAuthTestService(t)
	a := createAuthTestAdmin(t, repo, "logout-failure", "secret123")
	login, err := auth.Login("logout-failure", "secret123")
	if err != nil {
		t.Fatal(err)
	}
	failed := adminauthapp.NewService(&config.Config{}, failingLogoutStore{Store: repo})
	router := gin.New()
	router.Use(middleware.JWTAuthMiddleware("jwt-secret-for-test", repo))
	h := adminauthhttp.NewAdminLoginHandler(logoutTransport{auth: failed}, nil, nil)
	router.POST("/logout", h.Logout)
	req := httptest.NewRequest("POST", "/logout", nil)
	req.Header.Set("Authorization", "Bearer "+login.Token)
	reply := httptest.NewRecorder()
	router.ServeHTTP(reply, req)
	var body struct {
		Code int `json:"status_code"`
	}
	if err := json.Unmarshal(reply.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code == 0 {
		t.Fatalf("failure returned success: %s", reply.Body.String())
	}
	stored, err := repo.GetByID(a.ID)
	if err != nil || stored.TokenVersion != a.TokenVersion {
		t.Fatalf("session changed on failed write: %v", err)
	}
}

func TestLogoutIsSelfServiceForUnprivilegedAdmin(t *testing.T) {
	auth, _, repo := newAuthTestService(t)
	createAuthTestAdmin(t, repo, "logout-roleless", "secret123")
	login, err := auth.Login("logout-roleless", "secret123")
	if err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	roles, err := authz.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Use(middleware.JWTAuthMiddleware("jwt-secret-for-test", repo), middleware.AdminRBACMiddleware(roles))
	h := adminauthhttp.NewAdminLoginHandler(logoutTransport{auth: auth}, nil, nil)
	router.POST("/api/v1/admin/logout", h.Logout)
	router.POST("/api/v1/admin/logout-other", func(c *gin.Context) { c.Status(204) })
	request := func(path string) int {
		r := httptest.NewRequest("POST", path, nil)
		r.Header.Set("Authorization", "Bearer "+login.Token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != 200 {
			return w.Code
		}
		var b struct {
			Code int `json:"status_code"`
		}
		if json.Unmarshal(w.Body.Bytes(), &b) != nil {
			t.Fatal("invalid response")
		}
		return b.Code
	}
	if got := request("/api/v1/admin/logout-other"); got != 403 {
		t.Fatalf("unrelated route returned %d", got)
	}
	if got := request("/api/v1/admin/logout"); got != 0 {
		t.Fatalf("self logout returned %d", got)
	}
}

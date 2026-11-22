package siteconnectionhttp

import (
	"net/http/httptest"
	"strings"
	"testing"

	siteconnectionapp "github.com/dujiao-next/internal/modules/siteconnection/application"
	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"
	"github.com/dujiao-next/internal/shared/operationaudit"
	"github.com/gin-gonic/gin"
)

type auditConnectionStub struct {
	AdminService
	conn *siteconnectiondomain.Connection
}

func (s *auditConnectionStub) GetByID(uint) (*siteconnectiondomain.Connection, error) {
	return s.conn, nil
}
func (s *auditConnectionStub) Update(_ uint, in siteconnectionapp.UpdateInput) (*siteconnectiondomain.Connection, error) {
	s.conn.ApiKey = in.ApiKey
	s.conn.ApiSecret = "new-encrypted-secret"
	s.conn.Name = in.Name
	return s.conn, nil
}

type auditMarkupStub struct{}

func (*auditMarkupStub) ReapplyMarkup(uint) (int, error) { return 1, nil }
func TestConnectionAuditPreservesBeforeWhenServiceMutatesObject(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var got *operationaudit.Event
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("admin_operation_recorder", func(e *operationaudit.Event) { got = e })
		c.Next()
	})
	stub := &auditConnectionStub{conn: &siteconnectiondomain.Connection{ID: 1, ApiKey: "old-secret-key", ApiSecret: "old-ciphertext", Name: "old-private-name", Status: "active"}}
	r.PUT("/connections/:id", NewAdminHandler(stub, &auditMarkupStub{}).UpdateSiteConnection)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("PUT", "/connections/1", strings.NewReader(`{"api_key":"new-secret-key","name":"new-private-name"}`)))
	if w.Code != 200 || got == nil || got.Result != "success" {
		t.Fatalf("operation failed: %d %+v", w.Code, got)
	}
	if got.After["api_key_changed"] != true || got.After["api_secret_replaced"] != true || got.After["name_changed"] != true {
		t.Fatal("before values aliased mutated service object")
	}
	for _, fields := range []operationaudit.Fields{got.Before, got.After} {
		for _, forbidden := range []string{"api_key", "api_secret", "name", "base_url", "callback_url"} {
			if _, ok := fields[forbidden]; ok {
				t.Fatalf("sensitive field captured: %s", forbidden)
			}
		}
	}
}

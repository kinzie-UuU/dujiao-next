package auditloghttp

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dujiao-next/internal/modules/auditlog/domain"
	"github.com/dujiao-next/internal/platform/http/ginutil"
	"github.com/dujiao-next/internal/shared/operationaudit"
	"github.com/gin-gonic/gin"
)

type operationStoreStub struct {
	rows []*domain.AdminOperationLog
	err  error
}

func (s *operationStoreStub) RecordOperation(row *domain.AdminOperationLog) error {
	s.rows = append(s.rows, row)
	return s.err
}

func TestOperationAuditRequiresExplicitBusinessSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, success := range []bool{false, true} {
		store := &operationStoreStub{}
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Set("admin_id", uint(7))
			c.Set("username", "operator")
			c.Set("request_id", "req-7")
			c.Next()
		}, OperationMiddleware(store))
		r.POST("/operation", func(c *gin.Context) {
			e := operationaudit.New("order.status", "42", operationaudit.Fields{"status": "pending"})
			defer ginutil.RecordOperation(c, e)
			if success {
				e.Success(operationaudit.Fields{"status": "paid"})
			}
			c.JSON(200, gin.H{"code": 400, "secret": "not-collected"})
		})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", "/operation", strings.NewReader(`{"password":"never-collected"}`)))
		if len(store.rows) != 1 {
			t.Fatal("missing operation")
		}
		row := store.rows[0]
		want := "failed"
		if success {
			want = "success"
		}
		if row.Result != want || row.OperatorAdminID != 7 || row.RequestID != "req-7" || row.Target != "42" || row.ClientIP != "192.0.2.1" || row.CreatedAt.IsZero() {
			t.Fatalf("incorrect metadata: %+v", row)
		}
		if len(row.BeforeJSON) != 1 {
			t.Fatal("unexpected body capture")
		}
	}
}

func TestOperationAuditWriteFailurePreservesBusinessResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &operationStoreStub{err: errors.New("database unavailable")}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("admin_id", uint(7)); c.Next() }, OperationMiddleware(store))
	r.POST("/operation", func(c *gin.Context) {
		e := operationaudit.New("order.status", "42", nil)
		defer ginutil.RecordOperation(c, e)
		e.Success(nil)
		c.Status(204)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/operation", nil))
	if w.Code != 204 {
		t.Fatalf("business status changed: %d", w.Code)
	}
}

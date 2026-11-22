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
			c.Set("request_id", "16b221d6-8c36-4fa0-9c62-ab4b96b31d29")
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
		if row.Result != want || row.OperatorAdminID != 7 || row.RequestID != "16b221d6-8c36-4fa0-9c62-ab4b96b31d29" || row.Target != "42" || row.ClientIP != "192.0.2.1" || row.CreatedAt.IsZero() {
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

func TestOperationRequestIDRejectsSensitiveOrOversizedValues(t *testing.T) {
	for _, input := range []string{"", "secret-token", strings.Repeat("sensitive", 10000), "16b221d68c364fa09c62ab4b96b31d29"} {
		got := operationRequestID(input)
		if len(got) != 36 || got == input || operationRequestID(got) != got {
			t.Fatal("untrusted request ID persisted")
		}
	}
}

func TestOperationMiddlewareRejectsUntrustedRequestIDOnWriteFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := &operationStoreStub{err: errors.New("database unavailable")}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("admin_id", uint(7))
		c.Set("request_id", c.GetHeader("X-Request-ID"))
		c.Next()
	}, OperationMiddleware(store))
	r.POST("/operation", func(c *gin.Context) {
		e := operationaudit.New("order.status", "42", nil)
		defer ginutil.RecordOperation(c, e)
		e.Success(nil)
		c.Status(204)
	})
	request := httptest.NewRequest("POST", "/operation", nil)
	request.Header.Set("X-Request-ID", strings.Repeat("sensitive-token", 1000))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, request)
	if len(store.rows) != 1 || len(store.rows[0].RequestID) != 36 || strings.Contains(store.rows[0].RequestID, "sensitive") {
		t.Fatal("untrusted request ID entered audit/failure-log row")
	}
}

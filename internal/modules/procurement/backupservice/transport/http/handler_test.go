package backuphttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dujiao-next/internal/shared/operationaudit"
	"github.com/gin-gonic/gin"
)

type triggerStub struct {
	calls, order, admin uint
	claim               uint
	err                 error
}

func (s *triggerStub) TriggerPlusBackup(_ context.Context, orderID, adminID uint) (uint, error) {
	s.calls++
	s.order, s.admin = orderID, adminID
	return s.claim, s.err
}

type gateStub struct {
	enabled bool
	calls   int
	last    *bool
	err     error
}

func (g *gateStub) ChangeGate(_ uint, enable bool) error {
	g.calls++
	g.last = &enable
	g.enabled = enable
	return g.err
}

func (g *gateStub) GateStatus() (bool, error) {
	return g.enabled, g.err
}

func TestTriggerPlusAuthorizationAndInputBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name, path, body string
		super            bool
		admin            uint
		allowed          bool
	}{
		{"valid", "/orders/41", "", true, 7, true},
		{"not super", "/orders/41", "", false, 7, false},
		{"anonymous", "/orders/41", "", false, 0, false},
		{"missing admin", "/orders/41", "", true, 0, false},
		{"zero order", "/orders/0", "", true, 7, false},
		{"invalid order", "/orders/nope", "", true, 7, false},
		{"json object rejected", "/orders/41", `{}`, true, 7, false},
		{"caller fields rejected", "/orders/41", `{"product_id":41,"amount":1,"admin_id":99}`, true, 7, false},
		{"query rejected", "/orders/41?amount=1", "", true, 7, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &triggerStub{claim: 12}
			var audit *operationaudit.Event
			router := gin.New()
			router.POST("/orders/:id", func(c *gin.Context) {
				c.Set("admin_is_super", tt.super)
				if tt.admin != 0 {
					c.Set("admin_id", tt.admin)
				}
				c.Set("admin_operation_recorder", func(e *operationaudit.Event) { audit = e })
				NewHandler(stub, &gateStub{}).TriggerPlus(c)
			})
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			router.ServeHTTP(w, req)
			if tt.allowed {
				if stub.calls != 1 || stub.order != 41 || stub.admin != 7 || !strings.Contains(w.Body.String(), `"claim_id":12`) || audit == nil || audit.Action != "backup_procurement.plus_trigger" || audit.Target != "41" || audit.Result != "success" || len(audit.Before) != 0 || len(audit.After) != 1 || audit.After["claim_id"] != uint(12) {
					t.Fatalf("unexpected accepted request: stub=%+v audit=%+v response=%s", stub, audit, w.Body.String())
				}
			} else if stub.calls != 0 || audit != nil || strings.Contains(w.Body.String(), `"claim_id":12`) {
				t.Fatalf("unauthorized request reached trigger: stub=%+v audit=%+v", stub, audit)
			}
		})
	}
}

func TestTriggerPlusFailureDoesNotExposeSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &triggerStub{err: errors.New("secret-token account-password")}
	var audit *operationaudit.Event
	router := gin.New()
	router.POST("/orders/:id", func(c *gin.Context) {
		c.Set("admin_is_super", true)
		c.Set("admin_id", uint(7))
		c.Set("admin_operation_recorder", func(e *operationaudit.Event) { audit = e })
		NewHandler(stub, &gateStub{}).TriggerPlus(c)
	})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/orders/41", nil))
	if strings.Contains(w.Body.String(), "secret-token") || strings.Contains(w.Body.String(), "account-password") || audit == nil || audit.Action != "backup_procurement.plus_trigger" || audit.Target != "41" || audit.Result != "failed" || len(audit.Before) != 0 || len(audit.After) != 0 {
		t.Fatalf("failure leaked or was not audited: response=%s audit=%+v", w.Body.String(), audit)
	}
}

func TestSetGateRequiresSuperAdminAndAudits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	gate := &gateStub{}
	var audit *operationaudit.Event
	router := gin.New()
	router.PUT("/backup-procurement/gate", func(c *gin.Context) {
		c.Set("admin_is_super", true)
		c.Set("admin_id", uint(3))
		c.Set("admin_operation_recorder", func(e *operationaudit.Event) { audit = e })
		NewHandler(&triggerStub{}, gate).SetGate(c)
	})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/backup-procurement/gate", strings.NewReader(`{"enabled":true}`)))
	if gate.calls != 1 || gate.last == nil || !*gate.last || audit == nil || audit.Action != "backup_procurement.gate" || audit.Result != "success" {
		t.Fatalf("gate not updated: gate=%+v audit=%+v body=%s", gate, audit, w.Body.String())
	}
}

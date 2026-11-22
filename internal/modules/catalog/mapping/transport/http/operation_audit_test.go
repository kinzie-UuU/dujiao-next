package mappinghttp

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	mappingapp "github.com/dujiao-next/internal/modules/catalog/mapping/application"
	mappingdomain "github.com/dujiao-next/internal/modules/catalog/mapping/domain"
	"github.com/dujiao-next/internal/shared/operationaudit"
	"github.com/gin-gonic/gin"
)

type auditMappingStub struct{ ProductMappingService }

func (*auditMappingStub) GetMappedUpstreamIDs(uint) ([]uint, error) { return []uint{10}, nil }
func (*auditMappingStub) BatchImportUpstreamProducts(uint, []uint, uint, bool) ([]mappingapp.BatchUpstreamProductImportOutcome, error) {
	return []mappingapp.BatchUpstreamProductImportOutcome{{UpstreamProductID: 10, Mapping: &mappingdomain.Mapping{ID: 1, LocalProductID: 2, IsActive: true}}, {UpstreamProductID: 11, Err: errors.New("upstream response containing secret-token")}}, nil
}
func TestBatchImportAuditsPartialFailureWithoutUpstreamErrorBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var got *operationaudit.Event
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("admin_operation_recorder", func(e *operationaudit.Event) { got = e })
		c.Next()
	})
	r.POST("/import", NewAdminHandler(&auditMappingStub{}).BatchImportUpstreamProducts)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/import", strings.NewReader(`{"connection_id":3,"upstream_product_ids":[10,11]}`)))
	if w.Code != 200 || got == nil || got.Result != "partial_failure" {
		t.Fatalf("partial success not distinguished: %d %+v", w.Code, got)
	}
	if got.After["success_count"] != 1 || got.After["failed_count"] != 1 {
		t.Fatal("wrong per-item counts")
	}
	entries := got.After["mappings"].([]operationaudit.Fields)
	if len(entries[1]) != 2 || entries[1]["success"] != false {
		t.Fatalf("unexpected failure fields: %+v", entries[1])
	}
	before := got.Before["mappings"].([]operationaudit.Fields)
	if before[0]["mapped"] != true || before[1]["mapped"] != false {
		t.Fatal("incorrect mapping before state")
	}
}

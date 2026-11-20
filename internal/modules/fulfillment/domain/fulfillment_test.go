package domain

import (
	"github.com/dujiao-next/internal/shared/jsonmap"
	"reflect"
	"testing"
)

func TestCustomerDeliveryPreservesAccountsAndStoredReceipt(t *testing.T) {
	payload := "account@gmail.com----password----recovery@example.com----JBSWY3DPEHPK3PXP\n账号ID：ORDERACCOUNT\n订单号：DJ123\n上游订单：ORDERPRIVATE"
	f := &Fulfillment{Type: "manual", Payload: payload, LogisticsJSON: jsonmap.JSON{"upstream_order_no": "ORDERPRIVATE", "account_id": "ORDERACCOUNT", "order_id": "CUSTOMER", "tracking_no": "TRACK", "nested": []interface{}{map[string]interface{}{"supplier_order_id": "PRIVATE", "content": "credential\nUpstream Order ID: PRIVATE"}}}}
	want := "account@gmail.com----password----recovery@example.com----JBSWY3DPEHPK3PXP\n账号ID：ORDERACCOUNT\n订单号：DJ123"
	if got := f.CustomerPayload(); got != want {
		t.Fatalf("payload=%q", got)
	}
	data := f.CustomerDeliveryData()
	if _, ok := data["upstream_order_no"]; ok {
		t.Fatal("internal metadata escaped")
	}
	if data["account_id"] != "ORDERACCOUNT" || data["order_id"] != "CUSTOMER" || data["tracking_no"] != "TRACK" {
		t.Fatal("customer identifiers lost")
	}
	nested := data["nested"].([]interface{})[0].(jsonmap.JSON)
	if !reflect.DeepEqual(nested, jsonmap.JSON{"content": "credential"}) {
		t.Fatalf("nested=%v", nested)
	}
	nested["content"] = "changed"
	if f.Payload != payload || f.LogisticsJSON["upstream_order_no"] != "ORDERPRIVATE" || f.LogisticsJSON["nested"].([]interface{})[0].(map[string]interface{})["content"] != "credential\nUpstream Order ID: PRIVATE" {
		t.Fatal("stored audit data mutated")
	}
}
func TestCustomerPayloadOnlyRemovesExplicitMetadata(t *testing.T) {
	for _, payload := range []string{"ORDERBARE", "account: upstream_order_id", "user----password----ORDERID", "使用订单号查询", "id: 123", "password：https://customer.example/a:b"} {
		f := &Fulfillment{Payload: payload}
		if f.CustomerPayload() != payload {
			t.Fatalf("changed customer data %q", payload)
		}
	}
	for _, label := range []string{"上游单号", "上游订单号", "Upstream Order ID", "upstream_order_no", "supplier-order-id"} {
		f := &Fulfillment{Payload: "credential\n" + label + ": PRIVATE"}
		if f.CustomerPayload() != "credential" {
			t.Fatalf("leaked %s", label)
		}
	}
}

func TestCustomerPayloadUsesFirstDelimiter(t *testing.T) {
	f := &Fulfillment{Payload: "credential\n上游订单：https://supplier.example/order"}
	if got := f.CustomerPayload(); got != "credential" {
		t.Fatalf("output=%q", got)
	}
}

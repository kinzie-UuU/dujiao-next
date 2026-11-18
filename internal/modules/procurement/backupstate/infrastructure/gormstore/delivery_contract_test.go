package gormstore

import (
	"testing"

	backupstate "github.com/dujiao-next/internal/modules/procurement/backupstate"
)

func TestParseVerifiedDeliveryPayloadMuseAndPlus(t *testing.T) {
	muse := []byte(`{"success":true,"order":{"orderCode":"ORDER1","status":"completed","productId":"6aeedc6cadf360530f5faf80","productType":"account","quantity":1,"bonusQuantity":0,"finalQuantity":1},"payment":{"amount":"0.96","currency":"USD"},"delivery":{"accounts":[{"user":"muse@example.com","password":"https://mail.example/access/abc"}]}}`)
	got, err := parseVerifiedDeliveryPayload(backupstate.Muse, "6aeedc6cadf360530f5faf80", 1, muse)
	if err != nil || got != "muse@example.com----https://mail.example/access/abc" {
		t.Fatalf("muse: %q %v", got, err)
	}
	plus := []byte(`{"success":true,"order":{"orderCode":"ORDER2","status":"completed","productId":"6a4b4d7d1827db4ca39bb8be","productType":"account","quantity":1,"bonusQuantity":0,"finalQuantity":1},"payment":{"amount":"6.51","currency":"USD"},"delivery":{"accounts":[{"user":"plus@example.com","password":"secret-pass","otherInfo":"JBSWY3DPEHPK3PXP","expiryText":"2026-12-18"}]}}`)
	got, err = parseVerifiedDeliveryPayload(backupstate.Plus, "6a4b4d7d1827db4ca39bb8be", 1, plus)
	if err != nil || got != "plus@example.com----secret-pass----JBSWY3DPEHPK3PXP----2026-12-18" {
		t.Fatalf("plus: %q %v", got, err)
	}
	if _, err := parseVerifiedDeliveryPayload(backupstate.Plus, "6a4b4d7d1827db4ca39bb8be", 1, []byte(`{"success":true,"order":{"orderCode":"ORDER3","status":"completed","productId":"6a4b4d7d1827db4ca39bb8be","productType":"account","quantity":1,"bonusQuantity":0,"finalQuantity":1},"payment":{"amount":"6.51","currency":"USD"},"delivery":{"accounts":[{"user":"plus@example.com","password":"secret-pass"}]}}`)); err == nil {
		t.Fatal("plus without 2FA accepted")
	}
}

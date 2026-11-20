package gormstore

import (
	"testing"

	backupstate "github.com/dujiao-next/internal/modules/procurement/backupstate"
)

func TestParseVerifiedDeliveryPayloadMuseAndPlus(t *testing.T) {
	muse := []byte(`{"success":true,"order":{"orderCode":"ORDER1","status":"completed","productId":"6aeedc6cadf360530f5faf80","productType":"account","quantity":1,"bonusQuantity":0,"finalQuantity":1},"payment":{"amount":"0.96","currency":"USD"},"delivery":{"accounts":[{"user":"muse@example.com","password":"https://mail.example/access/abc"}]}}`)
	got, err := parseVerifiedDeliveryPayload(backupstate.Muse, backupstate.UpstreamMuseProductID, 1, backupstate.MusePreflightMaxUSD, muse)
	if err != nil || got != "muse@example.com----https://mail.example/access/abc" {
		t.Fatalf("muse: %q %v", got, err)
	}
	plus := []byte(`{"success":true,"order":{"orderCode":"ORDER2","status":"completed","productId":"6a4b4d7d1827db4ca39bb8be","productType":"account","quantity":1,"bonusQuantity":0,"finalQuantity":1},"payment":{"amount":"6.51","currency":"USD"},"delivery":{"accounts":[{"user":"plus@example.com","password":"secret-pass","otherInfo":"JBSWY3DPEHPK3PXP","expiryText":"2026-12-18"}]}}`)
	got, err = parseVerifiedDeliveryPayload(backupstate.Plus, backupstate.UpstreamPlusProductID, 1, backupstate.PlusPreflightMaxUSD, plus)
	if err != nil || got != "plus@example.com----secret-pass----JBSWY3DPEHPK3PXP----2026-12-18" {
		t.Fatalf("plus: %q %v", got, err)
	}
	if _, err := parseVerifiedDeliveryPayload(backupstate.Plus, backupstate.UpstreamPlusProductID, 1, backupstate.PlusPreflightMaxUSD, []byte(`{"success":true,"order":{"orderCode":"ORDER3","status":"completed","productId":"6a4b4d7d1827db4ca39bb8be","productType":"account","quantity":1,"bonusQuantity":0,"finalQuantity":1},"payment":{"amount":"6.51","currency":"USD"},"delivery":{"accounts":[{"user":"plus@example.com","password":"secret-pass"}]}}`)); err == nil {
		t.Fatal("plus without 2FA accepted")
	}
	over := []byte(`{"success":true,"order":{"orderCode":"ORDER4","status":"completed","productId":"6a4b4d7d1827db4ca39bb8be","productType":"account","quantity":1,"bonusQuantity":0,"finalQuantity":1},"payment":{"amount":"7.01","currency":"USD"},"delivery":{"accounts":[{"user":"plus@example.com","password":"secret-pass","otherInfo":"JBSWY3DPEHPK3PXP"}]}}`)
	if _, err := parseVerifiedDeliveryPayload(backupstate.Plus, backupstate.UpstreamPlusProductID, 1, backupstate.PlusPreflightMaxUSD, over); err == nil {
		t.Fatal("over-cap delivery accepted")
	}
}

func TestGmailDeliveryRecoveryOrTOTP(t *testing.T) {
	for _, tc := range []struct{ account, want string }{
		{`{"user":"aged@gmail.com","password":"secret","verifyEmail":"recovery@example.com"}`, "aged@gmail.com----secret----recovery@example.com"},
		{`{"user":"aged@gmail.com","password":"secret","otherInfo":"JBSWY3DPEHPK3PXP"}`, "aged@gmail.com----secret--------JBSWY3DPEHPK3PXP"},
		{`{"user":"aged@gmail.com","password":"secret","verifyEmail":"JBSWY3DPEHPK3PXP"}`, "aged@gmail.com----secret--------JBSWY3DPEHPK3PXP"},
		{`{"user":"aged@gmail.com","password":"secret"}`, ""},
		{`{"user":"aged@example.com","password":"secret","verifyEmail":"recovery@example.com"}`, ""},
		{`{"user":"aged@gmail.com","password":"secret","verifyEmail":"invalid"}`, ""},
	} {
		got, _, err := formatAccountLine(backupstate.Gmail, []byte(tc.account))
		if tc.want == "" {
			if err == nil {
				t.Fatal("incomplete Gmail accepted")
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("Gmail format: %q %v", got, err)
		}
	}
}

func TestGmailPackedReceiptDelivery(t *testing.T) {
	raw := []byte(`{"success":true,"order":{"orderCode":"ORDER_GMAIL_PACKED","status":"completed","productId":"6ae37dc5ed8e0e40e4de28b7","productType":"account","quantity":1,"bonusQuantity":0,"finalQuantity":1},"payment":{"amount":1.49,"currency":"USD"},"delivery":{"accounts":[{"user":"aged.account@gmail.com|syntheticPass|recovery@example.net|JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP\t\t\t\t\t\tThành phố Huế","password":"","verifyEmail":null,"otherInfo":null,"expiryText":null}]}}`)
	got, err := parseVerifiedDeliveryPayload(backupstate.Gmail, backupstate.UpstreamGmailProductID, 1, backupstate.GmailPreflightMaxUSD, raw)
	if err != nil || got != "aged.account@gmail.com----syntheticPass----recovery@example.net----JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP Thành phố Huế" {
		t.Fatalf("packed Gmail delivery failed: %v", err)
	}
	if _, err := parseVerifiedDeliveryPayload(backupstate.Plus, backupstate.UpstreamGmailProductID, 1, backupstate.GmailPreflightMaxUSD, raw); err == nil {
		t.Fatal("packed format changed Plus contract")
	}
	if _, err := parseVerifiedDeliveryPayload(backupstate.Muse, backupstate.UpstreamGmailProductID, 1, backupstate.GmailPreflightMaxUSD, raw); err == nil {
		t.Fatal("packed format changed Muse contract")
	}
}

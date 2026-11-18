package upstreamfactory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dujiao-next/internal/constants"
	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"
)

type testProvider struct {
	conn       *siteconnectiondomain.Connection
	decrypts   int
	decryptErr error
}

func (p *testProvider) GetByID(uint) (*siteconnectiondomain.Connection, error) { return p.conn, nil }
func (p *testProvider) DecryptSecret(string) (string, error) {
	p.decrypts++
	return "test-private-buyer-key", p.decryptErr
}

type testGate struct {
	enabled bool
	err     error
}

func (g testGate) PurchaseEnabled(context.Context, uint) (bool, error) { return g.enabled, g.err }

func identityForTest(c *siteconnectiondomain.Connection) string {
	data, _ := json.Marshal([]string{fmt.Sprint(c.ID), c.Protocol, c.BaseURL, c.ApiKey, c.ApiSecret})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestFactoryIdentityAndPurchaseGate(t *testing.T) {
	base := siteconnectiondomain.Connection{ID: 4, Protocol: constants.ConnectionProtocolFangfang, BaseURL: "https://canboso.com", ApiKey: "marker", ApiSecret: "test-ciphertext", Status: constants.ConnectionStatusActive}
	expected := identityForTest(&base)
	for _, tc := range []struct {
		name        string
		mutate      func(*siteconnectiondomain.Connection)
		hash        string
		gate        PurchaseGate
		decryptErr  error
		wantClient  bool
		wantDecrypt bool
	}{
		{"valid", nil, expected, testGate{enabled: true}, nil, true, true},
		{"missing gate", nil, expected, nil, nil, false, false},
		{"gate disabled", nil, expected, testGate{}, nil, false, false},
		{"gate error", nil, expected, testGate{err: errors.New("test-private-buyer-key")}, nil, false, false},
		{"disabled connection", func(c *siteconnectiondomain.Connection) { c.Status = "disabled" }, expected, testGate{enabled: true}, nil, false, false},
		{"changed credential", func(c *siteconnectiondomain.Connection) { c.ApiSecret = "other-ciphertext" }, expected, testGate{enabled: true}, nil, false, false},
		{"changed protocol", func(c *siteconnectiondomain.Connection) { c.Protocol = "other" }, expected, testGate{enabled: true}, nil, false, false},
		{"changed origin", func(c *siteconnectiondomain.Connection) { c.BaseURL = "https://example.com" }, expected, testGate{enabled: true}, nil, false, false},
		{"auto price", func(c *siteconnectiondomain.Connection) { c.AutoSyncPrice = true }, expected, testGate{enabled: true}, nil, false, false},
		{"malformed hash", nil, "secret-invalid", testGate{enabled: true}, nil, false, false},
		{"wrong hash", nil, strings.Repeat("0", 64), testGate{enabled: true}, nil, false, false},
		{"decryption error", nil, expected, testGate{enabled: true}, errors.New("test-private-buyer-key"), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := base
			if tc.mutate != nil {
				tc.mutate(&conn)
			}
			p := &testProvider{conn: &conn, decryptErr: tc.decryptErr}
			client, err := (Factory{Connections: p, Gate: tc.gate}).Open(context.Background(), 4, tc.hash)
			if (client != nil) != tc.wantClient || (p.decrypts != 0) != tc.wantDecrypt {
				t.Fatal("identity or gate check failed")
			}
			if tc.wantClient && err != nil {
				t.Fatal("valid factory rejected")
			}
			if !tc.wantClient && (err == nil || strings.Contains(err.Error(), "test-private") || strings.Contains(err.Error(), "ciphertext")) {
				t.Fatal("missing or sensitive factory error")
			}
		})
	}
}

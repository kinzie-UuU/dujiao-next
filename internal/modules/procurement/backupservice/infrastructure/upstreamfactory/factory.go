// Package upstreamfactory provides a fail-closed, connection-bound client
// factory. It is not wired to production; no purchase gate exists there yet.
package upstreamfactory

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dujiao-next/internal/constants"
	"github.com/dujiao-next/internal/modules/procurement/backupservice"
	siteconnectiondomain "github.com/dujiao-next/internal/modules/siteconnection/domain"
	"github.com/dujiao-next/internal/upstream"
)

// ConnectionProvider must load the persisted encrypted connection and decrypt
// only after identity and purchase authorization are verified.
type ConnectionProvider interface {
	GetByID(id uint) (*siteconnectiondomain.Connection, error)
	DecryptSecret(ciphertext string) (string, error)
}

// PurchaseGate is a dedicated, durable authorization switch; no production
// implementation currently exists. Missing, failed or false always denies.
type PurchaseGate interface {
	PurchaseEnabled(ctx context.Context, connectionID uint) (bool, error)
}

type Factory struct {
	Connections ConnectionProvider
	Gate        PurchaseGate
}

// Open creates a client for one exact ciphertext-bound connection snapshot.
// The caller must also synchronize changes to connection status/credentials and
// purchase gate with submission; checking here alone cannot close that race.
// All failure messages are intentionally generic and contain no secret data.
func (f Factory) Open(ctx context.Context, connectionID uint, expectedSHA256 string) (backupservice.Client, error) {
	denied := errors.New("backup purchase connection unavailable")
	if f.Connections == nil || f.Gate == nil || connectionID == 0 || ctx.Err() != nil {
		return nil, denied
	}
	expected, err := hex.DecodeString(expectedSHA256)
	if err != nil || len(expected) != sha256.Size {
		return nil, denied
	}
	conn, err := f.Connections.GetByID(connectionID)
	if err != nil || conn == nil || conn.ID != connectionID || conn.DeletedAt != nil || conn.Status != constants.ConnectionStatusActive || conn.Protocol != constants.ConnectionProtocolFangfang || conn.BaseURL != "https://canboso.com" || conn.ApiSecret == "" || conn.AutoSyncPrice {
		return nil, denied
	}
	identity, err := json.Marshal([]string{fmt.Sprint(conn.ID), conn.Protocol, conn.BaseURL, conn.ApiKey, conn.ApiSecret})
	if err != nil {
		return nil, denied
	}
	actual := sha256.Sum256(identity)
	if subtle.ConstantTimeCompare(actual[:], expected) != 1 {
		return nil, denied
	}
	enabled, err := f.Gate.PurchaseEnabled(ctx, connectionID)
	if err != nil || !enabled || ctx.Err() != nil {
		return nil, denied
	}
	secret, err := f.Connections.DecryptSecret(conn.ApiSecret)
	if err != nil || ctx.Err() != nil {
		return nil, denied
	}
	client, err := upstream.NewFangfangPurchaseClient(&siteconnectiondomain.Connection{ID: conn.ID, Protocol: conn.Protocol, BaseURL: conn.BaseURL, ApiKey: conn.ApiKey, ApiSecret: secret})
	if err != nil {
		return nil, denied
	}
	return client, nil
}

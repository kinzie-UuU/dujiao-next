package application

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"time"

	"github.com/dujiao-next/internal/cache"
	"github.com/google/uuid"
)

// Fixed-size stripes serialize local read/create/send sequences without an
// unbounded per-address mutex map. Redis reserves the interval across replicas.
var verificationSendLocks [256]sync.Mutex

func reserveVerificationSend(ctx context.Context, email, purpose string, interval time.Duration) (func(bool), error) {
	hash := sha256.Sum256([]byte(email + "\x00" + purpose))
	lock := &verificationSendLocks[hash[0]]
	lock.Lock()
	key := fmt.Sprintf("verification-send:%x", hash)
	owner := uuid.NewString()
	reserved, err := cache.SetNX(ctx, key, owner, interval)
	if err != nil || !reserved {
		lock.Unlock()
		if err != nil {
			return nil, err
		}
		return nil, ErrVerifyCodeTooFrequent
	}
	return func(sent bool) {
		if !sent {
			cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = cache.DelIfValue(cleanup, key, owner)
		}
		lock.Unlock()
	}, nil
}

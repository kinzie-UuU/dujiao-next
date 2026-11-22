package gormstore

import (
	"context"
	"errors"
	"testing"

	"gorm.io/gorm"
)

func TestControllerBeginFailsClosedWithoutClientIdentityFence(t *testing.T) {
	for _, controller := range []*Controller{
		nil,
		{},
		{DB: nil},
		{DB: &gorm.DB{}},
		{DB: &gorm.DB{}, Decrypt: func(string) (string, error) { return "", nil }},
	} {
		attempt, started, err := controller.Begin(context.Background(), 1)
		if !errors.Is(err, errSnapshotUnavailable) || started || attempt.ClaimID != 0 {
			t.Fatalf("unsafe begin: %+v %v %v", attempt, started, err)
		}
	}
}

package gormstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/dujiao-next/internal/modules/identity/userauth/challenge"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ChallengeRecord persists only a hash and short-lived counters, never OTPs or tokens.
// The database is authoritative even when Redis is absent, restarted or unavailable.
type ChallengeRecord struct {
	Key       string    `gorm:"primaryKey;size:64"`
	Attempts  int       `gorm:"not null"`
	Consumed  bool      `gorm:"not null"`
	ExpiresAt time.Time `gorm:"not null;index"`
}

func (ChallengeRecord) TableName() string { return "user_2fa_challenges" }

type ChallengeStore struct{ db *gorm.DB }

func NewChallengeStore(db *gorm.DB) *ChallengeStore { return &ChallengeStore{db: db} }
func challengeKey(jti string) string {
	v := sha256.Sum256([]byte(jti))
	return hex.EncodeToString(v[:])
}

// Reserve counts BEFORE verification so concurrent requests cannot bypass the bound.
// Limits apply only to this five-minute challenge, never to the user account.
func (s *ChallengeStore) Reserve(ctx context.Context, jti string) (bool, error) {
	if s == nil || s.db == nil || jti == "" {
		return false, errors.New("challenge storage unavailable")
	}
	db := s.db.WithContext(ctx)
	now := time.Now().UTC()
	// Rows outlive the signed token by at least one minute; cleanup cannot reopen it.
	if err := db.Where("expires_at < ?", now).Delete(&ChallengeRecord{}).Error; err != nil {
		return false, err
	}
	record := ChallengeRecord{Key: challengeKey(jti), Attempts: 1, ExpiresAt: now.Add(challenge.TTL + time.Minute)}
	result := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.Assignments(map[string]interface{}{"attempts": gorm.Expr("user_2fa_challenges.attempts + 1")}),
		Where:     clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "user_2fa_challenges.attempts < ? AND user_2fa_challenges.consumed = ?", Vars: []interface{}{challenge.MaxFailures, false}}}},
	}).Create(&record)
	return result.RowsAffected == 1, result.Error
}

// Consume is a compare-and-set: only one successful verification may issue a session.
func (s *ChallengeStore) Consume(ctx context.Context, jti string) (bool, error) {
	if s == nil || s.db == nil || jti == "" {
		return false, errors.New("challenge storage unavailable")
	}
	result := s.db.WithContext(ctx).Model(&ChallengeRecord{}).Where("key = ? AND consumed = ? AND expires_at > ?", challengeKey(jti), false, time.Now().UTC()).Update("consumed", true)
	return result.RowsAffected == 1, result.Error
}

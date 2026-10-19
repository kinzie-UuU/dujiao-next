package challenge

import "time"

const (
	// Versioned purpose rejects pre-migration challenges without invalidating access sessions.
	PurposeTwoFactor = "2fa_challenge_v2"
	TTL              = 5 * time.Minute
	MaxFailures      = 5
)

func FailureKey(jti string) string {
	return "2fa:challenge:" + jti + ":fails"
}

func RevocationKey(jti string) string {
	return "2fa:challenge:" + jti + ":revoked"
}

package challenge

import "time"

const (
	// Reject pre-migration challenges whose revocations existed only in Redis.
	PurposeTwoFactor = "user_2fa_challenge_v2"
	TTL              = 5 * time.Minute
	MaxFailures      = 5
)

func FailureKey(jti string) string {
	return "2fa:user:challenge:" + jti + ":fails"
}

func RevocationKey(jti string) string {
	return "2fa:user:challenge:" + jti + ":revoked"
}

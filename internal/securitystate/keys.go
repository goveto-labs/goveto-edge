package securitystate

import (
	"crypto/sha256"
	"encoding/hex"
)

func RateCounterKey(siteID, ruleID, value string) string {
	return "rl:" + siteID + ":" + ruleID + ":" + digest(value)
}

func ChallengeKey(token string) string {
	return "challenge:" + digest(token)
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}

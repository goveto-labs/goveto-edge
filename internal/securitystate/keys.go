package securitystate

import (
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
)

func RateCounterKey(siteID, ruleID, value string) string {
	return "rl:" + siteID + ":" + ruleID + ":" + digest(value)
}

func RateBlockKey(siteID, ruleID, value string) string {
	return "block:rate:" + siteID + ":" + ruleID + ":" + digest(value)
}

func ChallengeKey(token string) string {
	return "challenge:" + digest(token)
}

func GlobalBlockKey(address netip.Addr) string {
	return "block:global:" + digest(address.String())
}

func SiteBlockKey(siteID string, address netip.Addr) string {
	return "block:site:" + siteID + ":" + digest(address.String())
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}

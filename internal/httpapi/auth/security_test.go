package auth

import (
	"testing"
	"time"

	"goveto-edge/internal/storage/gen/model"
)

func TestRecoveryCodesAreUniqueAndStoredAsHashes(t *testing.T) {
	codes, hashes, err := newRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != recoveryCodeCount || len(hashes) != recoveryCodeCount {
		t.Fatalf("codes=%d hashes=%d", len(codes), len(hashes))
	}
	seen := map[string]bool{}
	for index, code := range codes {
		if seen[code] || code == hashes[index] || hashRecoveryCode(code) != hashes[index] {
			t.Fatalf("invalid recovery code pair %q / %q", code, hashes[index])
		}
		seen[code] = true
	}
}

func TestHasTOTPAndLoginDelay(t *testing.T) {
	secret := "secret"
	if hasTOTP(nil) || hasTOTP(&model.User{}) || !hasTOTP(&model.User{TotpSecret: &secret}) {
		t.Fatal("unexpected TOTP state classification")
	}
	if loginFailureDelay(5) <= loginFailureDelay(0) || loginFailureDelay(50) != loginFailureDelay(5) {
		t.Fatal("login failure delay is not increasing and capped")
	}
}

func TestPasswordResetHashIsStableAndDoesNotExposeToken(t *testing.T) {
	first := passwordResetHash("reset-token")
	if first != passwordResetHash("reset-token") || first == "reset-token" {
		t.Fatalf("password reset hash = %q", first)
	}
}

func TestPasswordResetTTLIsBounded(t *testing.T) {
	if passwordResetTTL <= 0 || passwordResetTTL > time.Hour {
		t.Fatalf("password reset TTL = %s", passwordResetTTL)
	}
}

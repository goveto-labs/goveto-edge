package waf

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/scrypt"

	"goveto-edge/internal/policy"
)

const (
	powVersion        = 3
	powAlgorithm      = "SCRYPT-HMAC"
	powCounterMinimum = uint32(24)
	powCounterMaximum = uint32(40)
	powScryptN        = 16_384
	powScryptR        = 8
	powScryptP        = 1
	powKeyLength      = 32
	powTargetLength   = 16
	challengeTTL      = 2 * time.Minute
	clearanceTTL      = 30 * time.Minute
)

var (
	powDomain           = []byte("goveto-edge/waf-scrypt/v3\x00")
	powWorkerSourceJSON = template.JS(strconv.Quote(mustEmbeddedFile("templates/pow-worker.js")))
	powGenerationSlots  = make(chan struct{}, 2)
	powChallengeCache   sync.Map
	powCacheOperations  atomic.Uint64
)

type challengeClaim struct {
	Version      int    `json:"v"`
	Kind         string `json:"kind"`
	SiteID       string `json:"site_id"`
	GroupID      string `json:"group_id"`
	Binding      string `json:"binding"`
	IssuedAt     int64  `json:"issued_at"`
	ExpiresAt    int64  `json:"expires_at"`
	Algorithm    string `json:"algorithm,omitempty"`
	Nonce        string `json:"nonce,omitempty"`
	Salt         string `json:"salt,omitempty"`
	Target       string `json:"target,omitempty"`
	KeySignature string `json:"key_signature,omitempty"`
	MaxCounter   uint32 `json:"max_counter,omitempty"`
	ScryptN      int    `json:"scrypt_n,omitempty"`
	ScryptR      int    `json:"scrypt_r,omitempty"`
	ScryptP      int    `json:"scrypt_p,omitempty"`
	Environment  string `json:"environment,omitempty"`
	Stateful     bool   `json:"stateful,omitempty"`
}

type challengeSolution struct {
	Counter     uint32             `json:"counter"`
	Key         string             `json:"key"`
	Environment browserEnvironment `json:"environment"`
}

type cachedChallenge struct {
	token     string
	expiresAt int64
}

func mustEmbeddedFile(name string) string {
	value, err := pageFiles.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return string(value)
}

func decodeChallengeSecret(encoded string) ([]byte, error) {
	secret, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(secret) < 32 {
		return nil, errors.New("challenge_secret must be a base64url-encoded 32-byte secret when CAPTCHA is enabled")
	}
	return append([]byte(nil), secret...), nil
}

func (h Handler) hasCaptchaGroup() bool {
	if !h.WAF.Enabled {
		return false
	}
	for _, group := range h.WAF.Groups {
		if group.Enabled && group.Action == policy.WAFActionCaptcha {
			return true
		}
	}
	return false
}

func (h Handler) challengeToken(groupID string, r *http.Request, ip string) (string, error) {
	cacheKey := h.challengeCacheKey(groupID, r, ip)
	now := time.Now()
	if value, ok := powChallengeCache.Load(cacheKey); ok {
		cached := value.(cachedChallenge)
		if cached.expiresAt > now.Unix()+5 {
			return cached.token, nil
		}
		powChallengeCache.Delete(cacheKey)
	}

	nonce := make([]byte, 16)
	salt := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	span := new(big.Int).SetUint64(uint64(powCounterMaximum - powCounterMinimum + 1))
	offset, err := rand.Int(rand.Reader, span)
	if err != nil {
		return "", err
	}
	counter := powCounterMinimum + uint32(offset.Uint64())
	powGenerationSlots <- struct{}{}
	key, err := deriveScryptKey(nonce, salt, counter, powScryptN, powScryptR, powScryptP)
	<-powGenerationSlots
	if err != nil {
		return "", err
	}
	target := base64.RawURLEncoding.EncodeToString(key[:powTargetLength])
	derivedKeySignature := keySignature(h.challengeKey, counter, key)
	for index := range key {
		key[index] = 0
	}
	issuedAt := now.Unix()
	expiresAt := now.Add(challengeTTL).Unix()
	claim := challengeClaim{
		Version: powVersion, Kind: "challenge", SiteID: h.SiteID, GroupID: groupID,
		Binding: requestBinding(h.challengeKey, r, ip), IssuedAt: issuedAt, ExpiresAt: expiresAt,
		Algorithm: powAlgorithm, Nonce: base64.RawURLEncoding.EncodeToString(nonce),
		Salt:   base64.RawURLEncoding.EncodeToString(salt),
		Target: target, KeySignature: derivedKeySignature, MaxCounter: powCounterMaximum,
		ScryptN: powScryptN, ScryptR: powScryptR, ScryptP: powScryptP,
		Stateful: true,
	}
	token, err := h.signClaim(claim)
	if err != nil {
		return "", err
	}
	store, stateful := h.distributed.(challengeStateStore)
	if !stateful || store.PutChallenge(r.Context(), token, challengeTTL) != nil {
		claim.Stateful = false
		token, err = h.signClaim(claim)
		if err != nil {
			return "", err
		}
	}
	powChallengeCache.Store(cacheKey, cachedChallenge{token: token, expiresAt: expiresAt})
	h.pruneChallengeCache(now)
	return token, nil
}

func (h Handler) challengeCacheKey(groupID string, r *http.Request, ip string) string {
	mac := hmac.New(sha256.New, h.challengeKey)
	_, _ = io.WriteString(mac, "challenge-cache\x00")
	_, _ = io.WriteString(mac, h.SiteID)
	_, _ = io.WriteString(mac, "\x00")
	_, _ = io.WriteString(mac, groupID)
	_, _ = io.WriteString(mac, "\x00")
	_, _ = io.WriteString(mac, requestBinding(h.challengeKey, r, ip))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (h Handler) pruneChallengeCache(now time.Time) {
	if powCacheOperations.Add(1)%256 != 0 {
		return
	}
	powChallengeCache.Range(func(key, value any) bool {
		if value.(cachedChallenge).expiresAt <= now.Unix() {
			powChallengeCache.Delete(key)
		}
		return true
	})
}

func deriveScryptKey(nonce, salt []byte, counter uint32, n, r, p int) ([]byte, error) {
	password := make([]byte, len(powDomain)+len(nonce)+4)
	copy(password, powDomain)
	copy(password[len(powDomain):], nonce)
	binary.BigEndian.PutUint32(password[len(password)-4:], counter)
	return scrypt.Key(password, salt, n, r, p, powKeyLength)
}

func keySignature(secret []byte, counter uint32, key []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = io.WriteString(mac, "derived-key\x00")
	var encodedCounter [4]byte
	binary.BigEndian.PutUint32(encodedCounter[:], counter)
	_, _ = mac.Write(encodedCounter[:])
	_, _ = mac.Write(key)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (h Handler) completeChallenge(w http.ResponseWriter, r *http.Request, groupID, ip string) bool {
	token := r.URL.Query().Get("__goveto_challenge")
	proof := r.URL.Query().Get("__goveto_proof")
	if token == "" || proof == "" || len(token) > 4096 || len(proof) > 8192 {
		return false
	}
	claim, ok := h.verifyClaim(token)
	assessment, valid := h.validChallengeClaim(claim, r, groupID, ip, proof)
	if !ok || !valid || !assessment.Accepted {
		w.Header().Set("X-Goveto-WAF-Challenge", "rejected")
		return false
	}
	if claim.Stateful {
		if store, ok := h.distributed.(challengeStateStore); ok {
			consumed, err := store.ConsumeChallenge(r.Context(), token)
			if err == nil {
				powChallengeCache.Delete(h.challengeCacheKey(groupID, r, ip))
			}
			if err == nil && !consumed {
				w.Header().Set("X-Goveto-WAF-Challenge", "replayed")
				return false
			}
		}
	}
	now := time.Now()
	environment, _ := json.Marshal(assessment)
	environmentHash := sha256.Sum256(environment)
	clearance, err := h.signClaim(challengeClaim{
		Version: powVersion, Kind: "clearance", SiteID: h.SiteID, GroupID: groupID,
		Binding: requestBinding(h.challengeKey, r, ip), IssuedAt: now.Unix(),
		ExpiresAt:   now.Add(clearanceTTL).Unix(),
		Environment: base64.RawURLEncoding.EncodeToString(environmentHash[:16]),
	})
	if err != nil {
		return false
	}
	http.SetCookie(w, &http.Cookie{
		Name: captchaCookieName(h.SiteID, groupID), Value: clearance, Path: "/",
		MaxAge: int(clearanceTTL.Seconds()), HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode,
	})
	clean := *r.URL
	query := clean.Query()
	query.Del("__goveto_challenge")
	query.Del("__goveto_proof")
	clean.RawQuery = query.Encode()
	http.Redirect(w, r, clean.String(), http.StatusSeeOther)
	return true
}

func (h Handler) validChallengeClaim(claim challengeClaim, r *http.Request, groupID, ip, encodedProof string) (environmentAssessment, bool) {
	rejected := environmentAssessment{Accepted: false}
	if claim.Version != powVersion || claim.Kind != "challenge" || claim.Algorithm != powAlgorithm ||
		claim.SiteID != h.SiteID || claim.GroupID != groupID ||
		claim.Binding != requestBinding(h.challengeKey, r, ip) || claim.MaxCounter != powCounterMaximum ||
		claim.ScryptN != powScryptN || claim.ScryptR != powScryptR || claim.ScryptP != powScryptP {
		return rejected, false
	}
	proof, err := decodeChallengeSolution(encodedProof)
	if err != nil || proof.Counter > claim.MaxCounter {
		return rejected, false
	}
	key, err := base64.RawURLEncoding.DecodeString(proof.Key)
	if err != nil || len(key) != powKeyLength {
		return rejected, false
	}
	target, err := base64.RawURLEncoding.DecodeString(claim.Target)
	if err != nil || len(target) != powTargetLength || !hmac.Equal(key[:powTargetLength], target) {
		return rejected, false
	}
	if !hmac.Equal([]byte(keySignature(h.challengeKey, proof.Counter, key)), []byte(claim.KeySignature)) {
		return rejected, false
	}
	assessment := assessBrowserEnvironment(r, proof.Environment)
	return assessment, true
}

func decodeChallengeSolution(encoded string) (challengeSolution, error) {
	var solution challengeSolution
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return solution, err
	}
	if err = json.Unmarshal(payload, &solution); err != nil {
		return solution, err
	}
	return solution, nil
}

func encodeChallengeSolution(solution challengeSolution) (string, error) {
	payload, err := json.Marshal(solution)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func (h Handler) hasClearance(r *http.Request, groupID, ip string) bool {
	cookie, err := r.Cookie(captchaCookieName(h.SiteID, groupID))
	if err != nil || len(cookie.Value) > 4096 {
		return false
	}
	claim, ok := h.verifyClaim(cookie.Value)
	return ok && claim.Version == powVersion && claim.Kind == "clearance" &&
		claim.SiteID == h.SiteID && claim.GroupID == groupID &&
		claim.Binding == requestBinding(h.challengeKey, r, ip)
}

func captchaCookieName(siteID, groupID string) string {
	sum := sha256.Sum256([]byte(siteID + "\x00" + groupID))
	return "__goveto_waf_" + base64.RawURLEncoding.EncodeToString(sum[:9])
}

func requestBinding(key []byte, r *http.Request, ip string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = io.WriteString(mac, "request-binding\x00")
	_, _ = io.WriteString(mac, ip)
	_, _ = io.WriteString(mac, "\x00")
	_, _ = io.WriteString(mac, r.UserAgent())
	_, _ = io.WriteString(mac, "\x00")
	_, _ = io.WriteString(mac, strings.TrimSpace(r.Header.Get("Accept-Language")))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
}

func (h Handler) signClaim(claim challengeClaim) (string, error) {
	if len(h.challengeKey) < 32 {
		return "", errors.New("WAF challenge key is unavailable")
	}
	payload, err := json.Marshal(claim)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, h.challengeKey)
	_, _ = io.WriteString(mac, encoded)
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (h Handler) verifyClaim(token string) (challengeClaim, bool) {
	var claim challengeClaim
	parts := strings.Split(token, ".")
	if len(parts) != 2 || len(h.challengeKey) < 32 {
		return claim, false
	}
	mac := hmac.New(sha256.New, h.challengeKey)
	_, _ = io.WriteString(mac, parts[0])
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || base64.RawURLEncoding.EncodeToString(signature) != parts[1] || !hmac.Equal(signature, mac.Sum(nil)) {
		return claim, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != parts[0] || json.Unmarshal(payload, &claim) != nil {
		return challengeClaim{}, false
	}
	now := time.Now().Unix()
	if claim.Version != powVersion || claim.IssuedAt > now+30 || claim.ExpiresAt < now ||
		claim.ExpiresAt < claim.IssuedAt || claim.ExpiresAt-claim.IssuedAt > int64(clearanceTTL/time.Second)+30 {
		return challengeClaim{}, false
	}
	return claim, true
}

func decodeChallengeForSolver(token string) (challengeClaim, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return challengeClaim{}, fmt.Errorf("invalid challenge token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return challengeClaim{}, err
	}
	var claim challengeClaim
	if err = json.Unmarshal(payload, &claim); err != nil {
		return challengeClaim{}, err
	}
	return claim, nil
}

package node

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"goveto-edge/internal/storage/gen/client"
	"goveto-edge/internal/storage/gen/model"
	"goveto-edge/internal/storage/gen/query"
)

// Stable error markers for host-key failures. Installation must not retry until
// an operator explicitly trusts the presented key.
var (
	errHostKeyMismatch  = errors.New("SSH host key mismatch")
	errHostKeyNotPinned = errors.New("SSH host key not pinned")
)

// HostKeyMismatchError reports a pinned host key that differs from the key the
// server presented. It unwraps to errPermanentInstallConfiguration so the
// installation worker stops retrying until the new key is re-trusted.
type HostKeyMismatchError struct {
	Expected string
	Actual   string
}

func (e HostKeyMismatchError) Error() string {
	return fmt.Sprintf(
		"SSH host key changed: pinned %s, server presented %s; verify the host out-of-band and re-trust the key",
		e.Expected, e.Actual,
	)
}

func (e HostKeyMismatchError) Unwrap() []error {
	return []error{errHostKeyMismatch, errPermanentInstallConfiguration}
}

// HostKeyNotPinnedError reports that a known node has no pinned host key yet.
// Operators must preview and trust the key explicitly; install paths never
// perform silent trust-on-first-use.
type HostKeyNotPinnedError struct{}

func (HostKeyNotPinnedError) Error() string {
	return "no pinned SSH host key; verify the host out-of-band and trust the key before connecting"
}

func (HostKeyNotPinnedError) Unwrap() []error {
	return []error{errHostKeyNotPinned, errPermanentInstallConfiguration}
}

// IsHostKeyMismatch reports whether err is a host key verification failure.
func IsHostKeyMismatch(err error) bool {
	return errors.Is(err, errHostKeyMismatch)
}

// IsHostKeyNotPinned reports whether err means the node has no trusted key yet.
func IsHostKeyNotPinned(err error) bool {
	return errors.Is(err, errHostKeyNotPinned)
}

// IsHostKeyIssue reports mismatch or missing-pin failures that need operator trust.
func IsHostKeyIssue(err error) bool {
	return IsHostKeyMismatch(err) || IsHostKeyNotPinned(err)
}

// HostKeySettings configures how an SSH client verifies the remote host key.
type HostKeySettings struct {
	Callback   ssh.HostKeyCallback
	Algorithms []string
}

func hostKeyParts(key ssh.PublicKey) (keyType, publicKey, fingerprint string) {
	return key.Type(),
		strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))),
		ssh.FingerprintSHA256(key)
}

// SSHHostKeySummary returns the algorithm and SHA256 fingerprint of a host
// key for operator display.
func SSHHostKeySummary(key ssh.PublicKey) (keyType, fingerprint string) {
	keyType, _, fingerprint = hostKeyParts(key)
	return keyType, fingerprint
}

// DescribeHostKey returns the algorithm, authorized_keys public key material
// and SHA256 fingerprint used when pinning a host key.
func DescribeHostKey(key ssh.PublicKey) (keyType, publicKey, fingerprint string) {
	return hostKeyParts(key)
}

// checkPresentedHostKey compares a presented key against a required pin.
func checkPresentedHostKey(pinned *model.NodeSSHHostKey, key ssh.PublicKey) error {
	if pinned == nil {
		return HostKeyNotPinnedError{}
	}
	_, publicKey, fingerprint := hostKeyParts(key)
	if pinned.PublicKey == publicKey {
		return nil
	}
	return HostKeyMismatchError{Expected: pinned.FingerprintSha256, Actual: fingerprint}
}

// PinnedHostKeyVerifier returns SSH host-key settings that enforce the node's
// pinned key. Nodes without a pin are rejected so operators must trust the key
// explicitly. HostKeyAlgorithms is restricted to the pinned key type to avoid
// false mismatches when the server offers multiple host-key algorithms.
func PinnedHostKeyVerifier(ctx context.Context, db *client.Client, nodeID string) (HostKeySettings, error) {
	pinned, err := LoadPinnedHostKey(ctx, db, nodeID)
	if err != nil {
		return HostKeySettings{}, fmt.Errorf("load pinned SSH host key: %w", err)
	}
	if pinned == nil {
		return HostKeySettings{}, HostKeyNotPinnedError{}
	}
	nodeIDCopy := nodeID
	pinnedCopy := *pinned
	return HostKeySettings{
		Algorithms: []string{pinned.KeyType},
		Callback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if err := checkPresentedHostKey(&pinnedCopy, key); err != nil {
				return err
			}
			_, _ = db.NodeSSHHostKey.Update().
				Where(query.NodeSSHHostKey.NodeId.Equals(nodeIDCopy)).
				Set(query.NodeSSHHostKey.LastVerifiedAt.Set(time.Now().UTC())).
				Do(ctx)
			return nil
		},
	}, nil
}

// CaptureHostKey returns host-key settings that record the presented key
// without verifying it, plus a function to read the recording. Use it only for
// explicit trust establishment: node creation, connection tests and operator
// re-trust flows.
func CaptureHostKey() (HostKeySettings, func() ssh.PublicKey) {
	var mu sync.Mutex
	var captured ssh.PublicKey
	return HostKeySettings{
			Callback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
				mu.Lock()
				captured = key
				mu.Unlock()
				return nil
			},
		}, func() ssh.PublicKey {
			mu.Lock()
			defer mu.Unlock()
			return captured
		}
}

// PinHostKey upserts the pinned SSH host key of a node.
func PinHostKey(ctx context.Context, db *client.Client, nodeID string, key ssh.PublicKey) (*model.NodeSSHHostKey, error) {
	keyType, publicKey, fingerprint := hostKeyParts(key)
	now := time.Now().UTC()
	created, err := db.NodeSSHHostKey.UpsertOne(
		ctx,
		query.NodeSSHHostKey.NodeId.Equals(nodeID),
		[]query.NodeSSHHostKeySetClause{
			query.NodeSSHHostKey.NodeId.Set(nodeID),
			query.NodeSSHHostKey.KeyType.Set(keyType),
			query.NodeSSHHostKey.PublicKey.Set(publicKey),
			query.NodeSSHHostKey.FingerprintSha256.Set(fingerprint),
			query.NodeSSHHostKey.FirstSeenAt.Set(now),
			query.NodeSSHHostKey.LastVerifiedAt.Set(now),
		},
		[]query.NodeSSHHostKeySetClause{
			query.NodeSSHHostKey.KeyType.Set(keyType),
			query.NodeSSHHostKey.PublicKey.Set(publicKey),
			query.NodeSSHHostKey.FingerprintSha256.Set(fingerprint),
			query.NodeSSHHostKey.LastVerifiedAt.Set(now),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("store pinned SSH host key: %w", err)
	}
	if created != nil {
		return created, nil
	}
	loaded, err := LoadPinnedHostKey(ctx, db, nodeID)
	if err != nil {
		return nil, err
	}
	if loaded == nil {
		return nil, fmt.Errorf("store pinned SSH host key: upsert returned no row")
	}
	return loaded, nil
}

// LoadPinnedHostKey returns the node's pinned SSH host key, or nil when the
// node has not been pinned yet.
func LoadPinnedHostKey(ctx context.Context, db *client.Client, nodeID string) (*model.NodeSSHHostKey, error) {
	pinned, err := db.NodeSSHHostKey.FindUnique(ctx, query.NodeSSHHostKey.NodeId.Equals(nodeID))
	if err != nil {
		return nil, err
	}
	return pinned, nil
}

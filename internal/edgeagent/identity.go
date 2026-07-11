package edgeagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

type Identity struct {
	NodeID           string `json:"node_id"`
	CommunicationKey string `json:"communication_key"`
}

func LoadIdentity(path string) (Identity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Identity{}, fmt.Errorf("read agent identity: %w", err)
	}
	var identity Identity
	if err := json.Unmarshal(data, &identity); err != nil {
		return Identity{}, fmt.Errorf("decode agent identity: %w", err)
	}
	if identity.NodeID == "" || identity.CommunicationKey == "" {
		return Identity{}, errors.New("agent identity is incomplete")
	}
	if _, err := uuid.Parse(identity.NodeID); err != nil {
		return Identity{}, errors.New("agent node_id must be a UUID")
	}
	return identity, nil
}

func WriteIdentity(path string, identity Identity) error {
	if identity.NodeID == "" || identity.CommunicationKey == "" {
		return errors.New("agent identity is incomplete")
	}
	if _, err := uuid.Parse(identity.NodeID); err != nil {
		return errors.New("agent node_id must be a UUID")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

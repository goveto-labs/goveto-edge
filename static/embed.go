// Package static contains artifacts embedded into the control-plane binary.
package static

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

var ErrAgentArtifactsUnavailable = errors.New("embedded agent artifacts are unavailable")

type AgentArtifact struct {
	Version string
	SHA256  string
	Binary  []byte
}

type agentManifest struct {
	Version   string `json:"version"`
	Artifacts map[string]struct {
		SHA256 string `json:"sha256"`
	} `json:"artifacts"`
}

var manifestState struct {
	sync.Once
	value agentManifest
	err   error
}

func loadAgentManifest() (agentManifest, error) {
	manifestState.Do(func() {
		if !agentArtifactsEmbedded {
			manifestState.err = ErrAgentArtifactsUnavailable
			return
		}
		data, err := artifactFiles.ReadFile("agent/manifest.json")
		if err == nil {
			err = json.Unmarshal(data, &manifestState.value)
		}
		if err == nil && (manifestState.value.Version == "" || len(manifestState.value.Artifacts) == 0) {
			err = fmt.Errorf("embedded agent manifest is incomplete")
		}
		manifestState.err = err
	})
	return manifestState.value, manifestState.err
}

func AgentVersion() (string, error) {
	manifest, err := loadAgentManifest()
	return manifest.Version, err
}

func Agent(goarch string) (AgentArtifact, error) {
	state, err := agentStateFor(goarch)
	if err != nil {
		return AgentArtifact{}, err
	}
	state.Do(func() {
		state.value, state.err = loadAgent(goarch)
	})
	return state.value, state.err
}

type cachedAgent struct {
	sync.Once
	value AgentArtifact
	err   error
}

var (
	amd64Agent cachedAgent
	arm64Agent cachedAgent
)

func agentStateFor(goarch string) (*cachedAgent, error) {
	switch goarch {
	case "amd64":
		return &amd64Agent, nil
	case "arm64":
		return &arm64Agent, nil
	default:
		return nil, fmt.Errorf("unsupported edge agent architecture %q", goarch)
	}
}

func loadAgent(goarch string) (AgentArtifact, error) {
	manifest, err := loadAgentManifest()
	if err != nil {
		return AgentArtifact{}, fmt.Errorf("load embedded agent manifest: %w", err)
	}
	metadata, found := manifest.Artifacts[goarch]
	if !found || metadata.SHA256 == "" {
		return AgentArtifact{}, fmt.Errorf("embedded edge agent metadata for %s is missing", goarch)
	}
	data, err := artifactFiles.ReadFile("agent/agent-linux-" + goarch)
	if err != nil {
		return AgentArtifact{}, fmt.Errorf("embedded edge agent for %s (run script/build_agent.sh): %w", goarch, err)
	}
	if len(data) == 0 {
		return AgentArtifact{}, fmt.Errorf("embedded edge agent for %s is empty (run script/build_agent.sh)", goarch)
	}
	digest := sha256.Sum256(data)
	actual := hex.EncodeToString(digest[:])
	if actual != metadata.SHA256 {
		return AgentArtifact{}, fmt.Errorf("embedded edge agent for %s failed SHA-256 verification", goarch)
	}
	return AgentArtifact{Version: manifest.Version, SHA256: actual, Binary: data}, nil
}

func AgentBinary(goarch string) ([]byte, error) {
	artifact, err := Agent(goarch)
	if err != nil {
		return nil, err
	}
	return artifact.Binary, nil
}

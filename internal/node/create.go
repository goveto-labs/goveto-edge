// Package node defines edge-node provisioning inputs and workflows.
package node

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

// CreateInput contains persistent node metadata and one-time SSH installation
// credentials. Password and private key values must never be written to the
// node tables or logs.
type CreateInput struct {
	ClusterID  string          `json:"-"`
	Name       string          `json:"name"`
	Addresses  []string        `json:"addresses"`
	DNSLineIDs []string        `json:"dns_line_ids"`
	GroupIDs   []string        `json:"group_ids"`
	RegionIDs  []string        `json:"region_ids"`
	SSH        SSHInstallInput `json:"ssh"`
}

type SSHInstallInput struct {
	EntryIP       string `json:"entry_ip"`
	Port          uint16 `json:"port"`
	User          string `json:"user"`
	Password      string `json:"password,omitempty"`
	PrivateKeyPEM string `json:"private_key,omitempty"`
	Passphrase    string `json:"passphrase,omitempty"`
}

type InstallPayload struct {
	NodeID           string          `json:"node_id"`
	CommunicationKey string          `json:"communication_key"`
	SSH              SSHInstallInput `json:"ssh"`
}

func (i *CreateInput) Validate() error {
	i.Name = strings.TrimSpace(i.Name)
	if i.ClusterID == "" || i.Name == "" {
		return errors.New("cluster_id and name are required")
	}
	if len(i.Addresses) == 0 {
		return errors.New("at least one node address is required")
	}
	seen := make(map[string]struct{}, len(i.Addresses))
	for index, address := range i.Addresses {
		address = strings.TrimSpace(address)
		if net.ParseIP(address) == nil {
			return fmt.Errorf("invalid node address %q", address)
		}
		if _, exists := seen[address]; exists {
			return fmt.Errorf("duplicate node address %q", address)
		}
		seen[address] = struct{}{}
		i.Addresses[index] = address
	}
	if err := i.SSH.Validate(); err != nil {
		return err
	}
	return nil
}

func (s *SSHInstallInput) Validate() error {
	s.EntryIP = strings.TrimSpace(s.EntryIP)
	s.User = strings.TrimSpace(s.User)
	if net.ParseIP(s.EntryIP) == nil {
		return errors.New("ssh.entry_ip must be a valid IP address")
	}
	if s.Port == 0 {
		s.Port = 22
	}
	if s.User == "" {
		return errors.New("ssh.user is required")
	}
	if s.UsesPassword() == s.UsesPrivateKey() {
		return errors.New("exactly one of ssh.password or ssh.private_key is required")
	}
	return nil
}

func (s SSHInstallInput) UsesPassword() bool {
	return s.Password != ""
}

func (s SSHInstallInput) UsesPrivateKey() bool {
	return s.PrivateKeyPEM != ""
}

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
	GroupID    *string         `json:"group_id"`
	RegionID   *string         `json:"region_id"`
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
	if net.ParseIP(strings.TrimSpace(i.SSH.EntryIP)) == nil {
		return errors.New("ssh.entry_ip must be a valid IP address")
	}
	if i.SSH.Port == 0 {
		i.SSH.Port = 22
	}
	if strings.TrimSpace(i.SSH.User) == "" {
		return errors.New("ssh.user is required")
	}
	if i.SSH.UsesPassword() == i.SSH.UsesPrivateKey() {
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

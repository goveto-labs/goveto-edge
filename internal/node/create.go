// Package node defines edge-node provisioning inputs and workflows.
package node

// CreateInput contains persistent node metadata and one-time SSH installation
// credentials. Password and private key values must never be written to the
// node tables or logs.
type CreateInput struct {
	ClusterID  string
	Name       string
	Addresses  []string
	DNSLineIDs []string
	GroupID    *string
	RegionID   *string
	SSH        SSHInstallInput
}

type SSHInstallInput struct {
	EntryIP       string
	Port          uint16
	User          string
	Password      string
	PrivateKeyPEM string
	Passphrase    string
}

func (s SSHInstallInput) UsesPassword() bool {
	return s.Password != ""
}

func (s SSHInstallInput) UsesPrivateKey() bool {
	return s.PrivateKeyPEM != ""
}

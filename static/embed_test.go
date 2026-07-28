package static

import "testing"

func TestEmbeddedAgentBinaries(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		binary, err := AgentBinary(arch)
		if err != nil {
			t.Skipf("agent binaries not built: %v", err)
		}
		if len(binary) < 4 || string(binary[:4]) != "\x7fELF" {
			t.Fatalf("%s artifact is not an ELF binary", arch)
		}
	}
}

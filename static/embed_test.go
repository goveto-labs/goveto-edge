package static

import (
	"bytes"
	"debug/elf"
	"testing"
)

func TestEmbeddedAgentBinaries(t *testing.T) {
	tests := map[string]elf.Machine{
		"amd64": elf.EM_X86_64,
		"arm64": elf.EM_AARCH64,
	}
	for arch, machine := range tests {
		t.Run(arch, func(t *testing.T) {
			binary, err := AgentBinary(arch)
			if err != nil {
				t.Fatal(err)
			}
			artifact, err := elf.NewFile(bytes.NewReader(binary))
			if err != nil {
				t.Fatalf("embedded %s agent is not an ELF binary: %v", arch, err)
			}
			defer artifact.Close()
			if artifact.Machine != machine {
				t.Fatalf("embedded %s agent machine = %s; want %s", arch, artifact.Machine, machine)
			}
		})
	}
}

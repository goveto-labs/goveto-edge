package static

import (
	"bytes"
	"debug/elf"
	"errors"
	"testing"
)

func TestEmbeddedAgentBinaries(t *testing.T) {
	tests := map[string]elf.Machine{
		"amd64": elf.EM_X86_64,
		"arm64": elf.EM_AARCH64,
	}
	for arch, machine := range tests {
		t.Run(arch, func(t *testing.T) {
			artifact, err := Agent(arch)
			if errors.Is(err, ErrAgentArtifactsUnavailable) {
				t.Skip("release agent artifacts are not embedded in a development test build")
			}
			if err != nil {
				t.Fatal(err)
			}
			if artifact.Version == "" || len(artifact.SHA256) != 64 {
				t.Fatalf("embedded %s metadata = %+v", arch, artifact)
			}
			binary, err := elf.NewFile(bytes.NewReader(artifact.Binary))
			if err != nil {
				t.Fatalf("embedded %s agent is not an ELF binary: %v", arch, err)
			}
			defer binary.Close()
			if binary.Machine != machine {
				t.Fatalf("embedded %s agent machine = %s; want %s", arch, binary.Machine, machine)
			}
		})
	}
}

package main

import (
	"bytes"
	"testing"

	"github.com/pierrec/lz4/v4"
)

func TestPatternPayloadIsDeterministicAndIncompressible(t *testing.T) {
	const size = 1 << 20
	first := deterministicPayload(size)
	second := deterministicPayload(size)
	if len(first) != size || !bytes.Equal(first, second) {
		t.Fatal("pattern payload is not deterministic")
	}
	compressed := new(bytes.Buffer)
	writer := lz4.NewWriter(compressed)
	if _, err := writer.Write(first); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if compressed.Len() < size*95/100 {
		t.Fatalf("pattern payload compressed to %d bytes from %d", compressed.Len(), size)
	}
}

func TestParsePatternPath(t *testing.T) {
	behavior, size, err := parseRequestPath("/pattern/16777216")
	if err != nil || !behavior.pattern || size != 16<<20 {
		t.Fatalf("behavior=%+v size=%d error=%v", behavior, size, err)
	}
}

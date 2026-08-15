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

func TestPatternSHA256SizeUsesHTTPPayloadBounds(t *testing.T) {
	for _, test := range []struct {
		value string
		ok    bool
	}{
		{value: "0", ok: true},
		{value: "16777216", ok: true},
		{value: "-1"},
		{value: "16777217"},
	} {
		_, err := parseSize(test.value)
		if (err == nil) != test.ok {
			t.Errorf("parseSize(%q) error=%v, want ok=%v", test.value, err, test.ok)
		}
	}
}

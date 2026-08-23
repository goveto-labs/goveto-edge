package static

import (
	"errors"
	"io/fs"
	"testing"
)

func TestEmbeddedConsole(t *testing.T) {
	console, err := ConsoleFS()
	if errors.Is(err, ErrConsoleUnavailable) {
		t.Skip("console artifacts are not embedded in a development test build")
	}
	if err != nil {
		t.Fatal(err)
	}
	index, err := fs.ReadFile(console, "web/dist/index.html")
	if err != nil {
		t.Fatalf("read embedded index.html: %v", err)
	}
	if len(index) == 0 {
		t.Fatal("embedded index.html is empty")
	}
	entries, err := fs.ReadDir(console, "web/dist/assets")
	if err != nil {
		t.Fatalf("read embedded assets directory: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("embedded console has no hashed asset files")
	}
}

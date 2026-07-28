//go:build !windows

package script_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFetchGeoIPDatabaseScript(t *testing.T) {
	root := t.TempDir()
	scriptDir := filepath.Join(root, "script")
	if err := os.MkdirAll(scriptDir, 0700); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("fetch_geoip_db.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(scriptDir, "fetch_geoip_db.sh")
	if err = os.WriteFile(script, source, 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "GeoLite2-City.mmdb")

	t.Run("license key is required", func(t *testing.T) {
		output, runErr := runScript(script, "", "")
		if runErr == nil || !strings.Contains(output, "MAXMIND_LICENSE_KEY is required") {
			t.Fatalf("output=%q err=%v", output, runErr)
		}
	})

	validArchive := filepath.Join(root, "valid.tar.gz")
	writeArchive(t, validArchive, "GeoLite2-City_20990101/GeoLite2-City.mmdb", []byte("new database"))
	if err = os.WriteFile(target, []byte("old database"), 0600); err != nil {
		t.Fatal(err)
	}
	oldInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if output, runErr := runScript(script, "test-key", "file://"+validArchive); runErr != nil {
		t.Fatalf("successful download: output=%q err=%v", output, runErr)
	}
	installed, err := os.ReadFile(target)
	if err != nil || string(installed) != "new database" {
		t.Fatalf("installed=%q err=%v", installed, err)
	}
	newInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(oldInfo, newInfo) {
		t.Fatal("successful download did not atomically replace the target inode")
	}

	t.Run("archive without database preserves current file", func(t *testing.T) {
		archive := filepath.Join(root, "missing.tar.gz")
		writeArchive(t, archive, "README.txt", []byte("missing"))
		if _, runErr := runScript(script, "test-key", "file://"+archive); runErr == nil {
			t.Fatal("archive without MMDB unexpectedly succeeded")
		}
		current, readErr := os.ReadFile(target)
		if readErr != nil || !bytes.Equal(current, installed) {
			t.Fatalf("current=%q err=%v", current, readErr)
		}
	})

	t.Run("download failure preserves current file", func(t *testing.T) {
		if _, runErr := runScript(script, "test-key", "file://"+filepath.Join(root, "does-not-exist")); runErr == nil {
			t.Fatal("missing download unexpectedly succeeded")
		}
		current, readErr := os.ReadFile(target)
		if readErr != nil || !bytes.Equal(current, installed) {
			t.Fatalf("current=%q err=%v", current, readErr)
		}
	})
}

func runScript(path, licenseKey, downloadURL string) (string, error) {
	command := exec.Command(path)
	environment := make([]string, 0, len(os.Environ())+2)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "MAXMIND_LICENSE_KEY=") && !strings.HasPrefix(value, "MAXMIND_DOWNLOAD_URL=") {
			environment = append(environment, value)
		}
	}
	if licenseKey != "" {
		environment = append(environment, "MAXMIND_LICENSE_KEY="+licenseKey)
	}
	if downloadURL != "" {
		environment = append(environment, "MAXMIND_DOWNLOAD_URL="+downloadURL)
	}
	command.Env = environment
	output, err := command.CombinedOutput()
	return string(output), err
}

func writeArchive(t *testing.T, path, name string, data []byte) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	if err = tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if _, err = tarWriter.Write(data); err != nil {
		t.Fatal(err)
	}
	if err = tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err = gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
}

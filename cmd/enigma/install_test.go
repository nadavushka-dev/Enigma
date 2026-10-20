package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fileRelease lays out a GitHub-like release of the test binary under dir
// (download/<tag>/, checksums.txt, latest) and returns the file:// URL.
func fileRelease(t *testing.T, dir, tag string, corrupt bool) string {
	t.Helper()
	return fileReleaseBuild(t, dir, tag, bin, corrupt)
}

// fileReleaseOf publishes binary as release tag (this platform only).
func fileReleaseOf(t *testing.T, dir, tag, binary string) string {
	t.Helper()
	return fileReleaseBuild(t, dir, tag, binary, false)
}

func fileReleaseBuild(t *testing.T, dir, tag, binary string, corrupt bool) string {
	t.Helper()
	b, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "enigma", Mode: 0o755, Size: int64(len(b))})
	tw.Write(b)
	tw.Close()
	gz.Close()
	asset := fmt.Sprintf("enigma_%s_%s_%s.tar.gz", strings.TrimPrefix(tag, "v"), runtime.GOOS, runtime.GOARCH)
	d := filepath.Join(dir, "download", tag)
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, asset), buf.Bytes(), 0o644)
	h := sha256.Sum256(buf.Bytes())
	sum := hex.EncodeToString(h[:])
	if corrupt {
		sum = strings.Repeat("0", 64)
	}
	os.WriteFile(filepath.Join(d, "checksums.txt"), []byte(sum+"  "+asset+"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "latest"), []byte(tag+"\n"), 0o644)
	return "file://" + dir
}

func runInstall(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("sh", append([]string{"../../install.sh"}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestInstallScriptInstallsTheLatestRelease(t *testing.T) {
	root := t.TempDir()
	url := fileRelease(t, filepath.Join(root, "rel"), "v1.2.3", false)
	dir := filepath.Join(root, "bin")
	out, err := runInstall(t, []string{"ENIGMA_RELEASES_URL=" + url, "ENIGMA_INSTALL_DIR=" + dir, "HOME=" + root})
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if v, err := exec.Command(filepath.Join(dir, "enigma"), "version").Output(); err != nil || !strings.HasPrefix(string(v), "enigma ") {
		t.Fatalf("installed binary: %q %v", v, err)
	}
	for _, want := range []string{"installed enigma", "not on your PATH", "enigma init", "machine add"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestInstallScriptRefusesAChecksumMismatch(t *testing.T) {
	root := t.TempDir()
	url := fileRelease(t, filepath.Join(root, "rel"), "v1.2.3", true)
	dir := filepath.Join(root, "bin")
	out, err := runInstall(t, []string{"ENIGMA_RELEASES_URL=" + url, "ENIGMA_INSTALL_DIR=" + dir, "HOME=" + root})
	if err == nil || !strings.Contains(out, "sha256 mismatch") {
		t.Fatalf("err %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "enigma")); !os.IsNotExist(err) {
		t.Fatal("a corrupt download was installed")
	}
}

func TestInstallScriptDryRunAndPinnedVersion(t *testing.T) {
	out, err := runInstall(t, []string{"ENIGMA_VERSION=0.4.0", "ENIGMA_RELEASES_URL=https://example.invalid/releases"}, "--dry-run")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := fmt.Sprintf("url=https://example.invalid/releases/download/v0.4.0/enigma_0.4.0_%s_%s.tar.gz", runtime.GOOS, map[string]string{"amd64": "amd64", "arm64": "arm64"}[runtime.GOARCH])
	if !strings.Contains(out, want) {
		t.Fatalf("dry run:\n%s\nwant %s", out, want)
	}
}

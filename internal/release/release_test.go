package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// fakeRelease lays out <dir>/download/<tag>/{asset,checksums.txt} like GitHub.
func fakeRelease(t *testing.T, dir, tag, platform, binary string, corrupt bool) {
	t.Helper()
	asset := AssetName(tag, platform)
	tb := tarball(t, map[string]string{"enigma": binary, "README.md": "readme"})
	d := filepath.Join(dir, "download", tag)
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, asset), tb, 0o644)
	s := sum(tb)
	if corrupt {
		s = strings.Repeat("0", 64)
	}
	os.WriteFile(filepath.Join(d, "checksums.txt"), []byte(fmt.Sprintf("%s  other.tar.gz\n%s  %s\n", strings.Repeat("1", 64), s, asset)), 0o644)
}

func TestAssetName(t *testing.T) {
	if got := AssetName("v1.2.3", "linux/arm64"); got != "enigma_1.2.3_linux_arm64.tar.gz" {
		t.Fatalf("got %q", got)
	}
}

func TestFetchFromFileURL(t *testing.T) {
	dir := t.TempDir()
	fakeRelease(t, dir, "v1.2.3", "linux/arm64", "BINARY", false)
	os.WriteFile(filepath.Join(dir, "latest"), []byte("v1.2.3\n"), 0o644)
	src := Source{Base: "file://" + dir}
	tag, err := src.Latest(context.Background())
	if err != nil || tag != "v1.2.3" {
		t.Fatalf("latest %q %v", tag, err)
	}
	bin, err := src.Fetch(context.Background(), "v1.2.3", "linux/arm64")
	if err != nil || string(bin) != "BINARY" {
		t.Fatalf("fetch %q %v", bin, err)
	}
}

func TestFetchRejectsAChecksumMismatch(t *testing.T) {
	dir := t.TempDir()
	fakeRelease(t, dir, "v1.2.3", "linux/arm64", "BINARY", true)
	_, err := Source{Base: "file://" + dir}.Fetch(context.Background(), "v1.2.3", "linux/arm64")
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("err %v", err)
	}
}

func TestFetchMissingAssetSaysWhich(t *testing.T) {
	dir := t.TempDir()
	fakeRelease(t, dir, "v1.2.3", "linux/arm64", "BINARY", false)
	_, err := Source{Base: "file://" + dir}.Fetch(context.Background(), "v1.2.3", "darwin/amd64")
	if err == nil || !strings.Contains(err.Error(), "enigma_1.2.3_darwin_amd64.tar.gz") {
		t.Fatalf("err %v", err)
	}
}

func TestLatestFollowsGitHubsRedirect(t *testing.T) {
	dir := t.TempDir()
	fakeRelease(t, dir, "v2.0.0", "darwin/arm64", "MAC", false)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/releases/latest":
			http.Redirect(w, r, "/releases/tag/v2.0.0", http.StatusFound)
		case r.URL.Path == "/releases/tag/v2.0.0":
			w.Write([]byte("<html>release page</html>"))
		case strings.HasPrefix(r.URL.Path, "/releases/download/"):
			http.ServeFile(w, r, filepath.Join(dir, strings.TrimPrefix(r.URL.Path, "/releases/")))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	src := Source{Base: srv.URL + "/releases"}
	tag, err := src.Latest(context.Background())
	if err != nil || tag != "v2.0.0" {
		t.Fatalf("latest %q %v", tag, err)
	}
	if bin, err := src.Fetch(context.Background(), tag, "darwin/arm64"); err != nil || string(bin) != "MAC" {
		t.Fatalf("fetch %q %v", bin, err)
	}
}

func TestNoReleaseYet(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	if _, err := (Source{Base: srv.URL}).Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "no release") {
		t.Fatalf("err %v", err)
	}
}

func TestDefaultSourceHonorsEnv(t *testing.T) {
	t.Setenv("ENIGMA_RELEASES_URL", "file:///x")
	if got := Default().Base; got != "file:///x" {
		t.Fatalf("got %q", got)
	}
	t.Setenv("ENIGMA_RELEASES_URL", "")
	if got := Default().Base; got != "https://github.com/nadavushka-dev/enigma/releases" {
		t.Fatalf("got %q", got)
	}
}

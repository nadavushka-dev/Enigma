// Package release downloads Enigma's published binaries from GitHub
// releases (SPEC §11, D17): enigma_<version>_<os>_<arch>.tar.gz assets,
// checked against the release's checksums.txt (sha256). install.sh does
// the same in shell; keep the names in step with .goreleaser.yaml.
package release

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const defaultBase = "https://github.com/nadavushka-dev/enigma/releases"

// maxAsset bounds a download; a release tarball is a few MB.
const maxAsset = 200 << 20

// Source is where releases live: GitHub's releases URL, or a file://
// directory with the same layout (download/<tag>/...) plus a "latest" file,
// for tests and offline mirrors.
type Source struct {
	Base   string
	Client *http.Client
}

// Default is GitHub, or $ENIGMA_RELEASES_URL.
func Default() Source {
	base := os.Getenv("ENIGMA_RELEASES_URL")
	if base == "" {
		base = defaultBase
	}
	return Source{Base: strings.TrimSuffix(base, "/")}
}

// AssetName is the tarball for a version (tag) and platform (os/arch).
func AssetName(tag, platform string) string {
	return "enigma_" + strings.TrimPrefix(tag, "v") + "_" + strings.ReplaceAll(platform, "/", "_") + ".tar.gz"
}

func (s Source) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: 2 * time.Minute}
}

func (s Source) local() (string, bool) { return strings.CutPrefix(s.Base, "file://") }

// Latest returns the newest release's tag. On GitHub, <releases>/latest
// redirects to <releases>/tag/<tag>; following it avoids the API and its
// rate limit.
func (s Source) Latest(ctx context.Context) (string, error) {
	if dir, ok := s.local(); ok {
		b, err := os.ReadFile(dir + "/latest")
		if err != nil {
			return "", fmt.Errorf("no release found in %s", dir)
		}
		return strings.TrimSpace(string(b)), nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, s.Base+"/latest", nil)
	if err != nil {
		return "", err
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	final := resp.Request.URL.Path
	if resp.StatusCode != http.StatusOK || !strings.Contains(final, "/tag/") {
		return "", fmt.Errorf("no release found at %s/latest", s.Base)
	}
	return final[strings.LastIndex(final, "/tag/")+len("/tag/"):], nil
}

func (s Source) get(ctx context.Context, tag, name string) ([]byte, error) {
	if dir, ok := s.local(); ok {
		b, err := os.ReadFile(dir + "/download/" + tag + "/" + name)
		if err != nil {
			return nil, fmt.Errorf("release %s has no %s", tag, name)
		}
		return b, nil
	}
	url := s.Base + "/download/" + tag + "/" + name
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release %s has no %s (%s: %s)", tag, name, url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxAsset+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxAsset {
		return nil, fmt.Errorf("%s is too large", url)
	}
	return b, nil
}

// Fetch downloads the binary of release tag for platform, verifying the
// tarball against checksums.txt.
func (s Source) Fetch(ctx context.Context, tag, platform string) ([]byte, error) {
	asset := AssetName(tag, platform)
	sums, err := s.get(ctx, tag, "checksums.txt")
	if err != nil {
		return nil, err
	}
	tb, err := s.get(ctx, tag, asset)
	if err != nil {
		return nil, err
	}
	want := ""
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 && f[1] == asset {
			want = f[0]
		}
	}
	if want == "" {
		return nil, fmt.Errorf("%s is not listed in checksums.txt", asset)
	}
	h := sha256.Sum256(tb)
	if got := hex.EncodeToString(h[:]); got != want {
		return nil, fmt.Errorf("sha256 mismatch for %s: want %s, got %s", asset, want, got)
	}
	return extract(tb)
}

// extract returns the "enigma" file from a .tar.gz.
func extract(tb []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(tb))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("no enigma binary in the release tarball")
		}
		if err != nil {
			return nil, err
		}
		if h.Name == "enigma" || strings.HasSuffix(h.Name, "/enigma") {
			return io.ReadAll(io.LimitReader(tr, maxAsset))
		}
	}
}

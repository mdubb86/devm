package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsPreviewHost(t *testing.T) {
	cases := []struct {
		host, tld string
		want      bool
	}{
		{"preview.sewtrue.test", "test", true},
		{"preview.sewtrue.e2e.test", "e2e.test", true},
		{"preview.sewtrue.test:443", "test", true}, // port stripped
		{"files.sewtrue.test", "test", false},
		{"preview.test", "test", false}, // no project segment
		{"preview.sewtrue.other", "test", false},
		{"preview.sewtrue.test", "", false}, // empty TLD disables matching
		{"", "test", false},
	}
	for _, c := range cases {
		got := isPreviewHost(c.host, c.tld)
		if got != c.want {
			t.Errorf("isPreviewHost(%q, %q) = %v, want %v", c.host, c.tld, got, c.want)
		}
	}
}

func TestPreviewFileServer_ServesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(path, []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(previewFileServerFromRoot(dir))
	defer srv.Close()

	r, err := http.Get(srv.URL + "/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	body, _ := io.ReadAll(r.Body)
	if string(body) != "world" {
		t.Fatalf("body = %q, want %q", body, "world")
	}
}

func TestPreviewFileServer_404sMissingFile(t *testing.T) {
	srv := httptest.NewServer(previewFileServerFromRoot(t.TempDir()))
	defer srv.Close()

	r, err := http.Get(srv.URL + "/missing.html")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != 404 {
		t.Fatalf("status = %d, want 404", r.StatusCode)
	}
}

func TestPreviewFileServer_ServesIndexHTML(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	_ = os.Mkdir(sub, 0o755)
	_ = os.WriteFile(filepath.Join(sub, "index.html"), []byte("<p>home</p>"), 0o644)

	srv := httptest.NewServer(previewFileServerFromRoot(dir))
	defer srv.Close()

	r, err := http.Get(srv.URL + "/sub/")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	body, _ := io.ReadAll(r.Body)
	if !strings.Contains(string(body), "<p>home</p>") {
		t.Fatalf("expected index.html body, got %q", body)
	}
}

func TestPreviewFileServer_404sDirWithoutIndex(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "nope")
	_ = os.Mkdir(sub, 0o755)
	_ = os.WriteFile(filepath.Join(sub, "a.txt"), []byte("a"), 0o644)

	srv := httptest.NewServer(previewFileServerFromRoot(dir))
	defer srv.Close()

	r, err := http.Get(srv.URL + "/nope/")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != 404 {
		t.Fatalf("status = %d, want 404 (no index.html, no listing)", r.StatusCode)
	}
}

func TestPreviewFileServer_PathTraversalIsChrooted(t *testing.T) {
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	_ = os.WriteFile(secret, []byte("nope"), 0o644)

	dir := t.TempDir()

	srv := httptest.NewServer(previewFileServerFromRoot(dir))
	defer srv.Close()

	// http.FileServer normalises away leading "../" segments, so this
	// becomes a request for /secret.txt relative to dir — expect 404.
	r, err := http.Get(srv.URL + "/../" + filepath.Base(outside) + "/secret.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode == 200 {
		t.Fatalf("path traversal served a file (status 200) — chroot escaped")
	}
}

func TestPreviewFileServer_SymlinkFollowed(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	_ = os.Mkdir(real, 0o755)
	_ = os.WriteFile(filepath.Join(real, "style.css"), []byte("body{color:red}"), 0o644)
	_ = os.Symlink(real, filepath.Join(dir, "link"))

	srv := httptest.NewServer(previewFileServerFromRoot(dir))
	defer srv.Close()

	r, err := http.Get(srv.URL + "/link/style.css")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("symlinked path status = %d, want 200 (relative asset paths through symlink'd dirs must resolve)", r.StatusCode)
	}
}

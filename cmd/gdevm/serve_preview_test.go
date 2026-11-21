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
		{"preview.foo/bar.test", "test", false}, // slash in project segment
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
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
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
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "index.html"), []byte("<p>home</p>"), 0o644); err != nil {
		t.Fatal(err)
	}

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
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "<p>home</p>") {
		t.Fatalf("expected index.html body, got %q", body)
	}
}

func TestPreviewFileServer_404sDirWithoutIndex(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "nope")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}

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
	if err := os.WriteFile(secret, []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	h := previewFileServerFromRoot(dir)

	// Call the handler directly: http.Client would clean "/../" before
	// the server ever saw it.
	r := httptest.NewRequest("GET", "http://preview.test/anything", nil)
	r.URL.Path = "/../" + filepath.Base(outside) + "/secret.txt"
	r.URL.RawPath = r.URL.Path
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == 200 {
		t.Fatalf("path traversal served a file (status 200)")
	}

	// Control: a legitimate file is served, so the above isn't passing
	// merely because the handler is broken.
	if err := os.WriteFile(filepath.Join(dir, "ok.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	r2 := httptest.NewRequest("GET", "http://preview.test/ok.txt", nil)
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)
	if w2.Code != 200 || w2.Body.String() != "ok" {
		t.Fatalf("control request failed: status=%d body=%q", w2.Code, w2.Body.String())
	}
}

func TestPreviewFileServer_SymlinkFollowed(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "style.css"), []byte("body{color:red}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}

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
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "body{color:red}" {
		t.Fatalf("symlinked body = %q, want %q", body, "body{color:red}")
	}
}

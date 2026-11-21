package main

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// isPreviewHost reports whether host carries the project's preview
// hostname. Shape: preview.<project>.<tld>, with <project> at least one
// non-empty segment. Returns false for an empty tld — a guest where
// DEVM_TLD was never plumbed stays in health-only mode.
func isPreviewHost(host, tld string) bool {
	if tld == "" || host == "" {
		return false
	}
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	suffix := "." + tld
	if !strings.HasSuffix(host, suffix) {
		return false
	}
	middle := strings.TrimSuffix(host, suffix)
	if !strings.HasPrefix(middle, "preview.") {
		return false
	}
	project := strings.TrimPrefix(middle, "preview.")
	return project != "" && !strings.Contains(project, "/")
}

// previewFileServer serves files from the guest filesystem rooted at
// "/". Directory requests without an index.html return 404 — filestash
// owns directory browsing (files.<project>.<tld>).
func previewFileServer() http.Handler {
	return previewFileServerFromRoot("/")
}

// previewFileServerFromRoot is the testable form of previewFileServer.
func previewFileServerFromRoot(root string) http.Handler {
	fs := http.FileServer(http.Dir(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isDirRequestWithoutIndex(root, r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		fs.ServeHTTP(w, r)
	})
}

// isDirRequestWithoutIndex returns true when urlPath identifies an
// existing directory on disk at (root + urlPath) that has no index.html.
// http.FileServer's default would render a listing; the preview server
// returns 404 so filestash remains the one answer for browsing.
func isDirRequestWithoutIndex(root, urlPath string) bool {
	cleaned := path.Clean("/" + urlPath)
	full := filepath.Join(root, filepath.FromSlash(cleaned))
	st, err := os.Stat(full)
	if err != nil || !st.IsDir() {
		return false
	}
	if _, err := os.Stat(filepath.Join(full, "index.html")); err == nil {
		return false
	}
	return true
}

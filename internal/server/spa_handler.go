package server

import (
	"io/fs"
	"net/http"
	"strings"
)

const (
	spaFrameAncestorsPolicy = "frame-ancestors 'none'"
	spaXFrameOptions        = "DENY"
)

func newSPAAssetHandler(
	frontend fs.FS,
	basePath string,
	bootstrapScript func() string,
) http.Handler {
	indexBytes, err := fs.ReadFile(frontend, "index.html")
	if err != nil {
		indexBytes = []byte("<!DOCTYPE html><html><body>frontend not found</body></html>")
	}
	indexTemplate := string(indexBytes)
	if basePath != "/" {
		prefix := strings.TrimSuffix(basePath, "/")
		indexTemplate = strings.ReplaceAll(indexTemplate, `src="/assets/`, `src="`+prefix+`/assets/`)
		indexTemplate = strings.ReplaceAll(indexTemplate, `href="/assets/`, `href="`+prefix+`/assets/`)
	}

	serveIndex := func(w http.ResponseWriter, r *http.Request) {
		script := ""
		if bootstrapScript != nil {
			script = bootstrapScript()
		}
		idx := strings.Replace(indexTemplate, "<head>",
			`<head><script>`+script+`</script>`, 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if !isWorkspaceEmbedRoute(r.URL.Path) {
			w.Header().Set("Content-Security-Policy", spaFrameAncestorsPolicy)
			w.Header().Set("X-Frame-Options", spaXFrameOptions)
		}
		// index.html references content-hashed bundles. Browsers
		// must always re-fetch it so a rebuild is picked up; the
		// hashed assets it references can still be cached forever.
		w.Header().Set("Cache-Control",
			"no-store, no-cache, must-revalidate, max-age=0")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(idx))
	}

	fileServer := http.FileServerFS(frontend)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" || name == "index.html" {
			serveIndex(w, r)
			return
		}
		f, err := frontend.Open(name)
		if err == nil {
			_ = f.Close()
			if strings.HasPrefix(r.URL.Path, "/assets/") {
				w.Header().Set("Cache-Control",
					"public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		// A missing /assets/* request is a stale-bundle fetch from
		// an old cached index.html. Returning the SPA HTML here
		// would 200 with the wrong Content-Type and leave the page
		// stuck on a failed module import.
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			http.NotFound(w, r)
			return
		}
		serveIndex(w, r)
	})
}

func isWorkspaceEmbedRoute(path string) bool {
	return path == "/workspaces/embed" || strings.HasPrefix(path, "/workspaces/embed/")
}

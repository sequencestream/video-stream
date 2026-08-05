// Package webui serves the WebUI from inside the binary.
//
// The Next.js project in ./webui is built as a static export and copied into
// dist by `make webui-build`. Embedding it means the whole product ships as a
// single executable: no Node runtime in production, no second container, and
// no way for the UI to be served from a different version than the API it
// talks to.
//
// dist is generated, so it is not in version control; only a .gitkeep is, which
// is enough to satisfy //go:embed on a fresh clone. A build without the UI is
// therefore still a working build, and Built reports which one you have.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// The all: prefix keeps .gitkeep visible to embed, which is what makes this
// compile before anyone has run a WebUI build.
//
//go:embed all:dist
var embedded embed.FS

// indexFile is the marker for a real export: a bare .gitkeep means the UI was
// never built into this binary.
const indexFile = "index.html"

// Built reports whether a WebUI export was embedded into this binary.
func Built() bool {
	dist, err := distFS()
	if err != nil {
		return false
	}
	_, err = fs.Stat(dist, indexFile)
	return err == nil
}

// Handler serves the embedded UI. When no export was built in, it serves a page
// explaining how to build one rather than a bare 404, because "the API works
// but / is empty" is otherwise a confusing first experience.
func Handler() http.Handler {
	dist, err := distFS()
	if err != nil || !Built() {
		return http.HandlerFunc(serveMissing)
	}
	return &handler{files: http.FileServerFS(dist), dist: dist}
}

func distFS() (fs.FS, error) { return fs.Sub(embedded, "dist") }

type handler struct {
	files http.Handler
	dist  fs.FS
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Next writes content-hashed filenames under /_next/static, so those are
	// safe to cache forever. Everything else is HTML that must be revalidated,
	// or the browser will keep showing the previous release after an upgrade.
	if strings.HasPrefix(r.URL.Path, "/_next/static/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}

	rec := &missRecorder{ResponseWriter: w}
	h.files.ServeHTTP(rec, r)
	if !rec.missed {
		return
	}

	// The export is fully pre-rendered, so a miss is a genuinely unknown route.
	// Hand back the app's own 404 page when it exists.
	page, err := fs.ReadFile(h.dist, "404.html")
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	// net/http strips Cache-Control while preparing its own error response, so
	// restore it here rather than leaving the 404 page cacheable by default.
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	w.Write(page)
}

// missRecorder swallows the file server's plain-text 404 so the caller can
// substitute the exported 404 page. Any other status passes straight through.
type missRecorder struct {
	http.ResponseWriter
	missed bool
}

func (m *missRecorder) WriteHeader(status int) {
	if status == http.StatusNotFound {
		m.missed = true
		return
	}
	m.ResponseWriter.WriteHeader(status)
}

func (m *missRecorder) Write(b []byte) (int, error) {
	if m.missed {
		return len(b), nil
	}
	return m.ResponseWriter.Write(b)
}

func serveMissing(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// 503 rather than 200: the UI really is unavailable, and a probe should be
	// able to tell the difference.
	w.WriteHeader(http.StatusServiceUnavailable)
	w.Write([]byte(missingPage))
}

const missingPage = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>video-stream</title></head>
<body style="font-family:system-ui,sans-serif;max-width:40rem;margin:4rem auto;padding:0 1rem">
<h1>WebUI not built into this binary</h1>
<p>The HTTP API is running. To serve the interface from here as well, build the
export and rebuild:</p>
<pre style="background:#f4f4f5;padding:1rem;border-radius:.5rem">make webui-build
make build</pre>
<p>For UI development, <code>make webui</code> runs the Next dev server instead.</p>
</body>
</html>
`

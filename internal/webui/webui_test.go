package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandlerServesTheEmbeddedExport is skipped on a bare checkout, where dist
// holds only .gitkeep. That is deliberate: `go test ./...` must pass without
// Node installed, but once someone has run `make webui-build` these assertions
// hold them to it.
func TestHandlerServesTheEmbeddedExport(t *testing.T) {
	requireBuilt(t)

	paths := []string{"/", "/wizard/1/", "/wizard/7/"}
	for _, path := range paths {
		rec := get(t, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "<html") {
			t.Errorf("GET %s did not return an HTML document", path)
		}
	}
}

// TestHandlerRedirectsTrailingSlashForms covers the URL a user types by hand.
// The export is written as <route>/index.html, so /wizard/1 must still land
// somewhere useful rather than 404.
func TestHandlerRedirectsTrailingSlashForms(t *testing.T) {
	requireBuilt(t)

	rec := get(t, "/wizard/1")
	if rec.Code != http.StatusMovedPermanently && rec.Code != http.StatusOK {
		t.Fatalf("GET /wizard/1 = %d, want a redirect or the page itself", rec.Code)
	}
}

// TestHandlerServesTheExported404Page keeps unknown routes on the app's own
// error page instead of a plain-text Go 404.
func TestHandlerServesTheExported404Page(t *testing.T) {
	requireBuilt(t)

	rec := get(t, "/no-such-page")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /no-such-page = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("the 404 body should be the exported HTML page, got %q", rec.Body.String())
	}
}

// TestHandlerCachesHashedAssetsOnly guards the upgrade path: hashed bundles are
// immutable, HTML is not. Getting this backwards leaves users on a stale UI
// after an upgrade with no way to recover but a hard refresh.
func TestHandlerCachesHashedAssetsOnly(t *testing.T) {
	requireBuilt(t)

	if got := get(t, "/").Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("HTML Cache-Control = %q, want no-cache", got)
	}

	asset := anyHashedAsset(t)
	rec := get(t, asset)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", asset, rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("hashed asset Cache-Control = %q, want an immutable directive", got)
	}
}

// anyHashedAsset finds one real file under /_next/static. Asking for a name
// that does not exist would test the 404 path instead, where net/http discards
// the headers we set.
func anyHashedAsset(t *testing.T) string {
	t.Helper()

	dist, err := distFS()
	if err != nil {
		t.Fatalf("dist: %v", err)
	}

	var found string
	err = fs.WalkDir(dist, "_next/static", func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() || found != "":
			return nil
		default:
			found = "/" + path
			return fs.SkipAll
		}
	})
	if err != nil || found == "" {
		t.Fatalf("no asset under _next/static: %v", err)
	}
	return found
}

// TestHandlerWithoutAnExportExplainsItself checks the fallback that makes a
// Node-free `go build` still produce a coherent binary.
func TestHandlerWithoutAnExportExplainsItself(t *testing.T) {
	if Built() {
		t.Skip("an export is embedded; the fallback page is not reachable")
	}

	rec := get(t, "/")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET / = %d, want 503 when no UI is embedded", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "make webui-build") {
		t.Fatalf("the fallback page should say how to build the UI, got %q", rec.Body.String())
	}
}

func requireBuilt(t *testing.T) {
	t.Helper()

	if !Built() {
		t.Skip("no WebUI export embedded; run make webui-build")
	}
}

func get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

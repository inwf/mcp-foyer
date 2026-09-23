package api_test

import (
	"io"
	"io/fs"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"mcp-foyer/internal/api"
)

// builtUI stands in for the frontend's build output.
func builtUI() fs.FS {
	return fstest.MapFS{
		"index.html":          {Data: []byte("<!doctype html><title>mcp-foyer</title>")},
		"assets/app.js":       {Data: []byte("console.log('app')")},
		"assets/app.css":      {Data: []byte("body{margin:0}")},
		"favicon.svg":         {Data: []byte("<svg/>")},
		"nested/deep/file.js": {Data: []byte("nested")},
	}
}

// serving starts an API with a web UI attached.
func serving(t *testing.T, files fs.FS) *harness {
	t.Helper()
	return start(t, func(o *api.Options) { o.WebUI = files })
}

// bodyOf reads a response body.
func bodyOf(t *testing.T, resp *http.Response) string {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the body: %v", err)
	}
	return string(raw)
}

// ===== serving files =====

func TestTheRootServesTheApp(t *testing.T) {
	h := serving(t, builtUI())

	resp := h.get(t, "/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if body := bodyOf(t, resp); !strings.Contains(body, "mcp-foyer") {
		t.Errorf("body = %q, want the entry document", body)
	}
}

func TestAssetsAreServed(t *testing.T) {
	h := serving(t, builtUI())

	for path, want := range map[string]string{
		"/assets/app.js":       "console.log('app')",
		"/assets/app.css":      "body{margin:0}",
		"/favicon.svg":         "<svg/>",
		"/nested/deep/file.js": "nested",
	} {
		resp := h.get(t, path)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", path, resp.StatusCode)
			continue
		}
		if got := bodyOf(t, resp); got != want {
			t.Errorf("%s: body = %q, want %q", path, got, want)
		}
	}
}

// A browser decides how to treat the response from its type, so a script
// served as plain text simply will not run.
func TestAssetsCarryTheirContentType(t *testing.T) {
	h := serving(t, builtUI())

	for path, want := range map[string]string{
		"/":               "text/html",
		"/assets/app.js":  "javascript",
		"/assets/app.css": "text/css",
	} {
		if got := h.get(t, path).Header.Get("Content-Type"); !strings.Contains(got, want) {
			t.Errorf("%s: content type = %q, want it to mention %q", path, got, want)
		}
	}
}

// ===== single-page fallback =====

// The app owns its own routing, so reloading the page on a deep link has
// to return the entry document rather than a 404.
func TestAnUnknownPathServesTheApp(t *testing.T) {
	h := serving(t, builtUI())

	for _, path := range []string{"/servers", "/servers/files/tools", "/settings"} {
		resp := h.get(t, path)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", path, resp.StatusCode)
			continue
		}
		if body := bodyOf(t, resp); !strings.Contains(body, "mcp-foyer") {
			t.Errorf("%s: body = %q, want the entry document", path, body)
		}
	}
}

// This is the line that must not blur. A client parsing HTML where it
// expected an error envelope gets a confusing decode failure instead of
// the status that actually happened.
func TestAnUnknownAPIPathStillReportsJSON(t *testing.T) {
	h := serving(t, builtUI())

	for _, path := range []string{"/api/nothing", "/api/servers/x/nothing", "/api"} {
		resp := h.get(t, path)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", path, resp.StatusCode)
			continue
		}
		if got := resp.Header.Get("Content-Type"); !strings.Contains(got, "application/json") {
			t.Errorf("%s: content type = %q, want JSON", path, got)
		}
	}
}

// A path that merely starts with the same letters is not an API path.
func TestAPathThatOnlyLooksLikeTheAPIPrefixGoesToTheApp(t *testing.T) {
	h := serving(t, builtUI())

	resp := h.get(t, "/apidocs")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want the app to have handled it", resp.StatusCode)
	}
	if body := bodyOf(t, resp); !strings.Contains(body, "mcp-foyer") {
		t.Errorf("body = %q, want the entry document", body)
	}
}

// A write to an unknown path is a mistake, not a page view: answering it
// with HTML would hide the mistake behind a 200.
func TestAWriteToAnUnknownPathIsNotTheApp(t *testing.T) {
	h := serving(t, builtUI())

	resp := h.do(t, http.MethodPost, "/servers", nil)
	if resp.StatusCode == http.StatusOK {
		t.Errorf("status = %d, want a POST to an unknown path to be rejected", resp.StatusCode)
	}
}

// The entry document names the current build's assets. A cached one
// would go on asking for the previous build's, which no longer exist.
func TestTheEntryDocumentIsNotCached(t *testing.T) {
	h := serving(t, builtUI())

	for _, path := range []string{"/", "/servers"} {
		if got := h.get(t, path).Header.Get("Cache-Control"); !strings.Contains(got, "no-cache") {
			t.Errorf("%s: cache-control = %q, want no-cache", path, got)
		}
	}
}

// ===== no web interface =====

// A backend-only build and a development tree with an unbuilt frontend
// are both this case, and neither should look like a broken route.
func TestWithoutAWebInterfaceTheAPIStillWorks(t *testing.T) {
	h := start(t, nil)

	if resp := h.get(t, "/api/health"); resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want the api still served", resp.StatusCode)
	}
	if resp := h.get(t, "/"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 where there is no web interface", resp.StatusCode)
	}
}

// A file system with no entry document cannot serve an app, and
// pretending otherwise would answer every path with a 500.
func TestAFileSystemWithoutAnEntryDocumentIsNotServed(t *testing.T) {
	h := serving(t, fstest.MapFS{"assets/app.js": {Data: []byte("orphan")}})

	if resp := h.get(t, "/"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// ===== path handling =====

// A path that climbs out of the file system must not reach anything.
// http.FS rejects these, and so does the cleaning done before the
// lookup; this pins both.
func TestATraversingPathCannotEscape(t *testing.T) {
	h := serving(t, builtUI())

	for _, path := range []string{
		"/../go.mod",
		"/assets/../../go.mod",
		"/%2e%2e%2fgo.mod",
	} {
		resp := h.get(t, path)
		if body := bodyOf(t, resp); strings.Contains(body, "module mcp-foyer") {
			t.Errorf("%s served the module file", path)
		}
	}
}

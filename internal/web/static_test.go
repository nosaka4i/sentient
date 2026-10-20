package web

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
)

func TestStaticFilesRevalidateWithAnETag(t *testing.T) {
	s := newTestServer(t, &fakeSource{})
	resp := getWith(t, s, "/static/prompts.js", nil)
	body, _ := io.ReadAll(resp.Body)
	etag := resp.Header.Get("ETag")
	if resp.StatusCode != http.StatusOK || len(body) < 1000 || !strings.HasPrefix(etag, `"`) || resp.Header.Get("Cache-Control") != "no-cache" ||
		!strings.HasPrefix(resp.Header.Get("Content-Type"), "text/javascript") && !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/javascript") {
		t.Fatalf("status %d type %q etag %q cache %q (%d bytes)", resp.StatusCode, resp.Header.Get("Content-Type"), etag, resp.Header.Get("Cache-Control"), len(body))
	}

	// Asking again with the ETag gets a 304 and no body; a different ETag gets the file.
	again := getWith(t, s, "/static/prompts.js", map[string]string{"If-None-Match": etag})
	if b, _ := io.ReadAll(again.Body); again.StatusCode != http.StatusNotModified || len(b) != 0 {
		t.Errorf("revalidation = %d with %d bytes", again.StatusCode, len(b))
	}
	if other := getWith(t, s, "/static/prompts.js", map[string]string{"If-None-Match": `"stale"`}); other.StatusCode != http.StatusOK {
		t.Errorf("stale ETag = %d", other.StatusCode)
	}

	if css := getWith(t, s, "/static/sentient.css", nil); !strings.HasPrefix(css.Header.Get("Content-Type"), "text/css") || css.Header.Get("ETag") == etag {
		t.Errorf("css type %q etag %q", css.Header.Get("Content-Type"), css.Header.Get("ETag"))
	}
	for _, p := range []string{"/static/nope.js", "/static/", "/static/../templates/layout.html"} {
		// a ".." path is cleaned by the mux (a redirect to a path nothing serves)
		if r := getWith(t, s, p, nil); r.StatusCode != http.StatusNotFound && r.StatusCode != http.StatusTemporaryRedirect {
			t.Errorf("%s = %d, want 404", p, r.StatusCode)
		}
	}
}

func TestStaticETagFollowsTheContent(t *testing.T) {
	h1, err := newStaticHandler(fstest.MapFS{"a.js": {Data: []byte("one")}, "dir/b.txt": {Data: []byte("two")}, "noext": {Data: []byte("x")}})
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := newStaticHandler(fstest.MapFS{"a.js": {Data: []byte("changed")}})
	if h1.files["a.js"].etag == h2.files["a.js"].etag || h1.files["a.js"].etag == "" {
		t.Error("the ETag must change with the content")
	}
	if h1.files["noext"].typ != "application/octet-stream" || h1.files["dir/b.txt"].typ == "" {
		t.Errorf("types = %q %q", h1.files["noext"].typ, h1.files["dir/b.txt"].typ)
	}
}

package web

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func getWith(t *testing.T, s *Server, path string, header map[string]string) *http.Response {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.Host = "localhost:7357"
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Result()
}

func TestResponsesAreGzippedWhenAsked(t *testing.T) {
	s := newTestServer(t, &fakeSource{window: sampleWindow(), st: fullStatus()})
	for _, path := range []string{"/prompts/data", "/prompts", "/", "/static/prompts.js", "/static/sentient.css"} {
		plain := getWith(t, s, path, nil)
		want, _ := io.ReadAll(plain.Body)
		if plain.Header.Get("Content-Encoding") != "" {
			t.Errorf("%s: compressed for a client that didn't ask", path)
		}

		resp := getWith(t, s, path, map[string]string{"Accept-Encoding": "gzip, deflate, br"})
		if resp.Header.Get("Content-Encoding") != "gzip" || !strings.Contains(resp.Header.Get("Vary"), "Accept-Encoding") || resp.Header.Get("Content-Length") != "" {
			t.Errorf("%s: headers = %v", path, resp.Header)
			continue
		}
		zr, err := gzip.NewReader(resp.Body)
		if err != nil {
			t.Errorf("%s: not gzip: %v", path, err)
			continue
		}
		got, _ := io.ReadAll(zr)
		// The test clock is fixed, so the bodies match exactly, and the type survives.
		if string(got) != string(want) || resp.Header.Get("Content-Type") != plain.Header.Get("Content-Type") {
			t.Errorf("%s: gunzipped body differs from the plain one (%d vs %d bytes)", path, len(got), len(want))
		}
	}
}

func TestGzipLeavesOtherResponsesAlone(t *testing.T) {
	s := newTestServer(t, &fakeSource{window: sampleWindow()})
	gz := map[string]string{"Accept-Encoding": "gzip"}
	// not asked, refused (q=0), or not even listed
	for _, ae := range []string{"", "br", "gzip;q=0", "identity"} {
		if resp := getWith(t, s, "/static/prompts.js", map[string]string{"Accept-Encoding": ae}); resp.Header.Get("Content-Encoding") != "" {
			t.Errorf("Accept-Encoding %q: compressed", ae)
		}
	}
	// HEAD has no body to compress
	req := httptest.NewRequest("HEAD", "/static/prompts.js", nil)
	req.Host, req.Header["Accept-Encoding"] = "localhost", []string{"gzip"}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Result().Header.Get("Content-Encoding") != "" {
		t.Error("HEAD was compressed")
	}
	// an error page is plain text and still comes back complete
	resp := getWith(t, s, "/session/nope", gz)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "no such session") {
		t.Errorf("404 = %d %q", resp.StatusCode, body)
	}
	// a 304 has no body and must not claim one
	etag := getWith(t, s, "/static/sentient.css", nil).Header.Get("ETag")
	cond := getWith(t, s, "/static/sentient.css", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": etag})
	if etag == "" || cond.StatusCode != http.StatusNotModified || cond.Header.Get("Content-Encoding") != "" {
		t.Errorf("conditional get = %d, encoding %q", cond.StatusCode, cond.Header.Get("Content-Encoding"))
	}
}

// readBody returns the response body, gunzipping if needed.
func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	var r io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		r = zr
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestGzipSniffsATypeWhenTheHandlerSetsNone(t *testing.T) {
	h := gzipped(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>hello</html>"))
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	resp := rec.Result()
	if resp.Header.Get("Content-Encoding") != "gzip" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Errorf("headers = %v", resp.Header)
	}
	if got := readBody(t, resp); got != "<html>hello</html>" {
		t.Errorf("body = %q", got)
	}

	// binary content is left alone
	bin := gzipped(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte{0x89, 'P', 'N', 'G'})
	}))
	rec = httptest.NewRecorder()
	bin.ServeHTTP(rec, req)
	if rec.Result().Header.Get("Content-Encoding") != "" {
		t.Error("PNG was compressed")
	}
}

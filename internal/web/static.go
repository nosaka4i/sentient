package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

// staticHandler serves the embedded CSS and JS with a content-hash ETag and
// Cache-Control: no-cache, so a browser asks "changed?" and gets a tiny 304
// instead of the file again. Embedded files carry no modification time, so
// http.FileServer can't do this on its own, and over an SSH tunnel or a
// phone link re-sending 60 KB on every page load is noticeable.
type staticHandler struct {
	files map[string]staticFile // by name, e.g. "prompts.js"
}

type staticFile struct {
	data []byte
	etag string
	typ  string
}

func newStaticHandler(fsys fs.FS) (*staticHandler, error) {
	h := &staticHandler{files: map[string]staticFile{}}
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		typ := mime.TypeByExtension(path.Ext(name))
		if typ == "" {
			typ = "application/octet-stream"
		}
		h.files[name] = staticFile{data: data, etag: `"` + hex.EncodeToString(sum[:8]) + `"`, typ: typ}
		return nil
	})
	return h, err
}

func (h *staticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/static/")
	f, ok := h.files[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", f.typ)
	w.Header().Set("ETag", f.etag)
	w.Header().Set("Cache-Control", "no-cache")
	// ServeContent answers If-None-Match with 304 and handles Range.
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(f.data))
}

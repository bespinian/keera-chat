// Package webui serves the chat page.
//
// It is plain ES modules and CSS, embedded with no build step, so
// `go build` is the whole build and air-gapped installs need no npm.
package webui

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

//go:embed assets
var assets embed.FS

// file is an embedded asset, gzipped once at start-up.
type file struct {
	body        []byte
	contentType string
	etag        string
	// gz is nil when gzip did not make it smaller.
	gz     []byte
	gzEtag string
}

var (
	files map[string]file
	// shell is served for every path that is not a file.
	shell file
)

const shellPath = "/assets/index.html"

func init() {
	files = map[string]file{}
	err := fs.WalkDir(assets, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := fs.ReadFile(assets, p)
		if err != nil {
			return err
		}
		files["/"+p] = newFile(p, body)
		return nil
	})
	if err != nil {
		panic("webui: " + err.Error())
	}
	var ok bool
	if shell, ok = files[shellPath]; !ok {
		panic("webui: " + shellPath + " is not embedded")
	}
}

func newFile(p string, body []byte) file {
	// No build step means no hashed file names, so an ETag does the job.
	sum := sha256.Sum256(body)
	tag := `"` + base64.RawURLEncoding.EncodeToString(sum[:12]) + `"`
	f := file{body: body, contentType: contentTypeFor(p), etag: tag}
	if gz := squeeze(body); gz != nil {
		f.gz, f.gzEtag = gz, tag[:len(tag)-1]+`-gzip"`
	}
	return f
}

func squeeze(body []byte) []byte {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil
	}
	if _, err := zw.Write(body); err != nil || zw.Close() != nil {
		return nil
	}
	if buf.Len() >= len(body) {
		return nil
	}
	return buf.Bytes()
}

func contentTypeFor(p string) string {
	switch path.Ext(p) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".woff2":
		return "font/woff2"
	case ".txt":
		return "text/plain; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

// Handler serves the assets, and the page for any other path, since the
// page routes on the URL.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := path.Clean(r.URL.Path)
		f, ok := files[name]
		if !ok {
			// HTML in place of a missing script would fail less clearly.
			if strings.HasPrefix(name, "/assets/") {
				http.NotFound(w, r)
				return
			}
			f = shell
		}
		zipped := f.gz != nil && acceptsGzip(r.Header.Get("Accept-Encoding"))
		body, etag := f.body, f.etag
		h := w.Header()
		if zipped {
			body, etag = f.gz, f.gzEtag
			h.Set("Content-Encoding", "gzip")
		}
		h.Set("Content-Type", f.contentType)
		h.Set("ETag", etag)
		h.Set("Vary", "Accept-Encoding")
		h.Set("Cache-Control", "no-cache, private")
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(body))
	})
}

// acceptsGzip reports whether Accept-Encoding allows gzip with q > 0.
func acceptsGzip(header string) bool {
	for part := range strings.SplitSeq(header, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		q, ok := strings.CutPrefix(strings.ReplaceAll(params, " ", ""), "q=")
		if !ok {
			return true
		}
		w, err := strconv.ParseFloat(q, 64)
		return err != nil || w > 0
	}
	return false
}

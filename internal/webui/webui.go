// Package webui serves the chat page.
//
// Like the gateway's panel, it is plain ES modules and CSS with no build
// step, embedded in the binary, so `go build` is the whole build and an
// air-gapped install needs no npm.
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
	"strings"
	"time"
)

//go:embed assets
var assets embed.FS

// file is one embedded asset, gzipped once at start-up rather than per
// request.
type file struct {
	body        []byte
	contentType string
	etag        string
	// gz is the gzipped body, or nil when compressing did not make it smaller.
	gz     []byte
	gzEtag string
}

var (
	files map[string]file
	// shell is the document every route that is not a file falls back to.
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
	// With no build step there are no content-hashed file names. An ETag
	// gives the same result: a changed file is fetched again, an unchanged one
	// is a 304.
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
	case ".js":
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

// Handler serves the assets, and the page for any other path: the page
// routes on the URL, so /c/<id> loads the same document as /.
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
			// A missing asset is a 404. The page would hand the browser HTML
			// where it expected JavaScript, which fails later and less
			// clearly.
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

// acceptsGzip reports whether an Accept-Encoding header allows gzip, and does
// not refuse it with q=0.
func acceptsGzip(header string) bool {
	for part := range strings.SplitSeq(header, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		q := strings.ReplaceAll(strings.TrimSpace(params), " ", "")
		return q != "q=0" && q != "q=0.0" && q != "q=0.00" && q != "q=0.000"
	}
	return false
}

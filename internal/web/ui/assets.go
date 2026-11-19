package ui

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"path"
	"strings"
)

// assetTypes maps an embedded asset's extension to its Content-Type. Only
// extensions listed here are served.
var assetTypes = map[string]string{
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".svg":   "image/svg+xml",
	".woff2": "font/woff2",
}

// compressibleAsset reports whether gzip helps for the extension. woff2 is
// already compressed.
func compressibleAsset(ext string) bool { return ext != ".woff2" }

// asset is one embedded file prepared at startup: its content hash, the
// content-hashed URL name, and a precomputed gzip variant.
type asset struct {
	name        string // source name, for example app.css
	hashedName  string // content-hashed name, for example app.1a2b3c4d5e6f.css
	contentType string
	body        []byte
	gzipped     []byte // nil when compression does not apply or does not shrink
	etag        string // strong validator for body
}

// assetSet is the immutable, startup-built delivery table.
type assetSet struct {
	byName   map[string]*asset // plain name -> asset (revalidated on each use)
	byHashed map[string]*asset // hashed name -> asset (immutable)
}

// hashedURL returns the immutable URL for a plain asset name, or the plain
// URL when the name is unknown so a typo never breaks a page silently.
func (s *assetSet) hashedURL(name string) string {
	if a, ok := s.byName[name]; ok {
		return "/assets/" + a.hashedName
	}
	return "/assets/" + name
}

func contentHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hashedAssetName(name, hash string) string {
	ext := path.Ext(name)
	return strings.TrimSuffix(name, ext) + "." + hash[:12] + ext
}

// buildAssets reads every embedded asset, rewrites the font URLs inside
// tokens.css to their hashed names, and prepares hashes and gzip variants.
func buildAssets() (*assetSet, error) {
	entries, err := files.ReadDir("assets")
	if err != nil {
		return nil, fmt.Errorf("read embedded assets: %w", err)
	}
	set := &assetSet{byName: map[string]*asset{}, byHashed: map[string]*asset{}}
	raw := map[string][]byte{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if _, ok := assetTypes[path.Ext(name)]; !ok {
			continue
		}
		body, err := files.ReadFile("assets/" + name)
		if err != nil {
			return nil, fmt.Errorf("read asset %s: %w", name, err)
		}
		raw[name] = body
	}

	// Fonts first: stylesheets reference them by URL, so their hashed names
	// must be known before a stylesheet's own hash is computed.
	fontNames := map[string]string{}
	for name, body := range raw {
		if path.Ext(name) == ".woff2" {
			fontNames[name] = hashedAssetName(name, contentHash(body))
		}
	}
	for name, body := range raw {
		if path.Ext(name) == ".css" {
			for font, hashed := range fontNames {
				body = bytes.ReplaceAll(body, []byte(`"/assets/`+font+`"`), []byte(`"/assets/`+hashed+`"`))
			}
			raw[name] = body
		}
	}

	for name, body := range raw {
		ext := path.Ext(name)
		hash := contentHash(body)
		a := &asset{
			name:        name,
			hashedName:  hashedAssetName(name, hash),
			contentType: assetTypes[ext],
			body:        body,
			etag:        `"` + hash[:32] + `"`,
		}
		if compressibleAsset(ext) {
			var buf bytes.Buffer
			zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
			if err != nil {
				return nil, fmt.Errorf("gzip asset %s: %w", name, err)
			}
			if _, err := zw.Write(body); err != nil {
				return nil, fmt.Errorf("gzip asset %s: %w", name, err)
			}
			if err := zw.Close(); err != nil {
				return nil, fmt.Errorf("gzip asset %s: %w", name, err)
			}
			if buf.Len() < len(body) {
				a.gzipped = buf.Bytes()
			}
		}
		set.byName[name] = a
		set.byHashed[a.hashedName] = a
	}
	return set, nil
}

// Asset serves embedded assets. Content-hashed names are immutable for a year;
// plain names stay revalidated with a strong ETag so a bookmarked or external
// reference never goes stale. Text assets are gzipped when the client accepts
// it.
func (r *Renderer) Asset(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := path.Base(request.URL.Path)
	a, immutable := r.assets.byHashed[name]
	if !immutable {
		var ok bool
		if a, ok = r.assets.byName[name]; !ok {
			http.NotFound(w, request)
			return
		}
	}

	body, etag := a.body, a.etag
	header := w.Header()
	header.Set("Content-Type", a.contentType)
	header.Set("X-Content-Type-Options", "nosniff")
	if a.gzipped != nil {
		header.Add("Vary", "Accept-Encoding")
		if acceptsGzip(request.Header.Get("Accept-Encoding")) {
			body, etag = a.gzipped, strings.TrimSuffix(a.etag, `"`)+`-gzip"`
			header.Set("Content-Encoding", "gzip")
		}
	}
	header.Set("ETag", etag)
	if immutable {
		header.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		header.Set("Cache-Control", "no-cache")
	}
	if etagMatches(request.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	w.WriteHeader(http.StatusOK)
	if request.Method == http.MethodGet {
		_, _ = w.Write(body)
	}
}

// acceptsGzip reports whether an Accept-Encoding value allows gzip.
func acceptsGzip(value string) bool {
	for _, part := range strings.Split(value, ",") {
		coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(coding), "gzip") {
			continue
		}
		params = strings.ReplaceAll(strings.ToLower(params), " ", "")
		return params != "q=0" && params != "q=0.0" && params != "q=0.00" && params != "q=0.000"
	}
	return false
}

// etagMatches implements If-None-Match weak comparison for GET and HEAD.
func etagMatches(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimPrefix(strings.TrimSpace(candidate), "W/")
		if candidate == "*" || candidate == etag {
			return true
		}
	}
	return false
}

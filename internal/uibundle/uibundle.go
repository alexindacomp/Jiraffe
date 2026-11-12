// Package uibundle packs the ui/ directory into one compressed blob for
// release builds and reads it back as an fs.FS: gzip of a JSON object
// {"path": "content", …} (slash paths relative to ui/). mock.js and hidden
// files are left out.
package uibundle

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

// Skipped reports whether a ui/ file stays out of the bundle: the browser
// mock (the real backend is present) and hidden files.
func Skipped(name string) bool {
	base := path.Base(name)
	return base == "mock.js" || strings.HasPrefix(base, ".")
}

// maxBundle bounds the decompressed bundle (a corrupt blob cannot exhaust
// memory).
const maxBundle = 16 << 20

// Build reads every regular file of src (recursively, except Skipped ones)
// and returns the compressed bundle. The output is deterministic. Files
// must be UTF-8 text (the UI has no binary files; Build fails otherwise).
func Build(src fs.FS) ([]byte, error) {
	files := map[string]string{}
	err := fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case p != "." && Skipped(p) && d.IsDir():
			return fs.SkipDir
		case d.IsDir() || Skipped(p) || !d.Type().IsRegular():
			return nil
		}
		b, err := fs.ReadFile(src, p)
		if err != nil {
			return err
		}
		// JSON strings hold UTF-8 only: other bytes would be replaced.
		if !utf8.Valid(b) {
			return fmt.Errorf("%s is not UTF-8 text", p)
		}
		files[p] = string(b)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read ui: %w", err)
	}
	if _, ok := files["index.html"]; !ok {
		return nil, errors.New("ui has no index.html")
	}
	var js bytes.Buffer
	enc := json.NewEncoder(&js)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(files); err != nil { // map keys are sorted
		return nil, err
	}
	var out bytes.Buffer
	zw, err := gzip.NewWriterLevel(&out, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(js.Bytes()); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Open decompresses a bundle into a read-only file system.
func Open(bundle []byte) (fs.FS, error) {
	zr, err := gzip.NewReader(bytes.NewReader(bundle))
	if err != nil {
		return nil, fmt.Errorf("ui bundle: %w", err)
	}
	b, err := io.ReadAll(io.LimitReader(zr, maxBundle+1))
	if err != nil {
		return nil, fmt.Errorf("ui bundle: %w", err)
	}
	if len(b) > maxBundle {
		return nil, errors.New("ui bundle: too large")
	}
	var files map[string]string
	if err := json.Unmarshal(b, &files); err != nil {
		return nil, fmt.Errorf("ui bundle: %w", err)
	}
	m := make(mapFS, len(files))
	for p, c := range files {
		if !fs.ValidPath(p) {
			return nil, fmt.Errorf("ui bundle: invalid path %q", p)
		}
		m[p] = []byte(c)
	}
	return m, nil
}

// mapFS is a flat read-only file system (files only; ReadFile is what
// webui uses).
type mapFS map[string][]byte

func (m mapFS) ReadFile(name string) ([]byte, error) {
	b, ok := m[name]
	if !ok || !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "read", Path: name, Err: fs.ErrNotExist}
	}
	return append([]byte(nil), b...), nil
}

func (m mapFS) Open(name string) (fs.File, error) {
	b, ok := m[name]
	if !ok || !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return &file{name: path.Base(name), r: bytes.NewReader(b), size: int64(len(b))}, nil
}

type file struct {
	name string
	r    *bytes.Reader
	size int64
}

func (f *file) Read(p []byte) (int, error) { return f.r.Read(p) }
func (f *file) Close() error               { return nil }
func (f *file) Stat() (fs.FileInfo, error) { return info{f.name, f.size}, nil }

type info struct {
	name string
	size int64
}

func (i info) Name() string       { return i.name }
func (i info) Size() int64        { return i.size }
func (i info) Mode() fs.FileMode  { return 0o444 }
func (i info) ModTime() time.Time { return time.Time{} }
func (i info) IsDir() bool        { return false }
func (i info) Sys() any           { return nil }

package uibundle

import (
	"bytes"
	"compress/gzip"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

func TestBuildOpen(t *testing.T) {
	src := fstest.MapFS{
		"index.html":    {Data: []byte(`<p>"<x>" & y</p>`)},
		"app.js":        {Data: []byte("a\n\tb")},
		"mock.js":       {Data: []byte("MOCK")},
		".DS_Store":     {Data: []byte("x")},
		"sub/editor.js": {Data: []byte("e")},
	}
	b, err := Build(src)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := Build(src)
	if !bytes.Equal(b, b2) {
		t.Error("not deterministic")
	}
	fsys, err := Open(b)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"index.html": `<p>"<x>" & y</p>`, "app.js": "a\n\tb", "sub/editor.js": "e"} {
		if got, err := fs.ReadFile(fsys, name); err != nil || string(got) != want {
			t.Errorf("%s: %q %v", name, got, err)
		}
	}
	for _, name := range []string{"mock.js", ".DS_Store", "../index.html", "nope"} {
		if _, err := fs.ReadFile(fsys, name); err == nil {
			t.Errorf("%s readable", name)
		}
	}
	f, err := fsys.Open("app.js")
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := f.Stat(); st.Size() != 4 || st.Name() != "app.js" || st.IsDir() {
		t.Errorf("stat %+v", st)
	}
	if _, err := Build(fstest.MapFS{"app.js": {Data: []byte("x")}}); err == nil {
		t.Error("bundle without index.html")
	}
	// Non-UTF-8 content would be corrupted by JSON: refused, naming the file.
	_, err = Build(fstest.MapFS{"index.html": {Data: []byte("ok")}, "logo.png": {Data: []byte{0x89, 'P', 'N', 'G', 0xff}}})
	if err == nil || !strings.Contains(err.Error(), "logo.png") {
		t.Errorf("binary file: %v", err)
	}
	if fsys, err := Open(mustBuild(t, fstest.MapFS{"index.html": {Data: []byte("Привіт, світ ✓")}})); err != nil {
		t.Fatal(err)
	} else if got, _ := fs.ReadFile(fsys, "index.html"); string(got) != "Привіт, світ ✓" {
		t.Errorf("UTF-8 round trip %q", got)
	}
}

func TestOpenRejects(t *testing.T) {
	gz := func(s string) []byte {
		var b bytes.Buffer
		w := gzip.NewWriter(&b)
		_, _ = w.Write([]byte(s))
		_ = w.Close()
		return b.Bytes()
	}
	for name, blob := range map[string][]byte{
		"not gzip":     []byte("plain"),
		"not JSON":     gz("<html>"),
		"bad path":     gz(`{"../x":"y"}`),
		"absolute":     gz(`{"/x":"y"}`),
		"truncated gz": gz(`{"a":"b"}`)[:10],
		"too large":    gz(`{"a":"` + string(bytes.Repeat([]byte("x"), maxBundle)) + `"}`),
	} {
		if _, err := Open(blob); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func mustBuild(t *testing.T, src fs.FS) []byte {
	t.Helper()
	b, err := Build(src)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

package applog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "app.log")
	w, err := Open(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", 39) + "\n"
	for i := 0; i < 5; i++ {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()
	cur, _ := os.ReadFile(path)
	old, _ := os.ReadFile(path + ".1")
	if len(cur) > 100 || len(old) == 0 || len(old) > 100 || !strings.HasSuffix(string(cur), line) {
		t.Fatalf("cur=%d old=%d", len(cur), len(old))
	}
}

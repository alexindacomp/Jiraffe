package update

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// v0.9: the Linux build looks for its own assets and accepts only ELF files.
func TestUseLinuxAssets(t *testing.T) {
	defer func(exe, sum, sig string, magic [2]byte) {
		AssetExe, AssetSum, AssetSig, exeMagic = exe, sum, sig, magic
	}(AssetExe, AssetSum, AssetSig, exeMagic)
	UseLinuxAssets()
	if AssetExe != "jiraffe-linux-amd64" || AssetSum != "jiraffe-linux-amd64.sha256" || AssetSig != "jiraffe-linux-amd64.sig" {
		t.Fatalf("assets %s %s %s", AssetExe, AssetSum, AssetSig)
	}
	rel := `{"tag_name":"v1.2.3","assets":[` +
		`{"name":"jiraffe.exe","browser_download_url":"` + assetURL("1.2.3", "jiraffe.exe") + `","size":10},` +
		`{"name":"jiraffe-linux-amd64","browser_download_url":"` + assetURL("1.2.3", "jiraffe-linux-amd64") + `","size":20},` +
		`{"name":"jiraffe-linux-amd64.sha256","browser_download_url":"` + assetURL("1.2.3", "jiraffe-linux-amd64.sha256") + `","size":90},` +
		`{"name":"jiraffe-linux-amd64.sig","browser_download_url":"` + assetURL("1.2.3", "jiraffe-linux-amd64.sig") + `","size":89}]}`
	r, err := ParseRelease([]byte(rel))
	if err != nil || r.Exe.Size != 20 {
		t.Fatalf("release %+v, %v", r, err)
	}
	if _, err := ParseChecksum([]byte(fmtSum("jiraffe-linux-amd64"))); err != nil {
		t.Fatalf("checksum: %v", err)
	}
	if _, err := ParseChecksum([]byte(fmtSum("jiraffe.exe"))); err == nil {
		t.Fatal("checksum of the Windows exe accepted")
	}

	dir := t.TempDir()
	check := func(content []byte) error {
		p := filepath.Join(dir, "f")
		if err := os.WriteFile(p, content, 0o644); err != nil {
			t.Fatal(err)
		}
		return Verify(OSFS{}, p, int64(len(content)), sha256.Sum256(content))
	}
	if err := check([]byte("\x7fELF binary")); err != nil {
		t.Fatalf("ELF: %v", err)
	}
	var e *Error
	if err := check([]byte("MZ windows")); !errors.As(err, &e) || e.Code != CodeNotExe {
		t.Fatalf("MZ on Linux: %v", err)
	}
}

func fmtSum(name string) string {
	return string(bytes.Repeat([]byte("ab"), 32)) + "  " + name + "\n"
}

// The staged binary must be executable.
func TestOSFSCreateIsExecutable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x")
	w, err := OSFS{}.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(w, "x")
	w.Close()
	if st, err := os.Stat(p); err != nil || st.Mode().Perm()&0o100 == 0 {
		t.Fatalf("mode %v %v", st.Mode(), err)
	}
}

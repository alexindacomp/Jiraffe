package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jiraffe/internal/update"
)

// testSeed is a throw-away key, not the release key.
var testSeed = bytes.Repeat([]byte{7}, ed25519.SeedSize)

func TestSignVerifies(t *testing.T) {
	exe := []byte("MZ fake executable")
	sigB64, err := sign("  "+base64.StdEncoding.EncodeToString(testSeed)+"\n", "1.2.3", bytes.NewReader(exe))
	if err != nil {
		t.Fatal(err)
	}
	sig, err := update.ParseSignature([]byte(sigB64 + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	pub := ed25519.NewKeyFromSeed(testSeed).Public().(ed25519.PublicKey)
	sum := sha256.Sum256(exe)
	if !update.VerifySignature(pub, "1.2.3", sum, sig) {
		t.Error("signature does not verify")
	}
	if update.VerifySignature(pub, "1.2.4", sum, sig) || update.VerifySignature(update.PublicKey, "1.2.3", sum, sig) {
		t.Error("signature verifies for another version or key")
	}
}

func TestSignRejects(t *testing.T) {
	seed := base64.StdEncoding.EncodeToString(testSeed)
	for name, tc := range map[string]struct{ seed, version string }{
		"no key":          {"", "1.2.3"},
		"key not base64":  {"%%%", "1.2.3"},
		"short key":       {base64.StdEncoding.EncodeToString(testSeed[:31]), "1.2.3"},
		"64-byte key":     {base64.StdEncoding.EncodeToString(append(testSeed, testSeed...)), "1.2.3"},
		"version with v":  {seed, "v1.2.3"},
		"version x.y":     {seed, "1.2"},
		"version not num": {seed, "1.2.x"},
		"empty version":   {seed, ""},
		"leading zero":    {seed, "1.02.3"},
	} {
		_, err := sign(tc.seed, tc.version, strings.NewReader("MZ"))
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		if tc.seed != "" && strings.Contains(err.Error(), tc.seed) {
			t.Errorf("%s: the error contains the key", name)
		}
	}
}

func TestRun(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "jiraffe.exe")
	if err := os.WriteFile(exe, []byte("MZ binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	seed := base64.StdEncoding.EncodeToString(testSeed)
	env := func(k string) string {
		if k == "RELEASE_SIGNING_KEY" {
			return seed
		}
		return ""
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"-version", "0.5.0", "-file", exe}, env, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if lines := strings.Split(out.String(), "\n"); len(lines) != 2 || lines[1] != "" || len(lines[0]) != 88 {
		t.Errorf("output %q", out.String())
	}
	if strings.Contains(out.String()+errOut.String(), seed) {
		t.Error("key printed")
	}
	for _, args := range [][]string{{"-version", "0.5.0"}, {"-version", "0.5.0", "-file", filepath.Join(dir, "none")}, {"-file", exe}, {"-x"}} {
		out.Reset()
		errOut.Reset()
		if code := run(args, env, &out, &errOut); code == 0 || out.Len() != 0 || errOut.Len() == 0 {
			t.Errorf("%v: exit %d out %q err %q", args, code, out.String(), errOut.String())
		}
		if strings.Contains(errOut.String(), seed) {
			t.Errorf("%v: key printed", args)
		}
	}
}

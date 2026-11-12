// Command sign signs a release executable with the ed25519 release key
// (SPEC v0.4 §5, "Release signature"). CI runs it in the release job:
//
//	RELEASE_SIGNING_KEY=<base64 seed> go run ./tools/sign -version X.Y.Z -file dist/jiraffe.exe > dist/jiraffe.exe.sig
//
// The key comes only from the environment (base64 of the 32-byte seed); it
// is never written anywhere or printed. The output is one line: base64 of
// the 64-byte signature over update.SignedMessage(version, sha256(file)).
package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"jiraffe/internal/update"
)

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// run is main without the process: it returns the exit code.
func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sign", flag.ContinueOnError)
	fs.SetOutput(stderr)
	version := fs.String("version", "", "release version X.Y.Z (without the v)")
	file := fs.String("file", "", "executable to sign")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *file == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: sign -version X.Y.Z -file jiraffe.exe (key in RELEASE_SIGNING_KEY)")
		return 2
	}
	f, err := os.Open(*file)
	if err != nil {
		fmt.Fprintln(stderr, "sign:", err)
		return 1
	}
	defer f.Close()
	sig, err := sign(getenv("RELEASE_SIGNING_KEY"), *version, f)
	if err != nil {
		fmt.Fprintln(stderr, "sign:", err)
		return 1
	}
	if _, err := fmt.Fprintln(stdout, sig); err != nil {
		fmt.Fprintln(stderr, "sign:", err)
		return 1
	}
	return 0
}

// sign returns base64 of the signature of the executable read from exe for
// version, with the key seedB64 (base64 of the 32-byte seed). Errors never
// contain key material.
func sign(seedB64, version string, exe io.Reader) (string, error) {
	if v, ok := update.ParseVersion(version); !ok || v.String() != version {
		return "", fmt.Errorf("version %q is not X.Y.Z", version)
	}
	seedB64 = strings.TrimSpace(seedB64)
	if seedB64 == "" {
		return "", errors.New("RELEASE_SIGNING_KEY is not set")
	}
	seed, err := base64.StdEncoding.DecodeString(seedB64)
	if err != nil || len(seed) != ed25519.SeedSize {
		return "", errors.New("RELEASE_SIGNING_KEY must be base64 of a 32-byte ed25519 seed")
	}
	h := sha256.New()
	if _, err := io.Copy(h, exe); err != nil {
		return "", fmt.Errorf("read the executable: %w", err)
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	key := ed25519.NewKeyFromSeed(seed)
	for i := range seed {
		seed[i] = 0
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(key, update.SignedMessage(version, sum))), nil
}

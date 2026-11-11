package winapi

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

const cryptprotectUIForbidden = 0x1

// dpapiEntropy binds the blobs to this application.
var dpapiEntropy = []byte("Jiraffe/token/v1")

// DPAPI protects secrets for the current Windows user (CryptProtectData).
type DPAPI struct{}

func blob(b []byte) *windows.DataBlob {
	if len(b) == 0 {
		return &windows.DataBlob{}
	}
	return &windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
}

func takeBlob(out *windows.DataBlob) []byte {
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	if out.Size == 0 {
		return nil
	}
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...)
}

// Protect encrypts plain for the current user.
func (DPAPI) Protect(plain []byte) ([]byte, error) {
	var out windows.DataBlob
	if err := windows.CryptProtectData(blob(plain), nil, blob(dpapiEntropy), 0, nil, cryptprotectUIForbidden, &out); err != nil {
		return nil, fmt.Errorf("CryptProtectData: %w", err)
	}
	return takeBlob(&out), nil
}

// Unprotect decrypts a blob produced by Protect.
func (DPAPI) Unprotect(b []byte) ([]byte, error) {
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(blob(b), nil, blob(dpapiEntropy), 0, nil, cryptprotectUIForbidden, &out); err != nil {
		return nil, fmt.Errorf("CryptUnprotectData: %w", err)
	}
	return takeBlob(&out), nil
}

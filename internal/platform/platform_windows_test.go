//go:build windows

package platform

import (
	"bytes"
	"testing"
)

func TestProtectRoundTrip(t *testing.T) {
	in := []byte(`{"refresh_token":"secret-123"}`)
	enc, err := Protect(in)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(enc, []byte("secret-123")) {
		t.Fatal("not encrypted")
	}
	out, err := Unprotect(enc)
	if err != nil || !bytes.Equal(in, out) {
		t.Fatalf("roundtrip failed: %v %q", err, out)
	}
}

func TestRegistryAndDrives(t *testing.T) {
	if err := regSetString(`Software\SelvaSyncTest`, "v", "hello"); err != nil {
		t.Fatal(err)
	}
	regDeleteTree(`Software\SelvaSyncTest`)
	if len(Drives()) == 0 {
		t.Fatal("no drives")
	}
	if !SingleInstance() {
		t.Fatal("first instance should be true")
	}
	if SingleInstance() {
		t.Fatal("second instance should be false")
	}
	if UserLanguage() == "" {
		t.Fatal("no language")
	}
	var d notifyIconData
	_ = d
}

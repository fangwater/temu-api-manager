package shopregistry

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateKeyUsesPrivateStableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.key")
	first, err := loadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("key mode = %o, want 600", got)
	}
	second, err := loadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("credential key changed after reload")
	}
}

func TestLoadOrCreateKeyRejectsBroadPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shop.key")
	if _, err := loadOrCreateKey(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateKey(path); err == nil {
		t.Fatal("expected insecure key permissions to be rejected")
	}
}

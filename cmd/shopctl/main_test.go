package main

import (
	"strings"
	"testing"
)

func TestCredentialFromEnv(t *testing.T) {
	t.Setenv("TEMU_TEST_CREDENTIAL", "  test-value  ")
	value, err := credentialFromEnv("TEMU_TEST_CREDENTIAL")
	if err != nil {
		t.Fatal(err)
	}
	if value != "test-value" {
		t.Fatalf("credential = %q", value)
	}
}

func TestCredentialFromEnvRejectsInvalidName(t *testing.T) {
	if _, err := credentialFromEnv("TEMU-SECRET"); err == nil {
		t.Fatal("invalid environment variable name must fail")
	}
}

func TestReadAccessTokenFromEnv(t *testing.T) {
	t.Setenv("TEMU_TEST_ACCESS_TOKEN", "stored-token")
	value, err := readAccessToken("TEMU_TEST_ACCESS_TOKEN", strings.NewReader("stdin-token"))
	if err != nil {
		t.Fatal(err)
	}
	if value != "stored-token" {
		t.Fatalf("access token = %q", value)
	}
}

func TestReadAccessTokenFromStdin(t *testing.T) {
	value, err := readAccessToken("", strings.NewReader("  stdin-token\n"))
	if err != nil {
		t.Fatal(err)
	}
	if value != "stdin-token" {
		t.Fatalf("access token = %q", value)
	}
}

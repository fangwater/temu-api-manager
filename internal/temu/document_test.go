package temu

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestDownloadDocumentSendsSignedDownloadHeaders(t *testing.T) {
	const (
		appKey      = "test-app-key"
		appSecret   = "test-secret"
		accessToken = "test-token"
		timestamp   = int64(1785758400)
	)

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", request.Method)
		}
		random := request.Header.Get("toa-random")
		if !regexp.MustCompile(`^[A-Za-z]{32}$`).MatchString(random) {
			t.Errorf("toa-random = %q, want 32 letters", random)
		}
		headers := map[string]any{
			"toa-app-key":      request.Header.Get("toa-app-key"),
			"toa-access-token": request.Header.Get("toa-access-token"),
			"toa-random":       random,
			"toa-timestamp":    request.Header.Get("toa-timestamp"),
		}
		if got, want := request.Header.Get("toa-sign"), BuildSignature(headers, appSecret); got != want {
			t.Errorf("toa-sign = %q, want %q", got, want)
		}
		if got := request.Header.Get("User-Agent"); got != documentUserAgent {
			t.Errorf("User-Agent = %q", got)
		}
		if got := request.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		if got := request.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		writer.Header().Set("Content-Type", "application/pdf")
		_, _ = io.WriteString(writer, "%PDF-1.4 test")
	}))
	defer server.Close()

	client := NewClient("https://unused.example", appKey, appSecret, accessToken, time.Second)
	client.httpClient = server.Client()
	client.clock = func() time.Time { return time.Unix(timestamp, 0) }
	body, contentType, err := client.DownloadDocument(context.Background(), server.URL+"/label.pdf")
	if err != nil {
		t.Fatalf("DownloadDocument() error = %v", err)
	}
	if got, want := string(body), "%PDF-1.4 test"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	if contentType != "application/pdf" {
		t.Fatalf("content type = %q", contentType)
	}
}

func TestDownloadDocumentIncludesUpstreamError(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(writer, `{ "error": "signature rejected" }`)
	}))
	defer server.Close()

	client := NewClient("https://unused.example", "key", "secret", "token", time.Second)
	client.httpClient = server.Client()
	_, _, err := client.DownloadDocument(context.Background(), server.URL+"/label.pdf")
	if err == nil || !strings.Contains(err.Error(), "signature rejected") {
		t.Fatalf("DownloadDocument() error = %v", err)
	}
}

func TestDownloadDocumentUsesConfiguredProxy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got, want := request.URL.Path, "/pkg-label-u/generated/label.pdf"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		if got, want := request.URL.RawQuery, "token=preserved"; got != want {
			t.Errorf("query = %q, want %q", got, want)
		}
		writer.Header().Set("Content-Type", "application/pdf")
		_, _ = io.WriteString(writer, "%PDF-1.4 proxied")
	}))
	defer server.Close()

	client := NewClient("https://unused.example", "key", "secret", "token", time.Second)
	client.httpClient = server.Client()
	if err := client.SetDocumentProxyBaseURL(server.URL); err != nil {
		t.Fatalf("SetDocumentProxyBaseURL() error = %v", err)
	}
	body, _, err := client.DownloadDocument(context.Background(), "https://openapi-b-us.temu.com/pkg-label-u/generated/label.pdf?token=preserved")
	if err != nil {
		t.Fatalf("DownloadDocument() error = %v", err)
	}
	if got, want := string(body), "%PDF-1.4 proxied"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

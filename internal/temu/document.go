package temu

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
)

const documentUserAgent = "Temu API Client-Golang/0.0.1"

func (c *Client) DownloadDocument(ctx context.Context, documentURL string) ([]byte, string, error) {
	requestURL, err := c.documentRequestURL(documentURL)
	if err != nil {
		return nil, "", err
	}
	random, err := randomLetters(32)
	if err != nil {
		return nil, "", err
	}
	headers := map[string]any{
		"toa-app-key":      c.appKey,
		"toa-access-token": c.accessToken,
		"toa-random":       random,
		"toa-timestamp":    strconv.FormatInt(c.clock().Unix(), 10),
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, "", err
	}
	for key, value := range headers {
		request.Header.Set(key, value.(string))
	}
	request.Header.Set("toa-sign", BuildSignature(headers, c.appSecret))
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", documentUserAgent)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
		detail := strings.Join(strings.Fields(string(raw)), " ")
		if detail == "" {
			return nil, "", fmt.Errorf("Temu document download returned HTTP %d", response.StatusCode)
		}
		return nil, "", fmt.Errorf("Temu document download returned HTTP %d: %s", response.StatusCode, detail)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return nil, "", err
	}
	contentType := response.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/pdf"
	}
	return body, contentType, nil
}

func (c *Client) documentRequestURL(raw string) (string, error) {
	upstream, err := neturl.Parse(raw)
	if err != nil || upstream.Scheme != "https" || upstream.Host == "" {
		return "", fmt.Errorf("Temu returned an invalid document URL")
	}
	if c.documentProxyBaseURL == "" {
		return upstream.String(), nil
	}
	proxy, err := neturl.Parse(c.documentProxyBaseURL)
	if err != nil {
		return "", err
	}
	proxy.Path = strings.TrimRight(proxy.Path, "/") + "/" + strings.TrimLeft(upstream.Path, "/")
	proxy.RawPath = ""
	proxy.RawQuery = upstream.RawQuery
	proxy.Fragment = ""
	return proxy.String(), nil
}

func randomLetters(length int) (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	raw := make([]byte, length)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	for index := range raw {
		raw[index] = alphabet[int(raw[index])%len(alphabet)]
	}
	return string(raw), nil
}

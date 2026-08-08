package temu

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type APIError struct {
	APIType   string
	Status    int
	Code      string
	Message   string
	Temporary bool
	Cause     error
}

func (e *APIError) Error() string {
	operation := "Temu API"
	if e.APIType != "" {
		operation += " " + e.APIType
	}
	if e.Code == "" {
		return fmt.Sprintf("%s HTTP %d: %s", operation, e.Status, e.Message)
	}
	return fmt.Sprintf("%s error code=%s msg=%s", operation, e.Code, e.Message)
}

func (e *APIError) Unwrap() error { return e.Cause }

func IsRateLimitError(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(apiErr.Message))
	return apiErr.Code == "4000004" || apiErr.Status == http.StatusTooManyRequests ||
		strings.Contains(message, "too frequent requests") || strings.Contains(message, "rate limit")
}

type Client struct {
	baseURL              string
	documentProxyBaseURL string
	appKey               string
	appSecret            string
	accessToken          string
	httpClient           *http.Client
	clock                func() time.Time
	requestInterval      time.Duration
	requestMu            sync.Mutex
	nextRequestAt        time.Time
	shipmentCreateSlots  chan struct{}
}

func NewClient(baseURL, appKey, appSecret, accessToken string, timeout time.Duration) *Client {
	return &Client{
		baseURL: strings.TrimSpace(baseURL), appKey: appKey, appSecret: appSecret, accessToken: accessToken,
		httpClient: &http.Client{Timeout: timeout}, clock: time.Now, shipmentCreateSlots: make(chan struct{}, 2),
	}
}

func (c *Client) SetRequestInterval(interval time.Duration) error {
	if interval < 0 || interval > 10*time.Second {
		return errors.New("Temu API request interval must be between 0 and 10 seconds")
	}
	c.requestMu.Lock()
	defer c.requestMu.Unlock()
	c.requestInterval = interval
	c.nextRequestAt = time.Time{}
	return nil
}

func (c *Client) waitForRequestSlot(ctx context.Context) error {
	c.requestMu.Lock()
	interval := c.requestInterval
	if interval <= 0 {
		c.requestMu.Unlock()
		return nil
	}
	now := time.Now()
	scheduled := now
	if c.nextRequestAt.After(scheduled) {
		scheduled = c.nextRequestAt
	}
	c.nextRequestAt = scheduled.Add(interval)
	c.requestMu.Unlock()
	delay := time.Until(scheduled)
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) SetShipmentCreateConcurrency(limit int) error {
	if limit < 1 || limit > 20 {
		return errors.New("Temu shipment create concurrency must be between 1 and 20")
	}
	c.shipmentCreateSlots = make(chan struct{}, limit)
	return nil
}

func (c *Client) acquireShipmentCreate(ctx context.Context) (func(), error) {
	select {
	case c.shipmentCreateSlots <- struct{}{}:
		return func() { <-c.shipmentCreateSlots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *Client) SetDocumentProxyBaseURL(baseURL string) error {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		c.documentProxyBaseURL = ""
		return nil
	}
	parsed, err := neturl.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("Temu document proxy base URL must be an absolute HTTP(S) URL")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("Temu document proxy base URL cannot contain a query or fragment")
	}
	c.documentProxyBaseURL = parsed.String()
	return nil
}

func (c *Client) Call(ctx context.Context, apiType string, parameters map[string]any, result any) (json.RawMessage, error) {
	payload, err := c.BuildPayload(apiType, parameters)
	if err != nil {
		return nil, err
	}
	if err := c.waitForRequestSlot(ctx); err != nil {
		return nil, &APIError{APIType: apiType, Message: err.Error(), Temporary: true, Cause: err}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal Temu request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, &APIError{APIType: apiType, Message: err.Error(), Temporary: true}
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return nil, &APIError{APIType: apiType, Status: response.StatusCode, Message: err.Error(), Temporary: true}
	}
	var envelope struct {
		Success   bool            `json:"success"`
		ErrorCode json.RawMessage `json:"errorCode"`
		ErrorMsg  string          `json:"errorMsg"`
		Result    json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return raw, &APIError{APIType: apiType, Status: response.StatusCode, Message: "invalid JSON response", Temporary: response.StatusCode >= 500 || response.StatusCode == http.StatusTooManyRequests}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !envelope.Success {
		apiErr := &APIError{APIType: apiType, Status: response.StatusCode, Code: rawScalar(envelope.ErrorCode), Message: envelope.ErrorMsg, Temporary: response.StatusCode >= 500 || response.StatusCode == http.StatusTooManyRequests}
		apiErr.Temporary = apiErr.Temporary || IsRateLimitError(apiErr)
		return raw, apiErr
	}
	if result != nil && len(envelope.Result) > 0 && string(envelope.Result) != "null" {
		if err := json.Unmarshal(envelope.Result, result); err != nil {
			return raw, fmt.Errorf("decode Temu %s result: %w", apiType, err)
		}
	}
	return raw, nil
}

func (c *Client) BuildPayload(apiType string, parameters map[string]any) (map[string]any, error) {
	payload := map[string]any{
		"access_token": c.accessToken,
		"app_key":      c.appKey,
		"data_type":    "JSON",
		"timestamp":    strconv.FormatInt(c.clock().Unix(), 10),
		"type":         apiType,
	}
	for key, value := range parameters {
		if _, reserved := payload[key]; reserved || key == "sign" {
			return nil, errors.New("reserved Temu parameter cannot be overridden: " + key)
		}
		if value != nil {
			payload[key] = value
		}
	}
	payload["sign"] = BuildSignature(payload, c.appSecret)
	return payload, nil
}

func BuildSignature(parameters map[string]any, appSecret string) string {
	keys := make([]string, 0, len(parameters))
	for key, value := range parameters {
		if key != "sign" && value != nil {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var joined strings.Builder
	joined.WriteString(appSecret)
	for _, key := range keys {
		joined.WriteString(key)
		joined.WriteString(serializeSignValue(parameters[key]))
	}
	joined.WriteString(appSecret)
	sum := md5.Sum([]byte(joined.String()))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func serializeSignValue(value any) string {
	switch current := value.(type) {
	case string:
		return current
	case bool:
		return strconv.FormatBool(current)
	case json.Number:
		return current.String()
	default:
		if encoded, err := json.Marshal(current); err == nil {
			return string(encoded)
		}
		return fmt.Sprint(current)
	}
}

func rawScalar(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	return strings.Trim(string(raw), "\"")
}

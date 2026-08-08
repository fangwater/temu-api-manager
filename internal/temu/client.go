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
	"time"
)

type APIError struct {
	Status    int
	Code      string
	Message   string
	Temporary bool
	Cause     error
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("Temu HTTP %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("Temu API error code=%s msg=%s", e.Code, e.Message)
}

func (e *APIError) Unwrap() error { return e.Cause }

type Client struct {
	baseURL              string
	documentProxyBaseURL string
	appKey               string
	appSecret            string
	accessToken          string
	httpClient           *http.Client
	clock                func() time.Time
	shipmentCreateSlots  chan struct{}
}

func NewClient(baseURL, appKey, appSecret, accessToken string, timeout time.Duration) *Client {
	return &Client{
		baseURL: strings.TrimSpace(baseURL), appKey: appKey, appSecret: appSecret, accessToken: accessToken,
		httpClient: &http.Client{Timeout: timeout}, clock: time.Now, shipmentCreateSlots: make(chan struct{}, 2),
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
		return nil, &APIError{Message: err.Error(), Temporary: true}
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return nil, &APIError{Status: response.StatusCode, Message: err.Error(), Temporary: true}
	}
	var envelope struct {
		Success   bool            `json:"success"`
		ErrorCode json.RawMessage `json:"errorCode"`
		ErrorMsg  string          `json:"errorMsg"`
		Result    json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return raw, &APIError{Status: response.StatusCode, Message: "invalid JSON response", Temporary: response.StatusCode >= 500}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !envelope.Success {
		return raw, &APIError{Status: response.StatusCode, Code: rawScalar(envelope.ErrorCode), Message: envelope.ErrorMsg, Temporary: response.StatusCode >= 500}
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

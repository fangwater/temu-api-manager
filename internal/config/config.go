package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultAPIBaseURL                = "http://13.115.227.29:6355/openapi/router"
	DefaultDocumentProxyBaseURL      = "http://13.115.227.29:6355"
	DefaultListen                    = "127.0.0.1:18082"
	DefaultWarehouseDecisionURL      = "https://pangutech.online/warehouse-console/api/temu/warehouse-availability/query"
	DefaultXLWMSOutboundURL          = "https://pangutech.online/warehouse-console/api/outbound"
	DefaultOrderSyncInterval         = 5 * time.Minute
	DefaultOMSQueryInterval          = 2 * time.Minute
	DefaultPickupAuditInterval       = 10 * time.Minute
	DefaultFulfillmentInterval       = 5 * time.Second
	DefaultFulfillmentConcurrency    = 8
	DefaultShipmentCreateConcurrency = 2
	DefaultRequestTimeout            = 30 * time.Second
	DefaultShippingQuoteLifetime     = 10 * time.Minute
	DefaultStoreCode                 = "panda-homes"
	DefaultStoreName                 = "PANDA HOMES"
)

type Config struct {
	DatabaseURL               string
	APIBaseURL                string
	DocumentProxyBaseURL      string
	AccessToken               string
	AppKey                    string
	AppSecret                 string
	OperationKey              string
	StoreCode                 string
	StoreName                 string
	ShopCredentialsKeyFile    string
	Listen                    string
	StaticRoot                string
	WarehouseDecisionURL      string
	XLWMSOutboundURL          string
	OrderSyncInterval         time.Duration
	OMSQueryInterval          time.Duration
	PickupAuditInterval       time.Duration
	FulfillmentInterval       time.Duration
	FulfillmentConcurrency    int
	ShipmentCreateConcurrency int
	RequestTimeout            time.Duration
	QuoteLifetime             time.Duration
}

func Load() (Config, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return Config{}, err
	}
	envFile := strings.TrimSpace(os.Getenv("TEMU_ENV_FILE"))
	if envFile == "" {
		envFile = filepath.Join(cwd, ".env")
	} else if !filepath.IsAbs(envFile) {
		envFile = filepath.Join(cwd, envFile)
	}
	if err := loadDotEnv(envFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("load .env: %w", err)
	}
	cfg := Config{
		DatabaseURL:               strings.TrimSpace(os.Getenv("TEMU_DATABASE_URL")),
		APIBaseURL:                strings.TrimSpace(envOrDefault("TEMU_API_BASE_URL", DefaultAPIBaseURL)),
		DocumentProxyBaseURL:      strings.TrimSpace(envOrDefault("TEMU_DOCUMENT_PROXY_BASE_URL", DefaultDocumentProxyBaseURL)),
		AccessToken:               strings.TrimSpace(os.Getenv("TEMU_ACCESS_TOKEN")),
		AppKey:                    strings.TrimSpace(os.Getenv("TEMU_APP_KEY")),
		AppSecret:                 strings.TrimSpace(os.Getenv("TEMU_APP_SECRET")),
		OperationKey:              strings.TrimSpace(os.Getenv("TEMU_OPERATION_KEY")),
		StoreCode:                 strings.TrimSpace(envOrDefault("TEMU_STORE_CODE", DefaultStoreCode)),
		StoreName:                 strings.TrimSpace(envOrDefault("TEMU_STORE_NAME", DefaultStoreName)),
		ShopCredentialsKeyFile:    strings.TrimSpace(envOrDefault("TEMU_SHOP_CREDENTIALS_KEY_FILE", filepath.Join(cwd, ".temu_shop_credentials_key"))),
		Listen:                    strings.TrimSpace(envOrDefault("TEMU_LISTEN", DefaultListen)),
		StaticRoot:                strings.TrimSpace(envOrDefault("TEMU_STATIC_ROOT", filepath.Join(cwd, "web"))),
		WarehouseDecisionURL:      strings.TrimSpace(envOrDefault("XLWMS_WAREHOUSE_DECISION_URL", DefaultWarehouseDecisionURL)),
		XLWMSOutboundURL:          strings.TrimSpace(envOrDefault("XLWMS_OUTBOUND_URL", DefaultXLWMSOutboundURL)),
		OrderSyncInterval:         DefaultOrderSyncInterval,
		OMSQueryInterval:          DefaultOMSQueryInterval,
		PickupAuditInterval:       DefaultPickupAuditInterval,
		FulfillmentInterval:       DefaultFulfillmentInterval,
		FulfillmentConcurrency:    DefaultFulfillmentConcurrency,
		ShipmentCreateConcurrency: DefaultShipmentCreateConcurrency,
		RequestTimeout:            DefaultRequestTimeout,
		QuoteLifetime:             DefaultShippingQuoteLifetime,
	}
	for name, value := range map[string]string{
		"TEMU_DATABASE_URL":  cfg.DatabaseURL,
		"TEMU_ACCESS_TOKEN":  cfg.AccessToken,
		"TEMU_APP_KEY":       cfg.AppKey,
		"TEMU_APP_SECRET":    cfg.AppSecret,
		"TEMU_OPERATION_KEY": cfg.OperationKey,
	} {
		if value == "" {
			return Config{}, errors.New(name + " is required")
		}
	}
	if cfg.OrderSyncInterval, err = duration("TEMU_ORDER_SYNC_INTERVAL", cfg.OrderSyncInterval); err != nil {
		return Config{}, err
	}
	if cfg.OMSQueryInterval, err = duration("TEMU_OMS_QUERY_INTERVAL", cfg.OMSQueryInterval); err != nil {
		return Config{}, err
	}
	if cfg.PickupAuditInterval, err = duration("TEMU_PICKUP_AUDIT_INTERVAL", cfg.PickupAuditInterval); err != nil {
		return Config{}, err
	}
	if cfg.FulfillmentInterval, err = duration("TEMU_FULFILLMENT_INTERVAL", cfg.FulfillmentInterval); err != nil {
		return Config{}, err
	}
	if cfg.FulfillmentConcurrency, err = positiveInt("TEMU_FULFILLMENT_CONCURRENCY", cfg.FulfillmentConcurrency); err != nil {
		return Config{}, err
	}
	if cfg.ShipmentCreateConcurrency, err = positiveInt("TEMU_SHIPMENT_CREATE_CONCURRENCY", cfg.ShipmentCreateConcurrency); err != nil {
		return Config{}, err
	}
	if cfg.RequestTimeout, err = duration("TEMU_REQUEST_TIMEOUT", cfg.RequestTimeout); err != nil {
		return Config{}, err
	}
	if cfg.QuoteLifetime, err = duration("TEMU_SHIPPING_QUOTE_LIFETIME", cfg.QuoteLifetime); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func duration(name string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, errors.New(name + " must be a positive duration")
	}
	return value, nil
}
func positiveInt(name string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > 32 {
		return 0, errors.New(name + " must be an integer between 1 and 32")
	}
	return value, nil
}

func loadDotEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, raw, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(strings.TrimPrefix(name, "export "))
		if !ok || name == "" {
			continue
		}
		if _, exists := os.LookupEnv(name); exists {
			continue
		}
		value := strings.TrimSpace(raw)
		if len(value) >= 2 && ((value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"')) {
			if value[0] == '"' {
				if unquoted, unquoteErr := strconv.Unquote(value); unquoteErr == nil {
					value = unquoted
				}
			} else {
				value = value[1 : len(value)-1]
			}
		}
		if err := os.Setenv(name, value); err != nil {
			return err
		}
	}
	return scanner.Err()
}

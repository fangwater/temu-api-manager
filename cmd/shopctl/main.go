package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"temu-api-manager/internal/config"
	"temu-api-manager/internal/shopregistry"
	"temu-api-manager/internal/store"
	"temu-api-manager/internal/temu"
)

func main() {
	code := flag.String("code", "", "shop code")
	name := flag.String("name", "", "shop display name")
	schema := flag.String("schema", "", "PostgreSQL schema")
	appKeyEnv := flag.String("app-key-env", "TEMU_APP_KEY", "environment variable containing the app key")
	appSecretEnv := flag.String("app-secret-env", "TEMU_APP_SECRET", "environment variable containing the app secret")
	accessTokenEnv := flag.String("access-token-env", "", "environment variable containing the access token; defaults to standard input")
	enabled := flag.Bool("enabled", true, "enable the registered shop")
	check := flag.Bool("check", false, "check a stored shop access token")
	flag.Parse()
	cfg, err := config.Load()
	if err != nil {
		fatal(err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	registry, err := shopregistry.New(ctx, cfg.DatabaseURL, cfg.ShopCredentialsKeyFile)
	if err != nil {
		fatal(err.Error())
	}
	defer registry.Close()
	if *check {
		checkShop(ctx, registry, cfg, *code)
		return
	}
	if *code == "" || *name == "" || *schema == "" {
		fatal("code, name, and schema are required")
	}
	appKey, err := credentialFromEnv(*appKeyEnv)
	if err != nil {
		fatal(err.Error())
	}
	appSecret, err := credentialFromEnv(*appSecretEnv)
	if err != nil {
		fatal(err.Error())
	}
	accessToken, err := readAccessToken(*accessTokenEnv, os.Stdin)
	if err != nil {
		fatal(err.Error())
	}
	if err := registry.Upsert(ctx, shopregistry.Shop{
		Code: *code, Name: *name, SchemaName: *schema,
		AppKey: appKey, AppSecret: appSecret, AccessToken: accessToken, Enabled: *enabled,
	}); err != nil {
		fatal(err.Error())
	}
	destination, err := store.NewPostgresInSchema(ctx, cfg.DatabaseURL, *schema)
	if err != nil {
		fatal(err.Error())
	}
	defer destination.Close()
	if err := destination.Migrate(ctx); err != nil {
		fatal(err.Error())
	}
	fmt.Printf("shop %s stored with encrypted credentials; enabled=%t\n", *code, *enabled)
}

func checkShop(ctx context.Context, registry *shopregistry.Registry, cfg config.Config, code string) {
	code = strings.TrimSpace(code)
	if code == "" {
		fatal("code is required for token check")
	}
	shops, err := registry.List(ctx)
	if err != nil {
		fatal(err.Error())
	}
	for _, shop := range shops {
		if shop.Code != code {
			continue
		}
		client := temu.NewClient(cfg.APIBaseURL, shop.AppKey, shop.AppSecret, shop.AccessToken, cfg.RequestTimeout)
		info, _, err := client.TokenInfo(ctx)
		if err != nil {
			fatal(err.Error())
		}
		fmt.Printf("shop %s token valid; expires %s; scopes %d\n", shop.Code, time.Unix(info.ExpiredTime, 0).Format(time.RFC3339), len(info.APIScopes))
		return
	}
	fatal("shop not found")
}

var envNamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

func credentialFromEnv(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !envNamePattern.MatchString(name) {
		return "", fmt.Errorf("invalid credential environment variable name %q", name)
	}
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}

func readAccessToken(envName string, input io.Reader) (string, error) {
	if strings.TrimSpace(envName) != "" {
		return credentialFromEnv(envName)
	}
	tokenBytes, err := io.ReadAll(io.LimitReader(input, 16*1024))
	if err != nil {
		return "", fmt.Errorf("read access token: %w", err)
	}
	accessToken := strings.TrimSpace(string(tokenBytes))
	if accessToken == "" {
		return "", fmt.Errorf("access token must be provided on standard input or with -access-token-env")
	}
	return accessToken, nil
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}

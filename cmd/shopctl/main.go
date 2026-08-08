package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
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
	tokenBytes, err := io.ReadAll(io.LimitReader(os.Stdin, 16*1024))
	if err != nil {
		fatal("read access token: " + err.Error())
	}
	accessToken := strings.TrimSpace(string(tokenBytes))
	if accessToken == "" {
		fatal("access token must be provided on standard input")
	}
	if err := registry.Upsert(ctx, shopregistry.Shop{
		Code: *code, Name: *name, SchemaName: *schema,
		AppKey: cfg.AppKey, AppSecret: cfg.AppSecret, AccessToken: accessToken, Enabled: true,
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
	fmt.Printf("shop %s stored with encrypted credentials\n", *code)
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

func fatal(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}

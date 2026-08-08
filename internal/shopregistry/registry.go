package shopregistry

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	codePattern   = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	schemaPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

type Shop struct {
	Code        string
	Name        string
	SchemaName  string
	AppKey      string
	AppSecret   string
	AccessToken string
	Enabled     bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Registry struct {
	pool *pgxpool.Pool
	aead cipher.AEAD
}

func New(ctx context.Context, databaseURL, keyFile string) (*Registry, error) {
	key, err := loadOrCreateKey(keyFile)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize shop credential cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize shop credential GCM: %w", err)
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("create shop registry pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect shop registry database: %w", err)
	}
	registry := &Registry{pool: pool, aead: aead}
	if err := registry.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return registry, nil
}

func (r *Registry) Close() {
	r.pool.Close()
}

func (r *Registry) migrate(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `
        CREATE TABLE IF NOT EXISTS public.temu_shops (
            code text PRIMARY KEY,
            name text NOT NULL,
            schema_name text NOT NULL UNIQUE,
            app_key_cipher text NOT NULL,
            app_secret_cipher text NOT NULL,
            access_token_cipher text NOT NULL,
            enabled boolean NOT NULL DEFAULT true,
            created_at timestamptz NOT NULL DEFAULT now(),
            updated_at timestamptz NOT NULL DEFAULT now()
        )
    `)
	if err != nil {
		return fmt.Errorf("migrate shop registry: %w", err)
	}
	return nil
}

func (r *Registry) Upsert(ctx context.Context, shop Shop) error {
	return r.write(ctx, shop, true)
}

// Ensure seeds a shop only when it is not already registered.
func (r *Registry) Ensure(ctx context.Context, shop Shop) error {
	var exists bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM public.temu_shops WHERE code=$1)`, strings.TrimSpace(shop.Code)).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	return r.write(ctx, shop, false)
}

func (r *Registry) write(ctx context.Context, shop Shop, update bool) error {
	shop.Code = strings.TrimSpace(shop.Code)
	shop.Name = strings.TrimSpace(shop.Name)
	shop.SchemaName = strings.TrimSpace(shop.SchemaName)
	if !codePattern.MatchString(shop.Code) {
		return errors.New("shop code must use lowercase letters, digits, and hyphens")
	}
	if shop.Name == "" {
		return errors.New("shop name is required")
	}
	if !schemaPattern.MatchString(shop.SchemaName) {
		return errors.New("shop schema must use lowercase letters, digits, and underscores")
	}
	if shop.AppKey == "" || shop.AppSecret == "" || shop.AccessToken == "" {
		return errors.New("shop app key, app secret, and access token are required")
	}
	appKey, err := r.encrypt(shop.AppKey)
	if err != nil {
		return err
	}
	appSecret, err := r.encrypt(shop.AppSecret)
	if err != nil {
		return err
	}
	accessToken, err := r.encrypt(shop.AccessToken)
	if err != nil {
		return err
	}
	if _, err := r.pool.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS `+shop.SchemaName); err != nil {
		return fmt.Errorf("create shop schema: %w", err)
	}
	query := `
        INSERT INTO public.temu_shops(
            code,name,schema_name,app_key_cipher,app_secret_cipher,access_token_cipher,enabled
        ) VALUES($1,$2,$3,$4,$5,$6,$7)
        ON CONFLICT(code) DO NOTHING`
	if update {
		query = `
        INSERT INTO public.temu_shops(
            code,name,schema_name,app_key_cipher,app_secret_cipher,access_token_cipher,enabled
        ) VALUES($1,$2,$3,$4,$5,$6,$7)
        ON CONFLICT(code) DO UPDATE SET
            name=EXCLUDED.name,
            schema_name=EXCLUDED.schema_name,
            app_key_cipher=EXCLUDED.app_key_cipher,
            app_secret_cipher=EXCLUDED.app_secret_cipher,
            access_token_cipher=EXCLUDED.access_token_cipher,
            enabled=EXCLUDED.enabled,
updated_at=now()`
	}
	_, err = r.pool.Exec(ctx, query, shop.Code, shop.Name, shop.SchemaName, appKey, appSecret, accessToken, shop.Enabled)
	if err != nil {
		return fmt.Errorf("upsert shop: %w", err)
	}
	return nil
}

func (r *Registry) List(ctx context.Context) ([]Shop, error) {
	rows, err := r.pool.Query(ctx, `
        SELECT code,name,schema_name,app_key_cipher,app_secret_cipher,access_token_cipher,
               enabled,created_at,updated_at
        FROM public.temu_shops
        ORDER BY name,code
    `)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	shops := make([]Shop, 0, 4)
	for rows.Next() {
		var shop Shop
		var appKey, appSecret, accessToken string
		if err := rows.Scan(&shop.Code, &shop.Name, &shop.SchemaName, &appKey, &appSecret, &accessToken, &shop.Enabled, &shop.CreatedAt, &shop.UpdatedAt); err != nil {
			return nil, err
		}
		if shop.AppKey, err = r.decrypt(appKey); err != nil {
			return nil, fmt.Errorf("decrypt app key for shop %s: %w", shop.Code, err)
		}
		if shop.AppSecret, err = r.decrypt(appSecret); err != nil {
			return nil, fmt.Errorf("decrypt app secret for shop %s: %w", shop.Code, err)
		}
		if shop.AccessToken, err = r.decrypt(accessToken); err != nil {
			return nil, fmt.Errorf("decrypt access token for shop %s: %w", shop.Code, err)
		}
		shops = append(shops, shop)
	}
	return shops, rows.Err()
}

func (r *Registry) encrypt(value string) (string, error) {
	nonce := make([]byte, r.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate credential nonce: %w", err)
	}
	encrypted := r.aead.Seal(nil, nonce, []byte(value), nil)
	payload := append(nonce, encrypted...)
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func (r *Registry) decrypt(value string) (string, error) {
	payload, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", errors.New("invalid encrypted credential")
	}
	nonceSize := r.aead.NonceSize()
	if len(payload) <= nonceSize {
		return "", errors.New("invalid encrypted credential")
	}
	decrypted, err := r.aead.Open(nil, payload[:nonceSize], payload[nonceSize:], nil)
	if err != nil {
		return "", errors.New("decrypt encrypted credential")
	}
	return string(decrypted), nil
}

func loadOrCreateKey(path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("shop credential key file is required")
	}
	raw, err := os.ReadFile(path)
	if err == nil {
		info, statErr := os.Stat(path)
		if statErr != nil {
			return nil, statErr
		}
		if info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("shop credential key file %s must have mode 600", path)
		}
		key, decodeErr := base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if decodeErr != nil || len(key) != 32 {
			return nil, errors.New("shop credential key file is invalid")
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	encoded := base64.RawURLEncoding.EncodeToString(key) + "\n"
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := file.WriteString(encoded); err != nil {
		file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	return key, nil
}

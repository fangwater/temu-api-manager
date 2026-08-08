package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var schemaNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

var privateTables = []string{
	"temu_sync_runs",
	"temu_orders",
	"temu_order_lines",
	"temu_order_details",
	"temu_order_manual_reviews",
	"temu_order_warehouse_checks",
	"temu_shipping_quotes",
	"temu_shipments",
	"temu_shipment_orders",
	"temu_shipment_events",
	"temu_oms_sync_checks",
	"temu_auto_fulfillment_jobs",
	"temu_bulk_fulfillment_batches",
	"temu_bulk_fulfillment_items",
}

func main() {
	shopCode := flag.String("shop", "panda-homes", "registered shop code")
	fromSchema := flag.String("from", "public", "current private-data schema")
	toSchema := flag.String("to", "temu_panda_homes", "destination private-data schema")
	flag.Parse()

	if err := run(context.Background(), strings.TrimSpace(os.Getenv("TEMU_DATABASE_URL")), strings.TrimSpace(*shopCode), strings.TrimSpace(*fromSchema), strings.TrimSpace(*toSchema)); err != nil {
		fmt.Fprintln(os.Stderr, "shop schema migration failed:", err)
		os.Exit(1)
	}
}

func run(parent context.Context, databaseURL, shopCode, fromSchema, toSchema string) error {
	if databaseURL == "" {
		return errors.New("TEMU_DATABASE_URL is required")
	}
	if shopCode == "" {
		return errors.New("shop is required")
	}
	if !schemaNamePattern.MatchString(fromSchema) || !schemaNamePattern.MatchString(toSchema) || fromSchema == toSchema {
		return errors.New("from and to must be different valid PostgreSQL schema names")
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	defer cancel()
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return fmt.Errorf("parse database configuration: %w", err)
	}
	poolConfig.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('temu:shop-schema-migration'))`); err != nil {
		return fmt.Errorf("lock migration: %w", err)
	}
	var registeredSchema string
	if err := tx.QueryRow(ctx, `SELECT schema_name FROM public.temu_shops WHERE code=$1 FOR UPDATE`, shopCode).Scan(&registeredSchema); err != nil {
		return fmt.Errorf("load shop registry: %w", err)
	}
	if registeredSchema == toSchema {
		fmt.Printf("shop %s already uses schema %s\n", shopCode, toSchema)
		return tx.Commit(ctx)
	}
	if registeredSchema != fromSchema {
		return fmt.Errorf("shop registry uses %s, expected %s", registeredSchema, fromSchema)
	}
	var targetRelations int
	if err := tx.QueryRow(ctx, `
        SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
        WHERE n.nspname=$1 AND c.relkind IN ('r','p','S','v','m','f')
    `, toSchema).Scan(&targetRelations); err != nil {
		return fmt.Errorf("inspect target schema: %w", err)
	}
	if targetRelations != 0 {
		return fmt.Errorf("target schema %s is not empty", toSchema)
	}
	if _, err := tx.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS `+pgx.Identifier{toSchema}.Sanitize()); err != nil {
		return fmt.Errorf("create target schema: %w", err)
	}
	for _, table := range privateTables {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, fromSchema+"."+table).Scan(&exists); err != nil {
			return fmt.Errorf("inspect %s: %w", table, err)
		}
		if !exists {
			return fmt.Errorf("required private table %s.%s is missing", fromSchema, table)
		}
		statement := `ALTER TABLE ` + pgx.Identifier{fromSchema, table}.Sanitize() + ` SET SCHEMA ` + pgx.Identifier{toSchema}.Sanitize()
		if _, err := tx.Exec(ctx, statement); err != nil {
			return fmt.Errorf("move %s: %w", table, err)
		}
	}
	command, err := tx.Exec(ctx, `UPDATE public.temu_shops SET schema_name=$1, updated_at=now() WHERE code=$2 AND schema_name=$3`, toSchema, shopCode, fromSchema)
	if err != nil {
		return fmt.Errorf("switch shop registry: %w", err)
	}
	if command.RowsAffected() != 1 {
		return errors.New("shop registry was not updated")
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	fmt.Printf("moved %d private tables for %s from %s to %s\n", len(privateTables), shopCode, fromSchema, toSchema)
	return nil
}

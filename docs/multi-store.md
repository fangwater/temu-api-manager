# Temu multi-shop runtime

The Temu console uses one URL, one Go process, and one PostgreSQL database. Operators switch shops from the dashboard header.

| Shop | Code | PostgreSQL schema | Runtime state | Public path |
| --- | --- | --- | --- | --- |
| PANDA HOMES | `panda-homes` | `temu_panda_homes` | Enabled | `/temu/` |
| PANDA BUY | `panda-buy` | `temu_panda_buy` | Enabled | `/temu/` |
| Hans Living | `hans-living` | `temu_hans_living` | Enabled | `/temu/` |
| WovenWhispers | `woven-whispers` | `temu_woven_whispers` | Enabled | `/temu/` |

Temu credentials are encrypted in `public.temu_shops`. The AES-GCM master key remains in `.temu_shop_credentials_key` with mode `600`; it must not be committed or copied into logs.

Register an additional shop by sending its access token to `shopctl` over standard input. The command inherits the primary app key and app secret from `.env` and never accepts credentials as command-line arguments:

```bash
go build -o bin/temu-shopctl ./cmd/shopctl
read -rs TEMU_NEW_SHOP_TOKEN
printf '%s' "$TEMU_NEW_SHOP_TOKEN" | ./bin/temu-shopctl \
  -code panda-buy -name "PANDA BUY" -schema temu_panda_buy
unset TEMU_NEW_SHOP_TOKEN
```

For a shop authorized under another app, keep the app credentials and access
token in `.env`, then pass only their environment variable names:

```bash
./bin/temu-shopctl \
  -code hans-living -name "Hans Living" -schema temu_hans_living \
  -app-key-env TEMU_EVERSTORAGE_APP_KEY \
  -app-secret-env TEMU_EVERSTORAGE_APP_SECRET \
  -access-token-env TEMU_HANS_LIVING_ACCESS_TOKEN
```

Use `-enabled=false` to retain an encrypted registration without starting its
API handler or background workers, for example while an authorization token is
being refreshed.

Each enabled shop receives its own Temu client, order tables, shipment ledger, bulk batch, and background workers. API requests select the shop through `X-Temu-Shop`; missing headers use PANDA HOMES.

Warehouses and warehouse SKU inventory thresholds are server-wide resources. `public.temu_warehouses`, `public.temu_warehouse_mappings`, and the XLWMS inventory-threshold registry are shared by all shops, while orders and fulfillment records remain in each shop's registered PostgreSQL schema. Synchronizing warehouses from any registered shop updates the shared registry, and every shop immediately sees the same mappings and SKU rules.

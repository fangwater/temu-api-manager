# Temu multi-shop runtime

The Temu console uses one URL, one Go process, and one PostgreSQL database. Operators switch shops from the dashboard header.

| Shop | Code | PostgreSQL schema | Public path |
| --- | --- | --- | --- |
| PANDA HOMES | `panda-homes` | `temu_panda_homes` | `/temu/` |
| PANDA BUY | `panda-buy` | `temu_panda_buy` | `/temu/` |

Temu credentials are encrypted in `public.temu_shops`. The AES-GCM master key remains in `.temu_shop_credentials_key` with mode `600`; it must not be committed or copied into logs.

Register an additional shop by sending its access token to `shopctl` over standard input. The command inherits the primary app key and app secret from `.env` and never accepts credentials as command-line arguments:

```bash
go build -o bin/temu-shopctl ./cmd/shopctl
read -rs TEMU_NEW_SHOP_TOKEN
printf '%s' "$TEMU_NEW_SHOP_TOKEN" | ./bin/temu-shopctl \
  -code panda-buy -name "PANDA BUY" -schema temu_panda_buy
unset TEMU_NEW_SHOP_TOKEN
```

Each enabled shop receives its own Temu client, order tables, shipment ledger, bulk batch, and background workers. API requests select the shop through `X-Temu-Shop`; missing headers use PANDA HOMES.

Warehouses and warehouse SKU inventory thresholds are server-wide resources. `public.temu_warehouses`, `public.temu_warehouse_mappings`, and the XLWMS inventory-threshold registry are shared by all shops, while orders and fulfillment records remain in `temu_panda_homes` or `temu_panda_buy`. Synchronizing warehouses from any registered shop updates the shared registry, and every shop immediately sees the same mappings and SKU rules.

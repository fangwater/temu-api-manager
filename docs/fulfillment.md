# Temu Fulfillment Service

The Go service owns the Temu order cache, carrier selection, Buy Label workflow,
and shipment idempotency ledger. It reads the existing private `.env`; secrets
are never returned by HTTP APIs.

## Workflow

1. `bg.order.list.v2.get` synchronizes status `2` / `UN_SHIPPING` seller-fulfilled orders.
2. PostgreSQL stores current orders, lines, and the complete raw `bg.order.detail.v2.get` response. HTTP responses expose only a structured detail summary.
3. Unbound-SKU, one-order-multiple-item, merge-candidate, and Temu-consolidated orders are persisted in the manual-review queue. "sku_unbound" is a hard block: the order cannot be approved, quoted, or pushed into fulfillment until every line has an "extCode" and XLWMS returns the SKU from an active warehouse. Other classifications block automatic quoting until an operator approves the order.
4. The XLWMS warehouse-decision API validates every order SKU and selects one warehouse that can cover all line quantities.
   The fulfillment dialog calls the warehouse preview first and shows every SKU across DPS002, ARP East, DPS004, and ARP West. Any region at or below the 50-unit safety line blocks both regions and requires manual handling. When every active warehouse query succeeds but all return "sku_found=false", the preview persists "sku_unbound"; the manual queue's binding recheck removes that reason only after XLWMS returns the SKU.
5. `bg.logistics.shippingservices.get` returns the live Temu carrier/channel candidates for the package.
6. Carrier selection uses an explicitly selected channel, the current shop and OMS warehouse policy, then estimated cost.
   Each shop configures an independent priority and enabled state for every supported carrier in each OMS warehouse. Automatic selection applies that warehouse's priority within USD 0.50 of the live minimum; a disabled carrier cannot be selected manually or automatically.
   Carrier lookup starts automatically after warehouse validation. Both available and unavailable Temu channels are returned; the dashboard directly lists every unavailable channel with its reason.
7. Before `bg.logistics.shipment.create`, PostgreSQL reserves every parent order with a unique primary key. The same transaction stores the final selected channel in `temu_label_purchase_choices` and the three lowest-priced eligible cross-warehouse candidates in `temu_label_purchase_candidates`. Only quotes that enter a purchase transaction are recorded. Existing historical quotes are not backfilled; a legacy quote without the embedded choice snapshot remains purchasable but creates no fabricated analysis row.
8. Buy Label uses `shipLater=true`; label state is reconciled with `bg.logistics.shipment.result.get`.
9. Labels are downloaded server-side through `bg.logistics.shipment.document.get` and signed document headers.
10. Actual handoff is confirmed separately with `bg.logistics.shipped.package.confirm`.

`submission_unknown` is deliberately not retried automatically. An external timeout may occur after Temu accepted a request, so retrying could purchase a duplicate label. An operator must reconcile it first.

## HTTP API

```text
GET    /api/system/token-status
POST   /api/orders/sync
GET    /api/orders
GET    /api/orders/{parentOrderSN}
GET    /api/orders/history
GET    /api/orders/{parentOrderSN}/detail
POST   /api/orders/{parentOrderSN}/warehouse-preview
POST   /api/orders/details/sync
GET    /api/manual-orders
PUT    /api/orders/{parentOrderSN}/manual-review
POST   /api/warehouses/sync
GET    /api/warehouses
PUT    /api/warehouse-mappings/{omsKey}
DELETE /api/warehouse-mappings/{omsKey}
GET    /api/carrier-policies
PUT    /api/carrier-policies/{warehouseKey}
POST   /api/shipping/quotes
POST   /api/shipping/purchase
GET    /api/shipments
GET    /api/shipments/{id}
GET    /api/packages/{packageSn}/tracking?language=en
POST   /api/shipments/{id}/refresh
GET    /api/shipments/{id}/documents
GET    /api/shipments/{id}/label
POST   /api/shipments/{id}/confirm
```

Package tracking calls `temu.track.trackinginfo.get`. The `language` query
parameter is optional. The response includes the Temu package number, the
last-mile `trackingNum`, and `trackingInfo` events with
`logisticsUpdatedAt`, `logisticsStatus`, and `statusText`.

"ARP_WEST" is temporarily disabled because the warehouse is vacant. Its former PG1955 mapping must not be used; preview and quote operations reject this warehouse even if it is mapped again.
Warehouse mapping, manual-review status changes, Buy Label purchase, and final shipment confirmation require
`X-Temu-Operation-Key`. Carrier-policy changes do not require this key. The browser keeps this key only in session storage.

## Runtime

```bash
make test
make build
pm2 start deploy/pm2/go-ecosystem.config.cjs
```

The production console is served at `https://pangutech.online/temu/`.

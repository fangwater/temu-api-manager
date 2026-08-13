# Temu API Manager

Local Python client for signed Temu Open API requests. The first implemented
workflow retrieves order lists through `bg.order.list.v2.get`.

The production fulfillment console is implemented in Go under `cmd/server`.
The operator first previews live XLWMS inventory, chooses an eligible warehouse
and Temu carrier, and confirms the Buy Label purchase. After a shipment record
is created, the durable PostgreSQL worker automatically waits for the label,
confirms the Temu shipment, and then waits for read-only XLWMS verification.
The worker resumes after a service restart and never purchases a second label
for an order that already has a shipment record.

## Assign A Purchased-Label Order In XLWMS

After Temu confirms a purchased label, the platform order can be pushed into
XLWMS as status `0` (pending). The dashboard exposes this under
**领星订单状态 -> 待处理**. Each pending row has a **分配仓库物流** action that
opens the same automatic warehouse-routing confirmation used by the XLWMS
console.

The browser calls same-origin Temu endpoints:

```text
GET  /api/oms-platform-orders/{parentOrderSN}/warehouse-assignment-preview
POST /api/oms-platform-orders/{parentOrderSN}/warehouse-assignment
```

Submit body:

```json
{
  "logistics_carrier": "_AUTO_MATCH_",
  "confirm": true
}
```

`logistics_carrier` can be `_AUTO_MATCH_` or `other`. The Temu service resolves
the OMS account and warehouse from the selected shop's immutable purchased-label
ledger, then calls the XLWMS routing preview and warehouse-assignment APIs. The
browser cannot provide `account` or `warehouse_code`; strict JSON decoding
rejects both fields.

Before preview and again immediately before submission, the server requires:

1. The selected Temu shop has one confirmed shipment for the parent order.
2. The shipment has a tracking number and at least one package number.
3. The warehouse mapping still matches the Temu warehouse used to buy the
   label.
4. The configured OMS account returns exactly one same-number platform order
   in status `0`.
5. XLWMS independently resolves the same warehouse from authoritative Temu
   purchased-label evidence and has the upload-label channel enabled.

Submission assigns the purchased-label warehouse, selects the upload-label
channel, and immediately approves the XLWMS platform order. XLWMS performs a
final live pending-state check, so a repeated or racing submission is rejected
instead of approving the order twice.

## Shop SKU Warehouse Rules

SKU warehouse restrictions belong to a Temu shop, not to a warehouse. The
shared `public.temu_sku_disabled_warehouses` table uses
`(shop_code, warehouse_sku, oms_warehouse_key)` as its primary key. It stores
only disabled combinations:

- No rows for a shop and SKU means every globally enabled OMS warehouse is
  allowed.
- One or more rows disable only those warehouses for that SKU in that shop.
- Saving an empty disabled list deletes the rows and restores the default.

The dashboard exposes the rules under **SKU 发货仓库**. The HTTP endpoints are
`GET /api/sku-warehouse-rules` and `PUT /api/sku-warehouse-rules`; normal
multi-shop routing selects the shop through `X-Temu-Shop`.

The service applies the restrictions after each live XLWMS inventory decision,
so warehouse preview, manual quotes, and automatic fulfillment share the same
selection behavior. It also checks the current rule again immediately before a
new Buy Label or failed-shipment recovery submission. Updating a rule
invalidates cached warehouse classifications for orders containing that SKU.

## Label Purchase Price Analysis

The Go fulfillment service stores a new, immutable price snapshot when a quote
actually enters a Buy Label transaction. Quote previews that are never
purchased are not included. The data is private per shop schema, for example
`temu_panda_homes` and `temu_panda_buy`.

The relationship is:

```text
temu_shipping_quotes (1) -> (0..1) temu_label_purchase_choices
temu_label_purchase_choices (1) -> (1..3) temu_label_purchase_candidates
temu_label_purchase_choices (many) -> (1) temu_shipments
```

A recovery can create another quote and choice row for the same shipment.
Repeated submission of the same quote does not duplicate its choice snapshot.

### `temu_label_purchase_choices`

This is the analysis header. It contains one row per quote that entered a
purchase transaction and always stores the final selected option, including
when that option is outside the three lowest prices.

| Column | Stored data |
| --- | --- |
| `quote_id` | Primary key and the source quote ID. |
| `shipment_id` | Shipment ledger ID. Join this to `temu_shipments` for purchase result and current status. |
| `parent_order_sn` | Temu parent order number. |
| `selection_source` | `automatic` for the carrier-rule result or `manual` for an operator-selected channel. |
| `selected_price_rank` | `1`, `2`, or `3` when the selected option is in the low-price Top 3; `NULL` when it is outside the Top 3. |
| `selected_oms_warehouse_key` | Logical OMS warehouse used by the selected option, such as `DPS002` or `ARP_EAST`. |
| `selected_temu_warehouse_id` | Temu warehouse ID used in the shipping request. |
| `selected_channel_id` | Temu logistics channel ID. |
| `selected_ship_company_id` | Temu shipping-company ID. |
| `selected_carrier_code` | Normalized carrier code, such as `GOFO`, `SWIFTX`, or `UPS`. |
| `selected_company_name` | Shipping-company name returned by Temu. |
| `selected_logistics_type` | Logistics service type returned by Temu. |
| `selected_estimated_amount` | Selected live shipping price as `numeric(18,4)`. |
| `selected_currency_code` | Currency returned by Temu, normally `USD`; empty means Temu omitted it. |
| `selection_reason` | Selection-rule explanation captured at quote time. |
| `purchased_at` | Time the purchase transaction reserved the selection snapshot. |

### `temu_label_purchase_candidates`

This is the Top 3 detail table. It contains one to three rows for each
`quote_id`, ranked by eligible live shipping price across all quoted
warehouses.

| Column | Stored data |
| --- | --- |
| `quote_id` | Foreign key to `temu_label_purchase_choices.quote_id`. |
| `price_rank` | Low-price rank `1` through `3`; together with `quote_id` it is the primary key. |
| `oms_warehouse_key` | Logical OMS warehouse for this candidate. |
| `temu_warehouse_id` | Temu warehouse ID for this candidate. |
| `channel_id` | Temu logistics channel ID. |
| `ship_company_id` | Temu shipping-company ID. |
| `carrier_code` | Normalized carrier code. |
| `shipping_company_name` | Shipping-company name returned by Temu. |
| `ship_logistics_type` | Logistics service type returned by Temu. |
| `estimated_amount` | Candidate live shipping price as `numeric(18,4)`. |
| `currency_code` | Currency returned by Temu, normally `USD`. |
| `is_selected` | `true` only when this ranked candidate is the final selection. All three rows are `false` when the final selection is outside the Top 3. |

### Snapshot Rules

1. Candidates are collected after the automatic whitelist, no-signature,
   comparable-price/currency, per-warehouse carrier enablement, and failed
   carrier exclusion rules have been applied.
2. The Top 3 is sorted strictly by estimated amount across all eligible
   warehouses. Existing deterministic tie-breaking applies: DPS before ARP at
   the same price, then lower channel ID.
3. The final option is selected separately by the current manual or automatic
   carrier-priority rule. The automatic rule can select an option up to USD
   0.50 above the minimum, so the selected option is not guaranteed to be in
   the low-price Top 3.
4. The header and candidate rows are written atomically with shipment
   reservation or recovery. A row means a Buy Label call was prepared, not
   necessarily that Temu successfully produced a label. Join
   `temu_shipments.status` through `shipment_id` to analyze outcomes.
5. Historical quotes and shipments are not backfilled because their complete
   cross-warehouse candidate set cannot be reconstructed. Legacy quotes remain
   purchasable but do not create fabricated analysis rows.

Example price-premium query for the current shop schema:

```sql
SELECT
    choice.parent_order_sn,
    choice.purchased_at,
    choice.selected_carrier_code,
    choice.selected_price_rank,
    choice.selected_estimated_amount,
    min(candidate.estimated_amount) AS lowest_eligible_amount,
    choice.selected_estimated_amount - min(candidate.estimated_amount)
        AS selected_price_premium
FROM temu_label_purchase_choices choice
JOIN temu_label_purchase_candidates candidate USING (quote_id)
GROUP BY
    choice.quote_id,
    choice.parent_order_sn,
    choice.purchased_at,
    choice.selected_carrier_code,
    choice.selected_price_rank,
    choice.selected_estimated_amount
ORDER BY choice.purchased_at DESC;
```

## Setup

```bash
. /home/ubuntu/.venv/bin/activate
cd /home/ubuntu/temu-api-manager
pip install -e .
```

The shared environment at `/home/ubuntu/.venv` is also used by the XLWMS API
Manager. Do not create a project-local virtual environment.

The local `.env` already contains the runtime endpoint and credentials. Keep it
private and do not commit it.

## Fetch One Order Page

```bash
python -m temu_api_manager order-list --page-number 1 --page-size 10
```

Use Temu filters through JSON:

```bash
python -m temu_api_manager order-list \
  --params-json '{"parentOrderStatus":2,"sortby":"updateTime"}' \
  --page-number 1 \
  --page-size 100
```

Save the complete raw response:

```bash
python -m temu_api_manager order-list \
  --page-number 1 \
  --page-size 100 \
  --output exports/orders_page_1.json
```

`pageSize` must be between 1 and 100. Paired time filters such as
`createAfter`/`createBefore` and `updateAtStart`/`updateAtEnd` must be supplied
together as Unix timestamps in seconds.

## Fetch Order Detail

Use the parent order number returned by the order-list API:

```bash
python -m temu_api_manager order-detail \
  --parent-order-sn "PARENT_ORDER_SN" \
  --output exports/order_detail.json
```

Optionally repeat `--fulfillment-type` with `fulfillBySeller` or
`fulfillByCooperativeWarehouse`.

## Fetch Order Amounts

`bg.order.amount.query` is a sensitive Temu API and requires separate approval:

```bash
python -m temu_api_manager order-amount \
  --parent-order-sn "PARENT_ORDER_SN" \
  --output exports/order_amount.json
```

The response contains parent-order totals and child-order supply/base price
amounts. Keep exported amount data private.

## Recommend Product Base Price

Pass the complete documented `supplierPriceEstimateQry` object through a JSON
file because the request includes nested category, specification, dimensions,
weight, and external-price fields:

```bash
python -m temu_api_manager baseprice-recommend \
  --params-file request/baseprice_recommend.json \
  --language en \
  --output exports/baseprice_recommend.json
```

The command validates the top-level request shape and signs nested arrays and
objects using Temu's compact JSON serialization rules.

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

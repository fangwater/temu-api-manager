CREATE TABLE IF NOT EXISTS temu_sync_runs (
    id bigserial PRIMARY KEY,
    status text NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
    started_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    fetched_orders integer NOT NULL DEFAULT 0,
    fetched_lines integer NOT NULL DEFAULT 0,
    error_message text NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS temu_orders (
    parent_order_sn text PRIMARY KEY,
    parent_order_status integer NOT NULL,
    fulfillment_type text NOT NULL DEFAULT '',
    region_id bigint,
    expect_ship_latest_time bigint,
    update_time bigint,
    order_labels jsonb NOT NULL DEFAULT '[]'::jsonb,
    fulfillment_warnings jsonb NOT NULL DEFAULT '[]'::jsonb,
    raw_payload jsonb NOT NULL,
    is_open boolean NOT NULL DEFAULT true,
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    last_synced_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS temu_orders_open_deadline_idx
    ON temu_orders (is_open, expect_ship_latest_time, update_time DESC);

CREATE TABLE IF NOT EXISTS temu_order_lines (
    order_sn text PRIMARY KEY,
    parent_order_sn text NOT NULL REFERENCES temu_orders(parent_order_sn) ON DELETE CASCADE,
    order_status integer NOT NULL,
    quantity integer NOT NULL CHECK (quantity >= 0),
    goods_id bigint,
    sku_id bigint,
    ext_code text NOT NULL DEFAULT '',
    goods_name text NOT NULL DEFAULT '',
    spec text NOT NULL DEFAULT '',
    raw_payload jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE temu_order_lines
    ALTER COLUMN goods_id TYPE bigint USING goods_id::bigint,
    ALTER COLUMN sku_id TYPE bigint USING sku_id::bigint;

CREATE INDEX IF NOT EXISTS temu_order_lines_parent_idx ON temu_order_lines(parent_order_sn);
CREATE INDEX IF NOT EXISTS temu_order_lines_ext_code_idx ON temu_order_lines(ext_code);

CREATE TABLE IF NOT EXISTS temu_order_details (
    parent_order_sn text PRIMARY KEY REFERENCES temu_orders(parent_order_sn) ON DELETE CASCADE,
    source_update_time bigint NOT NULL DEFAULT 0,
    detail_payload jsonb NOT NULL,
    batch_order_number_list text[] NOT NULL DEFAULT '{}',
    is_shipment_consolidated boolean NOT NULL DEFAULT false,
    region_names text[] NOT NULL DEFAULT '{}',
    order_open_at_fetch boolean NOT NULL DEFAULT true,
    fetched_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS temu_order_details_refresh_idx
    ON temu_order_details(order_open_at_fetch, source_update_time, fetched_at);

CREATE TABLE IF NOT EXISTS temu_order_manual_reviews (
    parent_order_sn text PRIMARY KEY REFERENCES temu_orders(parent_order_sn) ON DELETE CASCADE,
    reasons text[] NOT NULL DEFAULT '{}',
    merge_order_sn_list text[] NOT NULL DEFAULT '{}',
    status text NOT NULL CHECK (status IN ('detected', 'manual_pending', 'approved', 'resolved')),
    active boolean NOT NULL DEFAULT true,
    detected_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    approved_at timestamptz
);

CREATE INDEX IF NOT EXISTS temu_order_manual_reviews_queue_idx
    ON temu_order_manual_reviews(active, status, updated_at DESC);

CREATE TABLE IF NOT EXISTS temu_order_warehouse_checks (
    parent_order_sn text PRIMARY KEY REFERENCES temu_orders(parent_order_sn) ON DELETE CASCADE,
    source_update_time bigint NOT NULL DEFAULT 0,
    status text NOT NULL CHECK (status IN ('eligible', 'manual', 'failed')),
    categories text[] NOT NULL DEFAULT '{}',
    reason_details text[] NOT NULL DEFAULT '{}',
    error_message text NOT NULL DEFAULT '',
    checked_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS temu_order_warehouse_checks_queue_idx
    ON temu_order_warehouse_checks(status, checked_at);

CREATE TABLE IF NOT EXISTS public.temu_warehouses (
    warehouse_id text PRIMARY KEY,
    warehouse_name text NOT NULL DEFAULT '',
    region_id bigint,
    enable_buy_shipping_label boolean NOT NULL DEFAULT false,
    default_warehouse boolean NOT NULL DEFAULT false,
    warehouse_management_type integer,
    raw_payload jsonb NOT NULL,
    synced_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS public.temu_warehouse_mappings (
    oms_warehouse_key text PRIMARY KEY,
    temu_warehouse_id text NOT NULL REFERENCES public.temu_warehouses(warehouse_id),
logical_warehouse_key text NOT NULL DEFAULT '',
    oms_warehouse_code text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE public.temu_warehouse_mappings
ADD COLUMN IF NOT EXISTS oms_warehouse_code text NOT NULL DEFAULT '',
ADD COLUMN IF NOT EXISTS logical_warehouse_key text NOT NULL DEFAULT '';

UPDATE public.temu_warehouse_mappings SET
    oms_warehouse_code = CASE oms_warehouse_key
        WHEN 'DPS002' THEN 'DPSNY002'
        WHEN 'DPS004' THEN 'DPSCA004'
        WHEN 'ARP_EAST' THEN 'HYTX30'
        ELSE oms_warehouse_code END
WHERE oms_warehouse_code = '';

UPDATE public.temu_warehouse_mappings
SET logical_warehouse_key=oms_warehouse_key
WHERE logical_warehouse_key='';

ALTER TABLE public.temu_warehouse_mappings
    DROP CONSTRAINT IF EXISTS temu_warehouse_mappings_dispatch_strategy_check,
    DROP COLUMN IF EXISTS oms_dispatch_strategy;

CREATE TABLE IF NOT EXISTS public.temu_shop_warehouses (
shop_code text NOT NULL REFERENCES public.temu_shops(code) ON DELETE CASCADE,
logical_warehouse_key text NOT NULL,
warehouse_id text NOT NULL REFERENCES public.temu_warehouses(warehouse_id) ON DELETE CASCADE,
synced_at timestamptz NOT NULL DEFAULT now(),
PRIMARY KEY(shop_code,logical_warehouse_key),
UNIQUE(shop_code,warehouse_id)
);

CREATE INDEX IF NOT EXISTS temu_shop_warehouses_warehouse_idx
ON public.temu_shop_warehouses(warehouse_id);

CREATE TABLE IF NOT EXISTS public.temu_carrier_policies (
    shop_code text NOT NULL REFERENCES public.temu_shops(code) ON DELETE CASCADE,
    oms_warehouse_key text NOT NULL,
    carrier_code text NOT NULL,
    priority integer NOT NULL CHECK (priority > 0),
    enabled boolean NOT NULL DEFAULT true,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (shop_code, oms_warehouse_key, carrier_code)
);

CREATE INDEX IF NOT EXISTS temu_carrier_policies_lookup_idx
ON public.temu_carrier_policies(shop_code, oms_warehouse_key, priority);

CREATE TABLE IF NOT EXISTS public.temu_sku_disabled_warehouses (
    shop_code text NOT NULL REFERENCES public.temu_shops(code) ON DELETE CASCADE,
    warehouse_sku text NOT NULL,
    oms_warehouse_key text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (shop_code, warehouse_sku, oms_warehouse_key)
);

CREATE INDEX IF NOT EXISTS temu_sku_disabled_warehouses_lookup_idx
ON public.temu_sku_disabled_warehouses(shop_code, warehouse_sku);

CREATE TABLE IF NOT EXISTS temu_shipping_quotes (
    id text PRIMARY KEY,
    parent_order_sn text NOT NULL REFERENCES temu_orders(parent_order_sn),
    oms_warehouse_key text NOT NULL,
    temu_warehouse_id text NOT NULL,
    region text NOT NULL,
    selected_channel_id bigint NOT NULL,
    selected_ship_company_id bigint NOT NULL,
    selected_company_name text NOT NULL DEFAULT '',
    selected_logistics_type text NOT NULL DEFAULT '',
    selected_reason text NOT NULL DEFAULT '',
    request_payload jsonb NOT NULL,
    response_payload jsonb NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS temu_shipping_quotes_parent_idx
    ON temu_shipping_quotes(parent_order_sn, created_at DESC);

CREATE TABLE IF NOT EXISTS temu_shipments (
    id text PRIMARY KEY,
    quote_id text NOT NULL REFERENCES temu_shipping_quotes(id),
    idempotency_key text NOT NULL UNIQUE,
    status text NOT NULL CHECK (status IN (
        'submitting', 'label_pending', 'label_ready', 'label_failed',
        'submission_unknown', 'shipped', 'confirm_failed'
    )),
    selection_mode text NOT NULL DEFAULT 'exact_channel',
    warehouse_id text NOT NULL,
    channel_id bigint NOT NULL,
    ship_company_id bigint NOT NULL,
    shipping_company_name text NOT NULL DEFAULT '',
    ship_logistics_type text NOT NULL DEFAULT '',
    package_sn_list text[] NOT NULL DEFAULT '{}',
    tracking_number text NOT NULL DEFAULT '',
    request_payload jsonb NOT NULL,
    response_payload jsonb,
    error_code text NOT NULL DEFAULT '',
    error_message text NOT NULL DEFAULT '',
    submission_attempts integer NOT NULL DEFAULT 0,
    last_submission_at timestamptz NOT NULL DEFAULT now(),
    confirmation_attempts integer NOT NULL DEFAULT 0,
    last_confirmation_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    confirmed_at timestamptz
);

ALTER TABLE temu_shipments
    ADD COLUMN IF NOT EXISTS submission_attempts integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_submission_at timestamptz,
    ADD COLUMN IF NOT EXISTS confirmation_attempts integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_confirmation_at timestamptz,
    ADD COLUMN IF NOT EXISTS failed_carrier_codes text[] NOT NULL DEFAULT '{}';

UPDATE temu_shipments SET
    submission_attempts=GREATEST(submission_attempts,1),
    last_submission_at=coalesce(last_submission_at,created_at);

UPDATE temu_shipments SET
    confirmation_attempts=GREATEST(confirmation_attempts,1),
    last_confirmation_at=coalesce(last_confirmation_at,updated_at)
WHERE status IN ('confirm_failed','shipped');

ALTER TABLE temu_shipments
    ALTER COLUMN last_submission_at SET DEFAULT now(),
    ALTER COLUMN last_submission_at SET NOT NULL;

CREATE TABLE IF NOT EXISTS temu_shipment_orders (
    parent_order_sn text PRIMARY KEY REFERENCES temu_orders(parent_order_sn),
    shipment_id text NOT NULL REFERENCES temu_shipments(id),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS temu_shipment_events (
    id bigserial PRIMARY KEY,
    shipment_id text NOT NULL REFERENCES temu_shipments(id) ON DELETE CASCADE,
    event_type text NOT NULL,
    payload jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);


CREATE TABLE IF NOT EXISTS temu_label_purchase_choices (
    quote_id text PRIMARY KEY REFERENCES temu_shipping_quotes(id) ON DELETE CASCADE,
    shipment_id text NOT NULL REFERENCES temu_shipments(id) ON DELETE CASCADE,
    parent_order_sn text NOT NULL REFERENCES temu_orders(parent_order_sn),
    selection_source text NOT NULL CHECK (selection_source IN ('automatic', 'manual')),
    selected_price_rank smallint CHECK (selected_price_rank BETWEEN 1 AND 3),
    selected_oms_warehouse_key text NOT NULL,
    selected_temu_warehouse_id text NOT NULL,
    selected_channel_id bigint NOT NULL,
    selected_ship_company_id bigint NOT NULL,
    selected_carrier_code text NOT NULL DEFAULT '',
    selected_company_name text NOT NULL DEFAULT '',
    selected_logistics_type text NOT NULL DEFAULT '',
    selected_estimated_amount numeric(18,4) NOT NULL CHECK (selected_estimated_amount >= 0),
    selected_currency_code text NOT NULL DEFAULT '',
    selection_reason text NOT NULL DEFAULT '',
    purchased_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS temu_label_purchase_choices_parent_idx
    ON temu_label_purchase_choices(parent_order_sn, purchased_at DESC);
CREATE INDEX IF NOT EXISTS temu_label_purchase_choices_shipment_idx
    ON temu_label_purchase_choices(shipment_id, purchased_at DESC);

CREATE TABLE IF NOT EXISTS temu_label_purchase_candidates (
    quote_id text NOT NULL REFERENCES temu_label_purchase_choices(quote_id) ON DELETE CASCADE,
    price_rank smallint NOT NULL CHECK (price_rank BETWEEN 1 AND 3),
    oms_warehouse_key text NOT NULL,
    temu_warehouse_id text NOT NULL,
    channel_id bigint NOT NULL,
    ship_company_id bigint NOT NULL,
    carrier_code text NOT NULL DEFAULT '',
    shipping_company_name text NOT NULL DEFAULT '',
    ship_logistics_type text NOT NULL DEFAULT '',
    estimated_amount numeric(18,4) NOT NULL CHECK (estimated_amount >= 0),
    currency_code text NOT NULL DEFAULT '',
    is_selected boolean NOT NULL DEFAULT false,
    PRIMARY KEY (quote_id, price_rank)
);

CREATE INDEX IF NOT EXISTS temu_label_purchase_candidates_carrier_idx
    ON temu_label_purchase_candidates(carrier_code, estimated_amount, price_rank);

ALTER TABLE IF EXISTS temu_oms_dispatches RENAME TO temu_oms_sync_checks;

CREATE TABLE IF NOT EXISTS temu_oms_sync_checks (
    shipment_id text PRIMARY KEY REFERENCES temu_shipments(id) ON DELETE CASCADE,
    oms_warehouse_key text NOT NULL,
    warehouse_code text NOT NULL,
    status text NOT NULL,
    outbound_order_nos text[] NOT NULL DEFAULT '{}',
    tracking_number text NOT NULL DEFAULT '',
    attempts integer NOT NULL DEFAULT 0,
    error_message text NOT NULL DEFAULT '',
    response_summary jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    verified_at timestamptz
);

ALTER TABLE temu_oms_sync_checks
    DROP CONSTRAINT IF EXISTS temu_oms_dispatches_status_check,
    DROP CONSTRAINT IF EXISTS temu_oms_sync_checks_status_check;

UPDATE temu_oms_sync_checks SET status='waiting_sync'
WHERE status IN ('pushing', 'verification_pending');

ALTER TABLE temu_oms_sync_checks
    DROP COLUMN IF EXISTS strategy,
    ADD CONSTRAINT temu_oms_sync_checks_status_check
        CHECK (status IN ('querying', 'waiting_sync', 'verified', 'failed'));

DROP INDEX IF EXISTS temu_oms_dispatches_status_idx;
CREATE INDEX IF NOT EXISTS temu_oms_sync_checks_status_idx
    ON temu_oms_sync_checks(status, updated_at DESC);
CREATE INDEX IF NOT EXISTS temu_shipment_events_shipment_idx
    ON temu_shipment_events(shipment_id, created_at DESC);

CREATE TABLE IF NOT EXISTS temu_auto_fulfillment_jobs (
    parent_order_sn text PRIMARY KEY REFERENCES temu_orders(parent_order_sn) ON DELETE CASCADE,
    shipment_id text REFERENCES temu_shipments(id) ON DELETE SET NULL,
    status text NOT NULL CHECK (status IN (
        'queued', 'running', 'waiting_label', 'confirming',
        'waiting_oms', 'completed', 'skipped', 'failed'
    )),
    attempts integer NOT NULL DEFAULT 0,
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    completed_at timestamptz
);

ALTER TABLE temu_auto_fulfillment_jobs
    DROP CONSTRAINT IF EXISTS temu_auto_fulfillment_jobs_status_check;
ALTER TABLE temu_auto_fulfillment_jobs
    ADD CONSTRAINT temu_auto_fulfillment_jobs_status_check CHECK (status IN (
        'queued', 'running', 'waiting_label', 'confirming',
        'waiting_oms', 'completed', 'skipped', 'failed'
    ));

CREATE INDEX IF NOT EXISTS temu_auto_fulfillment_jobs_status_idx
    ON temu_auto_fulfillment_jobs(status, updated_at);

INSERT INTO temu_auto_fulfillment_jobs(parent_order_sn, shipment_id, status, created_at, updated_at, completed_at)
SELECT so.parent_order_sn, s.id,
    CASE
        WHEN s.status='shipped' AND d.status='verified' THEN 'completed'
        WHEN s.status='shipped' THEN 'waiting_oms'
        WHEN s.status IN ('label_ready','confirm_failed') THEN 'confirming'
        WHEN s.status IN ('submitting','label_pending','submission_unknown') THEN 'waiting_label'
        ELSE 'failed'
    END,
    s.created_at, s.updated_at,
    CASE WHEN s.status='shipped' AND d.status='verified' THEN d.verified_at ELSE NULL END
FROM temu_shipment_orders so
JOIN temu_shipments s ON s.id=so.shipment_id
LEFT JOIN temu_oms_sync_checks d ON d.shipment_id=s.id
ON CONFLICT(parent_order_sn) DO NOTHING;

CREATE TABLE IF NOT EXISTS temu_bulk_fulfillment_batches (
    id text PRIMARY KEY,
    status text NOT NULL CHECK (status IN ('running', 'stopped', 'completed')),
    total_orders integer NOT NULL DEFAULT 0,
    succeeded_orders integer NOT NULL DEFAULT 0,
    failed_orders integer NOT NULL DEFAULT 0,
    failed_order_sn text NOT NULL DEFAULT '',
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);

CREATE UNIQUE INDEX IF NOT EXISTS temu_bulk_fulfillment_batches_one_running_idx
    ON temu_bulk_fulfillment_batches(status) WHERE status='running';

CREATE TABLE IF NOT EXISTS temu_bulk_fulfillment_items (
    batch_id text NOT NULL REFERENCES temu_bulk_fulfillment_batches(id) ON DELETE CASCADE,
    sequence_no integer NOT NULL,
    parent_order_sn text NOT NULL REFERENCES temu_orders(parent_order_sn),
    status text NOT NULL CHECK (status IN ('pending', 'running', 'succeeded', 'failed')),
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(batch_id, sequence_no),
    UNIQUE(batch_id, parent_order_sn)
);

CREATE INDEX IF NOT EXISTS temu_bulk_fulfillment_items_next_idx
    ON temu_bulk_fulfillment_items(batch_id, status, sequence_no);

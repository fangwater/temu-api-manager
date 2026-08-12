package model

import (
	"encoding/json"
	"time"
)

type Order struct {
	ParentOrderSN        string              `json:"parent_order_sn"`
	Status               int                 `json:"status"`
	FulfillmentType      string              `json:"fulfillment_type"`
	RegionID             int64               `json:"region_id,omitempty"`
	ExpectShipLatestTime int64               `json:"expect_ship_latest_time,omitempty"`
	UpdateTime           int64               `json:"update_time,omitempty"`
	Labels               json.RawMessage     `json:"labels"`
	Warnings             json.RawMessage     `json:"warnings"`
	Raw                  json.RawMessage     `json:"-"`
	Open                 bool                `json:"open"`
	FirstSeenAt          time.Time           `json:"first_seen_at"`
	LastSeenAt           time.Time           `json:"last_seen_at"`
	LastSyncedAt         time.Time           `json:"last_synced_at"`
	Lines                []OrderLine         `json:"lines"`
	Shipment             *ShipmentBrief      `json:"shipment,omitempty"`
	BatchOrderSNs        []string            `json:"batch_order_sns,omitempty"`
	Consolidated         bool                `json:"platform_consolidated"`
	Detail               *OrderDetail        `json:"detail,omitempty"`
	ManualReview         *ManualReview       `json:"manual_review,omitempty"`
	AutoFulfillment      *AutoFulfillmentJob `json:"auto_fulfillment,omitempty"`
}

type BulkFulfillmentBatch struct {
	ID              string     `json:"id"`
	Status          string     `json:"status"`
	TotalOrders     int        `json:"total_orders"`
	SucceededOrders int        `json:"succeeded_orders"`
	FailedOrders    int        `json:"failed_orders"`
	CurrentOrderSN  string     `json:"current_order_sn,omitempty"`
	FailedOrderSN   string     `json:"failed_order_sn,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
}

type BulkFulfillmentItem struct {
	BatchID       string    `json:"batch_id"`
	SequenceNo    int       `json:"sequence_no"`
	ParentOrderSN string    `json:"parent_order_sn"`
	Status        string    `json:"status"`
	LastError     string    `json:"last_error,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}
type AutoFulfillmentJob struct {
	ParentOrderSN string     `json:"parent_order_sn"`
	ShipmentID    string     `json:"shipment_id,omitempty"`
	Status        string     `json:"status"`
	Attempts      int        `json:"attempts"`
	LastError     string     `json:"last_error,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
}

type OrderDetail struct {
	ParentOrderSN  string          `json:"parent_order_sn"`
	RegionNames    []string        `json:"region_names,omitempty"`
	BatchOrderSNs  []string        `json:"batch_order_sns,omitempty"`
	Consolidated   bool            `json:"platform_consolidated"`
	SourceUpdateAt int64           `json:"source_update_time,omitempty"`
	OpenAtFetch    bool            `json:"open_at_fetch"`
	FetchedAt      time.Time       `json:"fetched_at"`
	Raw            json.RawMessage `json:"-"`
}

type ManualReview struct {
	ParentOrderSN string       `json:"parent_order_sn"`
	Reasons       []string     `json:"reasons"`
	MergeOrderSNs []string     `json:"merge_order_sns,omitempty"`
	Status        string       `json:"status"`
	Active        bool         `json:"active"`
	DetectedAt    time.Time    `json:"detected_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
	ApprovedAt    *time.Time   `json:"approved_at,omitempty"`
	Lines         []OrderLine  `json:"lines,omitempty"`
	Detail        *OrderDetail `json:"detail,omitempty"`
	Details       []string     `json:"details,omitempty"`
}

type WarehouseClassification struct {
	ParentOrderSN  string    `json:"parent_order_sn"`
	SourceUpdateAt int64     `json:"source_update_time"`
	Status         string    `json:"status"`
	Categories     []string  `json:"categories"`
	ReasonDetails  []string  `json:"reason_details"`
	ErrorMessage   string    `json:"error_message,omitempty"`
	CheckedAt      time.Time `json:"checked_at"`
}

type OrderLine struct {
	OrderSN       string          `json:"order_sn"`
	ParentOrderSN string          `json:"parent_order_sn"`
	Status        int             `json:"status"`
	Quantity      int             `json:"quantity"`
	GoodsID       int64           `json:"goods_id,omitempty"`
	SKUID         int64           `json:"sku_id,omitempty"`
	ExtCode       string          `json:"ext_code"`
	GoodsName     string          `json:"goods_name"`
	Spec          string          `json:"spec"`
	Raw           json.RawMessage `json:"-"`
}

type ShipmentBrief struct {
	ID                  string    `json:"id"`
	Status              string    `json:"status"`
	ShippingCompanyName string    `json:"shipping_company_name"`
	TrackingNumber      string    `json:"tracking_number"`
	CreatedAt           time.Time `json:"created_at"`
}

type SyncStatus struct {
	ID            int64      `json:"id"`
	Status        string     `json:"status"`
	StartedAt     time.Time  `json:"started_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	FetchedOrders int        `json:"fetched_orders"`
	FetchedLines  int        `json:"fetched_lines"`
	ErrorMessage  string     `json:"error_message,omitempty"`
}

type Warehouse struct {
	ID                     string          `json:"warehouse_id"`
	Name                   string          `json:"warehouse_name"`
	LogicalKey             string          `json:"logical_warehouse_key,omitempty"`
	ShopCodes              []string        `json:"shop_codes,omitempty"`
	RegionID               int64           `json:"region_id,omitempty"`
	EnableBuyShippingLabel bool            `json:"enable_buy_shipping_label"`
	Default                bool            `json:"default_warehouse"`
	ManagementType         int             `json:"warehouse_management_type,omitempty"`
	Raw                    json.RawMessage `json:"-"`
	SyncedAt               time.Time       `json:"synced_at"`
}

type WarehouseMapping struct {
	OMSKey           string    `json:"oms_warehouse_key"`
	OMSWarehouseCode string    `json:"oms_warehouse_code"`
	OMSAccount       string    `json:"oms_account"`
	TemuWarehouseID  string    `json:"temu_warehouse_id"`
	TemuName         string    `json:"temu_warehouse_name"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type CarrierPolicy struct {
	WarehouseKey string `json:"warehouse_key"`
	CarrierCode  string `json:"carrier_code"`
	Priority     int    `json:"priority"`
	Enabled      bool   `json:"enabled"`
}

type WarehouseCarrierPolicies struct {
	WarehouseKey string          `json:"warehouse_key"`
	Carriers     []CarrierPolicy `json:"carriers"`
}

type SKUWarehouseRule struct {
	WarehouseSKU          string     `json:"warehouse_sku"`
	ProductName           string     `json:"product_name,omitempty"`
	DisabledWarehouseKeys []string   `json:"disabled_warehouse_keys"`
	Customized            bool       `json:"customized"`
	UpdatedAt             *time.Time `json:"updated_at,omitempty"`
}

type ShipmentPOGroup struct {
	WarehouseKey string   `json:"warehouse_key"`
	PONumbers    []string `json:"po_numbers"`
}

type FulfillmentAuditShipment struct {
	ParentOrderSN  string
	WarehouseKey   string
	WarehouseCode  string
	TrackingNumber string
	ConfirmedAt    *time.Time
}

type OMSSync struct {
	ShipmentID       string          `json:"shipment_id"`
	OMSWarehouseKey  string          `json:"oms_warehouse_key"`
	WarehouseCode    string          `json:"warehouse_code"`
	Status           string          `json:"status"`
	OutboundOrderNos []string        `json:"outbound_order_nos"`
	TrackingNumber   string          `json:"tracking_number"`
	Attempts         int             `json:"attempts"`
	ErrorMessage     string          `json:"error_message,omitempty"`
	Summary          json.RawMessage `json:"summary,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	VerifiedAt       *time.Time      `json:"verified_at,omitempty"`
}

type OMSPlatformOrderStatus struct {
	PlatformOrderSN   string     `json:"platform_order_sn"`
	OMSOrderNo        string     `json:"oms_order_no"`
	OMSAccount        string     `json:"oms_account"`
	Status            int        `json:"status"`
	StatusKey         string     `json:"status_key"`
	StatusText        string     `json:"status_text"`
	WarehouseCode     string     `json:"warehouse_code"`
	SendWarehouseCode string     `json:"send_warehouse_code"`
	TrackingNumber    string     `json:"tracking_number"`
	AuditTime         string     `json:"audit_time,omitempty"`
	SyncStatus        string     `json:"sync_status"`
	JobStatus         string     `json:"job_status"`
	Archived          bool       `json:"archived"`
	QueriedAt         time.Time  `json:"queried_at"`
	VerifiedAt        *time.Time `json:"verified_at,omitempty"`
}

type PackageSpec struct {
	Weight           string `json:"weight"`
	WeightUnit       string `json:"weight_unit"`
	ExtendWeight     string `json:"extend_weight,omitempty"`
	ExtendWeightUnit string `json:"extend_weight_unit,omitempty"`
	Length           string `json:"length"`
	Width            string `json:"width"`
	Height           string `json:"height"`
	DimensionUnit    string `json:"dimension_unit"`
}

type Quote struct {
	ID                  string          `json:"id"`
	ParentOrderSN       string          `json:"parent_order_sn"`
	OMSWarehouseKey     string          `json:"oms_warehouse_key"`
	TemuWarehouseID     string          `json:"temu_warehouse_id"`
	Region              string          `json:"region"`
	ChannelID           int64           `json:"selected_channel_id"`
	ShipCompanyID       int64           `json:"selected_ship_company_id"`
	ShippingCompanyName string          `json:"selected_company_name"`
	ShipLogisticsType   string          `json:"selected_logistics_type"`
	SelectionReason     string          `json:"selected_reason"`
	RequestPayload      json.RawMessage `json:"-"`
	ResponsePayload     json.RawMessage `json:"options"`
	ExpiresAt           time.Time       `json:"expires_at"`
	CreatedAt           time.Time       `json:"created_at"`
}

type LabelPurchaseCandidate struct {
	PriceRank             int    `json:"price_rank,omitempty"`
	OMSWarehouseKey       string `json:"oms_warehouse_key"`
	TemuWarehouseID       string `json:"temu_warehouse_id"`
	ChannelID             int64  `json:"channel_id"`
	ShipCompanyID         int64  `json:"ship_company_id"`
	CarrierCode           string `json:"carrier_code"`
	ShippingCompanyName   string `json:"shipping_company_name"`
	ShipLogisticsType     string `json:"ship_logistics_type"`
	EstimatedAmount       string `json:"estimated_amount"`
	EstimatedCurrencyCode string `json:"estimated_currency_code"`
}

type LabelPurchaseChoice struct {
	SelectionSource string                   `json:"selection_source"`
	SelectionReason string                   `json:"selection_reason"`
	Selected        LabelPurchaseCandidate   `json:"selected"`
	TopCandidates   []LabelPurchaseCandidate `json:"top_candidates"`
}

type Shipment struct {
	ID                   string          `json:"id"`
	QuoteID              string          `json:"quote_id"`
	IdempotencyKey       string          `json:"idempotency_key"`
	Status               string          `json:"status"`
	SelectionMode        string          `json:"selection_mode"`
	WarehouseID          string          `json:"warehouse_id"`
	ChannelID            int64           `json:"channel_id"`
	ShipCompanyID        int64           `json:"ship_company_id"`
	ShippingCompanyName  string          `json:"shipping_company_name"`
	ShipLogisticsType    string          `json:"ship_logistics_type"`
	FailedCarrierCodes   []string        `json:"failed_carrier_codes,omitempty"`
	PackageSNList        []string        `json:"package_sn_list"`
	TrackingNumber       string          `json:"tracking_number"`
	RequestPayload       json.RawMessage `json:"-"`
	ResponsePayload      json.RawMessage `json:"response,omitempty"`
	ErrorCode            string          `json:"error_code,omitempty"`
	ErrorMessage         string          `json:"error_message,omitempty"`
	SubmissionAttempts   int             `json:"submission_attempts"`
	LastSubmissionAt     time.Time       `json:"last_submission_at"`
	ConfirmationAttempts int             `json:"confirmation_attempts"`
	LastConfirmationAt   *time.Time      `json:"last_confirmation_at,omitempty"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
	ConfirmedAt          *time.Time      `json:"confirmed_at,omitempty"`
	ParentOrderSN        string          `json:"parent_order_sn"`
	OMSWarehouseKey      string          `json:"oms_warehouse_key"`
	OMSWarehouseCode     string          `json:"oms_warehouse_code"`
	OMSSync              *OMSSync        `json:"oms_sync,omitempty"`
}

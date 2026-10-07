package inventory

import (
	"context"
	"net/http"
	"net/url"
)

type WarehouseBinding struct {
	WarehouseKey          string `json:"warehouse_key"`
	OMSCode               string `json:"oms_code"`
	PlatformWarehouseID   string `json:"platform_warehouse_id"`
	PlatformWarehouseName string `json:"platform_warehouse_name"`
	Enabled               bool   `json:"enabled"`
	Effective             bool   `json:"effective"`
	Revision              int64  `json:"revision"`
}

func (c *Client) WarehouseBindings(ctx context.Context, platform, shop string) ([]WarehouseBinding, error) {
	endpoint, err := c.managerEndpoint("/fulfillment-warehouse-bindings")
	if err != nil {
		return nil, err
	}
	items := []WarehouseBinding{}
	values := url.Values{"platform": {platform}, "shop": {shop}}
	err = c.doJSON(ctx, http.MethodGet, endpoint+"?"+values.Encode(), nil, "", "", &items)
	return items, err
}

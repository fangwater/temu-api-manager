package temu

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type SalesOrderPage struct {
	PageItems    []json.RawMessage `json:"pageItems"`
	TotalItemNum int               `json:"totalItemNum"`
}

func (p *SalesOrderPage) UnmarshalJSON(raw []byte) error {
	var value struct {
		PageItems []json.RawMessage `json:"pageItems"`
		Total     *int              `json:"totalItemNum"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if value.Total == nil || *value.Total < 0 {
		return fmt.Errorf("sales page missing total count")
	}
	p.PageItems, p.TotalItemNum = value.PageItems, *value.Total
	return nil
}

// All statuses, including cancelled orders and orders shipped between polls.
func (c *Client) SalesOrders(ctx context.Context, mode string, start, end time.Time, page int) (SalesOrderPage, error) {
	var result SalesOrderPage
	params := map[string]any{"pageNumber": page, "pageSize": 100, "fulfillmentTypeList": []string{"fulfillBySeller", "fulfillByCooperativeWarehouse"}}
	switch mode {
	case "updated":
		params["updateAtStart"], params["updateAtEnd"], params["sortby"] = start.Unix(), end.Unix(), "updateTime"
	case "created":
		params["createAfter"], params["createBefore"], params["sortby"] = start.Unix(), end.Unix(), "createTime"
	default:
		return result, fmt.Errorf("invalid sales sync mode")
	}
	_, err := c.Call(ctx, OrderListAPI, params, &result)
	return result, err
}

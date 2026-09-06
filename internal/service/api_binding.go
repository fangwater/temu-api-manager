package service

import (
	"errors"
	"fmt"
	"strings"
	"temu-api-manager/internal/inventory"
)

// Resolve each candidate warehouse from the API scope used for its SKU inventory.
func fulfillmentAccountForWarehouse(decision inventory.DecisionResponse, warehouseKey string) (string, error) {
	if len(decision.Records) == 0 {
		return "", errors.New("缺少 SKU 库存与 API 绑定信息")
	}
	account := ""
	for _, record := range decision.Records {
		skuAccount := ""
		matches := 0
		for _, region := range record.Regions {
			for _, warehouse := range region.Warehouses {
				if warehouse.Key != warehouseKey {
					continue
				}
				matches++
				binding := warehouse.APIBinding
				if !warehouse.Active || warehouse.QueryStatus != "succeeded" || binding == nil || strings.TrimSpace(binding.CredentialKey) == "" {
					return "", fmt.Errorf("SKU %s 在所选仓库缺少有效 API 绑定", record.SKU)
				}
				var valid bool
				skuAccount, valid = normalizeOMSAccount(binding.OMSAccountKey)
				if !valid {
					return "", fmt.Errorf("SKU %s 的 API 未绑定有效 OMS 账户", record.SKU)
				}
			}
		}
		if matches != 1 {
			return "", fmt.Errorf("SKU %s 在所选仓库的 API 范围不唯一或不存在", record.SKU)
		}
		if account != "" && account != skuAccount {
			return "", errors.New("所选仓库的 SKU 分属不同 OMS 账户，转人工处理")
		}
		account = skuAccount
	}
	return account, nil
}

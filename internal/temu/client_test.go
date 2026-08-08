package temu

import (
	"encoding/json"
	"testing"
)

func TestBuildSignatureMatchesOfficialExample(t *testing.T) {
	parameters := map[string]any{
		"access_token":    "2nifvmpyymvypwmcms5ct4uqqudrwgpmzbcnmkt1jzjkuaf3x56iixym",
		"app_key":         "f9d5cc9313893a20d5aa85c654e8f503",
		"data_type":       "JSON",
		"sendRequestList": json.RawMessage(`[{"orderSendInfoList":[{"quantity":1,"orderSn":"211-21905473070712792","parentOrderSn":"PO-211-21905452099192792","goodsId":601099548666279,"skuId":17592352673534}],"carrierId":"699272611","trackingNumber":"270324232756"}]`),
		"sendType":        0, "timestamp": int64(1711009072), "type": "bg.logistics.shipment.confirm",
	}
	secret := "c7e0a1a63542be4de3cb5488f9fba8149e8fc290"
	if got, want := BuildSignature(parameters, secret), "4CCF219942D4180C6DDA3CE36C1B838F"; got != want {
		t.Fatalf("signature mismatch: got %s want %s", got, want)
	}
}

func TestSerializeSignValueUsesCompactJSON(t *testing.T) {
	if got := serializeSignValue([]string{"a", "b"}); got != `["a","b"]` {
		t.Fatalf("unexpected array serialization: %s", got)
	}
	if got := serializeSignValue(map[string]any{"enabled": false}); got != `{"enabled":false}` {
		t.Fatalf("unexpected object serialization: %s", got)
	}
}

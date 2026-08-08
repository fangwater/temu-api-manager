from __future__ import annotations

import json

from temu_api_manager.client import TemuClient, build_signature, serialize_sign_value


def test_signature_matches_temu_official_example() -> None:
    parameters = {
        "access_token": "2nifvmpyymvypwmcms5ct4uqqudrwgpmzbcnmkt1jzjkuaf3x56iixym",
        "app_key": "f9d5cc9313893a20d5aa85c654e8f503",
        "data_type": "JSON",
        "sendRequestList": [
            {
                "orderSendInfoList": [
                    {
                        "quantity": 1,
                        "orderSn": "211-21905473070712792",
                        "parentOrderSn": "PO-211-21905452099192792",
                        "goodsId": 601099548666279,
                        "skuId": 17592352673534,
                    }
                ],
                "carrierId": "699272611",
                "trackingNumber": "270324232756",
            }
        ],
        "sendType": 0,
        "timestamp": 1711009072,
        "type": "bg.logistics.shipment.confirm",
    }
    secret = "c7e0a1a63542be4de3cb5488f9fba8149e8fc290"

    assert build_signature(parameters, secret) == "4CCF219942D4180C6DDA3CE36C1B838F"


def test_build_payload_uses_seconds_and_does_not_include_secret() -> None:
    client = TemuClient(
        base_url="http://example.test/openapi/router",
        app_key="app-key",
        app_secret="app-secret",
        access_token="access-token",
        clock=lambda: 1711009072.9,
    )

    payload = client.build_payload(
        "bg.order.list.v2.get",
        {"pageNumber": 1, "pageSize": 10},
    )

    assert payload["timestamp"] == "1711009072"
    assert payload["sign"] == build_signature(payload, "app-secret")
    assert "app-secret" not in json.dumps(payload)


def test_sign_value_serialization_is_compact_json() -> None:
    assert serialize_sign_value(True) == "true"
    assert serialize_sign_value(["a", "b"]) == '["a","b"]'
    assert serialize_sign_value({"enabled": False}) == '{"enabled":false}'

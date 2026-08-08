from __future__ import annotations

import pytest

from temu_api_manager.products import ProductService


class RecordingClient:
    def __init__(self) -> None:
        self.calls: list[tuple[str, dict[str, object]]] = []

    def request(self, api_type: str, parameters: dict[str, object]) -> dict[str, object]:
        self.calls.append((api_type, parameters))
        return {"success": True, "result": {}}


def test_baseprice_recommend_calls_temu_product_api() -> None:
    client = RecordingClient()
    service = ProductService(client)  # type: ignore[arg-type]
    parameters = {
        "language": "en",
        "supplierPriceEstimateQry": {
            "goodsBasicInfo": {"catId": 123},
            "supplierPriceEstimateSkuQryList": [],
        },
    }

    response = service.recommend_baseprice(parameters)

    assert response["success"] is True
    assert client.calls == [
        ("temu.local.goods.baseprice.recommend", parameters)
    ]


def test_baseprice_recommend_requires_query_object() -> None:
    service = ProductService(RecordingClient())  # type: ignore[arg-type]

    with pytest.raises(ValueError, match="supplierPriceEstimateQry"):
        service.recommend_baseprice({})


def test_baseprice_recommend_rejects_non_string_language() -> None:
    service = ProductService(RecordingClient())  # type: ignore[arg-type]

    with pytest.raises(ValueError, match="language"):
        service.recommend_baseprice(
            {"language": 1, "supplierPriceEstimateQry": {}}
        )

from __future__ import annotations

import pytest

from temu_api_manager.orders import OrderService, validate_order_list_parameters


class RecordingClient:
    def __init__(self) -> None:
        self.calls: list[tuple[str, dict[str, object]]] = []

    def request(self, api_type: str, parameters: dict[str, object]) -> dict[str, object]:
        self.calls.append((api_type, parameters))
        return {"success": True, "result": {}}


def test_order_list_accepts_documented_page_size_limit() -> None:
    validate_order_list_parameters({"pageNumber": 1, "pageSize": 100})


@pytest.mark.parametrize("page_size", [0, 101, True])
def test_order_list_rejects_invalid_page_size(page_size: object) -> None:
    with pytest.raises(ValueError, match="pageSize"):
        validate_order_list_parameters({"pageNumber": 1, "pageSize": page_size})


def test_time_filters_must_be_paired() -> None:
    with pytest.raises(ValueError, match="createAfter and createBefore"):
        validate_order_list_parameters(
            {"pageNumber": 1, "pageSize": 10, "createAfter": 1711009072}
        )


def test_time_filter_end_cannot_precede_start() -> None:
    with pytest.raises(ValueError, match="createBefore"):
        validate_order_list_parameters(
            {
                "pageNumber": 1,
                "pageSize": 10,
                "createAfter": 1711009072,
                "createBefore": 1711000000,
            }
        )


def test_order_detail_calls_v2_api_with_parent_order_number() -> None:
    client = RecordingClient()
    service = OrderService(client)  # type: ignore[arg-type]

    response = service.order_detail(
        "PO-123",
        fulfillment_types=["fulfillBySeller"],
    )

    assert response["success"] is True
    assert client.calls == [
        (
            "bg.order.detail.v2.get",
            {
                "parentOrderSn": "PO-123",
                "fulfillmentTypeList": ["fulfillBySeller"],
            },
        )
    ]


def test_order_detail_requires_parent_order_number() -> None:
    service = OrderService(RecordingClient())  # type: ignore[arg-type]

    with pytest.raises(ValueError, match="parentOrderSn"):
        service.order_detail("   ")


def test_order_detail_rejects_unknown_fulfillment_type() -> None:
    service = OrderService(RecordingClient())  # type: ignore[arg-type]

    with pytest.raises(ValueError, match="fulfillmentTypeList"):
        service.order_detail("PO-123", fulfillment_types=["unknown"])


def test_order_amount_calls_sensitive_amount_api() -> None:
    client = RecordingClient()
    service = OrderService(client)  # type: ignore[arg-type]

    response = service.order_amount("PO-123")

    assert response["success"] is True
    assert client.calls == [
        ("bg.order.amount.query", {"parentOrderSn": "PO-123"})
    ]


def test_order_amount_requires_parent_order_number() -> None:
    service = OrderService(RecordingClient())  # type: ignore[arg-type]

    with pytest.raises(ValueError, match="parentOrderSn"):
        service.order_amount("   ")

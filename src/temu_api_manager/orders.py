from __future__ import annotations

from typing import Any, Mapping

from .client import TemuClient


ORDER_LIST_V2_TYPE = "bg.order.list.v2.get"
ORDER_DETAIL_V2_TYPE = "bg.order.detail.v2.get"
ORDER_AMOUNT_QUERY_TYPE = "bg.order.amount.query"
MAX_PAGE_SIZE = 100
FULFILLMENT_TYPES = frozenset(
    {
        "fulfillBySeller",
        "fulfillByCooperativeWarehouse",
    }
)

PAIRED_TIME_FILTERS = (
    ("createAfter", "createBefore"),
    ("expectShipLatestTimeStart", "expectShipLatestTimeEnd"),
    ("updateAtStart", "updateAtEnd"),
    ("parentConfirmTimeStart", "parentConfirmTimeEnd"),
)


class OrderService:
    def __init__(self, client: TemuClient) -> None:
        self.client = client

    def order_list(
        self,
        parameters: Mapping[str, Any] | None = None,
        *,
        page_number: int = 1,
        page_size: int = 10,
    ) -> dict[str, Any]:
        request_parameters = dict(parameters or {})
        request_parameters["pageNumber"] = page_number
        request_parameters["pageSize"] = page_size
        validate_order_list_parameters(request_parameters)
        return self.client.request(ORDER_LIST_V2_TYPE, request_parameters)

    def order_detail(
        self,
        parent_order_sn: str,
        *,
        fulfillment_types: list[str] | None = None,
    ) -> dict[str, Any]:
        parent_order_sn = parent_order_sn.strip()
        if not parent_order_sn:
            raise ValueError("parentOrderSn is required")
        parameters: dict[str, Any] = {"parentOrderSn": parent_order_sn}
        if fulfillment_types:
            invalid = sorted(set(fulfillment_types) - FULFILLMENT_TYPES)
            if invalid:
                raise ValueError(f"invalid fulfillmentTypeList values: {', '.join(invalid)}")
            parameters["fulfillmentTypeList"] = fulfillment_types
        return self.client.request(ORDER_DETAIL_V2_TYPE, parameters)

    def order_amount(self, parent_order_sn: str) -> dict[str, Any]:
        parent_order_sn = parent_order_sn.strip()
        if not parent_order_sn:
            raise ValueError("parentOrderSn is required")
        return self.client.request(
            ORDER_AMOUNT_QUERY_TYPE,
            {"parentOrderSn": parent_order_sn},
        )


def validate_order_list_parameters(parameters: Mapping[str, Any]) -> None:
    page_number = parameters.get("pageNumber", 1)
    page_size = parameters.get("pageSize", 10)
    if not isinstance(page_number, int) or isinstance(page_number, bool) or page_number < 1:
        raise ValueError("pageNumber must be an integer greater than or equal to 1")
    if (
        not isinstance(page_size, int)
        or isinstance(page_size, bool)
        or not 1 <= page_size <= MAX_PAGE_SIZE
    ):
        raise ValueError("pageSize must be an integer between 1 and 100")

    for start_key, end_key in PAIRED_TIME_FILTERS:
        has_start = parameters.get(start_key) is not None
        has_end = parameters.get(end_key) is not None
        if has_start != has_end:
            raise ValueError(f"{start_key} and {end_key} must be provided together")
        if has_start:
            start = parameters[start_key]
            end = parameters[end_key]
            if not isinstance(start, int) or not isinstance(end, int):
                raise ValueError(f"{start_key} and {end_key} must be Unix seconds")
            if end < start:
                raise ValueError(f"{end_key} must be greater than or equal to {start_key}")


def extract_page_items(response: Mapping[str, Any]) -> list[dict[str, Any]]:
    result = response.get("result")
    if not isinstance(result, dict):
        return []
    items = result.get("pageItems")
    if not isinstance(items, list):
        return []
    return [item for item in items if isinstance(item, dict)]

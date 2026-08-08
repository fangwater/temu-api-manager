from __future__ import annotations

from typing import Any, Mapping

from .client import TemuClient


BASEPRICE_RECOMMEND_TYPE = "temu.local.goods.baseprice.recommend"


class ProductService:
    def __init__(self, client: TemuClient) -> None:
        self.client = client

    def recommend_baseprice(
        self,
        parameters: Mapping[str, Any],
    ) -> dict[str, Any]:
        request_parameters = dict(parameters)
        query = request_parameters.get("supplierPriceEstimateQry")
        if not isinstance(query, dict):
            raise ValueError("supplierPriceEstimateQry must be a JSON object")
        language = request_parameters.get("language")
        if language is not None and not isinstance(language, str):
            raise ValueError("language must be a string")
        return self.client.request(BASEPRICE_RECOMMEND_TYPE, request_parameters)

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any

from .client import TemuApiError, TemuClient
from .config import Settings, load_settings
from .orders import OrderService, extract_page_items
from .products import ProductService


def main(argv: list[str] | None = None) -> int:
    settings = load_settings()
    parser = build_parser(settings)
    args = parser.parse_args(argv)
    try:
        return args.func(args, settings)
    except TemuApiError as exc:
        if isinstance(exc.payload, dict):
            print(json.dumps(exc.payload, ensure_ascii=False, indent=2), file=sys.stderr)
        else:
            print(f"error: {exc}", file=sys.stderr)
        return 1
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1


def build_parser(settings: Settings) -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="temu-manager")
    sub = parser.add_subparsers(dest="command", required=True)

    order_list = sub.add_parser(
        "order-list",
        help="Call bg.order.list.v2.get",
    )
    order_list.add_argument("--api-base-url", default=settings.api_base_url)
    order_list.add_argument("--page-number", type=int, default=1)
    order_list.add_argument("--page-size", type=int, default=10)
    source = order_list.add_mutually_exclusive_group()
    source.add_argument("--params-json", default="{}")
    source.add_argument("--params-file")
    order_list.add_argument(
        "--set",
        action="append",
        default=[],
        help="Extra request parameter as key=JSON_VALUE",
    )
    order_list.add_argument("--output", help="Write the raw JSON response to this file")
    order_list.set_defaults(func=cmd_order_list)

    order_detail = sub.add_parser(
        "order-detail",
        help="Call bg.order.detail.v2.get",
    )
    order_detail.add_argument("--api-base-url", default=settings.api_base_url)
    order_detail.add_argument("--parent-order-sn", required=True)
    order_detail.add_argument(
        "--fulfillment-type",
        action="append",
        choices=("fulfillBySeller", "fulfillByCooperativeWarehouse"),
        help="Optional fulfillment type; repeat to pass multiple values",
    )
    order_detail.add_argument("--output", help="Write the raw JSON response to this file")
    order_detail.set_defaults(func=cmd_order_detail)

    order_amount = sub.add_parser(
        "order-amount",
        help="Call bg.order.amount.query",
    )
    order_amount.add_argument("--api-base-url", default=settings.api_base_url)
    order_amount.add_argument("--parent-order-sn", required=True)
    order_amount.add_argument("--output", help="Write the raw JSON response to this file")
    order_amount.set_defaults(func=cmd_order_amount)

    baseprice = sub.add_parser(
        "baseprice-recommend",
        help="Call temu.local.goods.baseprice.recommend",
    )
    baseprice.add_argument("--api-base-url", default=settings.api_base_url)
    baseprice_source = baseprice.add_mutually_exclusive_group()
    baseprice_source.add_argument("--params-json", default="{}")
    baseprice_source.add_argument("--params-file")
    baseprice.add_argument(
        "--set",
        action="append",
        default=[],
        help="Extra request parameter as key=JSON_VALUE",
    )
    baseprice.add_argument("--language", help="Optional Temu response language")
    baseprice.add_argument("--output", help="Write the raw JSON response to this file")
    baseprice.set_defaults(func=cmd_baseprice_recommend)
    return parser


def cmd_order_list(args: argparse.Namespace, settings: Settings) -> int:
    client = client_from_settings(settings, base_url=args.api_base_url)
    parameters = load_parameters(args)
    service = OrderService(client)
    response = service.order_list(
        parameters,
        page_number=args.page_number,
        page_size=args.page_size,
    )
    rendered = json.dumps(response, ensure_ascii=False, indent=2)
    if args.output:
        output_path = Path(args.output)
        write_private_text(output_path, rendered + "\n")
        result = response.get("result") or {}
        print(
            json.dumps(
                {
                    "output": str(output_path),
                    "pageItems": len(extract_page_items(response)),
                    "totalItemNum": result.get("totalItemNum"),
                    "requestId": response.get("requestId"),
                },
                ensure_ascii=False,
                indent=2,
            )
        )
    else:
        print(rendered)
    return 0


def cmd_order_detail(args: argparse.Namespace, settings: Settings) -> int:
    client = client_from_settings(settings, base_url=args.api_base_url)
    service = OrderService(client)
    response = service.order_detail(
        args.parent_order_sn,
        fulfillment_types=args.fulfillment_type,
    )
    rendered = json.dumps(response, ensure_ascii=False, indent=2)
    if args.output:
        output_path = Path(args.output)
        write_private_text(output_path, rendered + "\n")
        result = response.get("result") or {}
        order_list = result.get("orderList") or []
        print(
            json.dumps(
                {
                    "output": str(output_path),
                    "orderItems": len(order_list),
                    "requestId": response.get("requestId"),
                },
                ensure_ascii=False,
                indent=2,
            )
        )
    else:
        print(rendered)
    return 0


def cmd_order_amount(args: argparse.Namespace, settings: Settings) -> int:
    client = client_from_settings(settings, base_url=args.api_base_url)
    service = OrderService(client)
    response = service.order_amount(args.parent_order_sn)
    rendered = json.dumps(response, ensure_ascii=False, indent=2)
    if args.output:
        output_path = Path(args.output)
        write_private_text(output_path, rendered + "\n")
        result = response.get("result") or {}
        order_list = result.get("orderList") or []
        warning = result.get("warning") or []
        print(
            json.dumps(
                {
                    "output": str(output_path),
                    "orderItems": len(order_list),
                    "warnings": len(warning),
                    "requestId": response.get("requestId"),
                },
                ensure_ascii=False,
                indent=2,
            )
        )
    else:
        print(rendered)
    return 0


def cmd_baseprice_recommend(args: argparse.Namespace, settings: Settings) -> int:
    client = client_from_settings(settings, base_url=args.api_base_url)
    parameters = load_parameters(args)
    if args.language:
        parameters["language"] = args.language
    service = ProductService(client)
    response = service.recommend_baseprice(parameters)
    rendered = json.dumps(response, ensure_ascii=False, indent=2)
    if args.output:
        output_path = Path(args.output)
        write_private_text(output_path, rendered + "\n")
        result = response.get("result") or {}
        estimate = result.get("supplierPriceEstimateInfo") or {}
        sku_estimates = estimate.get("skuEstimateInfoList") or []
        print(
            json.dumps(
                {
                    "output": str(output_path),
                    "skuEstimates": len(sku_estimates),
                    "requestId": response.get("requestId"),
                },
                ensure_ascii=False,
                indent=2,
            )
        )
    else:
        print(rendered)
    return 0


def write_private_text(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content, encoding="utf-8")
    path.chmod(0o600)


def client_from_settings(settings: Settings, *, base_url: str) -> TemuClient:
    missing = [
        name
        for name, value in (
            ("TEMU_ACCESS_TOKEN", settings.access_token),
            ("TEMU_APP_KEY", settings.app_key),
            ("TEMU_APP_SECRET", settings.app_secret),
        )
        if not value
    ]
    if missing:
        raise ValueError(f"missing required environment variables: {', '.join(missing)}")
    return TemuClient(
        base_url=base_url,
        access_token=settings.access_token,
        app_key=settings.app_key,
        app_secret=settings.app_secret,
    )


def load_parameters(args: argparse.Namespace) -> dict[str, Any]:
    if args.params_file:
        raw = Path(args.params_file).read_text(encoding="utf-8")
    else:
        raw = args.params_json
    parameters = json.loads(raw)
    if not isinstance(parameters, dict):
        raise ValueError("request parameters must be a JSON object")
    for assignment in args.set:
        key, separator, raw_value = assignment.partition("=")
        if not separator or not key:
            raise ValueError(f"invalid --set value: {assignment}; expected key=JSON_VALUE")
        try:
            value = json.loads(raw_value)
        except json.JSONDecodeError:
            value = raw_value
        parameters[key] = value
    return parameters

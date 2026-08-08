from __future__ import annotations

import hashlib
import json
import time
import urllib.error
import urllib.request
from typing import Any, Callable, Mapping


class TemuApiError(RuntimeError):
    def __init__(
        self,
        message: str,
        *,
        status: int | None = None,
        payload: Any = None,
    ) -> None:
        super().__init__(message)
        self.status = status
        self.payload = payload


def serialize_sign_value(value: Any) -> str:
    if isinstance(value, str):
        return value
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, (dict, list)):
        return json.dumps(value, separators=(",", ":"), ensure_ascii=False)
    return str(value)


def build_signature(parameters: Mapping[str, Any], app_secret: str) -> str:
    signable = {
        key: value
        for key, value in parameters.items()
        if key != "sign" and value is not None
    }
    joined = "".join(
        f"{key}{serialize_sign_value(signable[key])}"
        for key in sorted(signable)
    )
    signature_text = f"{app_secret}{joined}{app_secret}"
    return hashlib.md5(signature_text.encode("utf-8")).hexdigest().upper()


class TemuClient:
    def __init__(
        self,
        *,
        base_url: str,
        app_key: str,
        app_secret: str,
        access_token: str,
        timeout_seconds: int = 30,
        clock: Callable[[], float] = time.time,
    ) -> None:
        self.base_url = base_url
        self.app_key = app_key
        self.app_secret = app_secret
        self.access_token = access_token
        self.timeout_seconds = timeout_seconds
        self.clock = clock

    def build_payload(
        self,
        api_type: str,
        parameters: Mapping[str, Any] | None = None,
        *,
        version: str | None = None,
    ) -> dict[str, Any]:
        payload: dict[str, Any] = {
            "access_token": self.access_token,
            "app_key": self.app_key,
            "data_type": "JSON",
            "timestamp": str(int(self.clock())),
            "type": api_type,
        }
        if version:
            payload["version"] = version
        for key, value in (parameters or {}).items():
            if key in payload or key == "sign":
                raise ValueError(f"reserved Temu parameter cannot be overridden: {key}")
            if value is not None:
                payload[key] = value
        payload["sign"] = build_signature(payload, self.app_secret)
        return payload

    def request(
        self,
        api_type: str,
        parameters: Mapping[str, Any] | None = None,
        *,
        version: str | None = None,
        require_success: bool = True,
    ) -> dict[str, Any]:
        payload = self.build_payload(api_type, parameters, version=version)
        body = json.dumps(payload, separators=(",", ":"), ensure_ascii=False).encode("utf-8")
        request = urllib.request.Request(
            self.base_url,
            data=body,
            headers={
                "Accept": "application/json",
                "Content-Type": "application/json",
            },
            method="POST",
        )
        try:
            with urllib.request.urlopen(request, timeout=self.timeout_seconds) as response:
                raw = response.read().decode("utf-8", errors="replace")
                status = response.status
        except urllib.error.HTTPError as exc:
            raw = exc.read().decode("utf-8", errors="replace")
            parsed = _parse_json(raw)
            raise TemuApiError(
                f"Temu HTTP {exc.code}: {raw}",
                status=exc.code,
                payload=parsed,
            ) from exc
        except urllib.error.URLError as exc:
            raise TemuApiError(f"Temu request failed: {exc}") from exc

        parsed = _parse_json(raw)
        if not isinstance(parsed, dict):
            raise TemuApiError(
                f"Temu returned non-object JSON: {raw}",
                status=status,
                payload=parsed,
            )
        if require_success and parsed.get("success") is not True:
            raise TemuApiError(
                "Temu API error "
                f"code={parsed.get('errorCode')} msg={parsed.get('errorMsg')}",
                status=status,
                payload=parsed,
            )
        return parsed


def _parse_json(raw: str) -> Any:
    if not raw:
        return {}
    try:
        return json.loads(raw)
    except json.JSONDecodeError as exc:
        raise TemuApiError(f"Invalid JSON response: {raw}") from exc

from __future__ import annotations

import os
from dataclasses import dataclass

try:
    from dotenv import load_dotenv
except ModuleNotFoundError:
    def load_dotenv(*_args: object, **_kwargs: object) -> bool:
        return False


@dataclass(frozen=True)
class Settings:
    api_base_url: str
    api_upstream_url: str | None
    access_token: str | None
    app_key: str | None
    app_secret: str | None


def load_settings() -> Settings:
    load_dotenv()
    return Settings(
        api_base_url=os.getenv(
            "TEMU_API_BASE_URL",
            "http://13.115.227.29:6355/openapi/router",
        ),
        api_upstream_url=os.getenv("TEMU_API_UPSTREAM_URL") or None,
        access_token=os.getenv("TEMU_ACCESS_TOKEN") or None,
        app_key=os.getenv("TEMU_APP_KEY") or None,
        app_secret=os.getenv("TEMU_APP_SECRET") or None,
    )

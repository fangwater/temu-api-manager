from __future__ import annotations

from pathlib import Path

from temu_api_manager.cli import write_private_text


def test_write_private_text_restricts_file_permissions(tmp_path: Path) -> None:
    output = tmp_path / "exports" / "orders.json"

    write_private_text(output, "{}\n")

    assert output.read_text(encoding="utf-8") == "{}\n"
    assert output.stat().st_mode & 0o777 == 0o600

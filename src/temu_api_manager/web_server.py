"""Upload web application for Temu business spreadsheets."""

from __future__ import annotations

import argparse
from datetime import datetime, timezone
from email.parser import BytesParser
from email.policy import default
import hashlib
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import mimetypes
import os
from pathlib import Path
import re
import secrets
from urllib.parse import urlsplit

from .xlsx_reader import InvalidWorkbook, inspect_xlsx


MAX_FILE_SIZE = 50 * 1024 * 1024
MAX_REQUEST_SIZE = MAX_FILE_SIZE + 1024 * 1024
PROJECT_ROOT = Path(__file__).resolve().parents[2]
WEB_ROOT = PROJECT_ROOT / "web"
UPLOAD_ROOT = PROJECT_ROOT / "uploads"
SAFE_NAME = re.compile(r"[^A-Za-z0-9._\-\u4e00-\u9fff]+")


def _clean_filename(value: str) -> str:
    name = Path(value.replace("\\", "/")).name.strip()
    name = SAFE_NAME.sub("_", name).strip("._")
    return name[:180] or "workbook.xlsx"


def _json_bytes(payload: object) -> bytes:
    return json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")


def _metadata_path(workbook_path: Path) -> Path:
    return workbook_path.with_suffix(workbook_path.suffix + ".json")


def _save_private(path: Path, data: bytes) -> None:
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(descriptor, "wb") as handle:
            handle.write(data)
    except BaseException:
        path.unlink(missing_ok=True)
        raise


def _list_uploads() -> list[dict[str, object]]:
    UPLOAD_ROOT.mkdir(mode=0o700, parents=True, exist_ok=True)
    items: list[dict[str, object]] = []
    for path in UPLOAD_ROOT.glob("*.xlsx.json"):
        try:
            items.append(json.loads(path.read_text(encoding="utf-8")))
        except (OSError, ValueError):
            continue
    return sorted(items, key=lambda item: str(item.get("uploaded_at", "")), reverse=True)


class TemuWebHandler(BaseHTTPRequestHandler):
    server_version = "TemuUpload/1.0"

    def log_message(self, format_string: str, *args: object) -> None:
        # Request bodies, filenames, and spreadsheet content are never logged.
        print(f"{self.address_string()} - {format_string % args}")

    def _send(self, status: HTTPStatus, body: bytes, content_type: str) -> None:
        self.send_response(status.value)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("X-Content-Type-Options", "nosniff")
        self.send_header("X-Frame-Options", "SAMEORIGIN")
        self.send_header("Referrer-Policy", "strict-origin-when-cross-origin")
        self.end_headers()
        self.wfile.write(body)

    def _send_json(self, status: HTTPStatus, payload: object) -> None:
        self._send(status, _json_bytes(payload), "application/json; charset=utf-8")

    def _path(self) -> str:
        path = urlsplit(self.path).path
        if path == "/temu":
            return "/"
        if path.startswith("/temu/"):
            return path[5:]
        return path

    def do_GET(self) -> None:
        path = self._path()
        if path == "/healthz":
            return self._send_json(HTTPStatus.OK, {"success": True, "status": "ok"})
        if path == "/api/uploads":
            return self._send_json(HTTPStatus.OK, {"success": True, "data": _list_uploads()})
        self._serve_static(path)

    def do_POST(self) -> None:
        if self._path() != "/api/uploads":
            return self._send_json(HTTPStatus.NOT_FOUND, {"success": False, "error": "接口不存在"})
        self._upload()

    def _serve_static(self, path: str) -> None:
        relative = "index.html" if path in ("", "/") else path.lstrip("/")
        candidate = (WEB_ROOT / relative).resolve()
        if WEB_ROOT.resolve() not in candidate.parents and candidate != WEB_ROOT.resolve():
            return self._send_json(HTTPStatus.NOT_FOUND, {"success": False, "error": "页面不存在"})
        if not candidate.is_file():
            candidate = WEB_ROOT / "index.html"
        content_type = mimetypes.guess_type(candidate.name)[0] or "application/octet-stream"
        if content_type.startswith("text/") or content_type in ("application/javascript", "application/json"):
            content_type += "; charset=utf-8"
        self._send(HTTPStatus.OK, candidate.read_bytes(), content_type)

    def _upload(self) -> None:
        try:
            content_length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            content_length = 0
        if content_length <= 0 or content_length > MAX_REQUEST_SIZE:
            return self._send_json(HTTPStatus.REQUEST_ENTITY_TOO_LARGE, {"success": False, "error": "文件不能超过 50 MB"})
        content_type = self.headers.get("Content-Type", "")
        if not content_type.lower().startswith("multipart/form-data"):
            return self._send_json(HTTPStatus.UNSUPPORTED_MEDIA_TYPE, {"success": False, "error": "请求格式无效"})

        body = self.rfile.read(content_length)
        message = BytesParser(policy=default).parsebytes(
            f"Content-Type: {content_type}\r\nMIME-Version: 1.0\r\n\r\n".encode("ascii") + body
        )
        file_part = next((part for part in message.iter_parts() if part.get_param("name", header="content-disposition") == "file"), None)
        if file_part is None:
            return self._send_json(HTTPStatus.BAD_REQUEST, {"success": False, "error": "没有收到文件"})
        original_name = file_part.get_filename() or ""
        data = file_part.get_payload(decode=True) or b""
        if not original_name.lower().endswith(".xlsx"):
            return self._send_json(HTTPStatus.UNSUPPORTED_MEDIA_TYPE, {"success": False, "error": "仅支持 .xlsx 格式的 Excel 文件"})
        if not data or len(data) > MAX_FILE_SIZE:
            return self._send_json(HTTPStatus.REQUEST_ENTITY_TOO_LARGE, {"success": False, "error": "文件为空或超过 50 MB"})
        try:
            sheets = inspect_xlsx(data)
        except InvalidWorkbook as error:
            return self._send_json(HTTPStatus.UNPROCESSABLE_ENTITY, {"success": False, "error": str(error)})

        UPLOAD_ROOT.mkdir(mode=0o700, parents=True, exist_ok=True)
        os.chmod(UPLOAD_ROOT, 0o700)
        clean_name = _clean_filename(original_name)
        file_id = f"{datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ')}-{secrets.token_hex(4)}"
        stored_name = f"{file_id}-{clean_name}"
        workbook_path = UPLOAD_ROOT / stored_name
        metadata = {
            "id": file_id,
            "original_name": original_name,
            "stored_name": stored_name,
            "size": len(data),
            "sha256": hashlib.sha256(data).hexdigest(),
            "uploaded_at": datetime.now(timezone.utc).isoformat(),
            "sheet_count": len(sheets),
            "sheets": sheets,
        }
        try:
            _save_private(workbook_path, data)
            _save_private(_metadata_path(workbook_path), _json_bytes(metadata))
        except OSError:
            workbook_path.unlink(missing_ok=True)
            return self._send_json(HTTPStatus.INTERNAL_SERVER_ERROR, {"success": False, "error": "文件保存失败"})
        self._send_json(HTTPStatus.CREATED, {"success": True, "data": metadata})


def create_server(host: str, port: int) -> ThreadingHTTPServer:
    return ThreadingHTTPServer((host, port), TemuWebHandler)


def main() -> None:
    parser = argparse.ArgumentParser(description="Run the Temu Excel upload web application")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=18082)
    args = parser.parse_args()
    server = create_server(args.host, args.port)
    print(f"Temu upload console listening on http://{args.host}:{args.port}/temu/")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()

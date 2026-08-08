"""Small, dependency-free reader for workbook structure and dimensions."""

from __future__ import annotations

from dataclasses import asdict, dataclass
from io import BytesIO
from pathlib import PurePosixPath
import re
from zipfile import BadZipFile, ZipFile
from xml.etree import ElementTree


MAX_EXPANDED_SIZE = 250 * 1024 * 1024
MAX_ZIP_ENTRIES = 10_000
CELL_REFERENCE = re.compile(r"([A-Z]+)([0-9]+)")
REL_NS = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
PACKAGE_REL_NS = "http://schemas.openxmlformats.org/package/2006/relationships"
SHEET_NS = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"


class InvalidWorkbook(ValueError):
    """Raised when an uploaded file is not a readable XLSX workbook."""


@dataclass(frozen=True)
class SheetInfo:
    name: str
    rows: int
    columns: int


def _column_number(reference: str) -> int:
    match = CELL_REFERENCE.fullmatch(reference)
    if not match:
        return 0
    value = 0
    for letter in match.group(1):
        value = value * 26 + ord(letter) - ord("A") + 1
    return value


def _dimension_size(reference: str | None) -> tuple[int, int]:
    if not reference:
        return 0, 0
    final_cell = reference.split(":")[-1].replace("$", "")
    match = CELL_REFERENCE.fullmatch(final_cell)
    if not match:
        return 0, 0
    return int(match.group(2)), _column_number(final_cell)


def _safe_target(target: str) -> str:
    path = PurePosixPath("xl") / target
    normalized: list[str] = []
    for part in path.parts:
        if part in ("", "."):
            continue
        if part == "..":
            if not normalized:
                raise InvalidWorkbook("工作表路径无效")
            normalized.pop()
        else:
            normalized.append(part)
    return "/".join(normalized)


def inspect_xlsx(data: bytes) -> list[dict[str, int | str]]:
    """Return sheet names and used dimensions without evaluating cell content."""
    try:
        with ZipFile(BytesIO(data)) as archive:
            entries = archive.infolist()
            if len(entries) > MAX_ZIP_ENTRIES:
                raise InvalidWorkbook("Excel 文件包含过多内部文件")
            if sum(entry.file_size for entry in entries) > MAX_EXPANDED_SIZE:
                raise InvalidWorkbook("Excel 解压后的内容过大")
            try:
                workbook = ElementTree.fromstring(archive.read("xl/workbook.xml"))
                relationships = ElementTree.fromstring(archive.read("xl/_rels/workbook.xml.rels"))
            except (KeyError, ElementTree.ParseError) as error:
                raise InvalidWorkbook("文件不是有效的 .xlsx 工作簿") from error

            targets = {
                relation.attrib["Id"]: _safe_target(relation.attrib["Target"])
                for relation in relationships.findall(f"{{{PACKAGE_REL_NS}}}Relationship")
                if "Id" in relation.attrib and "Target" in relation.attrib
            }
            result: list[SheetInfo] = []
            for sheet in workbook.findall(f".//{{{SHEET_NS}}}sheet"):
                name = sheet.attrib.get("name", "未命名工作表")
                relation_id = sheet.attrib.get(f"{{{REL_NS}}}id")
                target = targets.get(relation_id or "")
                if not target:
                    continue
                try:
                    worksheet = ElementTree.fromstring(archive.read(target))
                except (KeyError, ElementTree.ParseError) as error:
                    raise InvalidWorkbook(f"无法读取工作表：{name}") from error
                dimension = worksheet.find(f"{{{SHEET_NS}}}dimension")
                rows, columns = _dimension_size(dimension.attrib.get("ref") if dimension is not None else None)
                if rows == 0:
                    row_nodes = worksheet.findall(f".//{{{SHEET_NS}}}row")
                    rows = max((int(node.attrib.get("r", "0")) for node in row_nodes), default=0)
                    columns = max(
                        (_column_number(node.attrib["r"].replace("$", "")) for node in worksheet.findall(f".//{{{SHEET_NS}}}c")),
                        default=0,
                    )
                result.append(SheetInfo(name=name, rows=rows, columns=columns))
    except BadZipFile as error:
        raise InvalidWorkbook("文件不是有效的 .xlsx 工作簿") from error
    if not result:
        raise InvalidWorkbook("工作簿中没有可读取的工作表")
    return [asdict(sheet) for sheet in result]

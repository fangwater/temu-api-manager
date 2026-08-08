from io import BytesIO
from zipfile import ZIP_DEFLATED, ZipFile

import pytest

from temu_api_manager.xlsx_reader import InvalidWorkbook, inspect_xlsx


def workbook_bytes() -> bytes:
    output = BytesIO()
    with ZipFile(output, "w", ZIP_DEFLATED) as archive:
        archive.writestr(
            "xl/workbook.xml",
            '<?xml version="1.0"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" '
            'xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>'
            '<sheet name="订单明细" sheetId="1" r:id="rId1"/></sheets></workbook>',
        )
        archive.writestr(
            "xl/_rels/workbook.xml.rels",
            '<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">'
            '<Relationship Id="rId1" Target="worksheets/sheet1.xml" /></Relationships>',
        )
        archive.writestr(
            "xl/worksheets/sheet1.xml",
            '<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">'
            '<dimension ref="A1:F24"/><sheetData/></worksheet>',
        )
    return output.getvalue()


def test_inspect_xlsx_reads_sheet_dimensions():
    assert inspect_xlsx(workbook_bytes()) == [{"name": "订单明细", "rows": 24, "columns": 6}]


def test_inspect_xlsx_rejects_non_zip_content():
    with pytest.raises(InvalidWorkbook, match="有效"):
        inspect_xlsx(b"not an excel file")

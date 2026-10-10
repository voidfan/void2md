# void2md Go API Service

This service provides a REST API endpoint for converting files to Markdown using the Rust `void2md` library.

Support file list：
```text
PDF - PDF documents
DOCX - Microsoft Word documents
PPTX - Microsoft PowerPoint presentations
XLSX - Microsoft Excel spreadsheets
EPUB - EPUB e-books
IPYNB - Jupyter notebooks
HTML - HTML documents
XML - XML documents
CSV - CSV files
JSON - JSON files
YAML - YAML files
Plain text - Various text formats (.txt, .md, .rst, etc.)
Audio metadata - MP3, FLAC, etc. (extracts metadata)
Image metadata - JPEG, PNG, etc. (extracts EXIF data)
RSS - RSS feeds
GitHub - GitHub specific content
Wikipedia - Wikipedia pages
```

## Features

- `/v1/fileparse` POST endpoint accepts file uploads via multipart form data
- Supports all file formats supported by the underlying Rust library (PDF, DOCX, PPTX, XLSX, EPUB, HTML, etc.)
- Authentication via Bearer token in Authorization header
- Returns JSON response with converted Markdown content
- Docker containerization with multi-stage build

### Docker Build and run

```bash
docker build -t void2md .
docker run -p 3457:3457 void2md
```

## API Usage

### File Upload

```bash
curl -H "Authorization: Bearer test-key" \
     -F "file=@test.pdf;type=application/pdf" \
     http://localhost:3457/v1/fileparse

# 启用 DOCX 标题重排和印章检测
curl -H "Authorization: Bearer test-key" \
     -F "file=@document.docx" \
     -F "relevel_titles=true" \
     -F "seal_rec=true" \
     http://localhost:3457/v1/fileparse

# 启用 PPTX 图表分析
curl -H "Authorization: Bearer test-key" \
     -F "file=@presentation.pptx" \
     -F "chart_analysis=true" \
     http://localhost:3457/v1/fileparse

# 限制 XLSX 行数
curl -H "Authorization: Bearer test-key" \
     -F "file=@data.xlsx" \
     -F "parseConfig={\"max_csv_rows\":5000}" \
     http://localhost:3457/v1/fileparse
```

### Response Format

Successful response:
```json
{
  "success": true,
  "markdown": "# Converted Content\n\nThis is the converted markdown...",
  "title": "Document Title"
}
```

Error response:
```json
{
  "success": false,
  "error": "Error message"
}
```

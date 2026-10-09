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
docker build -t void2md-api .
docker run -p 3457:3457 void2md-api
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


## void2md 和 file-parse 在 DOCX、PPTX、XLSX 三种文件类型的内部解析逻辑上存在以下主要差异：

1. 解析架构差异
void2md（单引擎架构）
纯 Rust 实现：直接在 rust/src/converters/ 目录下使用 Rust 代码解析 OOXML 格式
单一路径：每个文件类型只有唯一的解析路径，没有备选方案
直接转换：读取文件后直接转换为 Markdown，中间处理步骤较少
file-parse（多引擎架构）
混合实现：结合 Go + CGO + C++ 的多层架构
三级备选：
自研解析：通过 CGO 调用 liboffice_parse 库进行直接 OOXML 解析
LibreOffice 备份：当自研解析失败时，调用 LibreOffice 进行转换
MinerU API：支持委托给外部 MinerU 服务进行云端高级处理
复杂流水线：包含预处理、主解析、后处理等多个阶段
2. DOCX 解析逻辑差异
void2md
直接解析 word/document.xml 等核心 XML 文件
基础样式映射：标题→#、列表→-、表格→|等简单转换
不处理复杂元素：图表、公式、脚注等可能丢失或简化
file-parse
语义理解更强：能识别文档结构（章节、摘要、附录等）
智能处理：
自动检测和处理页眉页脚
识别并保留图表标题
支持印章识别（seal_rec 参数）
标题层级自动调整（relevel_titles 参数）
错误恢复：损坏的 DOCX 文件会尝试 LibreOffice 备份解析
3. PPTX 解析逻辑差异
void2md
按幻灯片顺序简单转换为 Markdown
基础文本提取，复杂布局信息丢失
图片和图表转换为简单的 Markdown 图片语法
file-parse
布局感知：保持幻灯片的布局结构信息
内容分类：
区分标题幻灯片、内容幻灯片、结束幻灯片
识别演讲者备注
处理动画和过渡效果的元数据
图像分析：支持 chart_analysis 参数进行图表内容分析
备份机制：复杂 PPTX 会启用 LibreOffice 进行更准确的转换
4. XLSX 解析逻辑差异
void2md
简单表格转换：将工作表转换为 Markdown 表格
基础单元格处理：忽略复杂格式和公式
单工作表处理：通常只处理第一个工作表
file-parse
多工作表支持：可以处理多个工作表并分别转换
公式处理：
保留原始公式或计算结果（可配置）
复杂公式会尝试 LibreOffice 计算
表格智能识别：
自动检测数据区域边界
识别合并单元格并正确处理
支持表格标题和说明文字的关联
性能优化：通过 maxCSVRows 参数限制大表格处理
5. 错误处理机制差异
void2md
简单错误处理：解析失败直接返回错误信息
无恢复机制：一旦失败就终止处理
错误信息简单：只有基本的错误描述
file-parse
分级错误诊断：使用 DiagnosisException 提供详细的错误分类
智能恢复：
自研解析失败 → 自动切换 LibreOffice
LibreOffice 失败 → 返回部分成功结果
网络问题 → 重试机制
详细错误码：提供具体的错误诊断码便于调试
6. 配置灵活性差异
void2md
硬编码行为：解析逻辑基本固定，配置选项极少
无运行时参数：无法通过请求参数调整解析行为
file-parse
丰富配置选项：
image_analysis：控制图片分析深度
chart_analysis：启用图表内容分析
keep_header_footer：控制页眉页脚处理
keep_appendix：控制附录处理
backend：指定使用哪个后端引擎
环境变量配置：大量可通过环境变量调整的参数
总结
void2md 采用简单直接的解析策略，适合处理结构规范、内容简单的文档，具有速度快、依赖少的优点。

file-parse 采用复杂稳健的解析策略，通过多引擎备选和智能处理，能够更好地处理复杂、损坏或格式特殊的文档，但代价是架构更复杂、依赖更多、处理速度相对较慢。

对于生产环境中的高质量文档处理需求，file-parse 的多引擎架构明显更可靠；而对于简单的文档转换场景，void2md 的轻量级实现更为合适。
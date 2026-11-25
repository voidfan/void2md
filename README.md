# void2md

`void2md` is a single Go service that turns heterogeneous content into Markdown and
exposes it through three API families:

- **File module** — document conversion (PDF, Office, EPUB, HTML, data files, media
  metadata), layout-aware Markdown chunking, and asynchronous website-to-Markdown
  crawling.
- **DBC module** — database access. A legacy LLM-assisted query pipeline under
  `/v1/dbc/*`, plus a modern, LLM-free, DBX-backed façade under `/v1/dbc/v2/*` that
  executes SQL supplied by the caller and exposes Redis, document, KV, Mongo, and
  HBase access through one command interface.
- **Structured module** — a Go reimplementation of the Python `structured` data-source
  service: metadata extraction, raw SQL execution, SQL formatting, natural-language
  querying, and DuckDB-backed Excel/S3/Mongo analysis.

The conversion and tokenization core is written in Rust and linked through cgo. The
HTTP layer is Go with Gin. Legacy Microsoft Office formats (`.doc`, `.xls`, `.ppt`,
`.wps`) are converted by headless LibreOffice inside the runtime image before the native
OOXML converters run.

---

## Contents

1. [Architecture](#1-architecture)
2. [File module](#2-file-module)
3. [DBC module](#3-dbc-module)
4. [Structured module](#4-structured-module)
5. [Configuration reference](#5-configuration-reference)
6. [Build and run](#6-build-and-run)
7. [API examples](#7-api-examples)
8. [Testing](#8-testing)
9. [Operational limits](#9-operational-limits)
10. [Security notes](#10-security-notes)
11. [Project layout](#11-project-layout)

---

## 1. Architecture

### 1.1 Process model

One Go binary (`void2md-api`) listens on port `3457` and mounts all three modules on a
shared Gin engine. Each module owns its configuration, handlers, and tests:

```text
                              ┌──────────────────────────────┐
                              │        Client / Gateway      │
                              │  Authorization: Bearer ...   │
                              └──────────────┬───────────────┘
                                             │
                     ┌───────────────────────▼───────────────────────┐
                     │              Gin engine (main.go)             │
                     │  /healthz  (no auth)                          │
                     │  authMiddleware: requires "Bearer <token>"    │
                     └───┬───────────────┬───────────────────┬───────┘
                         │               │                   │
        ┌────────────────▼──┐   ┌────────▼─────────┐   ┌─────▼──────────────────┐
        │  file module      │   │  dbc module      │   │  structured module     │
        │  /v1/fileparse    │   │  /v1/dbc/*       │   │  /v1/data_source/*     │
        │  /v1/pdfparse     │   │  /v1/dbc/v2/*    │   │                        │
        │  /v1/chunk        │   │                  │   │                        │
        │  /v1/htmlparse*   │   │                  │   │                        │
        └───┬──────────┬────┘   └───┬──────────┬───┘   └─────┬──────────────────┘
            │          │            │          │             │
            │ cgo      │ HTTP       │ HTTP     │ SQL         │ database/sql, cgo
            ▼          ▼            ▼          ▼             ▼
   libvoid2md.so   arbitrary    legacy LLM   DBX REST     MySQL / PostgreSQL /
   libtokenizer.a  HTTP crawl   pipeline     API          MSSQL / Oracle / SQLite /
   (Rust)          targets      (optional)   (t2y2/dbx)   StarRocks / DuckDB
            │
            ▼
   headless LibreOffice for legacy .doc/.xls/.ppt/.wps
```

### 1.2 Module lifecycle

Every module follows the same pattern:

1. `init()` loads module configuration once, at package import time.
2. `Mount(r *gin.Engine)` registers routes and adapts the module's native router to Gin.
3. Handlers use plain `net/http` signatures internally; Gin only provides routing,
   middleware, and process lifecycle.

The dbc module is fail-soft: if its configuration cannot be loaded, it logs a warning and
skips route registration instead of terminating the host service.

`GET /healthz` is registered before the auth middleware and returns the JSON string
`"ok"`; every other route requires `Authorization: Bearer <token>`. The process shuts
down gracefully on `SIGINT`/`SIGTERM` with a 30-second drain.

### 1.3 Design principles

- **Single deployable binary.** No Python runtime or external service is required for
  file parsing, chunking, crawling, or structured data-source access.
- **Rust for parsing/tokenization.** `file/rust` implements the converters; `file/tokenizer`
  implements the HuggingFace BPE tokenizer used for chunk budget accounting. Both are
  linked into the Go binary with cgo.
- **Compatibility by construction.** The file converters, chunker, and structured module
  are regression-tested against the reference `file-parse` / Python `structured` behavior,
  including byte-level output parity on the covered fixtures.
- **The connection file is the source of truth.** In dbc v2, DBX is only an execution
  copy; connection state survives DBX restarts and is synchronized lazily.
- **No LLM in the v2 path.** `/v1/dbc/v2/*` executes exactly the SQL or command supplied by
  the caller. The legacy `/v1/dbc/query` pipeline is retained for compatibility but is
  independent of v2.

---

## 2. File module

Routes are mounted under `/v1`:

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/v1/fileparse` | Convert an uploaded file to Markdown |
| `POST` | `/v1/pdfparse` | Alias of `/v1/fileparse` (file-parse compatibility) |
| `POST` | `/v1/chunk` | Split Markdown into layout-aware chunks |
| `POST` | `/v1/htmlparse` | Submit an asynchronous website crawl task |
| `GET` | `/v1/htmlparse/{taskId}` | Read crawl status and results |
| `DELETE` | `/v1/htmlparse/{taskId}` | Cancel a crawl task |

### 2.1 File conversion (`POST /v1/fileparse`)

**Request** — `multipart/form-data`:

| Field | Type | Required | Default | Meaning |
|---|---|---|---|---|
| `file` | file | yes | — | The uploaded content |
| `parseConfig` | JSON string | no | `{}` | Per-conversion options |
| `image_analysis` | bool | no | `true` | Embed extracted images as base64 data URIs |
| `chart_analysis` | bool | no | `false` | Analyze PPTX charts into textual descriptions |
| `seal_rec` | bool | no | `false` | Detect seals/stamps in DOCX |
| `relevel_titles` | bool | no | `true` | Re-rank DOCX heading levels |
| `keep_header_footer` | bool | no | `false` | Keep headers and footers in DOCX output |
| `keep_appendix` | bool | no | `true` | Keep appendix sections |

Form fields take precedence over the same keys in `parseConfig`. Boolean values accept
`true/false`, `1/0`, `yes/no`, `on/off`.

The maximum upload size is **32 MB** by default. Set
`VOID2MD_MAX_UPLOAD_SIZE_MB` to change it (the same variable also configures `/v1/chunk`,
whose default is 100 MB).

**Response** — always a JSON object:

```json
{
  "pages": 3,
  "markdown": "# Title\n\nConverted content...",
  "error": ""
}
```

`pages` is the number of pages or slides the converter recognized. Converters without a
page concept report `1`. On failure the HTTP status is non-2xx and `error` carries the
reason. The Rust core also extracts a document title internally, but the HTTP contract
matches `file-parse` and does not expose it.

**Implementation**

1. Gin receives the multipart upload and enforces the size limit.
2. The file bytes, MIME type, extension, and filename are passed to
   `rust.ConvertBufferToMarkdown`.
3. The Go binding calls the C ABI function
   `void2md_convert_buffer_with_config` exported by `libvoid2md.so`.
4. Rust builds a `StreamInfo`, walks the converter registry in priority order, and returns
   `{markdown, title, success, pages}`.
5. Go translates the C result back to Go strings, frees the C allocations, and writes the
   JSON response.
6. Conversion runs in a goroutine; if the client disconnects, the handler returns
   immediately instead of waiting for the result.

The C ABI is declared in [file/rust/src/ffi.rs](file/rust/src/ffi.rs) and consumed in
[file/rust/bindings.go](file/rust/bindings.go).

### 2.2 Supported formats

Converters are selected by extension first, then MIME type, then content sniffing. PDF is
also detected from the `%PDF-` magic bytes when no extension or MIME type is available.

| Group | Formats |
|---|---|
| Documents | PDF, DOCX, DOC, WPS, PPTX, PPT, XLSX, XLS, EPUB, IPYNB, Apple iWork |
| Web/markup | HTML, XML, RSS |
| Data | CSV, JSON, YAML |
| Text | Plain text, Markdown, reStructuredText, and other text-like extensions |
| Media metadata | Images (EXIF/PNG/JPEG metadata), audio (MP3/FLAC/etc. tags) |
| Remote content | GitHub pages, Wikipedia pages |
| Archives | ZIP archives, traversed with the same converter registry |

Converter registry order matters:

1. Format-specific converters, with legacy binary Word before DOCX (container sniffing)
   and legacy PPT/XLS before their OOXML equivalents.
2. ZIP, which recursively converts supported entries.
3. Generic XML and HTML converters.
4. Plain-text fallback, last.

### 2.3 Legacy Office and WPS files

Binary `.doc`, `.xls`, `.ppt`, and `.wps` files are not parsed natively. The Rust
converters shell out to headless LibreOffice, convert the input to its OOXML equivalent,
and then reuse the DOCX/XLSX/PPTX pipelines:

```text
.doc/.wps  ──LibreOffice──►  .docx  ──►  DOCX converter
.xls       ──LibreOffice──►  .xlsx  ──►  XLSX converter
.ppt       ──LibreOffice──►  .pptx  ──►  PPTX converter
```

The runtime image therefore installs `libreoffice-writer`, `libreoffice-draw`,
`libreoffice-calc`, and `libreoffice-impress`. Without the Calc and Impress filters,
`.xls` and `.ppt` conversion silently produces no output. The converter finds the binary
through `PATH` or `LIBREOFFICE_PATH` (set to `/usr/bin/libreoffice` in the image) and runs
with `SAL_USE_VCLPLUGIN=svp` so it stays headless and off X11.

### 2.4 Layout-aware chunking (`POST /v1/chunk`)

**Request**

```json
{
  "markdown": "# Document\n\n...",
  "chunk_sizes": {
    "text": 768,
    "figure": 768,
    "code": 1536,
    "table": 1536
  }
}
```

`chunk_sizes` is optional and merged over the server defaults. The request body limit is
100 MB by default (`VOID2MD_MAX_UPLOAD_SIZE_MB`).

**Response**

```json
{
  "chunks": [
    {
      "index": 0,
      "text": "...",
      "start_line": 1,
      "end_line": 24,
      "type": "text"
    }
  ],
  "count": 1
}
```

**Implementation**

1. Markdown is parsed into typed elements: title, text, figure, code, table.
2. Elements are grouped by layout rules, not by raw character count.
3. Token counts come from a HuggingFace BPE tokenizer compiled to the static library
   `libtokenizer.a`, the same implementation used by the reference `file-parse` service.
   Token counts are cached in an LRU cache keyed by content hash.
4. Each chunk type has its own token budget: 768 for text/figure, 1536 for code/table.
5. Oversized code blocks and tables are split recursively. HTML and Markdown tables keep
   their header rows and enclosing tags when split across chunks.
6. Post-processing normalizes chunk sizes: chunks above the maximum ratio are split,
   orphaned fragments are redistributed, and chunks below the minimum ratio are merged
   with a neighbor when the result stays under the maximum (0.15 / 1.5 by default).
   The `"\n\n\n\n\n"` separator is the structural marker the parser uses to delimit
   caption, figure, and table segments.

Because the tokenizer and chunking rules mirror `file-parse`, chunk boundaries are
regression-tested for parity with the reference service.

### 2.5 Website crawling (`/v1/htmlparse`)

**Submit** — `POST /v1/htmlparse`

```json
{
  "url": "https://docs.example.com/",
  "selector": ".docs-content",
  "maxPage": 200
}
```

| Field | Required | Meaning |
|---|---|---|
| `url` | yes | Entry URL; must be a valid `http`/`https` URL |
| `selector` | no | CSS selector scoping the content extracted from each page |
| `maxPage` | no | Page limit; omitted uses the server default, values above the hard cap are clamped |

The response is `{"taskId": "...", "status": "pending"}`. Submitting the same normalized
URL while a task is still alive returns the existing task ID instead of starting a second
crawl. When the maximum number of live tasks is reached, the service returns HTTP 429 with
a diagnostic code.

**Status** — `GET /v1/htmlparse/{taskId}`

```json
{
  "status": "success",
  "pages": 12,
  "markdown": "# Aggregated Markdown...",
  "items": [
    {"url": "https://docs.example.com/a", "title": "A", "markdown": "..."}
  ],
  "truncated": false,
  "error": ""
}
```

`status` is one of `pending`, `running`, `success`, `failed`, `cancelled`. `markdown` is
the concatenation of all accepted pages; `items` keeps per-page detail. `truncated` is set
when the page limit, per-page size limit, or task byte budget stopped the crawl early.

**Cancel** — `DELETE /v1/htmlparse/{taskId}` returns `204`. Cancelling a task that is
already terminal is a no-op.

**Implementation**

- Same-domain breadth-first crawl starting at the entry URL.
- Links are extracted from the whole DOM so navigation and pagination links are
  discovered, but content links are scheduled before header/footer/off-host links.
- Each page is fetched with a per-page timeout and size cap, parsed into a DOM, scoped by
  the selector, and converted to Markdown with the same Rust HTML pipeline used by file
  conversion.
- Byte-identical duplicate pages are dropped above a small threshold to avoid shell pages
  inflating the result.
- Tasks are in memory on the current node. Finished tasks are retained for a TTL (30
  minutes by default); terminal-task sweeping is lazy and piggybacks on submit/get calls.
- Progress is reported through the task snapshot; there is no streaming response.

---

## 3. DBC module

The dbc module exposes two distinct database APIs under the same prefix:

| Prefix | Purpose | LLM required |
|---|---|---|
| `/v1/dbc/*` | Legacy natural-language-to-SQL pipeline over one configured database | Yes |
| `/v1/dbc/v2/*` | LLM-free façade over the DBX REST API, one JSON connection file per connection | No |

New integrations should use `/v1/dbc/v2/*`. The legacy routes are kept for existing
callers and for the original single-database workflow.

### 3.1 Legacy endpoints

| Method | Path | Behavior |
|---|---|---|
| `POST` | `/v1/dbc/query` | Natural-language question → schema context → LLM plan/SQL → validation → execution |
| `GET` | `/v1/dbc/connections` | Returns the single database configured in `dbc/config/config.json` |
| `GET` | `/v1/dbc/schema?tables=a,b` | Returns the generated schema-context text |
| `GET` | `/v1/dbc/config/llm` | Reads the current LLM endpoint/model configuration |
| `PUT` | `/v1/dbc/config/llm` | Updates endpoint/model/API key at runtime |
| `POST` | `/v1/dbc/config/llm/reset` | Restores the startup LLM configuration |

The legacy pipeline uses the configured `database` connection directly (not DBX) and the
`llm` section of the dbc configuration. It also applies the legacy `security` validator
(allowed operations, row cap, query timeout). API keys are never echoed back in full.

### 3.2 The v2 model: connection files + DBX

```text
┌─────────────────────────┐        ┌──────────────────────────┐
│ dbc/config/             │        │  DBX REST API            │
│ dbx-<id>-connection.json│───────►│  /api/connection/save    │
│ (source of truth)       │  sync  │  /api/query/execute      │
└───────────┬─────────────┘        │  /api/schema/*           │
            │                      │  /api/redis/*            │
            │  read on demand      │  /api/document-store/*   │
            ▼                      └────────────▲─────────────┘
     connstore.Store                           │
            │                                  │
            ▼                                  │
       dbcsync.Syncer ──────── HTTP ───────────┘
            │
            ▼
     /v1/dbc/v2 handlers
```

The design separates three concerns:

- **`connstore`** owns the on-disk connection files. Each file is the authoritative
  record, including the password.
- **`dbcsync`** projects connection files into DBX. It caches a fingerprint of the last
  successful push, coalesces concurrent syncs for the same ID, and retries once after a
  forced resync if DBX says the connection disappeared.
- **`dbx.HTTPClient`** implements the DBX REST contract with per-attempt timeouts, a
  concurrency semaphore, retries for idempotent calls, and a circuit breaker.

#### Connection files

Files live in `dbc/config/` by default (`DBC_CONNECTIONS_DIR` overrides the directory):

```text
dbc/config/dbx-<id>-connection.json
```

- `<id>` must match `^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`.
- Files are written atomically with mode `0600`.
- Reads are capped at 1 MiB; invalid filenames, symlinks, and undecodable files are
  skipped with a warning.
- Files in DBX receive the `dbc-` ID prefix, so DBC-managed connections cannot collide
  with connections created by hand in DBX.

Example file (values are placeholders):

```json
{
  "name": "pg-main",
  "db_type": "postgres",
  "host": "127.0.0.1",
  "port": 5432,
  "username": "app",
  "password": "replace-me",
  "database": "app",
  "ssl": false,
  "driver_profile": null,
  "url_params": {},
  "external_config": {},
  "connect_timeout_secs": 10,
  "query_timeout_secs": 60,
  "save_password": true
}
```

Passwords are **write-only** through the API: they are accepted on create/update and never
returned. List/get responses report `has_password` instead.

#### Synchronization rules

- `Ensure(id)` reads the file and pushes it to DBX only when the fingerprint changed, the
  cached sync expired, or the previous attempt failed.
- Concurrent `Ensure`/`Sync` calls for the same ID join a single in-flight DBX request
  when they carry the same fingerprint.
- If DBX reports the connection is missing, the caller invalidates the cache, resyncs
  once, and retries the operation.
- Connection updates and deletes invalidate both the sync cache and the metadata cache.
- `sync_ttl_seconds` (default 600) bounds how long a successful projection is trusted.

### 3.3 `db_type` registry

`db_type` is normalized before use: aliases such as `postgresql`/`pg` → `postgres`,
`mssql`/`sql_server`/`tsql` → `sqlserver`, `mongo` → `mongodb`, `mariadb` → `mysql`,
`sqlite3` → `sqlite`, `dm` → `dameng`, and `kingbasees` → `kingbase` are accepted.

The canonical registry contains about 90 database types. They fall into three tiers:

| Tier | Behavior |
|---|---|
| First-class SQL dialect | Known default port, schema mode, SQL formatting dialect, server-version query |
| Non-SQL backend | Saved and connection-tested; SQL is rejected; HTTP/Redis/Mongo/KV-specific endpoints apply |
| Registry-only type | Accepted as a canonical `db_type`; no defaults, so the caller must provide a port |

Common first-class dialects:

| `db_type` | Default port | Default schema |
|---|---:|---|
| `mysql` | 3306 | database |
| `postgres` | 5432 | `public` |
| `sqlserver` | 1433 | `dbo` |
| `oracle` | 1521 | uppercased username |
| `sqlite` / `duckdb` | file path | none |
| `clickhouse` | 8123 | none |
| `starrocks` / `doris` | 9030 | database |
| `snowflake` / `bigquery` | 443 | none |
| `redshift` | 5439 | `public` |
| `dameng` | 5236 | none |
| `kingbase` / `uxdb` / `vastbase` / `gaussdb` / `opengauss` | 5432 | `public` |
| `highgo` | 5866 | `public` |
| `tdengine` | 6041 | none |

The full list, including query capabilities, is available at runtime from
`GET /v1/dbc/v2/db_types`.

Query families:

| Family | Types |
|---|---|
| `sql` | Every dialect not explicitly marked non-SQL |
| `redis` | `redis` |
| `mongo` | `mongodb` |
| `document` | Elasticsearch, Easysearch, Meilisearch, Solr, CouchDB, DynamoDB |
| `kv` | etcd, ZooKeeper, Consul |
| `hbase` | HBase |
| metadata-only | Vector stores (Qdrant, ChromaDB, Milvus, Weaviate), MQ/MQTT, Nacos, Salesforce, and other non-SQL types with no DBX query route (Neo4j, Cassandra, Nebula, InfluxDB, IoTDB, VictoriaMetrics, ManticoreSearch) |

Some drivers accept a URL instead of a bare host (`clickhouse`, Elasticsearch family,
Vector stores, InfluxDB, etc.). DBC normalizes a bare hostname to `http://...` for those
types and keeps the path for context URLs such as Solr's `/solr`.

### 3.4 v2 endpoints

All v2 endpoints are JSON and require the global Bearer header. Request bodies are capped
at **1 MiB**. Paths in the table are relative to the `/v1/dbc` mount, so
`GET /v2/connections` is served at `GET /v1/dbc/v2/connections`.

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/v2/connections` | List connections (redacted) |
| `POST` | `/v2/connections` | Create a connection |
| `GET` | `/v2/connections/{id}` | Read one connection |
| `PUT` | `/v2/connections/{id}` | Update a connection |
| `DELETE` | `/v2/connections/{id}` | Delete a connection and its DBX projection |
| `POST` | `/v2/conn_test` | Validate a saved or inline connection through DBX |
| `GET` | `/v2/health` | dbc/DBX health snapshot |
| `POST` | `/v2/sql_query` | Execute caller-supplied SQL |
| `POST` | `/v2/sql_format` | Format SQL for a connection's dialect |
| `POST` | `/v2/get_server_version` | Query and parse the server version |
| `POST` | `/v2/get_metadata` | Structured database metadata |
| `POST` | `/v2/list_tables` | List tables/views |
| `POST` | `/v2/describe_table` | Describe one table |
| `POST` | `/v2/redis_command` | Execute a Redis command through DBX |
| `POST` | `/v2/redis_databases` | List logical Redis databases |
| `POST` | `/v2/redis_scan_keys` | SCAN keys with a cursor |
| `POST` | `/v2/redis_get_value` | Read one typed Redis value |
| `GET` | `/v2/db_types` | Canonical `db_type` capabilities |
| `POST` | `/v2/capabilities` | Capabilities of one connection |
| `POST` | `/v2/list_objects` | Generic catalog for any queryable family |
| `POST` | `/v2/describe_object` | Describe one catalog object |
| `POST` | `/v2/query_command` | Unified read-only command entry point |

#### Connection requests

`POST` accepts an optional client-chosen `id`; when omitted, an ID is derived from the
name. Supported fields mirror the connection file: `name`, `db_type`, `host`, `port`,
`username`, `password`, `database`, `ssl`, `ssh`, `advanced`, `driver_profile`,
`url_params`, `external_config`, timeout fields, `save_password`, and `color`.

`connection_id` in every other endpoint is the DBC ID (without the `dbc-` prefix).
`GET /v2/connections` accepts a `q` filter matched against ID, name, and host.

#### `conn_test`

`conn_test` accepts either a saved connection or an inline configuration:

```json
{"connection_id": "pg-main"}
{"connection": {"name": "probe", "db_type": "mysql", "host": "127.0.0.1", "port": 3306, "username": "app", "password": "secret"}}
```

An inline connection is validated but never written to disk or DBX. The response is
`{"ok": true}` on success. When DBX executed the test and reported the connection
unusable, the HTTP status is still `200` with `{"ok": false, "error": "dbx_error",
"detail": "..."}` (secrets redacted); only an unreachable DBX yields `502
dbx_unavailable`.

#### `sql_query`

```json
{
  "connection_id": "pg-main",
  "sql": "SELECT id, name FROM users LIMIT 10",
  "database": "app",
  "schema": "public",
  "max_rows": 1000,
  "timeout_seconds": 30
}
```

Response:

```json
{
  "cols": ["id", "name"],
  "column_types": ["int8", "text"],
  "data": [[1, "alice"]],
  "truncated": false,
  "has_more": false,
  "affected_rows": 0,
  "execution_time_ms": 4
}
```

- `database` and `schema` default from the connection and dialect; an empty schema is
  passed through so DBX/database defaults apply.
- `max_rows` can only lower the server cap (`max_rows`, default 100000), never raise it.
  Rows past the effective limit are dropped and `truncated` is set.
- `timeout_seconds` must not exceed the configured query timeout (default 300).
- Non-SQL connections are rejected with `400 unsupported`.
- SQL is executed exactly as supplied; the v2 path performs no rewriting and no LLM
  generation.

#### Metadata

`get_metadata` returns the same `DatabaseMetadata` model as the structured module: database
name, tables, ordered columns, types, nullability, defaults, primary keys, foreign keys,
comments, and optional value examples.

Implementation details:

- Tables are paged from DBX (`schema_page_size`, default 500) up to `schema_max_tables`
  (default 10000).
- Columns for all tables are fetched in parallel with a concurrency cap of 8.
- Foreign keys are fetched only when requested and when the table count is at most
  `metadata_fk_max_tables` (default 200); otherwise an empty FK list is returned.
- Results are cached in process for `metadata_cache_seconds` (default 15), keyed by
  connection + database + schema + include-flags. Updating or deleting a connection
  invalidates its entries.
- `list_tables` supports explicit `limit`/`offset`; without a limit it performs full
  pagination and filters views according to `include_views`.
- `describe_table` returns `{table: {columns, primary_keys, foreign_keys}}`.

#### `sql_format`

Accepts either `connection_id` (preferred, dialect from the stored connection) or
`db_type`. Formatting uses the local sqlglot-compatible Go port, so no DBX call is made.
If the dialect has no formatting support, the response is the input SQL with
`"formatted": false`.

### 3.5 Redis

Redis is a first-class family. All Redis endpoints are `POST` + JSON and accept an
optional integer `db`; when omitted, the connection's configured database is used, then
`0`.

| Endpoint | Body | Response |
|---|---|---|
| `/v2/redis_command` | `{"connection_id","db"?,"command"}` | `{"command","safety","value"}` |
| `/v2/redis_databases` | `{"connection_id"}` | `{"databases":[{"db","keys"?}]}` |
| `/v2/redis_scan_keys` | `{"connection_id","db"?,"cursor","pattern","count"}` | `{"cursor","keys":[{"key_display","key_raw","key_type","ttl"}],"total_keys"?}` |
| `/v2/redis_get_value` | `{"connection_id","db"?,"key"\|"key_raw","preview_limit_bytes"?}` | `{"key_display","key_raw","ttl","redis_type","data"}` |

Rules:

- `redis_scan_keys` defaults `pattern` to `*` and `count` to 100; count must be between
  1 and 1000.
- `redis_get_value` accepts exactly one of `key` (plain UTF-8) or `key_raw` (base64).
  The handler base64-encodes plain keys before calling DBX.
- DBX blocks unsafe commands such as `KEYS`. Use the scan endpoint instead.
- DBX's `safety` classification is preserved in the response; the service does not add a
  bypass flag.
- A missing key is not an error: DBX returns `200` with `redis_type: "none"`.

### 3.6 Generic catalog and command surface

For the database-backed knowledge base, `list_objects`, `describe_object`, and
`query_command` provide one shape across families.

- `list_objects` returns the catalog (`tables` / `collections` / `keys` / `indexes`),
  optional field lists, optional samples, and a `truncated` flag.
- `describe_object` returns one object with fields, primary key, comments, and optional
  samples.
- `query_command` runs one read-only command. The `family` may be omitted and is inferred
  from the connection; if supplied, it must match.

The `command` object is family-specific. Every non-SQL command requires a `kind`
discriminator (`query_command` rejects a missing or mismatched kind):

| Family | `kind` values | Required fields | Optional fields |
|---|---|---|---|
| `sql` | — | `sql` | — |
| `redis` | — | `command` | `db` |
| `mongo` | `find`, `aggregate`, `count`, `distinct` | `collection` (+ `pipeline` for `aggregate`, `field` for `distinct`) | `filter`, `projection`, `sort`, `limit` |
| `document` | `search`, `count` | `index` (or `collection`) | `query` / `q`, `filter`, `projection`, `sort`, `limit` |
| `kv` | `get`, `list` | `key` for `get` | `prefix`, `limit` |
| `hbase` | `scan`, `get` | `table` (+ `row_key` for `get`) | `row_key_prefix`, `limit` |

```json
{"connection_id": "redis-main", "command": {"command": "GET model:index", "db": 0}}
{"connection_id": "mongo-main", "command": {"kind": "find", "collection": "users", "filter": {"active": true}, "limit": 20}}
{"connection_id": "pg-main", "command": {"sql": "SELECT * FROM users LIMIT 5"}}
```

For `query_command`, `max_rows` can only lower the configured cap (`command_max_rows`,
default 200) — it cannot raise it — and `timeout_seconds` must not exceed the connection's
query timeout.

The response normalizes all families into one shape:
`{family, kind, command, cols, column_types, data, row_count, truncated, has_more,
execution_time_ms, value}`. `value` carries scalar results such as a KV `get`, while
tabular results populate `cols`/`data`.

Every command passes a **read-only guard** before DBX is called:

- SQL must be a single statement starting with `SELECT`, `WITH`, `SHOW`, `DESC`,
  `DESCRIBE`, or `EXPLAIN`, and must not contain write/DDL/transaction keywords. The
  forbidden list is deliberately fail-closed: a query that merely mentions a name such
  as `comment` can be rejected rather than risking a write.
- Redis uses an explicit read-command allowlist (`KEYS` is refused with a hint to use
  `SCAN MATCH`). Mongo allows only `find`/`aggregate`/`count`/`distinct` and rejects
  `$out`, `$merge`, `$where`, `$function`, and `$accumulator`. Document stores allow
  `search`/`count`, KV stores `get`/`list`, and HBase `scan`/`get`.
- The guard blanks comments, string literals, quoted identifiers, and PostgreSQL
  dollar-quoted bodies before scanning, so keywords inside literals do not trigger false
  positives.
- DBX applies its own safety checks as a second line of defense.

### 3.7 Errors

All v2 errors use a stable envelope:

```json
{
  "error": "invalid_request",
  "message": "connection_id is required"
}
```

`detail` is omitted when empty; otherwise it carries redacted upstream text.

| HTTP | `error` | Meaning |
|---:|---|---|
| 400 | `invalid_request` | Invalid body, missing field, bad method usage |
| 400 | `unsupported` | Unsupported `db_type`, SQL on a non-SQL connection, unqueryable family |
| 404 | `not_found` | Connection does not exist |
| 409 | `conflict` | Connection ID already exists |
| 405 | `invalid_request` | Wrong HTTP method; `Allow` header is set |
| 502 | `dbx_error` | DBX rejected the request (business error or upstream failure) |
| 502 | `dbx_unavailable` | Transport failure or open circuit breaker |
| 500 | `internal` | Unexpected local failure |

DBX returns HTTP 500 for many ordinary business failures, so the breaker counts only
transport errors, not non-2xx responses.

### 3.8 Reliability behavior

The DBX client applies:

- control timeout: `timeout_seconds` (default 30 s);
- query timeout: `query_timeout_seconds` (default 300 s);
- concurrency semaphore: `max_concurrent_requests` (default 32);
- retries: up to 2 for idempotent calls, with exponential backoff;
- circuit breaker: opens after 3 consecutive transport failures for 5 seconds, then probes
  with a single request;
- gzip and keep-alive connection reuse.

Reads such as metadata, connections, and schema calls are idempotent and retried.
`sql_query` and Redis commands are never retried, because a retry could execute a
non-idempotent command twice.

`GET /v2/health` always returns HTTP 200 so probes can distinguish "dbc is up but DBX is
down" from "dbc is down":

```json
{
  "ok": true,
  "dbx_reachable": true,
  "dbx_url": "http://dbx:4224",
  "connections": 3,
  "time": "2026-11-24T12:00:00Z"
}
```

---

## 4. Structured module

The structured module is a Go port of the Python `structured` service. It intentionally
reproduces FastAPI/starlette behavior down to error messages, status codes, validation
payloads, and CORS handling, because existing clients depend on those details.

All routes live under `/v1/data_source/`.

### 4.1 Compatibility contract

- Exact URL paths and method checks.
- Pydantic-style `422` validation errors, including field location, message text, and
  union-tag errors.
- Trailing-slash behavior: a known path with the wrong trailing slash returns `307` with
  an absolute `Location`.
- Unknown paths return JSON `404 {"detail": "Not Found"}`.
- Wrong methods return `405` with `Allow` and the FastAPI detail body.
- CORS mirrors the request `Origin` (allow-all-origins with credentials), answers
  preflight with allowed methods and a 600 s max age, and mirrors requested headers.
- JSON responses do not HTML-escape `<`, `>`, or `&`.

### 4.2 Endpoints

| Method | Path | Request | Response |
|---|---|---|---|
| `POST` | `/conn_test` | `DataSourceConfig` | `{"msg":"ok"}` or `{"msg":"fail"}` |
| `POST` | `/get_metadata` | `DataSourceConfig` | `DatabaseMetadata` |
| `POST` | `/get_metadata_with_value_examples` | `DataSourceConfig` | `DatabaseMetadata` with 3 examples per column, 200 chars each |
| `POST` | `/get_server_version` | `DataSourceConfig` | `{"major","minor","patch"?}` |
| `POST` | `/sql_query` | `{"source_config","sql"}` | `{"cols":[],"data":[[]]}` |
| `POST` | `/sql_format` | `{"source_config","sql"}` | `{"sql":"..."}` |
| `POST` | `/query_by_nl` | see below | `{"answer","sql","sql_res","input_tokens","output_tokens"}` |
| `POST` | `/excel_upload` | multipart `file` + `source_config` | `{"msg","rows","cols","filename","file_id"?}` |
| `POST` | `/excel_delete` | multipart `filename` + `source_config` | `{"msg","filename"}` |

`DataSourceConfig` is a discriminated union on `type`:

```json
{
  "type": "postgres",
  "host": "127.0.0.1",
  "port": 5432,
  "username": "app",
  "password": "secret",
  "db_name": "app",
  "ns_name": "public",
  "conn_pool_size": 5
}
```

Supported tags: `sqlite`, `mssql`, `mysql`, `maria`, `starrocks`, `oracle`, `postgres`,
`duckdb`. Common fields are `host`, `port`, `username`, `password`, `db_name`, `ns_name`
(schema), and driver-specific options such as `encrypt` or `conn_pool_size`. SQLite uses
`db_path`.

Two compatibility quirks are inherited from the Python models and preserved deliberately:

- Every branch's declared port default is `3306`, including `postgres`, `oracle`, and
  `mssql`; the driver default is never inferred from the type. Always send `port`
  explicitly.
- `ns_name` is accepted under its pydantic alias `schema`; validation errors report the
  input key name.

### 4.3 Data source layer

`structured/pkg/ds` defines a driver-agnostic interface:

```go
type DataSource interface {
    Dialect() string
    StringTypes() map[string]bool
    JSONArrayAggFunc() string
    ConnTest(ctx) (bool, error)
    ServerVersion(ctx) (*metadata.DBServerVersion, error)
    Metadata(ctx) (*metadata.DatabaseMetadata, error)
    MetadataWithValueExamples(ctx, valueNum, valueStrMaxLength int) (*metadata.DatabaseMetadata, error)
    ExecuteRawSQL(ctx, sql string) ([]string, [][]any, *metadata.SQLException)
    AggDistinctValuesSQL(table *metadata.DBTable, limit int) (string, error)
}
```

Implementations use `database/sql` with the appropriate driver:

| Type | Driver |
|---|---|
| MySQL / MariaDB / StarRocks | `go-sql-driver/mysql` |
| PostgreSQL | `pgx/v5` stdlib (the legacy dbc client uses `lib/pq`) |
| SQL Server | `microsoft/go-mssqldb` |
| Oracle | `sijms/go-ora` |
| SQLite | `modernc.org/sqlite` (pure Go); the legacy dbc client uses `mattn/go-sqlite3` |
| DuckDB | `duckdb-go/v2` (cgo) |

Metadata is assembled from per-dialect SQL templates and normalized into a stable model:

- `DatabaseMetadata{name, comment, tables[]}`
- `DBTable{name, ns_name, comment, columns{}, primary_keys[], foreign_keys[], enabled, num_rows}`
- `TableColumn{name, type, comment, auto_increment, nullable, default, examples[], enabled, value_index}`

Column insertion order is preserved explicitly, because Go maps do not preserve order and
the Python service serializes dictionaries in insertion order.

### 4.4 SQL formatting

`structured/pkg/sqlg` is a local, sqlglot-compatible SQL parser/formatter. It supports
parsing, AST normalization, qualification, optimization, pretty-printing, and dialect
rendering.

`/sql_format` formats with `pretty`, 4-space indent, 4-space padding, and blank lines
between statements, matching `sqlglot.transpile`. Dialects unknown to the formatter
(`maria`, `mssql`) return the input unchanged. Parse errors propagate as HTTP 500, matching
the Python service.

The same parser underpins natural-language query planning and SQL validation.

### 4.5 Natural-language query (`/query_by_nl`)

Request:

```json
{
  "source_config": {"type": "postgres", "...": "..."},
  "query": "How many orders were placed last month?",
  "retrieved_metadata": null,
  "generate_sql_llm": {
    "provider": "openai",
    "model": "gpt-4o-mini",
    "api_key": "sk-...",
    "base_url": "https://api.openai.com/v1",
    "temperature": 0
  },
  "evaluate_sql_llm": null,
  "evidence": "",
  "result_num_limit": 100
}
```

Behavior:

1. Validate and open the data source.
2. Run the connection test and fetch the server version.
3. Build an agent with the generate LLM and, optionally, an evaluate LLM.
4. Run the agent loop: schema retrieval, SQL generation, validation, execution, and
   evaluation/repair.
5. Return the answer, final SQL, result rows, and aggregate token usage.

LLM configuration is supplied per request; the service does not store it. The
OpenAI-compatible chat client supports a custom `base_url` and `extra_body`.
`schema_format` is accepted for compatibility but ignored: like the Python service, the
agent always uses its default `m_schema` format. `evidence` defaults to `""` and
`result_num_limit` to `100`.

### 4.6 DuckDB and file-backed analysis

The DuckDB branch carries a nested, independently discriminated `store` object:

```json
{
  "type": "duckdb",
  "store": {"type": "s3", "kid": "dataset", "host": "minio:9000", "bucket": "private"}
}
```

| Store | Fields | Source |
|---|---|---|
| `local` | `dir_path` | Files on the local filesystem |
| `s3` | `kid`, `host`, `port`, `access_key`, `secret_key`, `bucket`, `region`, `use_ssl` | S3-compatible object storage |
| `mongo` | `uri`, `bucket`, `kid`, `file_ids` | MongoDB GridFS (bucket default `dataset`) |

When the S3 fields are omitted, the service falls back to environment variables:
`STORAGE_S3_ENDPOINT`, `STORAGE_ACCESS_KEY_ID`, `STORAGE_SECRET_ACCESS_KEY`,
`STORAGE_PRIVATE_BUCKET`. The Mongo store likewise reads `MONGODB_URI` and
`MONGODB_BUCKET` first and falls back to the request fields.

Supported analysis files are CSV, XLSX, JSON, and Parquet. The DuckDB engine loads the
`httpfs` and `excel` extensions. The runtime image bundles them under
`/root/.duckdb/extensions/v2.0.0/linux_<arch>/`, so no network access is required at
query time. Legacy spreadsheet extensions that the Python service reads through
fastexcel/xlrd (`.xls`, `.xlsb`, `.ods`, `.xla`, `.xlam`) have no Go reader and are
reported as unsupported instead of being silently skipped.

Values returned by DuckDB are normalized to match Python string representations for
timestamps, dates, times, decimals, intervals, UUIDs, and binary data.

### 4.7 Excel upload and delete

`excel_upload` accepts `multipart/form-data` with:

- `file` — `.xlsx` or `.csv` (`.xls` is intentionally unsupported because the Go stack has
  no XLS reader; extension matching is case-sensitive, as in the Python service);
- `source_config` — a JSON string with a flat store config, not the `DataSourceConfig`
  union: `{"type":"s3", ...}` or `{"type":"mongo","uri":...,"bucket":...,"kid":...}`.

The default maximum file size is 50 MB (`MAX_EXCEL_FILE_SIZE`, in MB). The service loads
the file into DuckDB, counts rows and columns, stores the original file through the store,
and returns `{"msg":"success","rows":N,"cols":M,"filename":"...","file_id":"..."}`
(`file_id` only when the store returns one).

`excel_delete` removes a stored file by filename and returns
`{"msg":"success","filename":"..."}`. Both endpoints require the filename to have exactly
one dot (`name.ext`); anything else reproduces the Python service's uncaught unpacking
error as HTTP 500. The write path supports only `s3` and `mongo` stores; local-store writes
are not implemented.

---

## 5. Configuration reference

Configuration is environment-first; module JSON files provide defaults and
module-specific settings.

### 5.1 Common runtime

| Variable | Default | Meaning |
|---|---|---|
| `VOID2MD_HOST` | `0.0.0.0` | Listen address |
| `VOID2MD_PORT` | `3457` | Listen port |
| `VOID2MD_LOG_LEVEL` | `INFO` | `DEBUG` enables Gin debug mode |
| `VOID2MD_LOG_FORMAT` | `plain` | Log format |

### 5.2 File module

| Variable | Default | Meaning |
|---|---|---|
| `VOID2MD_MAX_UPLOAD_SIZE_MB` | 32 (fileparse) / 100 (chunk) | Upload/body limit |
| `VOID2MD_CHUNKING_TOKENIZER_PATH` | `/app/data/qwen3-embedding-tokenizer/tokenizer.json` | BPE tokenizer file |
| `VOID2MD_CHUNKING_TEXT_SIZE` | 768 | Text/figure token budget |
| `VOID2MD_CHUNKING_FIGURE_SIZE` | 768 | Figure token budget |
| `VOID2MD_CHUNKING_CODE_SIZE` | 1536 | Code token budget |
| `VOID2MD_CHUNKING_TABLE_SIZE` | 1536 | Table token budget |
| `VOID2MD_CHUNKING_SEPARATOR` | five newlines | Chunk separator |
| `VOID2MD_CHUNKING_MIN_RATIO` | 0.15 | Minimum chunk ratio |
| `VOID2MD_CHUNKING_MAX_RATIO` | 1.5 | Maximum chunk ratio |

HTML crawl:

| Variable | Default | Meaning |
|---|---|---|
| `VOID2MD_HTML_CRAWL_CONCURRENCY` | 4 | Concurrent page fetches per task |
| `VOID2MD_HTML_MAX_PAGES` | 200 | Default page limit |
| `VOID2MD_HTML_HARD_MAX_PAGES` | 2000 | Hard cap for `maxPage` |
| `VOID2MD_HTML_PAGE_TIMEOUT` | 30 | Per-page timeout (seconds) |
| `VOID2MD_HTML_TASK_TIMEOUT` | 300 | Whole-task budget (seconds) |
| `VOID2MD_HTML_MAX_HTML_SIZE_MB` | 10 | Per-page HTML size cap |
| `VOID2MD_HTML_MAX_TASK_BYTES_MB` | 200 | Cumulative Markdown cap |
| `VOID2MD_HTML_USER_AGENT` | `void2md/1.0` | Crawl User-Agent |
| `VOID2MD_HTML_TASK_TTL_SECONDS` | 1800 | Finished-task retention |
| `VOID2MD_HTML_MAX_TASKS` | 20 | Maximum live crawl tasks |

### 5.3 DBC module

The dbc configuration file defaults to `dbc/config/config.json` and can be relocated with
`DBC_CONFIG`.

The `dbx` section supports:

| Key | Env override | Default | Meaning |
|---|---|---:|---|
| `api_url` | `DBX_API_URL` | `http://localhost:4224` | DBX REST base URL |
| `connections_dir` | `DBC_CONNECTIONS_DIR` | `dbc/config` | Connection-file directory |
| `timeout_seconds` | `DBX_TIMEOUT_SECONDS` | 30 | Control-plane timeout |
| `query_timeout_seconds` | `DBX_QUERY_TIMEOUT_SECONDS` | 300 | Query timeout |
| `sync_ttl_seconds` | — | 600 | Trusted sync cache TTL |
| `max_concurrent_requests` | — | 32 | DBX concurrency cap |
| `metadata_cache_seconds` | — | 15 | Metadata cache TTL |
| `metadata_fk_max_tables` | — | 200 | FK fetch threshold |
| `schema_page_size` | — | 500 | Schema pagination size |
| `schema_max_tables` | — | 10000 | Schema pagination cap |
| `max_rows` | — | 100000 | SQL result cap |
| `command_max_rows` | — | 200 (max 1000) | `query_command` row cap |
| `metadata_max_objects` | — | 1000 | `list_objects` cap |
| `metadata_sample_limit` | — | 0 (max 20) | `describe_object` sample cap |
| `command_max_bytes` | — | 65536 | Serialized command size cap |

If `api_url` is empty, it is derived from `mcp_url` by stripping `/mcp`, then falls back
to `http://localhost:4224`.

The legacy `database`, `llm`, `security`, and `agentic_workflow` sections configure the
legacy `/v1/dbc/*` pipeline. They are independent of v2.

### 5.4 Structured module

| Variable | Default | Meaning |
|---|---|---|
| `DATIVE_DATABASE_URL` | empty | Mirrors the Python setting |
| `DATIVE_REDIS_URL` | empty | Mirrors the Python setting |
| `DATIVE_LOG_LEVEL` | `INFO` | Structured log level |
| `DATIVE_LOG_FORMAT` | `plain` | Structured log format |
| `MAX_EXCEL_FILE_SIZE` | 50 (MB) | Excel upload limit |
| `STORAGE_S3_ENDPOINT` | empty | S3 endpoint fallback |
| `STORAGE_ACCESS_KEY_ID` | empty | S3 access key fallback |
| `STORAGE_SECRET_ACCESS_KEY` | empty | S3 secret key fallback |
| `STORAGE_PRIVATE_BUCKET` | empty | S3 bucket fallback |
| `MONGODB_URI` | empty | Mongo URI override for the Excel/GridFS path |
| `MONGODB_BUCKET` | empty | Mongo GridFS bucket override |

Settings are read from the environment, then from a `.env` file in the working directory,
then from defaults. Matching is case-insensitive, and a key present with an empty value
wins over the default (pydantic-settings behavior).

---

## 6. Build and run

### 6.1 Prerequisites

- Go **1.26.10** (matches `go.mod`)
- Rust **1.93** with Cargo
- A C toolchain (`gcc`/`clang`) with cgo enabled
- LibreOffice (runtime only, for legacy Office formats)
- DuckDB extensions `httpfs` and `excel` for the structured S3/Excel paths

### 6.2 Local build

Build the Rust shared libraries first:

```bash
cd file/rust
cargo build --release
cd ../tokenizer
cargo build --release
```

Place the artifacts where the Go linker can find them:

```bash
cp file/rust/target/release/libvoid2md.so /usr/local/lib/
cp file/tokenizer/target/release/libtokenizer.a /usr/local/lib/
```

Build the Go binary:

```bash
export CGO_ENABLED=1
export CGO_LDFLAGS="-L/usr/local/lib -lvoid2md -ltokenizer -lm"
export LD_LIBRARY_PATH="/usr/local/lib"
go build -trimpath -buildvcs=false -o void2md-api .
```

Run it:

```bash
./void2md-api
```

The default configuration expects `dbc/config/config.json` relative to the working
directory. Set `DBC_CONFIG` if the file lives elsewhere. The chunker's default tokenizer
path is the container path `/app/data/qwen3-embedding-tokenizer/tokenizer.json`; for a
local run, point `VOID2MD_CHUNKING_TOKENIZER_PATH` at
`file/data/qwen3-embedding-tokenizer/tokenizer.json`.

### 6.3 Docker build

The multi-stage [Dockerfile](Dockerfile) builds:

1. **rust-builder** — compiles `libvoid2md.so` and `libtokenizer.a`, using the rsproxy.cn
   crates mirror and BuildKit cache mounts.
2. **go-builder** — downloads Go modules through `goproxy.cn` and compiles the binary with
   cgo, linking both Rust artifacts.
3. **runtime** — Debian slim with CA certificates, LibreOffice writer/draw/calc/impress,
   Noto CJK fonts, the Go binary, `file/data`, `dbc/config`, and the DuckDB extensions.

```bash
docker build -t void2md:latest .
docker run --rm -p 3457:3457 void2md:latest
```

Build arguments:

| Argument | Default | Purpose |
|---|---|---|
| `APT_MIRROR` | `mirrors.aliyun.com` | Debian package mirror |
| `RUST_IMAGE` | `rust:1.93` | Rust builder base |
| `GO_IMAGE` | `golang:1.26.10` | Go builder base |
| `RUNTIME_IMAGE` | `debian:stable-slim` | Runtime base |

The runtime image is large (about 360 MB compressed, about 1.3 GB on disk in Docker
Desktop) because it bundles LibreOffice, CJK fonts, the statically linked Go binary, and
DuckDB extensions. LibreOffice is required for `.doc`/`.xls`/`.ppt`/`.wps` support.

### 6.4 Docker Compose

[docker-compose.yml](docker-compose.yml) starts both services:

```bash
docker compose up -d --build
```

Services:

- **void2md** — port `3457`, mounts `./dbc/config` read-write so runtime connection files
  survive container recreation, and points `DBX_API_URL` at the compose service name.
- **dbx** — port `4224`, persistent `dbx-data` volume.

`deploy.sh` is a convenience wrapper that unpacks a DBX source archive, builds the DBX
image if missing, and runs `docker compose up -d --build`.

### 6.5 Reaching DBX from the container

DBX must be reachable from the void2md process. Docker Desktop on macOS does not route
host traffic to the Docker bridge address, and host networking does not expose ports to
the macOS host. Use one of:

- Compose network (recommended): `DBX_API_URL=http://dbx:4224`;
- A host-published DBX port reached from a container through
  `host.docker.internal:<port>` (Docker Desktop) or the compose gateway
  (`172.18.0.1` on many Linux setups);
- The same host network on Linux.

`docker-compose-mac.yml` is the macOS variant: DBX runs with `network_mode: host` and the
void2md service keeps `host.docker.internal` wiring for the structured S3 store. It mounts
only `dbc/config/config.json` (not the whole directory) and does **not** set
`DBX_API_URL`, so export it explicitly (for example
`DBX_API_URL=http://host.docker.internal:4224`) before `docker compose up`.

---

## 7. API examples

All examples assume the service is at `http://localhost:3457` and send
`Authorization: Bearer test-key`.

### 7.1 Convert a file

```bash
curl -H "Authorization: Bearer test-key" \
     -F "file=@test.pdf;type=application/pdf" \
     http://localhost:3457/v1/fileparse
```

With options:

```bash
curl -H "Authorization: Bearer test-key" \
     -F "file=@document.docx" \
     -F "relevel_titles=true" \
     -F "seal_rec=true" \
     http://localhost:3457/v1/fileparse
```

### 7.2 Chunk Markdown

```bash
curl -H "Authorization: Bearer test-key" \
     -H "Content-Type: application/json" \
     -d '{"markdown":"# Title\n\nBody text...","chunk_sizes":{"text":512}}' \
     http://localhost:3457/v1/chunk
```

### 7.3 Crawl a site

```bash
curl -H "Authorization: Bearer test-key" \
     -H "Content-Type: application/json" \
     -d '{"url":"https://docs.example.com/","selector":".docs-content","maxPage":50}' \
     http://localhost:3457/v1/htmlparse

curl -H "Authorization: Bearer test-key" \
     http://localhost:3457/v1/htmlparse/<taskId>
```

### 7.4 DBC v2

Create a PostgreSQL connection:

```bash
curl -X POST http://localhost:3457/v1/dbc/v2/connections \
     -H "Authorization: Bearer test-key" \
     -H "Content-Type: application/json" \
     -d '{
       "name": "pg-main",
       "db_type": "postgres",
       "host": "127.0.0.1",
       "port": 5432,
       "username": "app",
       "password": "secret",
       "database": "app"
     }'
```

Test it:

```bash
curl -X POST http://localhost:3457/v1/dbc/v2/conn_test \
     -H "Authorization: Bearer test-key" \
     -H "Content-Type: application/json" \
     -d '{"connection_id":"pg-main"}'
```

Execute SQL:

```bash
curl -X POST http://localhost:3457/v1/dbc/v2/sql_query \
     -H "Authorization: Bearer test-key" \
     -H "Content-Type: application/json" \
     -d '{"connection_id":"pg-main","sql":"SELECT 1 AS ok","max_rows":10}'
```

List tables and describe one:

```bash
curl -X POST http://localhost:3457/v1/dbc/v2/list_tables \
     -H "Authorization: Bearer test-key" \
     -H "Content-Type: application/json" \
     -d '{"connection_id":"pg-main","limit":20}'

curl -X POST http://localhost:3457/v1/dbc/v2/describe_table \
     -H "Authorization: Bearer test-key" \
     -H "Content-Type: application/json" \
     -d '{"connection_id":"pg-main","table":"users"}'
```

Read Redis data:

```bash
curl -X POST http://localhost:3457/v1/dbc/v2/redis_scan_keys \
     -H "Authorization: Bearer test-key" \
     -H "Content-Type: application/json" \
     -d '{"connection_id":"redis-main","pattern":"model:*","count":100}'

curl -X POST http://localhost:3457/v1/dbc/v2/redis_get_value \
     -H "Authorization: Bearer test-key" \
     -H "Content-Type: application/json" \
     -d '{"connection_id":"redis-main","key":"model:index"}'
```

Health:

```bash
curl http://localhost:3457/v1/dbc/v2/health \
     -H "Authorization: Bearer test-key"
```

### 7.5 Structured

Connection test and raw SQL:

```bash
curl -X POST http://localhost:3457/v1/data_source/conn_test \
     -H "Authorization: Bearer test-key" \
     -H "Content-Type: application/json" \
     -d '{
       "type": "postgres",
       "host": "127.0.0.1",
       "port": 5432,
       "username": "app",
       "password": "secret",
       "db_name": "app",
       "ns_name": "public"
     }'

curl -X POST http://localhost:3457/v1/data_source/sql_query \
     -H "Authorization: Bearer test-key" \
     -H "Content-Type: application/json" \
     -d '{
       "source_config": {"type":"sqlite","db_path":"/tmp/demo.db"},
       "sql": "SELECT * FROM users LIMIT 5"
     }'
```

Metadata:

```bash
curl -X POST http://localhost:3457/v1/data_source/get_metadata \
     -H "Authorization: Bearer test-key" \
     -H "Content-Type: application/json" \
     -d '{"type":"sqlite","db_path":"/tmp/demo.db"}'
```

---

## 8. Testing

Run the Go test suite:

```bash
go test ./...
```

Module-specific suites:

```bash
go test ./file/...
go test ./dbc/...
go test ./structured/...
```

Rust unit tests:

```bash
cd file/rust && cargo test
cd file/tokenizer && cargo test
```

Notable test coverage:

- `dbc/pkg/dbx`: DBX payload shapes, Redis payloads, retry policy, breaker behavior,
  dialect normalization, family classification.
- `dbc/pkg/security`: read-only SQL, Redis, Mongo, document, KV, and HBase guards.
- `dbc/handlers`: connection CRUD, SQL/metadata endpoints, Redis endpoints, generic
  catalog/command endpoints, error envelopes.
- `dbc/pkg/dbcsync`: fingerprinting, coalescing, invalidation, failure recovery.
- `file/pkg/chunk`: tokenizer parity, table splitting, layout rules.
- `structured/handler`: golden responses for FastAPI-compatible validation, CORS,
  metadata, SQL formatting, and Excel flows.
- `structured/pkg/sqlg`: parser, formatter, optimizer, and golden SQL examples.

---

## 9. Operational limits

| Limit | Default |
|---|---:|
| `fileparse` upload | 32 MB |
| `chunk` request body | 100 MB |
| HTML crawl page limit | 200 (hard cap 2000) |
| HTML per-page size | 10 MB |
| HTML task Markdown budget | 200 MB |
| Live crawl tasks | 20 |
| Finished crawl retention | 30 minutes |
| DBC v2 request body | 1 MiB |
| SQL result rows | 100000 |
| `query_command` rows | 200 (max 1000) |
| `query_command` serialized size | 64 KiB |
| `list_objects` results | 1000 |
| `describe_object` samples | 0 (max 20) |
| Metadata cache | 15 seconds |
| Redis scan count | 100 (range 1–1000) |
| Excel upload | 50 MB |

---

## 10. Security notes

- **Bearer header.** Every route except `/healthz` requires an `Authorization: Bearer
  <token>` header. The built-in middleware validates the header shape, not the token
  value. Put a real authentication layer in front of the service, or extend
  `authMiddleware` in [main.go](main.go).
- **Passwords are write-only.** DBC v2 never returns a password. Connection files contain
  plaintext passwords by design (DBX needs them); they are written with `0600` and are
  excluded from git by `.gitignore`. Protect the `dbc/config` volume and backups.
- **No password logging.** Error redaction removes secrets from DBX error text; handlers
  log connection IDs and entry names, not credentials.
- **Read-only command guard.** `query_command` rejects writes before they reach DBX.
  The dedicated `/v2/redis_command` endpoint does **not** apply the local guard; it
  forwards the command and relies on DBX's safety policy. `sql_query` executes
  caller-supplied SQL verbatim, so expose it only to trusted callers; the v2 façade is not
  a SQL sandbox.
- **DBX safety.** DBX blocks dangerous Redis commands (for example `KEYS`) and applies its
  own command classification. The service does not bypass it.
- **LLM credentials.** The legacy `/v1/dbc/config/llm` endpoint stores an API key in
  process memory; `/query_by_nl` accepts a key per request and does not persist it.
- **Crawl surface.** `/v1/htmlparse` fetches arbitrary caller-supplied URLs. Restrict
  network egress or expose the endpoint only to trusted clients.

---

## 11. Project layout

```text
.
├── main.go                     Process entry: config, Gin, auth, health, graceful shutdown
├── common/                     Shared config, logging, diagnostics, response helpers
├── file/
│   ├── handlers/               /v1/fileparse, /v1/pdfparse, /v1/chunk route adapters
│   ├── pkg/handler/            /v1/htmlparse HTTP handlers
│   ├── pkg/chunk/              Layout-aware Markdown chunker + tokenizer bindings
│   ├── pkg/config/             File-module configuration
│   ├── pkg/crawler/            Same-domain BFS crawl, fetch, link extraction
│   ├── pkg/parse/              HTML parsing, selector scoping, content filtering
│   ├── pkg/service/            Chunk and crawl services (task store, dedupe)
│   ├── rust/                   Rust converters + C ABI (libvoid2md.so)
│   ├── tokenizer/              HuggingFace BPE tokenizer FFI (libtokenizer.a)
│   └── data/                   Tokenizer data and supporting assets
├── dbc/
│   ├── handlers/               Legacy /v1/dbc/* and v2 /v1/dbc/v2/* handlers
│   ├── pkg/config/             dbc configuration
│   ├── pkg/connstore/          Per-connection JSON file store
│   ├── pkg/dbcsync/            Connection-file → DBX synchronization
│   ├── pkg/dbx/                DBX REST client, dialects, families, adapters
│   ├── pkg/security/           Read-only SQL/command guard
│   ├── pkg/llm/                Legacy LLM client
│   ├── pkg/database/           Legacy direct database client
│   └── config/                 Default config and connection files
├── structured/
│   ├── handler/                FastAPI/starlette-compatible HTTP layer
│   ├── pkg/ds/                 Data-source drivers (SQL, DuckDB, S3, Mongo)
│   ├── pkg/metadata/           DatabaseMetadata model
│   ├── pkg/sqlg/               sqlglot-compatible SQL parser/formatter
│   ├── pkg/agent/              Natural-language query agent
│   ├── pkg/chatmodel/          OpenAI-compatible chat client
│   ├── pkg/sqlgen/             SQL generation helpers
│   ├── pkg/sqleval/            SQL result evaluation (Python-parity prompts)
│   ├── pkg/evaluation/         Result evaluation
│   ├── pkg/pyrepr/             Python-style value rendering
│   └── duckdb_extensions/      Offline httpfs/excel extensions per architecture
├── docs/                       Design specs
├── Dockerfile                  Multi-stage Rust + Go + LibreOffice image
├── docker-compose.yml          void2md + DBX compose stack
└── deploy.sh                   Convenience deployment wrapper
```

---

## License

The Rust converter crate declares `MIT`. Refer to the repository's license file and
dependency licenses for the Go and third-party components.
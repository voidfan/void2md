# tools/oracle — diff-based parity testing (Go port vs. the original Python implementation)

Byte-for-byte parity tests comparing the Go implementation against golden data captured
from the original Python service. The Python implementation and the oracle scripts that
generated the goldens were deleted; **the goldens are read-only artifacts** and can no
longer be regenerated — except `golden/e2e_service.json`, which `e2e_test.go` recaptures
live if the Python source is restored (`git restore src`).

## Artifacts

| golden | consumer |
| --- | --- |
| `golden_opt.json` | `opt_test.go`, `rules_test.go` |
| `golden/golden_rules.json` | `rules_test.go` |
| `golden/golden_pretty.json` | `pretty_test.go` |
| `golden/golden_roundtrip.json` | `diff_test.go` |
| `golden_examples.json` | `internal/ds/golden_examples_test.go` |
| `golden_metadata.json` | `internal/metadata/golden_test.go` |
| `golden_realcols.json` | `realcols_test.go` |
| `golden/e2e_service.json` | `e2e_test.go` |
| `golden_prompts.json` | `internal/sqlgen/prompts_test.go`, `internal/sqleval/prompts_test.go` |
| `internal/api/testdata/cors_{cases,golden}.json` | `internal/api/cors_golden_test.go` |

Mind the path split: `golden_opt.json`, `golden_realcols.json`, `golden_examples.json`,
`golden_metadata.json`, and `golden_prompts.json` live at the **top level** of
`tools/oracle/`; `golden_pretty.json`, `golden_rules.json`, `golden_roundtrip.json`, and
`e2e_service.json` live in `tools/oracle/golden/`.

## Running

```bash
# Run only the Go-side comparison (goldens are persisted; no Python needed)
go test ./tools/oracle/

# A single test
go test ./tools/oracle/ -run TestE2EServiceParity -v
```

`TestE2EServiceParity` collects goldens live from `python -m dative.main` (port 3000) if
`src/dative/main.py` exists; otherwise it automatically falls back to the offline
`golden/e2e_service.json`, so the service-level byte-for-byte checks always really run.
It fails only when neither the Python source nor the golden exists. Explicit offline mode:

```bash
DATIVE_E2E_OFFLINE=1 go test ./tools/oracle/ -run TestE2EServiceParity -v
```

### Environment variables

| var | effect |
| --- | --- |
| `DATIVE_E2E_OFFLINE=1` | skip the Python probe; read `golden/e2e_service.json` directly |
| `DATIVE_E2E_REUSE_PYTHON=1` | reuse a Python service already running on port 3000 |
| `DATIVE_GO_BASE_URL=...` | hit a real, running Go service instead of in-process httptest |
| `DATIVE_E2E_PYTHON=...` | path to the python executable (default: `python` on PATH) |
| `ORACLE_STRICT=1` | `opt_test.go` fails on any divergence (default: report only) |

The `*_test.go` files form one package and depend on each other (`rules_test.go` uses
`loadJSON` from `diff_test.go` and `loadOptCases` from `opt_test.go`), so single-file
invocations like `go test tools/oracle/rules_test.go` don't compile — use
`go test ./tools/oracle/`.

## Coverage boundaries

Parity is guaranteed byte for byte only for SQL shapes inside the corpus; shapes beyond it
have known differences listed in §5 of `DATIVE_GO_PORT_DELIVERY.md`.

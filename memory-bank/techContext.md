# Tech Context: go-rag

## Technologies
- Go 1.25
- SQLite via `modernc.org/sqlite` (pure Go, no CGO). Note: the driver supports only `_pragma`, `_time_format`, `_time_integer_format`, `_dqs`, `_error_rc` DSN params — `_journal=WAL`, `_busy_timeout`, and `_fk=1` are silently ignored. Use `_pragma=...` if a pragma is actually needed.
- UUID generation via `github.com/google/uuid`
- YAML config via `gopkg.in/yaml.v3`
- PDF parsing via `github.com/razvandimescu/gopdf` (no encryption support; its lexer stalls on undecodable content — see below)
- PDF decryption/normalization via `github.com/pdfcpu/pdfcpu` (`api.Decrypt` with an empty user password, `api.DisableConfigDir` via `sync.OnceFunc` so no config dir is written)

## Development setup
```powershell
go build -o go-rag.exe ./cmd/go-rag
go test ./...
```

## Gotchas
- gopdf v0.9.5: `readKeyword` returns `Token{Type: TKeyword, Str: ""}` without advancing the position for delimiters `NextToken` does not handle (`)`, `{`, `}`), which makes `ExtractPageText` loop forever on content it cannot tokenize. `readablePageContent` (internal/parser/pdf.go) pre-scans each page with gopdf's own lexer and skips such pages — keep that guard whenever page content is lexed.
- pdfcpu is the second-opinion reader for PDFs: it decrypts (empty user password), rebuilds damaged xref tables, and returns `pdfcpu.ErrNotEncrypted` / `ErrWrongPassword` (both matchable with `errors.Is`) for the non-encrypted / wrong-password cases.
- The repository is checked out with CRLF line endings, so `gofmt -l` flags every file on Windows; check formatting of the specific files touched instead of reformatting the tree.

## Project structure
```
cmd/go-rag/main.go       CLI entry point and command handlers
internal/
  chunker/               Text chunking
  embedder/              Embedding clients (OpenAI-compatible)
  index/                 Hierarchical indexing
  knowledge/             Knowledge graph
  llm/                   LLM clients
  parser/                Document parsers
  retriever/             Search, reranking, query rewriting
  storage/               SQLite storage and interface
pkg/config/              YAML configuration
```

## Tool usage patterns
- Use `go build ./cmd/go-rag` to build the binary.
- Use `go test ./...` for validation.
- Use `git` and `gh` for version control and releases.

## SQLite driver notes
- `modernc.org/sqlite` reads **only** `?_pragma=<statement>` (plus `_time_format`,
  `_time_integer_format`, `_timezone`, `_txlock`, `_dqs`, `_error_rc`) from the
  DSN. mattn-style `_journal`/`_busy_timeout`/`_fk` parameters are silently
  ignored — check `PRAGMA journal_mode` / `PRAGMA foreign_keys` when in doubt.
- Busy errors are `*sqlite.Error`; use `errors.As` and compare
  `Code()` against `sqlite3.SQLITE_BUSY` / `sqlite3.SQLITE_LOCKED` from
  `modernc.org/sqlite/lib`.

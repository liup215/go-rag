# Tech Context: go-rag

## Technologies
- Go 1.25
- SQLite via `modernc.org/sqlite` (pure Go, no CGO)
- UUID generation via `github.com/google/uuid`
- YAML config via `gopkg.in/yaml.v3`
- PDF parsing via `github.com/razvandimescu/gopdf`

## Development setup
```powershell
go build -o go-rag.exe ./cmd/go-rag
go test ./...
```

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

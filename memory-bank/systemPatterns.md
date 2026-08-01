# System Patterns: go-rag

## Architecture
- CLI entry point: `cmd/go-rag/main.go` dispatches subcommands via a `switch` on `os.Args[1]`.
- Packages under `internal/` handle specific concerns: storage, chunking, embedding, retrieval, parsing, knowledge graph.
- Configuration: YAML file managed by `pkg/config`.

## Key design patterns
- **Interface-based storage**: `storage.Storage` abstracts the database, currently backed by SQLite.
- **Worker-guarded writes**: SQLite writes are serialized through a single worker goroutine to avoid `SQLITE_BUSY`.
- **Command handlers**: Each subcommand loads config, initializes dependencies, performs work, prints results, and exits on error.
- **Mock implementations**: Retriever tests use an in-memory mock of `storage.Storage`.

## Component relationships
```
cmd/go-rag/main.go
  -> config.Load
  -> storage.NewStorage
  -> chunker / parser / embedder / retriever
```

## Critical implementation paths
- Adding a storage query requires updating the interface, SQLite implementation, and any mock implementations.
- CLI flags use `flag.NewFlagSet` plus `reorderArgs` to allow flags after positional arguments.

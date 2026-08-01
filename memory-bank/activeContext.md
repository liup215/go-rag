# Active Context: go-rag

## Current work focus
Recently added the `get-chunk` command to retrieve a single chunk by document ID and chunk index.

## Recent changes
- Extended `Storage` interface and SQLite implementation with `GetChunkByIndex(docID string, index int) (*Chunk, error)`.
- Added `go-rag get-chunk <doc-id> --index <n>` command handling, help text, and error messages.
- Added `internal/storage/sqlite_test.go` for happy-path and not-found cases.
- Updated `README.md` with the new command and example.
- Updated retriever test mock storage to satisfy the extended interface.

## Next steps
- Consider future enhancements: chunk ranges, JSON output, or chunk-level deletion.

## Active decisions
- New commands are added as top-level subcommands in `cmd/go-rag/main.go`.
- Storage interface changes require updating test mocks in dependent packages.

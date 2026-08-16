# Active Context: go-rag

## Current work focus
Integrated a lightweight, agent-managed personal wiki ("wiki") into go-rag, aligning with the Karpathy wiki pattern where the agent maintains an index of topics.

## Recent changes
- Added `wiki_indexes` and `wiki_entries` tables to SQLite storage.
- Extended `Storage` interface with wiki index and entry CRUD operations.
- Implemented `go-rag wiki` subcommands:
  - `index-list`, `index-create`, `index-delete`
  - `remember`, `list`, `get`, `update`, `forget`
- Added `cmd/go-rag/main.go` handlers and help text for the new commands.
- Updated `internal/storage/sqlite_test.go` with wiki CRUD and cascade-delete tests.
- Updated `internal/retriever/retriever_test.go` mock storage to satisfy the extended interface.
- Updated `README.md` and `SKILL.md` with wiki usage documentation.
- Fixed README feature list: changed "JSON storage" to "SQLite storage".
- Hardened `wiki remember` validation: index ID, title, and body are all required and trimmed; empty values now fail explicitly.
- Switched wiki entry body input/output to file-based workflow:
  - `remember` accepts `--body` or `--file` (mutually exclusive, one required).
  - `update` requires `--file` for body changes (or `--title` for title-only changes).
  - New `export` command writes entry body to a file for editing.
  - Wiki commands no longer read body from stdin.

## Next steps
- Observe how agents use the wiki commands and iterate on ergonomics.
- Consider optional enhancements: entry tags, date filters, or a wiki search fallback.

## Active decisions
- Wiki content is stored directly in SQLite, not as Markdown files, so agents never need to read the filesystem.
- Wiki indexes are agent-managed; go-rag does not call LLMs to generate or update the index.
- Wiki retrieval is symbolic: list indexes → list entries → get full entry body.

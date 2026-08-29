# Active Context: go-rag

## Current work focus
Search results now identify their source document, and `search`/`list` gained
`--json` machine-readable output (the JSON-output item from progress.md's todo
list).

## Recent changes
- `cmd/go-rag/main.go` (`handleSearch`): after retrieval, hits are enriched with
  the `documents` row. `loadDocumentsForResults` collects the distinct
  `Chunk.DocumentID`s and calls `GetDocument` once per ID (chosen over adding a
  SQL-join method to `storage.Storage`, which would have touched the interface
  plus every mock). Human output prints `Document Name:` / `Document Path:`
  under each result, next to the existing `Document ID:`.
- Missing documents degrade instead of erroring: a document that no longer
  exists yields empty `document_name`/`document_path` in JSON and `(unknown)` in
  human output; an empty `DocumentID` is never queried.
- `search --json` prints `{query, count, results}`; each result has
  `document_id`, `document_name`, `document_path`, `score`, `chunk_id`,
  `chunk_index`, `text` (snake_case, matching `storage.Document`'s tags). The
  embedding vectors are deliberately excluded — a dedicated JSON struct is used
  instead of marshalling `storage.Chunk`.
- `list --json` prints `{total, offset, count, documents}` reusing
  `storage.Document`; empty result sets serialize as `[]` (`nonNilDocuments`),
  never `null`. Filters, paging, and totals behave exactly as in table mode.
- `writeJSON` uses an indented, non-HTML-escaped encoder so paths and chunk
  text stay readable.
- `reorderArgs` gained a `booleanFlags` set: previously it attached the next
  token to any flag, so `go-rag search --json <query>` consumed the query as the
  flag's value. Only `json` is registered, so all pre-existing invocations are
  unaffected.
- Usage/help text and the README/SKILL command docs were updated for `--json`.
- Tests (`cmd/go-rag/main_test.go`): `docLookupStub` (embeds `storage.Storage`,
  overrides `GetDocument`) backs `TestLoadDocumentsForResults` (dedupe, missing
  doc → nil, empty ID skipped); `TestBuildSearchResultsJSON`; JSON shape tests
  pinning the wire format of both payloads; `TestNonNilDocuments`;
  `TestReorderArgsBooleanFlags`.

## Next steps
- Candidate follow-up (from known issues): `SQLiteStorage.SearchByKeyword`
  LIKE-pre-filters on the raw query, so multi-word Chinese queries can return an
  empty candidate set; tokenise into AND/OR LIKE clauses.
- Consider `--json` for `get-chunk` and `wiki` subcommands if scripting demand
  appears.

## Active decisions
- Document lookup for search results stays in the CLI layer via `GetDocument`
  per distinct ID — top-k is small, and it avoids changing the `Storage`
  interface (which would ripple into mocks). Revisit with a single
  `GetDocuments(ids)`/join if top-k or N+1 concerns grow.
- JSON keys are snake_case and stable regardless of hit state (no `omitempty`
  on name/path), so consumers get one schema; empty results marshal as `[]`,
  never `null`.
- JSON output is opt-in per command (`--json`); human-readable output is
  byte-for-byte unchanged apart from the two added document lines in `search`.
- `reorderArgs`' bool-flag list (`booleanFlags`) is a package-level map rather
  than a per-FlagSet parameter: flags are global to this CLI and it keeps the
  helper's signature unchanged.
- Previous: filtering/searching lives in the storage layer (SQL WHERE), not in
  the CLI; pagination defaults stay in the CLI (`--limit 100`); `0` means "no
  limit"; the `Storage` interface was changed in place rather than adding a
  parallel filtered method.

## Previous work
- CJK/BM25 tokenisation fix (bigrams + unigrams, punctuation separators) so
  Chinese keyword queries actually recall chunks.
- Personal wiki subsystem (`go-rag wiki`) with `wiki_indexes`/`wiki_entries`
  tables, symbolic recall flow (index-list → list → get), and file-based body
  create/update/export.

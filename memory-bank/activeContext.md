# Active Context: go-rag

## Current work focus
Made `go-rag list` fully paginated and searchable so document libraries larger than 100 entries can be browsed and individual documents located quickly.

## Recent changes
- Introduced `storage.DocumentQuery` (`Search`, `Filters`, `Limit`, `Offset`).
- `Storage.ListDocuments(query)` now applies search/filter/pagination; `limit 0` means no limit.
- Added `Storage.CountDocuments(query)` so the CLI can report the total number of matches (limit/offset ignored).
- Document search matches name and file path as a case-insensitive substring with LIKE wildcards (`%`, `_`, `\`) escaped.
- Document filters support keys `status`, `type`/`doc_type`, `name`, `path`/`file_path`; repeated keys combine with SQL `IN` (OR), different keys combine with AND; unsupported keys return an error.
- `go-rag list` gained `--limit` (default 100, 0 = all), `--offset`, `--page` (1-based, overrides `--offset`), `--search`, and repeatable `--filter key=value`.
- `go-rag list` output now ends with `Showing <n> of <total> documents (offset <o>)` plus a next-page hint when more results remain.
- Added pure CLI helpers `resolveListOffset` and `filterFlags` (a `flag.Value`), covered by new `cmd/go-rag/main_test.go`.
- Updated usage/help text, README.md, and SKILL.md.

## Next steps
- Observe how agents use the paginated list and iterate on ergonomics.
- Possible follow-ups: JSON output mode, date-range filters, sorting options.

## Active decisions
- Filtering/searching lives in the storage layer (SQL WHERE), not in the CLI, so totals and pages are always consistent.
- The `Storage` interface was changed in place (`ListDocuments(query)`) rather than adding a parallel filtered method — all call sites are internal.
- Pagination defaults stay in the CLI (`--limit 100`); the storage layer treats `0` as "no limit" and rejects negative values.

## Previous work
- Personal wiki subsystem (`go-rag wiki`) with `wiki_indexes`/`wiki_entries` tables, symbolic recall flow (index-list → list → get), and file-based body create/update/export.

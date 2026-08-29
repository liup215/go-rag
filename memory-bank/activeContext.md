# Active Context: go-rag

## Current work focus
Encrypted PDFs are now recognised instead of masquerading as "no text
extracted from pdf" (the encryption-diagnosis item from progress.md's known
issues). Owner-password-protected PDFs — the College Board AP documents: they
open in every viewer without a prompt but refuse text extraction — are
decrypted automatically; files with a real user password fail with an explicit
encryption error plus decryption hints.

## Recent changes
- New `internal/parser/pdf.go` holding the whole PDF pipeline. `parsePDF` now
  resolves encryption **before** gopdf touches content (gopdf has no decryption
  support, and its lexer spins forever on the garbage it decodes from an
  encrypted stream — see the bug below):
  1. trailer declares `/Encrypt` → pdfcpu tries the empty user password
     (`api.Decrypt`), which also rebuilds a damaged xref, then the decrypted
     copy is parsed with `Decrypted: true`;
  2. otherwise a plain gopdf parse runs; pages whose content gopdf cannot
     decode are skipped (`readablePageContent` guard) instead of hanging;
  3. if gopdf failed on the structure or decoded only garbage, pdfcpu gets a
     second opinion before missing text is blamed on a scanner;
  4. a file that rejects the empty password fails with `ErrPDFEncrypted`
     ("PDF is encrypted (owner password)"), never with the old misleading
     "no text extracted from pdf".
- `ErrPDFEncrypted` is the sentinel for the encryption case; the wrapped text
  always distinguishes "needs a password" from "has no text".
- `ParseResult.Decrypted` (parser.go) reports that a source PDF carried
  `/Encrypt` and was transparently decrypted; `add` prints a 🔓 notice.
- `cmd/go-rag/main.go`: `printParseError` + `parseFailureHints` attach
  follow-up advice — qpdf/pikepdf one-liners (paths passed via argv so no
  shell has to quote a Windows path) for encryption, OCR/rebuild advice for
  text-less PDFs, nothing for missing files or non-PDFs.
- **gopdf bug found (upstream-worthy)**: `lexer.go readKeyword()` returns
  `Token{Type: TKeyword, Str: ""}` *without advancing* for the delimiters its
  `NextToken` switch does not handle (`)`, `{`, `}`), so the operator loop in
  `extractTextWithResources` spins forever — 80 bytes of ciphertext produced
  >20M tokens in the probe. The empty keyword is the only non-advancing token
  the lexer can emit, which makes it a precise "cannot tokenize" signal, and
  `readablePageContent` uses exactly it. Every other token consumes ≥1 byte,
  so the guard loop is bound by the content length; it costs ~0% of extraction
  (measured: 100-page extract 546µs, lexer-only pass below timer resolution).
- `go.mod`: `github.com/pdfcpu/pdfcpu v0.15.0` (plus indirect deps) for
  decryption; `sync.OnceFunc(api.DisableConfigDir)` keeps pdfcpu from writing
  a config directory as a side effect.
- Tests: `internal/parser/pdf_test.go` builds PDFs from real byte offsets
  (`buildTestPDF(t, texts ...string)`, one page per text, "" = page without a
  content stream) and encrypts them with pdfcpu (AES-256/128, RC4-128): plain
  parse, empty-user-password decryption, user-password error, encrypted-but-
  text-less, no pages, undecodable content (a stray `)` — the hang repro, now
  deterministic), skip-only-the-broken-page, and `pdfHasEncryptDict`
  (including the `/EncryptMetadata` non-match). `cmd/go-rag/main_test.go`
  pins `parseFailureHints` per error class.

## Next steps
- Candidate follow-up (from known issues): `SQLiteStorage.SearchByKeyword`
  LIKE-pre-filters on the raw query, so multi-word Chinese queries can return
  an empty candidate set; tokenise into AND/OR LIKE clauses.
- Consider upstreaming/reporting the gopdf lexer stall (empty keyword, no
  position advance) — go-rag now guards around it locally.
- Consider `--json` for `get-chunk` and `wiki` subcommands if scripting demand
  appears.

## Active decisions
- Encryption is checked *first* (trailer via gopdf, then a scan of the newest
  xref section when gopdf cannot parse at all), because letting gopdf parse an
  encrypted file risks the lexer spin. The scan excludes `/EncryptMetadata`
  (a key of the encryption dictionary, not a declaration of encryption).
- "All pages decoded, still no text" is treated as a scanned document and
  returns immediately — no pdfcpu pass on the common scanned-PDF case.
  "Pages that cannot be decoded" (or a page tree gopdf cannot walk) go to
  pdfcpu, since that is how hidden encryption *and* damaged xref tables
  present; pdfcpu only rewrites what it decrypted, so its success always means
  "encrypted" and `Decrypted: true` is never a lie.
- Undecodable pages are skipped, not fatal: one broken page must not cost the
  text of the other 229. When *every* page is undecodable the error says so
  ("all N pages hold data that cannot be decoded") instead of a bare
  "no text extracted".
- pdfcpu (not pikepdf) for decryption: pure-Go, already acceptable as a
  dependency, and `api.Decrypt` performs the same normalization the manual
  pikepdf workaround did.
- Previous: document lookup for search results stays in the CLI layer via
  `GetDocument` per distinct ID; JSON keys are snake_case and stable; JSON
  output is opt-in per command; `reorderArgs`' bool-flag list is a
  package-level map.

## Previous work
- Search results identify their source document (`document_name`/`path`), and
  `search`/`list` gained `--json` output with a stable schema (`[]`, never
  `null`); `reorderArgs` learned boolean flags so `search --json <query>`
  keeps the query.
- CJK/BM25 tokenisation fix (bigrams + unigrams, punctuation separators) so
  Chinese keyword queries actually recall chunks.
- Personal wiki subsystem (`go-rag wiki`) with `wiki_indexes`/`wiki_entries`
  tables, symbolic recall flow (index-list → list → get), and file-based body
  create/update/export.

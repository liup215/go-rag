# Active Context: go-rag

## Current work focus
Fixed BM25 tokenisation for Chinese/CJK text so the keyword branch of hybrid retrieval actually fires: contiguous CJK runs are split into bigrams (+unigrams) and CJK punctuation separates tokens.

## Recent changes
- Root cause: `isWordChar` treated every rune > 127 (including CJK punctuation '，' '。') as a word character, so `splitWords` collapsed a whole Chinese sentence — sometimes a whole chunk — into a single token. BM25 could then only match when the query equalled that exact run, so Chinese keyword recall was effectively zero.
- `internal/retriever/retriever.go`: `isWordChar` now accepts non-ASCII runes only when they are not punctuation/symbol/space/control (Unicode category based), so CJK punctuation acts as a separator.
- New helpers `splitWordRun`, `cjkNgrams`, `isCJKRune`: contiguous CJK runs (Han/Kana/Hangul via `unicode.Is` script tables) split into overlapping bigrams with unigrams layered on top ("机器学习" → 机, 机器, 器, 器学, 学, 学习, 习); non-CJK sub-runs stay whole, so "GPT4模型" yields "gpt4" plus the 模型 n-grams.
- ASCII word behaviour is unchanged (runs of `[0-9A-Za-z]` are one lower-cased token); existing `TestTokenize` cases pass untouched.
- New tests in `internal/retriever/tokenize_cjk_test.go`: bigram/punctuation/single-char/mixed-script tokenisation, BM25 Chinese query matching ("机器学习" → chunk containing "…机器学习算法…"), single-char CJK query, retriever keyword and hybrid Chinese paths.

## Next steps
- Known follow-up: `SQLiteStorage.SearchByKeyword` pre-filters with one `LIKE '%<query>%'` on the raw query, so a multi-word Chinese query ("机器学习 算法") returns an empty candidate set before BM25 runs. Consider tokenising the query into AND/OR LIKE clauses. The hybrid path is unaffected (it loads all chunks).
- `BuildBM25Index` is rebuilt from all chunks on every search; consider caching if corpora grow (out of scope for now).

## Active decisions
- CJK n-gram tokenisation emits bigrams **and** unigrams: bigrams carry the discriminative power, unigrams keep one-character queries matchable. Task explicitly allowed stacking unigrams.
- Scripts needing n-grams are detected with stdlib script tables (`unicode.Is(unicode.Han|Hiragana|Katakana|Hangul, r)`), not hand-rolled ranges.
- Tokenisation change is scoped to `internal/retriever` (`tokenize` is only used by `bm25.go`); storage LIKE pre-filter left as-is for now.
- Previous: filtering/searching lives in the storage layer (SQL WHERE), not in the CLI; pagination defaults stay in the CLI (`--limit 100`); `0` means "no limit".
- The `Storage` interface was changed in place (`ListDocuments(query)`) rather than adding a parallel filtered method — all call sites are internal.

## Previous work
- Personal wiki subsystem (`go-rag wiki`) with `wiki_indexes`/`wiki_entries` tables, symbolic recall flow (index-list → list → get), and file-based body create/update/export.

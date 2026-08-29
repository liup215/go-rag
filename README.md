# go-rag

A lightweight RAG (Retrieval-Augmented Generation) command line tool written in pure Go.

## Features

- **Pure Go implementation** - No external dependencies
- **Multiple file formats** - Support for txt, md, html, xml, pdf, docx, xlsx, pptx
- **External embedding models** - Compatible with OpenAI, Ollama, and other OpenAI-compatible APIs
- **Hybrid search** - Vector search + BM25 keyword search fused with Reciprocal Rank Fusion (RRF)
- **Cross-encoder reranking** - Optional reranking pass using a bge-reranker or compatible API
- **SQLite storage** - Simple local database, no external database dependencies
- **Personal wiki** - Agent-managed topic indexes with symbolic retrieval
- **Cross-platform** - Windows, macOS, Linux (amd64, arm64)

## Installation

### Download from GitHub Releases

1. Go to [Releases](https://github.com/liup215/go-rag/releases)
2. Download the appropriate binary for your platform
3. Extract and place in your PATH

### Build from source

```bash
git clone https://github.com/liup215/go-rag.git
cd go-rag
go build -o go-rag ./cmd/go-rag
```

## Quick Start

### 1. Initialize configuration

```bash
go-rag init
```

This creates a config file at:
- Windows: `%APPDATA%\go-rag\config.yaml`
- macOS: `~/Library/Application Support/go-rag/config.yaml`
- Linux: `~/.config/go-rag/config.yaml`

### 2. Configure embedding service

View all available configuration options:

```bash
go-rag config help
```

Configure for OpenAI:

```bash
go-rag config set embedding.url https://api.openai.com/v1
go-rag config set embedding.api-key sk-your-api-key
go-rag config set embedding.model text-embedding-3-small
```

Configure for Ollama (local):

```bash
go-rag config set embedding.url http://localhost:11434
go-rag config set embedding.model nomic-embed-text
```

### 3. Add documents

```bash
go-rag add document.pdf
go-rag add notes.md
go-rag add report.docx
```

### 4. Search

Each result shows the document it came from (ID, name, and file path) plus the
chunk number, so you can pull surrounding context with `get-chunk` without a
second lookup.

```bash
go-rag search "machine learning"
go-rag search "project requirements" --top-k 10
go-rag search "budget analysis" --threshold 0.7

# Machine-readable output for scripts and agents
go-rag search "machine learning" --json
```

`--json` prints `{query, count, results}`, where each result carries
`document_id`, `document_name`, `document_path`, `score`, `chunk_id`,
`chunk_index`, and `text`. Documents that no longer exist leave
`document_name`/`document_path` empty instead of dropping the hit.

### 5. Get a specific chunk

```bash
go-rag get-chunk <doc-id> --index 3
```

### 6. List documents

`go-rag list` is paginated: by default it shows the 100 most recent documents
and reports the total number of matches, so you always know whether more
documents are available.

```bash
# First 100 documents (newest first), with the total count
go-rag list

# Next page
go-rag list --page 2

# Custom page size
go-rag list --limit 20 --offset 40

# Show everything at once
go-rag list --limit 0

# Only documents whose name or file path contains "report"
go-rag list --search report

# Exact-match filters (repeatable; multiple values mean OR)
go-rag list --filter status=indexed
go-rag list --filter status=indexed --filter status=indexing
go-rag list --filter type=pdf --filter path=docs/report.pdf

# Combine search, filters, and paging
go-rag list --search report --filter status=indexed --limit 50 --page 2

# Machine-readable output for scripts and agents
go-rag list --json
```

Supported `--filter` keys: `status`, `type` (document type), `name`,
and `path` (file path). The output footer shows
`Showing <n> of <total> documents (offset <o>)` plus a next-page hint
when more results remain.

With `--json`, `list` prints `{total, offset, count, documents}` — the same
filters, paging, and totals apply, and empty pages serialize as `[]` instead
of a message.

## Commands

| Command | Description |
|---------|-------------|
| `init` | Initialize configuration |
| `add <file>` | Add a document to the knowledge base |
| `search <query>` | Search the knowledge base |
| `list` | List documents with pagination, search, and filters |
| `delete <doc-id>` | Delete a document |
| `get-chunk <doc-id>` | Get a chunk by document ID and index |
| `wiki index-list` | List personal wiki indexes (topics) |
| `wiki index-create <title>` | Create a personal wiki index |
| `wiki index-delete <index-id>` | Delete a personal wiki index |
| `wiki remember <index-id> <title>` | Create a wiki entry under an index |
| `wiki list <index-id>` | List wiki entries in an index |
| `wiki get <entry-id>` | Show full body of a wiki entry |
| `wiki update <entry-id>` | Update a wiki entry |
| `wiki export <entry-id>` | Export entry body to a file |
| `wiki forget <entry-id>` | Delete a wiki entry |
| `config set <key> <value>` | Set a configuration value |
| `config get <key>` | Get a configuration value |
| `config list` | List all configuration |
| `config help` | Show all available configuration options |

## Personal Wiki

go-rag includes a lightweight, agent-managed personal wiki. Unlike the RAG
pipeline, the wiki system stores complete entries in SQLite and relies on
**symbolic navigation** through indexes (topics):

1. Agent lists all indexes with `wiki index-list`.
2. Agent chooses the relevant index and lists its entries with `wiki list <index-id>`.
3. Agent reads the desired full entry with `wiki get <entry-id>`.

The tool does not generate or maintain the index automatically; the agent is
responsible for deciding which index an entry belongs to and how entries are
organized.

### Create an index

```bash
go-rag wiki index-create "Architecture" --description "Design decisions"
```

### Remember an entry

```bash
# Quick note via --body
go-rag wiki remember <index-id> "SQLite WAL decision" \
  --body "We chose SQLite WAL mode to avoid SQLITE_BUSY errors."

# Longer content from a file
go-rag wiki remember <index-id> "SQLite WAL decision" --file ./sqlite-wal.md
```

`--body` and `--file` are mutually exclusive; you must provide exactly one of them.

### Recall workflow

```bash
# 1. List indexes
go-rag wiki index-list

# 2. List entry summaries in the chosen index
go-rag wiki list <index-id>

# 3. Read the full entry body
go-rag wiki get <entry-id>
```

### Update an entry body

Always export the body first, edit the file, then re-import:

```bash
go-rag wiki export <entry-id> --file ./draft.md
# edit draft.md
go-rag wiki update <entry-id> --file ./draft.md
```

Update only the title:

```bash
go-rag wiki update <entry-id> --title "Updated title"
```

### Delete

```bash
go-rag wiki forget <entry-id>
go-rag wiki index-delete <index-id>
```

## Configuration

```yaml
# config.yaml
embedding:
  url: "https://api.openai.com/v1"
  api_key: "sk-..."
  model: "text-embedding-3-small"

chunking:
  max_tokens: 512
  overlap: 100

storage:
  path: "~/.local/share/go-rag/db.sqlite"

reranker:
  enabled: false
  url: ""        # e.g. http://localhost:8080/rerank
  api_key: ""
  model: ""      # e.g. bge-reranker-v2-m3
```

## Supported File Formats

| Format | Extension | Notes |
|--------|-----------|-------|
| Plain Text | .txt | Direct text extraction |
| Markdown | .md | Preserves formatting |
| HTML | .html, .htm | Strips tags |
| XML | .xml | Extracts text content |
| PDF | .pdf | Basic text extraction |
| Word | .docx | Office Open XML |
| Excel | .xlsx | Office Open XML |
| PowerPoint | .pptx | Office Open XML |

## Reranking (Cross-Encoder)

When a cross-encoder reranker is configured, `go-rag` applies a two-stage
retrieval pipeline:

1. **First stage** – Hybrid search (vector + BM25) retrieves a wider candidate
   pool (`TopK × 3` documents).
2. **Second stage** – The cross-encoder scores every candidate against the query
   and returns only the top-`TopK` results re-ordered by relevance.

This cascade strategy gives the cross-encoder a rich pool to work with while
keeping final result latency low.

### Configure reranking

```bash
# Enable reranking and point to a locally served bge-reranker
go-rag config set reranker.enabled true
go-rag config set reranker.url http://localhost:8080/rerank
go-rag config set reranker.model bge-reranker-v2-m3

# Or use a hosted API (e.g., Jina Reranker)
go-rag config set reranker.url https://api.jina.ai/v1/rerank
go-rag config set reranker.api-key jina_...
go-rag config set reranker.model jina-reranker-v2-base-multilingual
```

The reranker API must accept:
```json
{"model":"<model>","query":"<query>","documents":["<doc1>","<doc2>"]}
```
and return:
```json
{"results":[{"index":0,"relevance_score":0.95},{"index":1,"relevance_score":0.42}]}
```

If the reranker is unavailable or returns an error, `go-rag` automatically
falls back to the original retrieval order so searches remain available.

## Architecture

```
go-rag/
├── cmd/go-rag/          # CLI entry point
├── internal/
│   ├── chunker/         # Text chunking
│   ├── embedder/        # Embedding client (OpenAI/Ollama)
│   ├── parser/          # File parsers
│   ├── retriever/       # Hybrid search + BM25 + RRF + Reranking
│   └── storage/         # SQLite storage
└── pkg/config/          # Configuration management
```

## How it works

1. **Parse**: Extract text from various file formats
2. **Chunk**: Split text into overlapping chunks (default 512 tokens)
3. **Embed**: Generate vector embeddings using external API
4. **Store**: Save chunks and vectors to SQLite
5. **Search**:
   - Generate query embedding
   - Run vector similarity search and BM25 keyword search in parallel
   - Fuse ranked lists with Reciprocal Rank Fusion (RRF)
   - *(Optional)* Re-score the candidate pool with a cross-encoder reranker
   - Return top-K results

## License

MIT License

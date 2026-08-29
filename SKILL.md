---
name: go-rag
description: "Install and use go-rag - a lightweight RAG command line tool for document indexing and semantic search. Use this skill when the user wants to set up a local RAG system, index documents for search, or perform semantic search over their documents."
version: "0.1.0"
category: tools
tags:
  - rag
  - search
  - documents
  - indexing
  - embedding
---

# go-rag Skill

This skill helps you install and use go-rag, a lightweight RAG (Retrieval-Augmented Generation) command line tool written in pure Go.

## Overview

go-rag allows you to:
- Index documents from various formats (txt, md, html, pdf, docx, xlsx, pptx)
- Generate embeddings using OpenAI-compatible APIs
- Perform semantic search over your documents
- Store everything locally in SQLite

## Installation

### Step 1: Download the binary

Download the appropriate binary from GitHub Releases based on your platform:

```bash
# macOS (Apple Silicon)
curl -L -o go-rag "https://github.com/liup215/go-rag/releases/latest/download/go-rag-darwin-arm64"
chmod +x go-rag

# macOS (Intel)
curl -L -o go-rag "https://github.com/liup215/go-rag/releases/latest/download/go-rag-darwin-amd64"
chmod +x go-rag

# Linux (x64)
curl -L -o go-rag "https://github.com/liup215/go-rag/releases/latest/download/go-rag-linux-amd64"
chmod +x go-rag

# Linux (ARM64)
curl -L -o go-rag "https://github.com/liup215/go-rag/releases/latest/download/go-rag-linux-arm64"
chmod +x go-rag

# Windows (PowerShell)
Invoke-WebRequest -Uri "https://github.com/liup215/go-rag/releases/latest/download/go-rag-windows-amd64.exe" -OutFile "go-rag.exe"
```

### Step 2: Move to PATH

```bash
# macOS/Linux
sudo mv go-rag /usr/local/bin/

# Windows (PowerShell)
# Create directory and move binary
New-Item -ItemType Directory -Path "$env:LOCALAPPDATA\Programs\go-rag" -Force
Move-Item go-rag.exe "$env:LOCALAPPDATA\Programs\go-rag\"

# Add to PATH (if not already added)
[Environment]::SetEnvironmentVariable("Path", $env:Path + ";$env:LOCALAPPDATA\Programs\go-rag", "User")
```

### Step 3: Initialize

```bash
go-rag init
```

## Configuration

### Option 1: OpenAI (Recommended)

```bash
go-rag config set embedding.url https://api.openai.com/v1
go-rag config set embedding.api-key sk-your-openai-key
go-rag config set embedding.model text-embedding-3-small
```

### Option 2: Ollama (Local)

First, install and run Ollama locally, then:

```bash
go-rag config set embedding.url http://localhost:11434
go-rag config set embedding.model nomic-embed-text
```

### Option 3: Other OpenAI-compatible APIs

```bash
# Example: DashScope
go-rag config set embedding.url https://dashscope.aliyuncs.com/compatible-mode/v1
go-rag config set embedding.api-key your-dashscope-key
go-rag config set embedding.model text-embedding-v3
```

## Usage

### Index Documents

```bash
# Single file
go-rag add document.pdf

# With custom chunking
go-rag add large-document.pdf --chunk-size 1024 --overlap 200
```

Adding a file whose path is already indexed does nothing: go-rag prints the
existing document ID and exits 0. Pass `--force` to delete the existing
document (chunks included) and index the file again — useful after editing a
document.

### Search

```bash
# Basic search
go-rag search "machine learning concepts"

# More results
go-rag search "project requirements" --top-k 10

# Higher similarity threshold
go-rag search "budget analysis" --threshold 0.7
```

### Manage Documents

`go-rag list` is paginated. By default it returns the 100 most recent documents
plus the total count, so you can tell whether more documents exist. Use
`--page`, `--offset`, or `--limit 0` (no limit) to see the rest, and use
`--search` / `--filter` to locate a document without scanning pages.

```bash
# First 100 documents (newest first) and the total count
go-rag list

# Next page
go-rag list --page 2

# Everything at once (only for small libraries)
go-rag list --limit 0

# Find documents by name or file path substring (case-insensitive)
go-rag list --search report

# Exact-match filters, repeatable; repeated keys mean OR
go-rag list --filter status=indexed
go-rag list --filter status=indexed --filter status=indexing
go-rag list --filter type=pdf

# Combine search, filters, and paging
go-rag list --search report --filter status=indexed --limit 50 --page 2

# Delete a document
go-rag delete <document-id>
```

Supported `--filter` keys: `status`, `type`, `name`, `path`. The footer reports
`Showing <n> of <total> documents (offset <o>)` and a next-page hint when more
documents remain — always check the total before assuming all documents were
listed.

### Inspect Chunks

```bash
# Get a specific chunk by document ID and chunk index
go-rag get-chunk <document-id> --index 0
```

#### Reading context around search results

When `go-rag search` returns a relevant chunk, the surrounding chunks often contain important context — for example, definitions, tables, diagrams, or chapter summaries. Always retrieve nearby chunks by adjusting the chunk index up and down.

```bash
# 1. Search and note the Document ID and Chunk number
go-rag search "prokaryotic cells vs eukaryotic cells"

# 2. Retrieve the matching chunk and its neighbors
go-rag get-chunk <document-id> --index 43
go-rag get-chunk <document-id> --index 44
go-rag get-chunk <document-id> --index 45
go-rag get-chunk <document-id> --index 46
```

This is especially useful for textbooks and long documents where a single chunk may start or end in the middle of a table or section.

### Personal Wiki

Use `go-rag wiki` for agent-managed symbolic memory. This is different from RAG search:
- Wiki stores complete entries in SQLite.
- Indexes (topics) are created and maintained by the agent.
- Recall is symbolic: index → entry summary → full entry.

Create an index:

```bash
go-rag wiki index-create "Architecture" --description "Design decisions"
```

Remember an entry:

```bash
# Quick note via --body
go-rag wiki remember <index-id> "SQLite WAL decision" \
  --body "We chose SQLite WAL mode to avoid SQLITE_BUSY errors."

# Longer content from a file
go-rag wiki remember <index-id> "SQLite WAL decision" --file ./sqlite-wal.md
```

`--body` and `--file` are mutually exclusive; you must provide exactly one of them.

Recall workflow:

```bash
# 1. List indexes
go-rag wiki index-list

# 2. List summaries in the chosen index
go-rag wiki list <index-id>

# 3. Read the full entry
go-rag wiki get <entry-id>
```

Update an entry:

To modify an entry body, always export it first, edit the file, then re-import:

```bash
# 1. Export the body to a file
go-rag wiki export <entry-id> --file ./draft.md

# 2. Edit draft.md with any editor

# 3. Re-import the file
go-rag wiki update <entry-id> --file ./draft.md
```

You can also update only the title:

```bash
go-rag wiki update <entry-id> --title "New title"
```

Or update both at once:

```bash
go-rag wiki update <entry-id> --title "New title" --file ./draft.md
```

Delete:

```bash
go-rag wiki forget <entry-id>
go-rag wiki index-delete <index-id>
```

## Common Workflows

### Setting up a new knowledge base

```bash
# 1. Install and initialize
go-rag init

# 2. Configure embedding service
go-rag config set embedding.url https://api.openai.com/v1
go-rag config set embedding.api-key $OPENAI_API_KEY

# 3. Add documents
go-rag add ~/Documents/*.pdf
go-rag add ~/Notes/*.md

# 4. Search
go-rag search "meeting notes from last week"
```

### Working with different file types

```bash
# Office documents
go-rag add report.docx
go-rag add data.xlsx
go-rag add presentation.pptx

# Web content
go-rag add page.html

# Code documentation
go-rag add README.md
go-rag add api-docs.xml
```

## Troubleshooting

### "embedding API key not configured"

Run:
```bash
go-rag config set embedding.api-key your-api-key
```

### "no text content extracted from file"

- For PDFs: Ensure it's a text-based PDF, not scanned images
- For Office files: Make sure they're .docx/.xlsx/.pptx (not older .doc/.xls/.ppt)

### Database locked errors

go-rag uses SQLite with WAL mode. If you see locking errors:
- Wait a moment and retry
- Ensure no other go-rag process is running

## Configuration Reference

| Key | Description | Default |
|-----|-------------|---------|
| `embedding.url` | API base URL | `https://api.openai.com/v1` |
| `embedding.api-key` | API key | (none) |
| `embedding.model` | Model name | `text-embedding-3-small` |
| `chunking.max-tokens` | Chunk size | `512` |
| `chunking.overlap` | Overlap size | `100` |
| `storage.path` | Database path | Platform-specific |

## Tips

1. **Chunk size**: Larger chunks (1024+) preserve more context but cost more tokens. Smaller chunks (256) are more precise but may lose context.

2. **Overlap**: Higher overlap (20-25% of chunk size) helps maintain continuity between chunks.

3. **API costs**: Embedding costs are based on token count. Monitor your usage when indexing large documents.

4. **Local models**: Ollama provides free local embeddings but requires running the server locally.

## Resources

- GitHub: https://github.com/liup215/go-rag
- Releases: https://github.com/liup215/go-rag/releases
- Issues: https://github.com/liup215/go-rag/issues

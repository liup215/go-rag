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

```bash
# List all documents
go-rag list

# Delete a document
go-rag delete <document-id>
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

- GitHub: https://github.com/user/go-rag
- Releases: https://github.com/user/go-rag/releases
- Issues: https://github.com/user/go-rag/issues

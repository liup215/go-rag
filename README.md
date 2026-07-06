# go-rag

A lightweight RAG (Retrieval-Augmented Generation) command line tool written in pure Go.

## Features

- **Pure Go implementation** - No external dependencies
- **Multiple file formats** - Support for txt, md, html, xml, pdf, docx, xlsx, pptx
- **External embedding models** - Compatible with OpenAI, Ollama, and other OpenAI-compatible APIs
- **Hybrid search** - Vector search with keyword fallback
- **JSON storage** - Simple file-based storage, no database dependencies
- **Cross-platform** - Windows, macOS, Linux (amd64, arm64)

## Installation

### Download from GitHub Releases

1. Go to [Releases](https://github.com/user/go-rag/releases)
2. Download the appropriate binary for your platform
3. Extract and place in your PATH

### Build from source

```bash
git clone https://github.com/user/go-rag.git
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

```bash
go-rag search "machine learning"
go-rag search "project requirements" --top-k 10
go-rag search "budget analysis" --threshold 0.7
```

## Commands

| Command | Description |
|---------|-------------|
| `init` | Initialize configuration |
| `add <file>` | Add a document to the knowledge base |
| `search <query>` | Search the knowledge base |
| `list` | List all documents |
| `delete <doc-id>` | Delete a document |
| `config set <key> <value>` | Set a configuration value |
| `config get <key>` | Get a configuration value |
| `config list` | List all configuration |
| `config help` | Show all available configuration options |

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

## Architecture

```
go-rag/
├── cmd/go-rag/          # CLI entry point
├── internal/
│   ├── chunker/         # Text chunking
│   ├── embedder/        # Embedding client (OpenAI/Ollama)
│   ├── parser/          # File parsers
│   ├── retriever/       # Hybrid search
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
   - Find similar vectors using cosine similarity
   - Fall back to keyword search if embedding fails

## License

MIT License

# Project Brief: go-rag

## Core requirements
- A lightweight, pure-Go RAG (Retrieval-Augmented Generation) CLI tool.
- Index documents from multiple formats (txt, md, html, pdf, docx, xlsx, pptx).
- Chunk documents, generate embeddings via OpenAI-compatible APIs, and store everything locally.
- Provide semantic + keyword hybrid search over indexed documents.
- Optional reranking, query rewriting, and corrective retrieval.

## Goals
- Simple local-first RAG workflow without external database dependencies.
- Cross-platform binary (Windows, macOS, Linux on amd64/arm64).
- Easy configuration via YAML and intuitive CLI commands.

## Current scope
- CLI commands: `init`, `add`, `search`, `list`, `delete`, `get-chunk`, `config`.
- SQLite storage for documents, chunks, and embeddings.
- OpenAI/Ollama-compatible embedding endpoints.

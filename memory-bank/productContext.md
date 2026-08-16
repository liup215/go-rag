# Product Context: go-rag

## Why this project exists
Users need a simple, self-contained tool to turn local documents into a searchable knowledge base using modern embedding models, without managing complex infrastructure.

## Problems it solves
- Converting heterogeneous documents into searchable chunks.
- Running semantic search locally with minimal setup.
- Keeping data local (SQLite) instead of relying on cloud databases.

## How it should work
1. Initialize configuration (`go-rag init`).
2. Configure embedding service (`go-rag config set ...`) for RAG features.
3. Add documents (`go-rag add <file>`) and search them (`go-rag search <query>`).
4. Inspect specific chunks (`go-rag get-chunk <doc-id> --index <n>`).
5. Use the personal wiki for agent-managed knowledge:
   - Create indexes (`go-rag wiki index-create <title>`).
   - Remember entries (`go-rag wiki remember <index-id> <title>`).
   - Recall symbolically (`index-list` → `list` → `get`).

## User experience goals
- Single binary, no runtime dependencies beyond an embedding API.
- Clear error messages and usage hints.
- Fast iteration on chunk size, overlap, and search parameters.

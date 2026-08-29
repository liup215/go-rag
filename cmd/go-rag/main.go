package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/liup215/go-rag/internal/chunker"
	"github.com/liup215/go-rag/internal/embedder"
	"github.com/liup215/go-rag/internal/parser"
	"github.com/liup215/go-rag/internal/retriever"
	"github.com/liup215/go-rag/internal/storage"
	"github.com/liup215/go-rag/pkg/config"
)

const version = "v0.4.0"

// booleanFlags names flags that take no value. reorderArgs must not treat the
// token after them as the flag's value, so "go-rag search --json <query>"
// keeps the query as a positional argument.
var booleanFlags = map[string]bool{
	"json":    true,
	"dry-run": true,
}

// reorderArgs moves flags (and their values) before positional arguments.
// The standard flag package stops parsing at the first non-flag argument,
// so this allows users to place flags after positional args.
func reorderArgs(args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			name := strings.TrimLeft(arg, "-")
			if strings.Contains(name, "=") {
				// --flag=value carries its own value.
				flags = append(flags, arg)
				i++
				continue
			}
			flags = append(flags, arg)
			if !booleanFlags[name] && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flags = append(flags, args[i+1])
				i += 2
			} else {
				i++
			}
		} else {
			positional = append(positional, arg)
			i++
		}
	}
	return append(flags, positional...)
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]

	switch command {
	case "init":
		handleInit()
	case "add":
		handleAdd()
	case "search":
		handleSearch()
	case "list":
		handleList()
	case "delete":
		handleDelete()
	case "gc":
		handleGC()
	case "get-chunk":
		handleGetChunk()
	case "config":
		handleConfig()
	case "config-help":
		printConfigHelp()
	case "wiki":
		handleWiki()
	case "version", "-v", "--version":
		fmt.Println("go-rag version", version)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", command)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("go-rag - Lightweight RAG command line tool")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  go-rag <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  init                    Initialize configuration")
	fmt.Println("  add <file>              Add a document to the knowledge base")
	fmt.Println("    --chunk-size <n>      Chunk size in tokens (default: 512)")
	fmt.Println("    --overlap <n>         Overlap size in tokens (default: 100)")
	fmt.Println("    --force               Delete the existing document at this path and re-index")
	fmt.Println("  search <query>          Search the knowledge base")
	fmt.Println("    --top-k <n>           Number of results (default: 5)")
	fmt.Println("    --threshold <f>       Similarity threshold (default: 0.5)")
	fmt.Println("    --doc-id <id>         Restrict search to a specific document ID")
	fmt.Println("    --json                Output machine-readable JSON")
	fmt.Println("  list                    List documents (paginated)")
	fmt.Println("    --limit <n>           Maximum documents to show, 0 = all (default: 100)")
	fmt.Println("    --offset <n>          Number of documents to skip (default: 0)")
	fmt.Println("    --page <n>            1-based page number (overrides --offset)")
	fmt.Println("    --search <text>       Only show documents whose name or path contains text")
	fmt.Println("    --filter <key=value>  Exact match filter, repeatable (status, type, name, path)")
	fmt.Println("    --json                Output machine-readable JSON (total, offset, documents)")
	fmt.Println("  delete <doc-id>         Delete a document and its chunks")
	fmt.Println("  gc                      Remove chunks whose document no longer exists")
	fmt.Println("    --dry-run             Report what would be deleted without deleting")
	fmt.Println("  get-chunk <doc-id>      Get a chunk by document ID and index")
	fmt.Println("    --index <n>           Chunk index (required)")
	fmt.Println("  config <set|get|list|help>       Manage configuration")
	fmt.Println("    set <key> <value>             Set a configuration value")
	fmt.Println("    get <key>                     Get a configuration value")
	fmt.Println("    list                          List all configuration")
	fmt.Println("    help                          Show available configuration options")
	fmt.Println("  wiki <subcommand>     Personal wiki commands")
	fmt.Println("    index-list                  List wiki indexes (topics)")
	fmt.Println("    index-create <title>        Create a wiki index")
	fmt.Println("    index-delete <index-id>     Delete a wiki index")
	fmt.Println("    remember <index-id> <title> Create a wiki entry")
	fmt.Println("    list <index-id>             List entries in an index")
	fmt.Println("    get <entry-id>              Show a wiki entry body")
	fmt.Println("    update <entry-id>           Update a wiki entry")
	fmt.Println("    forget <entry-id>           Delete a wiki entry")
	fmt.Println("  version                 Show version")
	fmt.Println("  help                    Show this help")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  go-rag init")
	fmt.Println("  go-rag config set embedding.url https://api.openai.com/v1")
	fmt.Println("  go-rag config set embedding.api-key sk-...")
	fmt.Println("  go-rag add document.pdf")
	fmt.Println("  go-rag search \"machine learning\" --top-k 10")
	fmt.Println("  go-rag search \"machine learning\" --json")
	fmt.Println("  go-rag get-chunk <doc-id> --index 3")
	fmt.Println("  go-rag list --page 2")
	fmt.Println("  go-rag list --json")
	fmt.Println("  go-rag list --search report --filter status=indexed --limit 50")
}

func handleInit() {
	configPath := config.ConfigPath()
	if _, err := os.Stat(configPath); err == nil {
		fmt.Println("Configuration already initialized.")
		fmt.Printf("Config file: %s\n", configPath)
		return
	}

	if err := config.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Configuration initialized.")
	fmt.Printf("Config file: %s\n", configPath)
}

func handleAdd() {
	// Parse flags
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	chunkSize := fs.Int("chunk-size", 512, "Chunk size in tokens")
	overlap := fs.Int("overlap", 100, "Overlap size in tokens")
	workers := fs.Int("workers", 10, "Number of concurrent workers (default: 10)")
	force := fs.Bool("force", false, "Delete an existing document at this path and re-index")

	fs.Parse(reorderArgs(os.Args[2:]))

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Error: file path required\n")
		fmt.Fprintf(os.Stderr, "Usage: go-rag add <file> [--chunk-size <n>] [--overlap <n>] [--workers <n>] [--force]\n")
		os.Exit(1)
	}

	// Clean the path so the same file always maps to the same record no matter
	// how it was spelled on the command line ("./report.pdf" vs "report.pdf").
	filePath := filepath.Clean(fs.Arg(0))

	// Load config
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	// Check embedding config
	if cfg.Embedding.APIKey == "" {
		fmt.Fprintf(os.Stderr, "Error: embedding API key not configured\n")
		fmt.Fprintf(os.Stderr, "Run: go-rag config set embedding.api-key <your-key>\n")
		os.Exit(1)
	}

	// Initialize storage
	store, err := storage.NewStorage(cfg.Storage.Path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing storage: %v\n", err)
		os.Exit(1)
	}

	// Duplicate detection: a document already indexed from this path is either
	// reported (skip) or deleted (--force), never silently duplicated.
	existing, err := documentsAtPath(store, filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error checking for existing document: %v\n", err)
		os.Exit(1)
	}
	if len(existing) > 0 && !*force {
		printDuplicateNotice(existing)
		return
	}

	// Parse file
	fmt.Printf("Parsing %s...\n", filePath)
	parseResult, err := parser.ParseFile(filePath)
	if err != nil {
		printParseError(filePath, err)
		os.Exit(1)
	}

	if parseResult.Decrypted {
		fmt.Println("🔓 PDF was encrypted with an empty user password; it was decrypted automatically before parsing.")
	}

	if parseResult.Text == "" {
		fmt.Fprintf(os.Stderr, "Error: no text content extracted from file\n")
		os.Exit(1)
	}

	fmt.Printf("✓ Extracted %d characters of text\n", len(parseResult.Text))

	// Chunk text
	c := chunker.NewChunkerWithSize(*chunkSize, *overlap)
	textChunks := c.Split(parseResult.Text)
	fmt.Printf("✓ Split into %d chunks\n", len(textChunks))

	if len(textChunks) == 0 {
		fmt.Fprintf(os.Stderr, "Error: no chunks produced\n")
		os.Exit(1)
	}

	// --force: drop the existing record(s) only now that parsing and chunking
	// succeeded, so a failure above never destroys already-indexed data.
	if len(existing) > 0 {
		fmt.Printf("⚠ --force: removing %d existing document(s) at this path\n", len(existing))
		for _, old := range existing {
			if err := store.DeleteDocument(old.ID); err != nil {
				fmt.Fprintf(os.Stderr, "Error removing existing document %s: %v\n", old.ID, err)
				os.Exit(1)
			}
			fmt.Printf("✓ Removed %s (status: %s)\n", old.ID, old.Status)
		}
	}

	// Create document record
	doc := &storage.Document{
		Name:        filepath.Base(filePath),
		FilePath:    filePath,
		DocType:     parseResult.FileType,
		ContentType: filepath.Ext(filePath),
		Status:      "indexing",
	}

	if err := store.CreateDocument(doc); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating document: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✓ Document ID: %s\n", doc.ID)

	// Prepare chunks
	var storageChunks []storage.Chunk
	for i, text := range textChunks {
		storageChunks = append(storageChunks, storage.Chunk{
			ID:         "", // Will be generated by storage
			DocumentID: doc.ID,
			Text:       text,
			Index:      i,
		})
	}

	// Async parallel embedding with progress
	fmt.Printf("\n🚀 Processing %d chunks with %d concurrent workers...\n\n", len(storageChunks), *workers)

	startTime := time.Now()
	totalChunks := len(storageChunks)
	completedChunks := int32(0)
	failedBatches := int32(0)

	ctx := context.Background()
	emb := embedder.NewOpenAIEmbedder(cfg.Embedding.APIKey, cfg.Embedding.URL, cfg.Embedding.Model)

	// Calculate batch size
	batchSize := emb.MaxBatchSize()
	totalBatches := (totalChunks + batchSize - 1) / batchSize

	// Channel for work distribution
	type batchWork struct {
		batchNum int
		startIdx int
		endIdx   int
	}

	workChan := make(chan batchWork, totalBatches)
	resultChan := make(chan error, totalBatches)

	// Start workers
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, *workers)

	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for work := range workChan {
				semaphore <- struct{}{} // Acquire

				batchStartTime := time.Now()
				batchChunks := storageChunks[work.startIdx:work.endIdx]
				batchTexts := make([]string, len(batchChunks))
				for i, chunk := range batchChunks {
					batchTexts[i] = chunk.Text
				}

				// Retry logic: 3 attempts
				var embeddings [][]float32
				var embedErr error

				for attempt := 1; attempt <= 3; attempt++ {
					embeddings, embedErr = emb.Embed(ctx, batchTexts)
					if embedErr == nil {
						break
					}

					if attempt < 3 {
						fmt.Printf("⚠ Batch %d/%d failed (attempt %d/3): %v, retrying...\n",
							work.batchNum, totalBatches, attempt, embedErr)
						time.Sleep(time.Duration(attempt) * time.Second) // Exponential backoff
					}
				}

				if embedErr != nil {
					fmt.Printf("✗ Batch %d/%d failed after 3 attempts: %v\n",
						work.batchNum, totalBatches, embedErr)
					atomic.AddInt32(&failedBatches, 1)
					resultChan <- embedErr
					<-semaphore // Release
					continue
				}

				// Update chunks with embeddings
				for i, emb := range embeddings {
					batchChunks[i].Embedding = emb
				}

				// Save batch immediately
				if err := store.CreateChunks(batchChunks); err != nil {
					fmt.Printf("✗ Batch %d/%d failed to save: %v\n",
						work.batchNum, totalBatches, err)
					atomic.AddInt32(&failedBatches, 1)
					resultChan <- err
					<-semaphore // Release
					continue
				}

				// Update progress
				completed := atomic.AddInt32(&completedChunks, int32(len(batchChunks)))
				_ = time.Since(batchStartTime) // Track batch duration for potential metrics
				elapsed := time.Since(startTime)

				// Calculate ETA
				progress := float64(completed) / float64(totalChunks)
				if progress > 0 {
					eta := time.Duration(float64(elapsed) / progress * (1 - progress))
					fmt.Printf("✓ Batch %d/%d completed | Progress: %d/%d (%.1f%%) | ETA: %s\n",
						work.batchNum, totalBatches, completed, totalChunks, progress*100,
						formatDuration(eta))
				} else {
					fmt.Printf("✓ Batch %d/%d completed | Progress: %d/%d (%.1f%%)\n",
						work.batchNum, totalBatches, completed, totalChunks, progress*100)
				}

				resultChan <- nil
				<-semaphore // Release
			}
		}(i)
	}

	// Distribute work
	for i := 0; i < totalChunks; i += batchSize {
		end := i + batchSize
		if end > totalChunks {
			end = totalChunks
		}
		workChan <- batchWork{
			batchNum: (i / batchSize) + 1,
			startIdx: i,
			endIdx:   end,
		}
	}
	close(workChan)

	// Wait for completion
	wg.Wait()
	close(resultChan)

	// Check results
	totalDuration := time.Since(startTime)
	hasErrors := false
	for err := range resultChan {
		if err != nil {
			hasErrors = true
		}
	}

	fmt.Printf("\n📊 Summary:\n")
	fmt.Printf("   Total chunks: %d\n", totalChunks)
	fmt.Printf("   Completed: %d\n", completedChunks)
	fmt.Printf("   Failed batches: %d\n", failedBatches)
	fmt.Printf("   Total time: %s\n", formatDuration(totalDuration))
	fmt.Printf("   Average speed: %.1f chunks/second\n", float64(completedChunks)/totalDuration.Seconds())

	// Update status or clean up on failure
	if hasErrors {
		if _, err := store.DeleteDocument(doc.ID); err != nil {
			fmt.Fprintf(os.Stderr, "Error cleaning up document after failure: %v\n", err)
		}
		fmt.Printf("\n⚠ Indexing failed: %d batch(es) failed. Document and chunks have been removed.\n", failedBatches)
	} else {
		if err := store.UpdateDocumentStatus(doc.ID, "indexed", ""); err != nil {
			fmt.Fprintf(os.Stderr, "Error updating status: %v\n", err)
		}
		fmt.Printf("\n✅ Successfully indexed %d chunks (ID: %s)\n", completedChunks, doc.ID)
	}
}

// documentsAtPath returns every document already stored at filePath, newest
// first. It powers duplicate detection in handleAdd: the "path" filter is an
// exact SQL match, so similar-looking paths are not treated as duplicates.
func documentsAtPath(store storage.Storage, filePath string) ([]storage.Document, error) {
	return store.ListDocuments(storage.DocumentQuery{
		Filters: map[string][]string{"path": {filePath}},
	})
}

// printDuplicateNotice reports an add that was skipped because the file path
// is already indexed, and how to force a rebuild.
func printDuplicateNotice(docs []storage.Document) {
	doc := docs[0]
	fmt.Println("⚠ Document already exists for this path, skipping ingestion.")
	fmt.Printf("  Doc ID: %s\n", doc.ID)
	fmt.Printf("  Name:   %s\n", doc.Name)
	fmt.Printf("  Path:   %s\n", doc.FilePath)
	fmt.Printf("  Status: %s\n", doc.Status)
	if len(docs) > 1 {
		fmt.Printf("  Note:   %d older duplicate document(s) also exist for this path.\n", len(docs)-1)
	}
	fmt.Println("  Re-run with --force to delete the existing document(s) and re-index.")
}

// printParseError reports a ParseFile failure together with the hints that
// help most. Encryption is called out explicitly: an encrypted PDF fails as "0
// pages" or "no text extracted", which is easy to misread as a scanned or
// wrong file.
func printParseError(filePath string, err error) {
	fmt.Fprintf(os.Stderr, "Error parsing file: %v\n", err)
	for _, hint := range parseFailureHints(filePath, err) {
		fmt.Fprintf(os.Stderr, "%s\n", hint)
	}
}

// parseFailureHints returns follow-up hints for a ParseFile failure, empty when
// none apply (path problems, non-PDF files the user can judge themselves).
func parseFailureHints(filePath string, err error) []string {
	if !strings.EqualFold(filepath.Ext(filePath), ".pdf") || errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if errors.Is(err, parser.ErrPDFEncrypted) {
		return []string{
			"Hint: this PDF is protected by a password go-rag does not have (it already tried an empty user password).",
			"Decrypt it once, then add the decrypted copy:",
			fmt.Sprintf("  qpdf --decrypt \"%s\" decrypted.pdf", filePath),
			// The paths go through argv so no shell and no Python string has to
			// quote a Windows path with its backslashes.
			fmt.Sprintf("  python -c \"import pikepdf, sys; pikepdf.open(sys.argv[1]).save(sys.argv[2])\" \"%s\" decrypted.pdf   # also rebuilds a damaged xref", filePath),
		}
	}

	return []string{
		"Hint: go-rag already tries to decrypt password-protected PDFs (empty user password).",
		"If that did not apply, the PDF may be scanned (image-only) and contain no text to extract —",
		"run OCR on it first, or rebuild it with `qpdf --decrypt` / pikepdf and try again.",
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	} else if d < time.Hour {
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	} else {
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func handleSearch() {
	// Parse flags
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	topK := fs.Int("top-k", 5, "Number of results")
	threshold := fs.Float64("threshold", 0.5, "Similarity threshold")
	docID := fs.String("doc-id", "", "Restrict search to a specific document ID")
	asJSON := fs.Bool("json", false, "Output machine-readable JSON")

	fs.Parse(reorderArgs(os.Args[2:]))

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Error: query required\n")
		fmt.Fprintf(os.Stderr, "Usage: go-rag search <query> [--top-k <n>] [--threshold <f>] [--doc-id <id>] [--json]\n")
		os.Exit(1)
	}

	query := fs.Arg(0)

	// Load config
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	// Initialize storage
	store, err := storage.NewStorage(cfg.Storage.Path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing storage: %v\n", err)
		os.Exit(1)
	}

	// Initialize embedder if API key is available
	var emb embedder.Embedder
	if cfg.Embedding.APIKey != "" {
		emb = embedder.NewOpenAIEmbedder(cfg.Embedding.APIKey, cfg.Embedding.URL, cfg.Embedding.Model)
	}

	// Initialize retriever
	ret := retriever.NewRetriever(store, emb)
	ret.SetThreshold(*threshold)

	// Attach reranker when configured and enabled.
	if cfg.Reranker.Enabled && cfg.Reranker.URL != "" {
		rr := retriever.NewCrossEncoderReranker(cfg.Reranker.URL, cfg.Reranker.APIKey, cfg.Reranker.Model)
		ret.SetReranker(rr)
	}

	// Attach query rewriter when configured and enabled.
	if cfg.QueryRewrite.Enabled {
		var qr retriever.QueryRewriter = retriever.NewRuleBasedQueryRewriter()
		if cfg.QueryRewrite.URL != "" {
			qr = retriever.NewLLMQueryRewriter(cfg.QueryRewrite.URL, cfg.QueryRewrite.APIKey, cfg.QueryRewrite.Model)
		}
		ret.SetQueryRewriter(qr, cfg.QueryRewrite.MaxQueries)
	}

	// Attach corrective evaluator and optional web fallback when enabled.
	if cfg.Corrective.Enabled {
		var evaluator retriever.QAEvaluator = retriever.NewHeuristicQAEvaluator()
		if cfg.Corrective.EvaluatorURL != "" {
			evaluator = retriever.NewLLMQAEvaluator(cfg.Corrective.EvaluatorURL, cfg.Corrective.APIKey, cfg.Corrective.Model)
		}
		ret.SetQAEvaluator(evaluator)
		if cfg.Corrective.WebSearchURL != "" {
			ret.SetWebSearcher(retriever.NewHTTPWebSearcher(cfg.Corrective.WebSearchURL, cfg.Corrective.APIKey))
		}
	}

	// Search
	ctx := context.Background()
	results, err := ret.Search(ctx, retriever.SearchOptions{
		Query:      query,
		TopK:       *topK,
		Threshold:  *threshold,
		DocumentID: *docID,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error searching: %v\n", err)
		os.Exit(1)
	}

	// Resolve the documents behind the hits so results can say where they
	// came from (name and file path), not just the document UUID.
	docs, err := loadDocumentsForResults(store, results)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading documents: %v\n", err)
		os.Exit(1)
	}

	if *asJSON {
		output := searchOutputJSON{
			Query:   query,
			Count:   len(results),
			Results: buildSearchResultsJSON(results, docs),
		}
		if err := writeJSON(output); err != nil {
			fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Display results
	if len(results) == 0 {
		fmt.Println("No results found.")
		return
	}

	fmt.Printf("Found %d results:\n\n", len(results))
	for i, result := range results {
		docName, docPath := documentDisplayInfo(docs, result.Chunk.DocumentID)
		fmt.Printf("--- Result %d (score: %.4f) ---\n", i+1, result.Score)
		fmt.Printf("Document ID:   %s\n", result.Chunk.DocumentID)
		fmt.Printf("Document Name: %s\n", docName)
		fmt.Printf("Document Path: %s\n", docPath)
		fmt.Printf("Chunk %d:\n", result.Chunk.Index)
		fmt.Println(result.Chunk.Text)
		fmt.Println()
	}
}

// searchResultJSON is the machine-readable form of one search hit.
type searchResultJSON struct {
	DocumentID   string  `json:"document_id"`
	DocumentName string  `json:"document_name"`
	DocumentPath string  `json:"document_path"`
	Score        float64 `json:"score"`
	ChunkID      string  `json:"chunk_id"`
	ChunkIndex   int     `json:"chunk_index"`
	Text         string  `json:"text"`
}

// searchOutputJSON is the top-level payload of `go-rag search --json`.
type searchOutputJSON struct {
	Query   string             `json:"query"`
	Count   int                `json:"count"`
	Results []searchResultJSON `json:"results"`
}

// listOutputJSON is the top-level payload of `go-rag list --json`.
type listOutputJSON struct {
	Total     int                `json:"total"`
	Offset    int                `json:"offset"`
	Count     int                `json:"count"`
	Documents []storage.Document `json:"documents"`
}

// nonNilDocuments normalizes a document slice so empty pages marshal as []
// instead of null.
func nonNilDocuments(docs []storage.Document) []storage.Document {
	if docs == nil {
		return []storage.Document{}
	}
	return docs
}

// loadDocumentsForResults resolves the documents referenced by search results.
// Each distinct document ID is fetched exactly once and the returned map is
// keyed by document ID; an entry is nil when the document no longer exists
// (e.g. orphaned chunks of a deleted document).
func loadDocumentsForResults(store storage.Storage, results []storage.SearchResult) (map[string]*storage.Document, error) {
	ids := make([]string, 0, len(results))
	seen := make(map[string]struct{}, len(results))
	for _, result := range results {
		id := result.Chunk.DocumentID
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	docs := make(map[string]*storage.Document, len(ids))
	for _, id := range ids {
		doc, err := store.GetDocument(id)
		if err != nil {
			return nil, err
		}
		docs[id] = doc
	}
	return docs, nil
}

// documentDisplayInfo returns the name and file path shown for a search hit.
// Documents that cannot be resolved degrade to a placeholder instead of
// hiding the hit.
func documentDisplayInfo(docs map[string]*storage.Document, docID string) (name, path string) {
	doc := docs[docID]
	if doc == nil {
		return "(unknown)", "(unknown)"
	}
	return doc.Name, doc.FilePath
}

// buildSearchResultsJSON converts search hits into their JSON form, enriching
// each hit with the document name and path when the document still exists.
// The returned slice is never nil so empty result sets marshal as [].
func buildSearchResultsJSON(results []storage.SearchResult, docs map[string]*storage.Document) []searchResultJSON {
	out := make([]searchResultJSON, 0, len(results))
	for _, result := range results {
		item := searchResultJSON{
			DocumentID: result.Chunk.DocumentID,
			Score:      result.Score,
			ChunkID:    result.Chunk.ID,
			ChunkIndex: result.Chunk.Index,
			Text:       result.Chunk.Text,
		}
		if doc := docs[result.Chunk.DocumentID]; doc != nil {
			item.DocumentName = doc.Name
			item.DocumentPath = doc.FilePath
		}
		out = append(out, item)
	}
	return out
}

// writeJSON prints v as indented JSON on stdout. HTML escaping is disabled so
// file paths and chunk text stay readable.
func writeJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// filterFlags collects repeated --filter key=value flags into a map of
// filter keys to accepted values.
type filterFlags map[string][]string

// String implements flag.Value.
func (f filterFlags) String() string {
	if len(f) == 0 {
		return ""
	}
	keys := make([]string, 0, len(f))
	for key := range f {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var parts []string
	for _, key := range keys {
		for _, value := range f[key] {
			parts = append(parts, key+"="+value)
		}
	}
	return strings.Join(parts, ", ")
}

// Set implements flag.Value. It is called once per --filter occurrence.
func (f filterFlags) Set(value string) error {
	key, val, found := strings.Cut(value, "=")
	key = strings.TrimSpace(key)
	val = strings.TrimSpace(val)
	if !found || key == "" || val == "" {
		return fmt.Errorf("expected non-empty key=value, got %q", value)
	}
	f[key] = append(f[key], val)
	return nil
}

// resolveListOffset converts --limit/--offset/--page into the effective offset.
// A positive --page overrides --offset; --limit 0 (list all) cannot be paged.
func resolveListOffset(limit, offset, page int) (int, error) {
	if limit < 0 {
		return 0, fmt.Errorf("--limit must be >= 0, got %d", limit)
	}
	if offset < 0 {
		return 0, fmt.Errorf("--offset must be >= 0, got %d", offset)
	}
	if page < 0 {
		return 0, fmt.Errorf("--page must be >= 1, got %d", page)
	}
	if page == 0 {
		return offset, nil
	}
	if limit == 0 {
		return 0, fmt.Errorf("--page cannot be combined with --limit 0 (which lists everything)")
	}
	return (page - 1) * limit, nil
}

func handleList() {
	// Parse flags
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	limit := fs.Int("limit", 100, "Maximum documents to show (0 = all)")
	offset := fs.Int("offset", 0, "Number of documents to skip")
	page := fs.Int("page", 0, "1-based page number (overrides --offset)")
	search := fs.String("search", "", "Only show documents whose name or file path contains this text")
	asJSON := fs.Bool("json", false, "Output machine-readable JSON")
	filters := filterFlags{}
	fs.Var(&filters, "filter", "Exact match filter key=value, repeatable (keys: status, type, name, path)")

	fs.Parse(reorderArgs(os.Args[2:]))

	effectiveOffset, err := resolveListOffset(*limit, *offset, *page)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		fmt.Fprintf(os.Stderr, "Usage: go-rag list [--limit <n>] [--offset <n>] [--page <n>] [--search <text>] [--filter <key=value>]\n")
		os.Exit(1)
	}

	// Load config
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	// Initialize storage
	store, err := storage.NewStorage(cfg.Storage.Path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing storage: %v\n", err)
		os.Exit(1)
	}

	query := storage.DocumentQuery{
		Search:  *search,
		Filters: filters,
		Limit:   *limit,
		Offset:  effectiveOffset,
	}

	// Count all matching documents so callers know whether more pages exist.
	total, err := store.CountDocuments(query)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error querying documents: %v\n", err)
		os.Exit(1)
	}

	// List documents
	docs, err := store.ListDocuments(query)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error querying documents: %v\n", err)
		os.Exit(1)
	}

	if *asJSON {
		output := listOutputJSON{
			Total:     total,
			Offset:    effectiveOffset,
			Count:     len(docs),
			Documents: nonNilDocuments(docs),
		}
		if err := writeJSON(output); err != nil {
			fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if len(docs) == 0 {
		if total == 0 {
			if *search != "" || len(filters) > 0 {
				fmt.Println("No documents found matching the given filters.")
			} else {
				fmt.Println("No documents found.")
			}
			return
		}
		fmt.Printf("No documents at offset %d (total: %d). Use a smaller --offset or --page.\n", effectiveOffset, total)
		return
	}

	fmt.Printf("%-36s %-20s %-12s %-20s %s\n", "ID", "Name", "Status", "Type", "Created")
	fmt.Println(strings.Repeat("-", 120))

	for _, doc := range docs {
		name := doc.Name
		if len(name) > 18 {
			name = name[:15] + "..."
		}
		fmt.Printf("%-36s %-20s %-12s %-20s %s\n",
			doc.ID,
			name,
			doc.Status,
			doc.DocType,
			doc.CreatedAt.Format(time.RFC3339),
		)
	}

	fmt.Printf("\nShowing %d of %d documents (offset %d)\n", len(docs), total, effectiveOffset)
	if remaining := total - effectiveOffset - len(docs); remaining > 0 {
		nextOffset := effectiveOffset + len(docs)
		fmt.Printf("%d more document(s) available. Use --offset %d to see the next page.\n", remaining, nextOffset)
	}
}

// errDocumentNotFound reports that no document with the given ID exists.
var errDocumentNotFound = errors.New("document not found")

// deleteDocumentChecked deletes a document and its chunks after verifying that
// the document exists, so callers can tell a typo'd/unknown ID apart from a
// successful delete.
//
// The check happens BEFORE the delete. DeleteDocument reports success with a
// chunk count of 0 for both an unknown ID and a document that simply has no
// chunks, and once the delete has run the document is gone either way — a
// post-delete lookup cannot distinguish the two and would misreport deleting a
// chunk-less document (e.g. one left behind by an interrupted add) as "not
// found". If the document disappears between the check and the delete (another
// process won the race), the delete still succeeds and the end state — the
// document gone — is what the caller asked for.
func deleteDocumentChecked(store storage.Storage, id string) (int64, error) {
	doc, err := store.GetDocument(id)
	if err != nil {
		return 0, fmt.Errorf("checking document %s: %w", id, err)
	}
	if doc == nil {
		return 0, errDocumentNotFound
	}
	return store.DeleteDocument(id)
}

func handleDelete() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Error: document ID required\n")
		fmt.Fprintf(os.Stderr, "Usage: go-rag delete <doc-id>\n")
		os.Exit(1)
	}

	docID := os.Args[2]

	// Load config
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	// Initialize storage
	store, err := storage.NewStorage(cfg.Storage.Path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing storage: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	// Delete document and its chunks (cascade, in one transaction). The
	// document is verified to exist first, so an unknown ID is an error rather
	// than a silent success.
	chunksDeleted, err := deleteDocumentChecked(store, docID)
	if err != nil {
		if errors.Is(err, errDocumentNotFound) {
			fmt.Fprintf(os.Stderr, "Error: document %s not found\n", docID)
		} else {
			fmt.Fprintf(os.Stderr, "Error deleting document: %v\n", err)
		}
		os.Exit(1)
	}

	fmt.Printf("Document deleted successfully (%d chunk(s) removed).\n", chunksDeleted)
}

// handleGC removes orphan chunks — chunks whose document no longer exists.
// Such rows are historical leftovers from deletes issued before cascade
// deletes were reliable; today's deletes remove chunks in the same
// transaction, so gc is only needed to clean up pre-existing databases.
func handleGC() {
	fs := flag.NewFlagSet("gc", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "Report what would be deleted without deleting")
	fs.Parse(reorderArgs(os.Args[2:]))

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	store, err := storage.NewStorage(cfg.Storage.Path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing storage: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	orphans, err := store.CountOrphanChunks()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error counting orphan chunks: %v\n", err)
		os.Exit(1)
	}

	if orphans == 0 {
		fmt.Println("No orphan chunks found.")
		return
	}

	fmt.Printf("Found %d orphan chunk(s) whose document no longer exists.\n", orphans)

	if *dryRun {
		fmt.Println("Dry run: nothing deleted. Run 'go-rag gc' to remove them.")
		return
	}

	deleted, err := store.DeleteOrphanChunks()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error deleting orphan chunks: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Deleted %d orphan chunk(s).\n", deleted)
}

func handleGetChunk() {
	fs := flag.NewFlagSet("get-chunk", flag.ExitOnError)
	index := fs.Int("index", -1, "Chunk index (required)")

	fs.Parse(reorderArgs(os.Args[2:]))

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Error: document ID required\n")
		fmt.Fprintf(os.Stderr, "Usage: go-rag get-chunk <doc-id> --index <n>\n")
		os.Exit(1)
	}

	if *index < 0 {
		fmt.Fprintf(os.Stderr, "Error: --index is required and must be >= 0\n")
		fmt.Fprintf(os.Stderr, "Usage: go-rag get-chunk <doc-id> --index <n>\n")
		os.Exit(1)
	}

	docID := fs.Arg(0)

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	store, err := storage.NewStorage(cfg.Storage.Path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing storage: %v\n", err)
		os.Exit(1)
	}

	chunk, err := store.GetChunkByIndex(docID, *index)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting chunk: %v\n", err)
		os.Exit(1)
	}

	if chunk == nil {
		fmt.Fprintf(os.Stderr, "Error: chunk not found for document %s at index %d\n", docID, *index)
		os.Exit(1)
	}

	fmt.Printf("Document ID: %s\n", chunk.DocumentID)
	fmt.Printf("Chunk ID:    %s\n", chunk.ID)
	fmt.Printf("Chunk Index: %d\n", chunk.Index)
	fmt.Printf("Created At:  %s\n", chunk.CreatedAt.Format(time.RFC3339))
	fmt.Println()
	fmt.Println(chunk.Text)
}

func handleConfig() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Error: config subcommand required\n")
		fmt.Fprintf(os.Stderr, "Usage: go-rag config <set|get> <key> [value]\n")
		os.Exit(1)
	}

	subcommand := os.Args[2]

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	switch subcommand {
	case "set":
		if len(os.Args) < 5 {
			fmt.Fprintf(os.Stderr, "Error: key and value required\n")
			fmt.Fprintf(os.Stderr, "Usage: go-rag config set <key> <value>\n")
			os.Exit(1)
		}
		key := os.Args[3]
		value := os.Args[4]

		if err := cfg.Set(key, value); err != nil {
			fmt.Fprintf(os.Stderr, "Error setting config: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Set %s = %s\n", key, value)

	case "get":
		if len(os.Args) < 4 {
			fmt.Fprintf(os.Stderr, "Error: key required\n")
			fmt.Fprintf(os.Stderr, "Usage: go-rag config get <key>\n")
			os.Exit(1)
		}
		key := os.Args[3]

		value, err := cfg.GetDisplay(key)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting config: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%s = %s\n", key, value)

	case "list":
		fmt.Println("Current configuration:")
		fmt.Printf("  embedding.url = %s\n", cfg.Embedding.URL)
		fmt.Printf("  embedding.api-key = %s\n", maskAPIKey(cfg.Embedding.APIKey))
		fmt.Printf("  embedding.model = %s\n", cfg.Embedding.Model)
		fmt.Printf("  chunking.max-tokens = %d\n", cfg.Chunking.MaxTokens)
		fmt.Printf("  chunking.overlap = %d\n", cfg.Chunking.Overlap)
		fmt.Printf("  storage.path = %s\n", cfg.Storage.Path)
		fmt.Printf("  reranker.enabled = %v\n", cfg.Reranker.Enabled)
		fmt.Printf("  reranker.url = %s\n", cfg.Reranker.URL)
		rerankerKey, _ := cfg.GetDisplay("reranker.api-key")
		fmt.Printf("  reranker.api-key = %s\n", rerankerKey)
		fmt.Printf("  reranker.model = %s\n", cfg.Reranker.Model)
		fmt.Printf("  corrective.enabled = %v\n", cfg.Corrective.Enabled)
		fmt.Printf("  corrective.evaluator-url = %s\n", cfg.Corrective.EvaluatorURL)
		correctiveKey, _ := cfg.GetDisplay("corrective.api-key")
		fmt.Printf("  corrective.api-key = %s\n", correctiveKey)
		fmt.Printf("  corrective.model = %s\n", cfg.Corrective.Model)
		fmt.Printf("  corrective.web-search-url = %s\n", cfg.Corrective.WebSearchURL)
		fmt.Printf("  query-rewrite.enabled = %v\n", cfg.QueryRewrite.Enabled)
		fmt.Printf("  query-rewrite.max-queries = %d\n", cfg.QueryRewrite.MaxQueries)
		fmt.Printf("  query-rewrite.url = %s\n", cfg.QueryRewrite.URL)
		queryRewriteKey, _ := cfg.GetDisplay("query-rewrite.api-key")
		fmt.Printf("  query-rewrite.api-key = %s\n", queryRewriteKey)
		fmt.Printf("  query-rewrite.model = %s\n", cfg.QueryRewrite.Model)
		fmt.Printf("\nConfig file: %s\n", config.ConfigPath())

	case "help":
		printConfigHelp()

	default:
		fmt.Fprintf(os.Stderr, "Unknown config subcommand: %s\n", subcommand)
		fmt.Fprintf(os.Stderr, "Usage: go-rag config <set|get|list|help>\n")
		fmt.Fprintf(os.Stderr, "Run 'go-rag config help' for available options\n")
		os.Exit(1)
	}
}

func maskAPIKey(key string) string {
	if key == "" {
		return "(not set)"
	}
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

func printConfigHelp() {
	fmt.Println("go-rag configuration options:")
	fmt.Println()
	fmt.Println("Usage: go-rag config <set|get|list|help> [key] [value]")
	fmt.Println()
	fmt.Println("Available configuration keys:")
	fmt.Println()

	items := config.GetConfigItems()

	// Group by category
	categories := make(map[string][]config.ConfigItem)
	for _, item := range items {
		categories[item.Category] = append(categories[item.Category], item)
	}

	// Print by category
	for _, category := range []string{"embedding", "chunking", "storage", "reranker", "corrective", "query-rewrite"} {
		if items, ok := categories[category]; ok {
			fmt.Printf("[%s]\n", category)
			for _, item := range items {
				required := ""
				if item.Required {
					required = " (required)"
				}
				fmt.Printf("  %-25s %s%s\n", item.Key, item.Description, required)
				fmt.Printf("                           Default: %s\n", item.Default)
				fmt.Println()
			}
		}
	}

	fmt.Println("Examples:")
	fmt.Println("  go-rag config set embedding.url https://api.openai.com/v1")
	fmt.Println("  go-rag config set embedding.api-key sk-xxxxxxxx")
	fmt.Println("  go-rag config set embedding.model text-embedding-3-small")
	fmt.Println("  go-rag config get embedding.url")
	fmt.Println("  go-rag config list")
}

// Helper function for string conversion
func strconvParseFloat(s string, bitSize int) (float64, error) {
	return strconv.ParseFloat(s, bitSize)
}

// ---- Wiki commands -------------------------------------------------------

func printWikiUsage() {
	fmt.Println("go-rag wiki - Personal wiki commands")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  go-rag wiki <subcommand> [options]")
	fmt.Println()
	fmt.Println("Subcommands:")
	fmt.Println("  index-list                          List all wiki indexes (topics)")
	fmt.Println("  index-create <title>                Create a new wiki index")
	fmt.Println("    --description <text>              Optional index description")
	fmt.Println("  index-delete <index-id>             Delete a wiki index and its entries")
	fmt.Println("  remember <index-id> <title>         Create a wiki entry under an index")
	fmt.Println("    --body <text>                     Entry body")
	fmt.Println("    --file <path>                     Entry body from file")
	fmt.Println("  list <index-id>                     List entries in an index")
	fmt.Println("  get <entry-id>                      Show full body of a wiki entry")
	fmt.Println("  update <entry-id>                   Update a wiki entry")
	fmt.Println("    --title <text>                    New title")
	fmt.Println("    --file <path>                     New body from file")
	fmt.Println("  export <entry-id> --file <path>     Export entry body to a file")
	fmt.Println("  forget <entry-id>                   Delete a wiki entry")
	fmt.Println("  help                                Show this help")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  go-rag wiki index-create Architecture --description 'Design decisions'")
	fmt.Println("  go-rag wiki remember <index-id> 'SQLite decision' --body 'We chose SQLite WAL'")
	fmt.Println("  go-rag wiki remember <index-id> 'SQLite decision' --file ./sqlite-wal.md")
	fmt.Println("  go-rag wiki export <entry-id> --file ./draft.md")
	fmt.Println("  go-rag wiki update <entry-id> --file ./draft.md")
	fmt.Println("  go-rag wiki list <index-id>")
	fmt.Println("  go-rag wiki get <entry-id>")
}

func handleWiki() {
	if len(os.Args) < 3 {
		printWikiUsage()
		os.Exit(1)
	}

	subcommand := os.Args[2]
	switch subcommand {
	case "index-list":
		handleWikiIndexList()
	case "index-create":
		handleWikiIndexCreate()
	case "index-delete":
		handleWikiIndexDelete()
	case "remember":
		handleWikiRemember()
	case "list":
		handleWikiList()
	case "get":
		handleWikiGet()
	case "update":
		handleWikiUpdate()
	case "export":
		handleWikiExport()
	case "forget":
		handleWikiForget()
	case "help", "-h", "--help":
		printWikiUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown wiki subcommand: %s\n", subcommand)
		printWikiUsage()
		os.Exit(1)
	}
}

func loadWikiStore() (*config.Config, storage.Storage, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, fmt.Errorf("loading config: %w", err)
	}
	store, err := storage.NewStorage(cfg.Storage.Path)
	if err != nil {
		return nil, nil, fmt.Errorf("opening storage: %w", err)
	}
	return cfg, store, nil
}

func readStdinIfAvailable() string {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return ""
	}
	if stat.Mode()&os.ModeCharDevice != 0 {
		return ""
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func handleWikiIndexList() {
	_, store, err := loadWikiStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	indexes, err := store.ListWikiIndexes()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing indexes: %v\n", err)
		os.Exit(1)
	}

	if len(indexes) == 0 {
		fmt.Println("No wiki indexes found.")
		return
	}

	fmt.Printf("%-36s %-20s %s\n", "ID", "Title", "Description")
	fmt.Println(strings.Repeat("-", 100))
	for _, idx := range indexes {
		title := truncateString(idx.Title, 18)
		desc := truncateString(idx.Description, 40)
		fmt.Printf("%-36s %-20s %s\n", idx.ID, title, desc)
	}
	fmt.Printf("\nTotal: %d indexes\n", len(indexes))
}

func handleWikiIndexCreate() {
	fs := flag.NewFlagSet("index-create", flag.ExitOnError)
	description := fs.String("description", "", "Index description")
	fs.Parse(reorderArgs(os.Args[3:]))

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Error: title required\n")
		fmt.Fprintf(os.Stderr, "Usage: go-rag wiki index-create <title> [--description <text>]\n")
		os.Exit(1)
	}

	_, store, err := loadWikiStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	idx := &storage.WikiIndex{
		Title:       fs.Arg(0),
		Description: *description,
	}
	if err := store.CreateWikiIndex(idx); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating index: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Created index: %s\n", idx.ID)
}

func handleWikiIndexDelete() {
	if len(os.Args) < 4 {
		fmt.Fprintf(os.Stderr, "Error: index ID required\n")
		fmt.Fprintf(os.Stderr, "Usage: go-rag wiki index-delete <index-id>\n")
		os.Exit(1)
	}

	_, store, err := loadWikiStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	id := os.Args[3]
	if err := store.DeleteWikiIndex(id); err != nil {
		fmt.Fprintf(os.Stderr, "Error deleting index: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Index deleted successfully.")
}

func handleWikiRemember() {
	fs := flag.NewFlagSet("remember", flag.ExitOnError)
	bodyFlag := fs.String("body", "", "Entry body")
	fileFlag := fs.String("file", "", "Path to file containing entry body")
	fs.Parse(reorderArgs(os.Args[3:]))

	if fs.NArg() < 2 {
		fmt.Fprintf(os.Stderr, "Error: index ID and title required\n")
		fmt.Fprintf(os.Stderr, "Usage: go-rag wiki remember <index-id> <title> [--body <text> | --file <path>]\n")
		os.Exit(1)
	}

	indexID := strings.TrimSpace(fs.Arg(0))
	title := strings.TrimSpace(fs.Arg(1))
	if indexID == "" {
		fmt.Fprintf(os.Stderr, "Error: index ID required\n")
		os.Exit(1)
	}
	if title == "" {
		fmt.Fprintf(os.Stderr, "Error: entry title required\n")
		os.Exit(1)
	}

	if *bodyFlag != "" && *fileFlag != "" {
		fmt.Fprintf(os.Stderr, "Error: cannot use both --body and --file\n")
		os.Exit(1)
	}

	body := ""
	if *bodyFlag != "" {
		body = strings.TrimSpace(*bodyFlag)
	} else if *fileFlag != "" {
		data, err := os.ReadFile(*fileFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot read file: %v\n", err)
			os.Exit(1)
		}
		body = strings.TrimSpace(string(data))
	}
	if body == "" {
		fmt.Fprintf(os.Stderr, "Error: entry body required (use --body or --file)\n")
		os.Exit(1)
	}

	_, store, err := loadWikiStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	idx, err := store.GetWikiIndex(indexID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error checking index: %v\n", err)
		os.Exit(1)
	}
	if idx == nil {
		fmt.Fprintf(os.Stderr, "Error: index %s not found\n", indexID)
		os.Exit(1)
	}

	entry := &storage.WikiEntry{
		IndexID: indexID,
		Title:   title,
		Body:    body,
	}
	if err := store.CreateWikiEntry(entry); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating wiki entry: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Created wiki entry: %s\n", entry.ID)
}

func handleWikiList() {
	if len(os.Args) < 4 {
		fmt.Fprintf(os.Stderr, "Error: index ID required\n")
		fmt.Fprintf(os.Stderr, "Usage: go-rag wiki list <index-id>\n")
		os.Exit(1)
	}

	indexID := os.Args[3]
	_, store, err := loadWikiStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	idx, err := store.GetWikiIndex(indexID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error checking index: %v\n", err)
		os.Exit(1)
	}
	if idx == nil {
		fmt.Fprintf(os.Stderr, "Error: index %s not found\n", indexID)
		os.Exit(1)
	}

	entries, err := store.ListWikiEntries(indexID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing entries: %v\n", err)
		os.Exit(1)
	}

	if len(entries) == 0 {
		fmt.Println("No entries found in this index.")
		return
	}

	fmt.Printf("Index: %s\n\n", idx.Title)
	fmt.Printf("%-36s %-20s %s\n", "ID", "Created", "Title")
	fmt.Println(strings.Repeat("-", 100))
	for _, entry := range entries {
		title := truncateString(entry.Title, 30)
		fmt.Printf("%-36s %-20s %s\n", entry.ID, entry.CreatedAt.Format(time.RFC3339), title)
	}
	fmt.Printf("\nTotal: %d entries\n", len(entries))
}

func handleWikiGet() {
	if len(os.Args) < 4 {
		fmt.Fprintf(os.Stderr, "Error: entry ID required\n")
		fmt.Fprintf(os.Stderr, "Usage: go-rag wiki get <entry-id>\n")
		os.Exit(1)
	}

	entryID := os.Args[3]
	_, store, err := loadWikiStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	entry, err := store.GetWikiEntry(entryID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting entry: %v\n", err)
		os.Exit(1)
	}
	if entry == nil {
		fmt.Fprintf(os.Stderr, "Error: entry %s not found\n", entryID)
		os.Exit(1)
	}

	fmt.Printf("Entry ID: %s\n", entry.ID)
	fmt.Printf("Index ID: %s\n", entry.IndexID)
	fmt.Printf("Title:    %s\n", entry.Title)
	fmt.Printf("Created:  %s\n", entry.CreatedAt.Format(time.RFC3339))
	fmt.Printf("Updated:  %s\n", entry.UpdatedAt.Format(time.RFC3339))
	fmt.Println()
	fmt.Println(entry.Body)
}

func handleWikiUpdate() {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	titleFlag := fs.String("title", "", "New title")
	fileFlag := fs.String("file", "", "Path to file containing new body")
	fs.Parse(reorderArgs(os.Args[3:]))

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Error: entry ID required\n")
		fmt.Fprintf(os.Stderr, "Usage: go-rag wiki update <entry-id> [--title <text>] [--file <path>]\n")
		os.Exit(1)
	}

	entryID := fs.Arg(0)
	_, store, err := loadWikiStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	entry, err := store.GetWikiEntry(entryID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting entry: %v\n", err)
		os.Exit(1)
	}
	if entry == nil {
		fmt.Fprintf(os.Stderr, "Error: entry %s not found\n", entryID)
		os.Exit(1)
	}

	updated := false
	if *titleFlag != "" {
		entry.Title = strings.TrimSpace(*titleFlag)
		updated = true
	}
	if *fileFlag != "" {
		data, err := os.ReadFile(*fileFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: cannot read file: %v\n", err)
			os.Exit(1)
		}
		body := strings.TrimSpace(string(data))
		if body == "" {
			fmt.Fprintf(os.Stderr, "Error: entry body cannot be empty\n")
			os.Exit(1)
		}
		entry.Body = body
		updated = true
	}
	if !updated {
		fmt.Fprintf(os.Stderr, "Error: nothing to update (use --title or --file)\n")
		os.Exit(1)
	}

	if err := store.UpdateWikiEntry(entry); err != nil {
		fmt.Fprintf(os.Stderr, "Error updating entry: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Entry updated successfully.")
}

func handleWikiExport() {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	fileFlag := fs.String("file", "", "Path to write entry body")
	fs.Parse(reorderArgs(os.Args[3:]))

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Error: entry ID required\n")
		fmt.Fprintf(os.Stderr, "Usage: go-rag wiki export <entry-id> --file <path>\n")
		os.Exit(1)
	}
	if *fileFlag == "" {
		fmt.Fprintf(os.Stderr, "Error: --file is required\n")
		os.Exit(1)
	}

	entryID := fs.Arg(0)
	_, store, err := loadWikiStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	entry, err := store.GetWikiEntry(entryID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting entry: %v\n", err)
		os.Exit(1)
	}
	if entry == nil {
		fmt.Fprintf(os.Stderr, "Error: entry %s not found\n", entryID)
		os.Exit(1)
	}

	if err := os.WriteFile(*fileFlag, []byte(entry.Body), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot write file: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Exported entry to %s\n", *fileFlag)
}

func handleWikiForget() {
	if len(os.Args) < 4 {
		fmt.Fprintf(os.Stderr, "Error: entry ID required\n")
		fmt.Fprintf(os.Stderr, "Usage: go-rag wiki forget <entry-id>\n")
		os.Exit(1)
	}

	entryID := os.Args[3]
	_, store, err := loadWikiStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	if err := store.DeleteWikiEntry(entryID); err != nil {
		fmt.Fprintf(os.Stderr, "Error deleting entry: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Entry deleted successfully.")
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/user/go-rag/internal/chunker"
	"github.com/user/go-rag/internal/embedder"
	"github.com/user/go-rag/internal/parser"
	"github.com/user/go-rag/internal/retriever"
	"github.com/user/go-rag/internal/storage"
	"github.com/user/go-rag/pkg/config"
)

const version = "0.1.0"

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
	case "config":
		handleConfig()
	case "config-help":
		printConfigHelp()
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
	fmt.Println("  search <query>          Search the knowledge base")
	fmt.Println("    --top-k <n>           Number of results (default: 5)")
	fmt.Println("    --threshold <f>       Similarity threshold (default: 0.5)")
	fmt.Println("  list                    List all documents")
	fmt.Println("  delete <doc-id>         Delete a document")
	fmt.Println("  config <set|get|list|help>       Manage configuration")
	fmt.Println("    set <key> <value>             Set a configuration value")
	fmt.Println("    get <key>                     Get a configuration value")
	fmt.Println("    list                          List all configuration")
	fmt.Println("    help                          Show available configuration options")
	fmt.Println("  version                 Show version")
	fmt.Println("  help                    Show this help")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  go-rag init")
	fmt.Println("  go-rag config set embedding.url https://api.openai.com/v1")
	fmt.Println("  go-rag config set embedding.api-key sk-...")
	fmt.Println("  go-rag add document.pdf")
	fmt.Println("  go-rag search \"machine learning\" --top-k 10")
}

func handleInit() {
	if err := config.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Configuration initialized.")
	fmt.Printf("Config file: %s\n", config.ConfigPath())
}

func handleAdd() {
	// Parse flags
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	chunkSize := fs.Int("chunk-size", 512, "Chunk size in tokens")
	overlap := fs.Int("overlap", 100, "Overlap size in tokens")
	workers := fs.Int("workers", 10, "Number of concurrent workers (default: 10)")

	fs.Parse(os.Args[2:])

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Error: file path required\n")
		fmt.Fprintf(os.Stderr, "Usage: go-rag add <file> [--chunk-size <n>] [--overlap <n>] [--workers <n>]\n")
		os.Exit(1)
	}

	filePath := fs.Arg(0)

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

	// Parse file
	fmt.Printf("Parsing %s...\n", filePath)
	parseResult, err := parser.ParseFile(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing file: %v\n", err)
		os.Exit(1)
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

	// Initialize storage
	store, err := storage.NewStorage(cfg.Storage.Path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing storage: %v\n", err)
		os.Exit(1)
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
			batchNum: (i/batchSize) + 1,
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
		if err := store.DeleteDocument(doc.ID); err != nil {
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

	fs.Parse(os.Args[2:])

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Error: query required\n")
		fmt.Fprintf(os.Stderr, "Usage: go-rag search <query> [--top-k <n>] [--threshold <f>]\n")
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

	// Search
	ctx := context.Background()
	results, err := ret.Search(ctx, retriever.SearchOptions{
		Query:     query,
		TopK:      *topK,
		Threshold: *threshold,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error searching: %v\n", err)
		os.Exit(1)
	}

	// Display results
	if len(results) == 0 {
		fmt.Println("No results found.")
		return
	}

	fmt.Printf("Found %d results:\n\n", len(results))
	for i, result := range results {
		fmt.Printf("--- Result %d (score: %.4f) ---\n", i+1, result.Score)
		fmt.Printf("Document ID: %s\n", result.Chunk.DocumentID)
		fmt.Printf("Chunk %d:\n", result.Chunk.Index)
		// Truncate text if too long
		text := result.Chunk.Text
		if len(text) > 500 {
			text = text[:500] + "..."
		}
		fmt.Println(text)
		fmt.Println()
	}
}

func handleList() {
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

	// List documents
	docs, err := store.ListDocuments(100, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing documents: %v\n", err)
		os.Exit(1)
	}

	if len(docs) == 0 {
		fmt.Println("No documents found.")
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

	fmt.Printf("\nTotal: %d documents\n", len(docs))
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

	// Delete document
	if err := store.DeleteDocument(docID); err != nil {
		fmt.Fprintf(os.Stderr, "Error deleting document: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Document deleted successfully.")
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

		value, err := cfg.Get(key)
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
	for _, category := range []string{"embedding", "chunking", "storage"} {
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

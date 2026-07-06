package chunker

import (
	"strings"
)

// Chunker splits text into token-aware chunks with configurable overlap.
type Chunker struct {
	MaxTokens      int              // target tokens per chunk
	MaxChunkTokens int              // hard cap on a single chunk
	Overlap        int              // overlap tokens
	chunkToken     func(string) int // lightweight token estimator
}

// NewChunker creates a Chunker with defaults.
func NewChunker() *Chunker {
	return &Chunker{
		MaxTokens:      512,
		MaxChunkTokens: 8192,
		Overlap:        100,
		chunkToken:     estimateTokens,
	}
}

// NewChunkerWithSize creates a Chunker with custom size.
func NewChunkerWithSize(maxTokens, overlap int) *Chunker {
	c := NewChunker()
	c.MaxTokens = maxTokens
	c.Overlap = overlap
	return c
}

// SetMaxTokens overrides the chunk size.
func (c *Chunker) SetMaxTokens(n int) {
	c.MaxTokens = n
	c.Overlap = n / 5
}

// TokenCount estimates tokens for a string.
func (c *Chunker) TokenCount(s string) int {
	return c.chunkToken(s)
}

// Split produces a sequential slice of text chunks.
func (c *Chunker) Split(text string) []string {
	if text == "" {
		return nil
	}

	paragraphs := splitParagraphs(text)
	var chunks []string
	var buf strings.Builder

	flush := func() {
		content := strings.TrimSpace(buf.String())
		if content == "" {
			return
		}

		// Hard cap: if a single paragraph exceeds MaxChunkTokens,
		// split it by sentences/words
		if c.chunkToken(content) > c.MaxChunkTokens {
			chunks = append(chunks, c.splitOversized(content)...)
		} else {
			chunks = append(chunks, content)
		}

		// Overlap strategy: keep last N tokens worth of text
		_, overlapText := c.overlapTail(content)
		buf.Reset()
		buf.WriteString(overlapText)
	}

	for _, para := range paragraphs {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}

		if buf.Len() > 0 && c.chunkToken(buf.String()+"\n\n"+para) > c.MaxTokens {
			flush()
		}

		if buf.Len() > 0 {
			buf.WriteString("\n\n")
		}
		buf.WriteString(para)
	}
	flush()

	return chunks
}

// splitOversized breaks a single too-large text into pieces.
func (c *Chunker) splitOversized(text string) []string {
	if c.chunkToken(text) <= c.MaxChunkTokens {
		return []string{text}
	}

	// Try sentence boundaries first
	sentences := splitSentences(text)
	var chunks []string
	var buf strings.Builder

	for _, s := range sentences {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}

		if buf.Len() > 0 && c.chunkToken(buf.String()+" "+s) > c.MaxChunkTokens {
			chunks = append(chunks, strings.TrimSpace(buf.String()))
			buf.Reset()
		}

		if buf.Len() > 0 {
			buf.WriteString(" ")
		}
		buf.WriteString(s)
	}

	if buf.Len() > 0 {
		chunks = append(chunks, strings.TrimSpace(buf.String()))
	}

	// If even one sentence is too long, fall back to word windows
	for i, ch := range chunks {
		if c.chunkToken(ch) > c.MaxChunkTokens {
			chunks[i] = ""
			chunks = append(chunks[:i], append([]string{}, chunks[i+1:]...)...)
			chunks = append(chunks, c.splitByWords(ch)...)
		}
	}

	return chunks
}

// splitByWords breaks text into fixed word windows.
func (c *Chunker) splitByWords(text string) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}

	var chunks []string
	var buf strings.Builder

	for _, w := range words {
		if buf.Len() > 0 && c.chunkToken(buf.String()+" "+w) > c.MaxChunkTokens {
			chunks = append(chunks, strings.TrimSpace(buf.String()))
			buf.Reset()
		}

		if buf.Len() > 0 {
			buf.WriteString(" ")
		}
		buf.WriteString(w)
	}

	if buf.Len() > 0 {
		chunks = append(chunks, strings.TrimSpace(buf.String()))
	}

	return chunks
}

// overlapTail returns the overlap text from the end.
func (c *Chunker) overlapTail(text string) (int, string) {
	words := strings.Fields(text)
	if len(words) <= c.Overlap {
		return 0, text
	}

	start := len(words) - c.Overlap
	tail := strings.Join(words[start:], " ")
	idx := strings.LastIndex(text, tail)
	if idx < 0 {
		idx = len(text) - len(tail)
		if idx < 0 {
			idx = 0
		}
	}
	return idx, tail
}

// splitParagraphs splits text into paragraphs.
func splitParagraphs(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.Split(text, "\n\n")
}

// splitSentences splits text into sentences.
func splitSentences(text string) []string {
	text = strings.ReplaceAll(text, "\n", " ")
	var out []string
	var buf strings.Builder

	for _, r := range text {
		buf.WriteRune(r)
		if r == '.' || r == '?' || r == '!' {
			out = append(out, strings.TrimSpace(buf.String()))
			buf.Reset()
		}
	}

	if buf.Len() > 0 {
		out = append(out, strings.TrimSpace(buf.String()))
	}

	if len(out) == 0 {
		out = append(out, text)
	}

	return out
}

// estimateTokens provides a rough token count.
func estimateTokens(s string) int {
	// Simple estimate: 1 token ~= 4 characters
	return len(s) / 4
}

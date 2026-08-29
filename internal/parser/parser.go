package parser

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ParseResult contains the parsed text and metadata.
type ParseResult struct {
	Text     string
	Title    string
	FileType string
	// Decrypted is set when the source PDF carried an /Encrypt dictionary and
	// was transparently decrypted (empty user password) before parsing. Callers
	// surface it so users know their file was protected.
	Decrypted bool
}

// ParseFile parses a file and extracts text content.
func ParseFile(filePath string) (*ParseResult, error) {
	// Check if file exists
	info, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("file not found: %w", err)
	}

	if info.IsDir() {
		return nil, fmt.Errorf("path is a directory, not a file")
	}

	// Read file content
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	// Determine file type by extension
	ext := strings.ToLower(filepath.Ext(filePath))

	switch ext {
	case ".txt", ".md", ".markdown", ".text":
		return parseText(string(data), ext)
	case ".html", ".htm":
		return parseHTML(string(data))
	case ".xml", ".svg":
		return parseXML(string(data))
	case ".json":
		return parseJSON(data)
	case ".yaml", ".yml":
		return parseYAML(data)
	case ".csv":
		return parseCSV(string(data))
	case ".pdf":
		return parsePDF(data)
	case ".docx":
		return parseDOCX(data)
	case ".xlsx":
		return parseXLSX(data)
	case ".pptx":
		return parsePPTX(data)
	case ".doc":
		return parseOldDOC(data)
	case ".rtf":
		return parseRTF(string(data))
	case ".go", ".py", ".js", ".ts", ".java", ".c", ".cpp", ".h", ".hpp", ".cs", ".php", ".rb", ".rs", ".swift", ".kt", ".scala", ".sh", ".bash", ".ps1", ".sql", ".css", ".scss", ".less":
		return parseCode(string(data), ext)
	default:
		// Try to parse as text
		return parseText(string(data), ext)
	}
}

// parseText parses plain text files.
func parseText(content string, ext string) (*ParseResult, error) {
	// Clean up the text
	text := cleanText(content)

	return &ParseResult{
		Text:     text,
		FileType: ext,
	}, nil
}

// parseHTML parses HTML files and extracts text.
func parseHTML(content string) (*ParseResult, error) {
	// Remove script and style tags
	text := removeTag(content, "script")
	text = removeTag(text, "style")

	// Remove all HTML tags
	text = stripTags(text)

	// Clean up
	text = cleanText(text)

	// Try to extract title
	title := extractTitle(content)

	return &ParseResult{
		Text:     text,
		Title:    title,
		FileType: ".html",
	}, nil
}

// parseXML parses XML files and extracts text.
func parseXML(content string) (*ParseResult, error) {
	// Try to parse as XML
	decoder := xml.NewDecoder(strings.NewReader(content))

	var text strings.Builder
	inCharData := false

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			// If XML parsing fails, treat as text
			return parseText(content, ".xml")
		}

		switch t := token.(type) {
		case xml.CharData:
			if inCharData {
				text.WriteString(string(t))
			}
		case xml.StartElement:
			inCharData = true
		case xml.EndElement:
			inCharData = false
			text.WriteString(" ")
		}
	}

	result := cleanText(text.String())

	return &ParseResult{
		Text:     result,
		FileType: ".xml",
	}, nil
}

// parsePDF parses PDF files; see pdf.go.
// parseDOCX parses Word documents.
func parseDOCX(data []byte) (*ParseResult, error) {
	// DOCX is a ZIP file containing XML
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("failed to read docx: %w", err)
	}

	var text strings.Builder

	// Read document.xml
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()

			content, err := io.ReadAll(rc)
			if err != nil {
				return nil, err
			}

			// Extract text from XML
			text.WriteString(extractDOCXText(string(content)))
			break
		}
	}

	result := cleanText(text.String())

	return &ParseResult{
		Text:     result,
		FileType: ".docx",
	}, nil
}

// parseXLSX parses Excel spreadsheets.
func parseXLSX(data []byte) (*ParseResult, error) {
	// XLSX is a ZIP file
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("failed to read xlsx: %w", err)
	}

	var text strings.Builder

	// Read shared strings
	sharedStrings := make(map[int]string)
	for _, f := range zr.File {
		if f.Name == "xl/sharedStrings.xml" {
			rc, err := f.Open()
			if err != nil {
				continue
			}
			content, err := io.ReadAll(rc)
			rc.Close()
			if err == nil {
				sharedStrings = parseSharedStrings(string(content))
			}
			break
		}
	}

	// Read sheet data
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "xl/worksheets/sheet") && strings.HasSuffix(f.Name, ".xml") {
			rc, err := f.Open()
			if err != nil {
				continue
			}
			content, err := io.ReadAll(rc)
			rc.Close()
			if err == nil {
				text.WriteString(extractXLSXText(string(content), sharedStrings))
				text.WriteString("\n")
			}
		}
	}

	result := cleanText(text.String())

	return &ParseResult{
		Text:     result,
		FileType: ".xlsx",
	}, nil
}

// parsePPTX parses PowerPoint presentations.
func parsePPTX(data []byte) (*ParseResult, error) {
	// PPTX is a ZIP file
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("failed to read pptx: %w", err)
	}

	var text strings.Builder

	// Read slide content
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "ppt/slides/slide") && strings.HasSuffix(f.Name, ".xml") {
			rc, err := f.Open()
			if err != nil {
				continue
			}
			content, err := io.ReadAll(rc)
			rc.Close()
			if err == nil {
				text.WriteString(extractPPTXText(string(content)))
				text.WriteString("\n")
			}
		}
	}

	result := cleanText(text.String())

	return &ParseResult{
		Text:     result,
		FileType: ".pptx",
	}, nil
}

// Helper functions

func cleanText(s string) string {
	// Replace multiple whitespace with single space
	re := regexp.MustCompile(`\s+`)
	s = re.ReplaceAllString(s, " ")

	// Trim whitespace
	s = strings.TrimSpace(s)

	return s
}

func removeTag(s, tag string) string {
	// Remove <tag>...</tag>
	openTag := "<" + tag
	closeTag := "</" + tag + ">"

	var result strings.Builder
	inTag := false
	depth := 0

	for i := 0; i < len(s); i++ {
		if strings.HasPrefix(s[i:], openTag) && !inTag {
			inTag = true
			depth = 1
			// Skip to end of opening tag
			for i < len(s) && s[i] != '>' {
				i++
			}
			continue
		}

		if inTag {
			if strings.HasPrefix(s[i:], openTag) {
				depth++
			} else if strings.HasPrefix(s[i:], closeTag) {
				depth--
				if depth == 0 {
					inTag = false
					i += len(closeTag) - 1
				}
			}
			continue
		}

		result.WriteByte(s[i])
	}

	return result.String()
}

func stripTags(s string) string {
	var result strings.Builder
	inTag := false

	for i := 0; i < len(s); i++ {
		if s[i] == '<' {
			inTag = true
			continue
		}
		if s[i] == '>' {
			inTag = false
			continue
		}
		if !inTag {
			result.WriteByte(s[i])
		}
	}

	return result.String()
}

func extractTitle(s string) string {
	// Try to find <title>...</title>
	re := regexp.MustCompile(`<title[^>]*>([^<]*)</title>`)
	matches := re.FindStringSubmatch(s)
	if len(matches) > 1 {
		return cleanText(matches[1])
	}
	return ""
}


func extractDOCXText(xml string) string {
	// Extract text from <w:t> tags
	re := regexp.MustCompile(`<w:t[^>]*>([^<]*)</w:t>`)
	matches := re.FindAllStringSubmatch(xml, -1)

	var text strings.Builder
	for _, match := range matches {
		if len(match) > 1 {
			text.WriteString(match[1])
		}
	}

	return text.String()
}

func parseSharedStrings(xml string) map[int]string {
	result := make(map[int]string)
	re := regexp.MustCompile(`<t>([^<]*)</t>`)
	matches := re.FindAllStringSubmatch(xml, -1)

	for i, match := range matches {
		if len(match) > 1 {
			result[i] = match[1]
		}
	}

	return result
}

func extractXLSXText(xml string, sharedStrings map[int]string) string {
	var text strings.Builder

	// Look for <c><v>...</v></c> for values
	// and <c><is><t>...</t></is></c> for inline strings
	// and <c t="s"><v>N</v></c> for shared strings

	// Extract inline strings
	re := regexp.MustCompile(`<t>([^<]*)</t>`)
	matches := re.FindAllStringSubmatch(xml, -1)
	for _, match := range matches {
		if len(match) > 1 {
			text.WriteString(match[1])
			text.WriteString(" ")
		}
	}

	return text.String()
}

func extractPPTXText(xml string) string {
	// Extract text from <a:t> tags
	re := regexp.MustCompile(`<a:t>([^<]*)</a:t>`)
	matches := re.FindAllStringSubmatch(xml, -1)

	var text strings.Builder
	for _, match := range matches {
		if len(match) > 1 {
			text.WriteString(match[1])
			text.WriteString(" ")
		}
	}

	return text.String()
}

// parseJSON parses JSON files and extracts text from string values.
func parseJSON(data []byte) (*ParseResult, error) {
	// Simple JSON string extraction using regex
	// This extracts all string values from JSON
	re := regexp.MustCompile(`"([^"\\]*(\\.[^"\\]*)*)"`)
	matches := re.FindAllStringSubmatch(string(data), -1)

	var text strings.Builder
	for _, match := range matches {
		if len(match) > 1 {
			// Skip JSON keys (usually followed by :)
			s := match[1]
			if len(s) > 0 && !strings.HasPrefix(s, "{") && !strings.HasPrefix(s, "[") {
				text.WriteString(s)
				text.WriteString(" ")
			}
		}
	}

	result := cleanText(text.String())
	return &ParseResult{
		Text:     result,
		FileType: ".json",
	}, nil
}

// parseYAML parses YAML files and extracts text content.
func parseYAML(data []byte) (*ParseResult, error) {
	// Extract text values from YAML (lines with : value or - value)
	lines := strings.Split(string(data), "\n")
	var text strings.Builder

	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Skip comments and empty lines
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		// Extract value after colon
		if idx := strings.Index(line, ":"); idx > 0 {
			value := strings.TrimSpace(line[idx+1:])
			if value != "" && !strings.HasPrefix(value, "{") && !strings.HasPrefix(value, "[") {
				text.WriteString(value)
				text.WriteString(" ")
			}
		}
	}

	result := cleanText(text.String())
	return &ParseResult{
		Text:     result,
		FileType: ".yaml",
	}, nil
}

// parseCSV parses CSV files and extracts text content.
func parseCSV(content string) (*ParseResult, error) {
	lines := strings.Split(content, "\n")
	var text strings.Builder

	for _, line := range lines {
		// Simple CSV parsing - split by comma and clean up
		fields := strings.Split(line, ",")
		for _, field := range fields {
			field = strings.TrimSpace(field)
			// Remove quotes if present
			if strings.HasPrefix(field, "\"") && strings.HasSuffix(field, "\"") {
				field = field[1 : len(field)-1]
			}
			if field != "" {
				text.WriteString(field)
				text.WriteString(" ")
			}
		}
	}

	result := cleanText(text.String())
	return &ParseResult{
		Text:     result,
		FileType: ".csv",
	}, nil
}

// parseOldDOC parses legacy .doc files (basic text extraction).
func parseOldDOC(data []byte) (*ParseResult, error) {
	// Legacy DOC format is binary, try to extract readable text
	// This is a basic implementation that looks for text patterns
	var text strings.Builder

	// Look for sequences of printable ASCII characters
	re := regexp.MustCompile(`[\x20-\x7E]{4,}`)
	matches := re.FindAll(data, -1)

	for _, match := range matches {
		s := string(match)
		// Filter out common binary patterns
		if !strings.Contains(s, "<?xml") && !strings.Contains(s, "<!DOCTYPE") {
			text.WriteString(s)
			text.WriteString(" ")
		}
	}

	result := cleanText(text.String())
	if len(result) < 100 {
		return nil, fmt.Errorf("legacy .doc format not fully supported, please convert to .docx")
	}

	return &ParseResult{
		Text:     result,
		FileType: ".doc",
	}, nil
}

// parseRTF parses RTF files and extracts text.
func parseRTF(content string) (*ParseResult, error) {
	// Remove RTF control words and extract text
	// This is a simplified implementation
	re := regexp.MustCompile(`\\[a-z]+\d*\s*|\\'[0-9a-fA-F]{2}|\\[~{}]|\\\n|[{}]`)
	text := re.ReplaceAllString(content, " ")

	// Replace common RTF escape sequences
	text = strings.ReplaceAll(text, "\\par", "\n")
	text = strings.ReplaceAll(text, "\\tab", "\t")
	text = strings.ReplaceAll(text, "\\line", "\n")
	text = strings.ReplaceAll(text, "\\page", "\n")

	result := cleanText(text)
	return &ParseResult{
		Text:     result,
		FileType: ".rtf",
	}, nil
}

// parseCode parses source code files and extracts text with comments.
func parseCode(content string, ext string) (*ParseResult, error) {
	// For code files, we keep the content as-is but clean up excessive whitespace
	result := cleanText(content)
	return &ParseResult{
		Text:     result,
		FileType: ext,
	}, nil
}

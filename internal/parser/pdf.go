package parser

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	pdfcpu "github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/razvandimescu/gopdf/pdf"
)

// ErrPDFEncrypted reports a PDF that go-rag cannot read because it is
// encrypted. Owner-password-protected PDFs (the common kind: the file opens
// without any prompt, e.g. the College Board AP documents) only refuse
// extraction, and gopdf reports them as 0 pages / no text instead of
// complaining about encryption, which sends debugging down the wrong path.
// Errors built from it are always prefixed "PDF is encrypted (owner
// password)"; test with errors.Is.
var ErrPDFEncrypted = errors.New("PDF is encrypted (owner password)")

// disablePDFCPUConfigDir keeps pdfcpu from creating its own configuration
// directory (~/.config/pdfcpu/config.yml) as a side effect of decryption:
// go-rag does not want to write outside its own storage, and core fonts are
// enough to rewrite a document.
var disablePDFCPUConfigDir = sync.OnceFunc(api.DisableConfigDir)

// parsePDF parses a PDF and extracts its text.
//
// gopdf reconstructs text lines and handles intra-word spacing internally,
// avoiding the per-letter spacing problems seen with coordinate-based parsers.
// It has no encryption support — and its lexer spins forever on the garbage it
// decodes from an encrypted stream — so encryption has to be resolved (or ruled
// out) before any extraction happens:
//  1. a file whose trailer declares /Encrypt goes straight to pdfcpu, which
//     decrypts documents that open with an empty user password (how
//     owner-password-protected files ship) and re-parses the decrypted copy,
//  2. anything else gets a plain gopdf parse (the common case); pages whose
//     content gopdf cannot decode are skipped instead of spun on,
//  3. when gopdf fails on the structure or decodes nothing but garbage, pdfcpu
//     — which also rebuilds a damaged cross-reference table — takes a second
//     look before missing text gets blamed on a scanner,
//  4. a file that rejects the empty user password fails with ErrPDFEncrypted,
//     so callers can tell the user to decrypt it themselves.
func parsePDF(data []byte) (*ParseResult, error) {
	if pdfHasEncryptDict(data) {
		return parseEncryptedPDF(data)
	}

	text, pages, unreadable, err := extractPDFText(data)
	if err == nil {
		return &ParseResult{Text: text, FileType: ".pdf"}, nil
	}

	// Pages that all decoded into valid PDF operators yet hold no text: a
	// scanned, image-only document. No second reader will find text that is not
	// there, and this is the common way to land here, so don't pay for one.
	if pages > 0 && unreadable == 0 {
		return nil, err
	}

	// gopdf failed on the structure itself (a damaged cross-reference table, or
	// an encrypted xref stream whose /Encrypt entry stayed out of sight) or its
	// content decoded to garbage. pdfcpu re-reads the document and only
	// succeeds when it could decrypt it, so a success means "encrypted", and
	// any remaining failure is about content, not access.
	decrypted, decErr := decryptPDFWithEmptyUserPassword(data)
	if decErr == nil {
		text, _, _, err := extractPDFText(decrypted)
		if err != nil {
			return nil, err
		}
		return &ParseResult{Text: text, FileType: ".pdf", Decrypted: true}, nil
	}
	if errors.Is(decErr, pdfcpu.ErrWrongPassword) {
		return nil, fmt.Errorf("%w: the file does not open with an empty user password", ErrPDFEncrypted)
	}

	// Not encrypted (pdfcpu.ErrNotEncrypted), or unreadable for another reason:
	// keep gopdf's diagnosis, which names the actual structural failure.
	return nil, err
}

// parseEncryptedPDF handles a PDF whose trailer declares /Encrypt. gopdf cannot
// decrypt such a file, so pdfcpu tries the empty user password first — how
// owner-password-protected documents ship: they open in every viewer without a
// prompt but refuse text extraction.
func parseEncryptedPDF(data []byte) (*ParseResult, error) {
	decrypted, decErr := decryptPDFWithEmptyUserPassword(data)
	if decErr == nil {
		text, _, _, err := extractPDFText(decrypted)
		if err != nil {
			// The file opened, so what remains is about content, not access.
			return nil, err
		}
		return &ParseResult{Text: text, FileType: ".pdf", Decrypted: true}, nil
	}

	switch {
	case errors.Is(decErr, pdfcpu.ErrNotEncrypted):
		// pdfcpu — the more robust reader — does not consider the file
		// encrypted, so the /Encrypt entry it saw is dangling. Parse it as a
		// plain document and keep gopdf's diagnosis of what is actually wrong.
		text, _, _, err := extractPDFText(data)
		if err != nil {
			return nil, err
		}
		return &ParseResult{Text: text, FileType: ".pdf"}, nil
	case errors.Is(decErr, pdfcpu.ErrWrongPassword):
		return nil, fmt.Errorf("%w: the file does not open with an empty user password", ErrPDFEncrypted)
	default:
		return nil, fmt.Errorf("%w: %v", ErrPDFEncrypted, decErr)
	}
}

// extractPDFText extracts the text of every page with gopdf.
//
// It returns the text ("" when nothing usable was found), the number of pages
// gopdf discovered (0 when the document structure could not be walked), the
// number of pages whose content gopdf could not decode (how an encrypted or
// damaged content stream presents), and an error explaining why no text was
// extracted. Pages it cannot decode are skipped rather than fatal, so a single
// broken page does not cost the text of the rest.
func extractPDFText(data []byte) (text string, pages int, unreadable int, err error) {
	r, err := pdf.Open(data)
	if err != nil {
		return "", 0, 0, fmt.Errorf("open pdf: %w", err)
	}

	pageDicts, err := r.Pages()
	if err != nil {
		return "", 0, 0, fmt.Errorf("walk pdf page tree: %w", err)
	}
	if len(pageDicts) == 0 {
		return "", 0, 0, fmt.Errorf("pdf contains no pages (its cross-reference table may be damaged)")
	}

	var all strings.Builder
	for i, page := range pageDicts {
		if !readablePageContent(r, page) {
			unreadable++
			continue
		}
		// Same pipeline as Page.TextLines(): ExtractPageText positions the
		// spans, BuildLines groups them into spatial lines.
		lines := pdf.BuildLines(pdf.ExtractPageText(page, r))
		for _, line := range lines {
			if line.Text == "" {
				continue
			}
			if all.Len() > 0 {
				all.WriteByte('\n')
			}
			all.WriteString(line.Text)
		}
		if i < len(pageDicts)-1 && len(lines) > 0 {
			all.WriteByte('\n')
		}
	}

	result := strings.TrimSpace(all.String())
	if result != "" {
		return result, len(pageDicts), unreadable, nil
	}
	if unreadable == len(pageDicts) {
		return "", len(pageDicts), unreadable,
			fmt.Errorf("no text extracted from pdf: all %d pages hold data that cannot be decoded", len(pageDicts))
	}
	return "", len(pageDicts), unreadable, fmt.Errorf("no text extracted from pdf")
}

// readablePageContent reports whether gopdf can be trusted with the page's
// content stream. Its lexer answers an unhandled delimiter — ')' '{' '}' —
// with an empty keyword and, crucially, without advancing the position, which
// makes the operator loop in ExtractPageText spin forever: a stream that failed
// to decode (the ciphertext of an encrypted document, say) provides exactly
// such bytes. The check replays the lexer over the decoded content, a fraction
// of the cost of the extraction itself, and rejects pages it cannot vouch for
// — the same pages ExtractPageText renders empty when PageContent fails. Every
// token but the empty keyword consumes at least one byte, so the loop is bound
// by the length of the content.
func readablePageContent(r *pdf.Reader, page pdf.Dict) bool {
	content, err := r.PageContent(page)
	if err != nil || content == nil {
		return false
	}
	lex := pdf.NewLexer(content)
	for {
		tok, err := lex.NextToken()
		if err != nil || tok.Type == pdf.TEOF {
			return true
		}
		if tok.Type == pdf.TKeyword && tok.Str == "" {
			return false
		}
	}
}

// pdfHasEncryptDict reports whether the PDF declares an /Encrypt entry in its
// trailer, i.e. the document is encrypted. When gopdf cannot parse the file at
// all (its cross-reference table may be unreadable before decryption), it
// falls back to scanning the newest cross-reference section, which is where a
// conforming writer records the entry.
func pdfHasEncryptDict(data []byte) bool {
	if r, err := pdf.Open(data); err == nil {
		trailer := r.Trailer()
		_, ok := trailer["Encrypt"]
		return ok
	}
	return trailerDeclaresEncrypt(data)
}

// trailerDeclaresEncrypt scans from the offset the last "startxref" points at
// to the end of the file — the newest xref table (plus its trailer) or xref
// stream dictionary — for an /Encrypt entry. "/EncryptMetadata" also contains
// the substring, so a match is only counted when it is not followed by a
// letter.
func trailerDeclaresEncrypt(data []byte) bool {
	region, ok := newestXRefSection(data)
	if !ok {
		return false
	}
	key := []byte("/Encrypt")
	for {
		i := bytes.Index(region, key)
		if i < 0 {
			return false
		}
		if i+len(key) >= len(region) || !isLetter(region[i+len(key)]) {
			return true
		}
		region = region[i+len(key):]
	}
}

func isLetter(b byte) bool {
	return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z'
}

// newestXRefSection returns the bytes from the offset named by the last
// "startxref" keyword to the end of the file.
func newestXRefSection(data []byte) ([]byte, bool) {
	i := bytes.LastIndex(data, []byte("startxref"))
	if i < 0 {
		return nil, false
	}
	rest := data[i+len("startxref"):]
	for len(rest) > 0 && (rest[0] == ' ' || rest[0] == '\t' || rest[0] == '\r' || rest[0] == '\n') {
		rest = rest[1:]
	}
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return nil, false
	}
	var offset int64
	for _, c := range rest[:end] {
		offset = offset*10 + int64(c-'0')
	}
	if offset <= 0 || offset >= int64(len(data)) {
		return nil, false
	}
	return data[offset:], true
}

// decryptPDFWithEmptyUserPassword decrypts an encrypted PDF whose user
// password is the empty string — how owner-password-protected files ship.
// pdfcpu parses the document, decrypts every string and stream, and writes a
// re-serialized copy; a damaged cross-reference table is rebuilt on the way.
// The result is the same normalization `pikepdf ... save` performs, so it also
// un-breaks files whose offsets no longer match their bytes. A file that is
// not encrypted fails with pdfcpu.ErrNotEncrypted, one whose empty password is
// rejected with pdfcpu.ErrWrongPassword.
func decryptPDFWithEmptyUserPassword(data []byte) ([]byte, error) {
	disablePDFCPUConfigDir()

	conf := model.NewDefaultConfiguration()
	var out bytes.Buffer
	if err := api.Decrypt(bytes.NewReader(data), &out, conf); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

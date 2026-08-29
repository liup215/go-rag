package parser

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

const testPDFText = "Hello go-rag encrypted PDF test"

// buildTestPDF assembles a PDF with one page per text ("" for a page without a
// content stream) and a classic cross-reference table built from the real byte
// offsets. Object layout: 1 catalog, 2 page tree, then the page dicts, then
// their content streams, then the shared font.
func buildTestPDF(t *testing.T, texts ...string) []byte {
	t.Helper()

	var buf bytes.Buffer
	offsets := map[int]int{}

	addObject := func(num int, body string) {
		offsets[num] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", num, body)
	}

	buf.WriteString("%PDF-1.4\n")

	const firstPageObject = 3
	firstContentObject := firstPageObject + len(texts)
	fontObject := firstContentObject + len(texts)

	hasText := false
	kids := "["
	for i, text := range texts {
		if i > 0 {
			kids += " "
		}
		kids += strconv.Itoa(firstPageObject+i) + " 0 R"

		pageDict := "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792]"
		if text != "" {
			hasText = true
			content := fmt.Sprintf("BT /F1 24 Tf 72 720 Td (%s) Tj ET\n", text)
			addObject(firstContentObject+i, fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content))
			pageDict += fmt.Sprintf(" /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R", fontObject, firstContentObject+i)
		}
		addObject(firstPageObject+i, pageDict+" >>")
	}
	kids += "]"

	addObject(1, "<< /Type /Catalog /Pages 2 0 R >>")
	addObject(2, fmt.Sprintf("<< /Type /Pages /Kids %s /Count %d >>", kids, len(texts)))
	lastObject := firstContentObject + len(texts) - 1
	if hasText {
		lastObject = fontObject
		addObject(fontObject, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	}

	xrefStart := buf.Len()
	buf.WriteString(fmt.Sprintf("xref\n0 %d\n0000000000 65535 f \n", lastObject+1))
	for i := 1; i <= lastObject; i++ {
		// Objects never emitted (a page without a content stream leaves a gap)
		// get a free entry, as a conforming writer would write.
		if offset, ok := offsets[i]; ok {
			fmt.Fprintf(&buf, "%010d 00000 n \n", offset)
		} else {
			buf.WriteString("0000000000 65535 f \n")
		}
	}
	buf.WriteString(fmt.Sprintf("trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n", lastObject+1))
	buf.WriteString(strconv.Itoa(xrefStart))
	buf.WriteString("\n%%EOF\n")

	return buf.Bytes()
}

// encryptTestPDF encrypts a PDF the way password-protected documents ship:
// owner password set (restricting extraction), user password possibly empty.
func encryptTestPDF(t *testing.T, data []byte, userPW, ownerPW string, keyLength int, aes bool) []byte {
	t.Helper()

	var conf *model.Configuration
	if aes {
		conf = model.NewAESConfiguration(userPW, ownerPW, keyLength)
	} else {
		conf = model.NewRC4Configuration(userPW, ownerPW, keyLength)
	}

	var out bytes.Buffer
	if err := api.Encrypt(bytes.NewReader(data), &out, conf); err != nil {
		t.Fatalf("pdfcpu encrypt: %v", err)
	}
	if out.Len() == 0 {
		t.Fatal("pdfcpu encrypt produced no output")
	}
	return out.Bytes()
}

func TestParsePDFUnencrypted(t *testing.T) {
	res, err := parsePDF(buildTestPDF(t, testPDFText))
	if err != nil {
		t.Fatalf("parsePDF: %v", err)
	}
	if res.Decrypted {
		t.Error("Decrypted = true for an unencrypted PDF, want false")
	}
	if res.FileType != ".pdf" {
		t.Errorf("FileType = %q, want .pdf", res.FileType)
	}
	if !strings.Contains(res.Text, "Hello go-rag") {
		t.Errorf("text %q does not contain the page contents", res.Text)
	}
}

// The College Board scenario: an owner-password-protected PDF opens in every
// viewer without a prompt (empty user password) and used to come back as
// "no text extracted from pdf". It must now be decrypted and parsed.
func TestParsePDFEncryptedEmptyUserPassword(t *testing.T) {
	for _, tc := range []struct {
		name      string
		keyLength int
		aes       bool
	}{
		{"AES-256", 256, true},
		{"AES-128", 128, true},
		{"RC4-128", 128, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := encryptTestPDF(t, buildTestPDF(t, testPDFText), "", "owner-secret", tc.keyLength, tc.aes)

			if !pdfHasEncryptDict(data) {
				t.Fatal("encrypted PDF not detected as encrypted via its trailer")
			}

			res, err := parsePDF(data)
			if err != nil {
				t.Fatalf("parsePDF: %v", err)
			}
			if !res.Decrypted {
				t.Error("Decrypted = false for an auto-decrypted PDF, want true")
			}
			if !strings.Contains(res.Text, "Hello go-rag") {
				t.Errorf("text %q does not contain the page contents", res.Text)
			}
		})
	}
}

// A user password (a document that prompts on open) cannot be bypassed: the
// error must name encryption instead of blaming the missing text.
func TestParsePDFEncryptedWithUserPassword(t *testing.T) {
	data := encryptTestPDF(t, buildTestPDF(t, testPDFText), "topsecret", "owner-secret", 256, true)

	_, err := parsePDF(data)
	if err == nil {
		t.Fatal("parsePDF succeeded on a PDF with a non-empty user password")
	}
	if !errors.Is(err, ErrPDFEncrypted) {
		t.Fatalf("error %v does not match ErrPDFEncrypted", err)
	}
	if strings.Contains(err.Error(), "no text extracted") {
		t.Errorf("error %v conflates encryption with missing text", err)
	}
}

// An encrypted, genuinely text-less PDF (a scanned document behind an owner
// password, say) still reports missing text once decryption succeeded — the
// encryption report is reserved for files that could not be opened.
func TestParsePDFEncryptedWithoutText(t *testing.T) {
	data := encryptTestPDF(t, buildTestPDF(t, ""), "", "owner-secret", 256, true)

	_, err := parsePDF(data)
	if err == nil {
		t.Fatal("parsePDF succeeded on a PDF without text")
	}
	if !strings.Contains(err.Error(), "no text extracted") {
		t.Fatalf("error %v should report missing text, got encryption confusion", err)
	}
	if errors.Is(err, ErrPDFEncrypted) {
		t.Errorf("error %v should not be an encryption error", err)
	}
}

func TestParsePDFWithoutPages(t *testing.T) {
	// A well-formed xref pointing at a page tree without pages: gopdf walks the
	// document but finds nothing, and pdfcpu confirms the file is not encrypted.
	_, err := parsePDF(buildTestPDF(t))
	if err == nil {
		t.Fatal("parsePDF succeeded on a PDF without pages")
	}
	if !strings.Contains(err.Error(), "no pages") {
		t.Fatalf("error %v should say the PDF has no pages", err)
	}
	if errors.Is(err, ErrPDFEncrypted) {
		t.Errorf("error %v should not be an encryption error", err)
	}
}

// gopdf's lexer spins forever on a content stream it cannot tokenize: an
// unhandled delimiter — the stray ')' below — comes back as an empty keyword
// that never advances the position. Undecodable bytes are also how an
// encrypted document presents, so a page like this must be skipped instead of
// hanging the parser.
func TestParsePDFPageWithUndecodableContent(t *testing.T) {
	data := buildTestPDF(t, ")")

	_, err := parsePDF(data)
	if err == nil {
		t.Fatal("parsePDF succeeded on a page whose content cannot be decoded")
	}
	if !strings.Contains(err.Error(), "no text extracted") {
		t.Fatalf("error %v should report missing text", err)
	}
	if errors.Is(err, ErrPDFEncrypted) {
		t.Errorf("error %v should not be an encryption error (pdfcpu sees a plain file)", err)
	}
}

// A single undecodable page must not cost the text of the others.
func TestParsePDFSkipsOnlyUndecodablePage(t *testing.T) {
	data := buildTestPDF(t, testPDFText, ")")

	res, err := parsePDF(data)
	if err != nil {
		t.Fatalf("parsePDF: %v", err)
	}
	if res.Decrypted {
		t.Error("Decrypted = true for a plain PDF, want false")
	}
	if !strings.Contains(res.Text, testPDFText) {
		t.Errorf("text %q does not contain the readable page's contents", res.Text)
	}
}

func TestPDFHasEncryptDict(t *testing.T) {
	t.Run("unencrypted", func(t *testing.T) {
		if pdfHasEncryptDict(buildTestPDF(t, testPDFText)) {
			t.Error("plain PDF reported as encrypted")
		}
	})

	t.Run("encrypted", func(t *testing.T) {
		data := encryptTestPDF(t, buildTestPDF(t, testPDFText), "", "owner-secret", 256, true)
		if !pdfHasEncryptDict(data) {
			t.Error("encrypted PDF not reported as encrypted")
		}
	})

	t.Run("unparsable file scanned for the entry", func(t *testing.T) {
		// gopdf cannot parse these at all, so the trailer scan decides.
		for name, tc := range map[string]struct {
			payload string
			want    bool
		}{
			"trailer entry": {"1 0 obj\nendobj\ntrailer\n<< /Size 2 /Root 1 0 R /Encrypt 12 0 R /ID [<0102> <0304>] >>\nstartxref\n9\n%%EOF\n", true},
			"xref stream":   {"<< /Type /XRef /Size 2 /Root 1 0 R /Encrypt 7 0 R >>\nstartxref\n9\n%%EOF\n", true},
			// "/EncryptMetadata" contains the substring but is a key of the
			// encryption dictionary itself, not a declaration of encryption.
			"EncryptMetadata only": {"<< /Filter /Standard /EncryptMetadata true >>\nstartxref\n9\n%%EOF\n", false},
			"no entry":             {"<< /Type /XRef /Size 2 /Root 1 0 R >>\nstartxref\n9\n%%EOF\n", false},
		} {
			if got := pdfHasEncryptDict([]byte(tc.payload)); got != tc.want {
				t.Errorf("%s: pdfHasEncryptDict = %v, want %v", name, got, tc.want)
			}
		}
	})
}

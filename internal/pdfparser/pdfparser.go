package pdfparser

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"pdf-archive/internal/config"
	"pdf-archive/internal/utils"
	"pdf-archive/models"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

type PDFParser struct {
	cfg *config.Config
}

func New(cfg *config.Config) *PDFParser {
	return &PDFParser{cfg: cfg}
}

func (p *PDFParser) Parse(ctx context.Context, filePath string) (*models.ParsedDocument, error) {
	fileSize, err := utils.FileSize(filePath)
	if err != nil {
		return nil, fmt.Errorf("stat file: %w", err)
	}
	md5, err := utils.FileMD5(filePath)
	if err != nil {
		return nil, fmt.Errorf("md5 file: %w", err)
	}

	doc := &models.ParsedDocument{
		FilePath: filePath,
		FileSize: fileSize,
		MD5:      md5,
	}

	metadata, err := p.extractMetadata(filePath)
	if err == nil {
		doc.Metadata = metadata
	}

	textPages, textBlocks, extractErr := p.extractTextWithLayout(filePath)
	if extractErr != nil {
		doc.ParseError = extractErr.Error()
	}

	totalTextLen := 0
	for _, t := range textPages {
		totalTextLen += len(t)
	}

	needsOCR := p.cfg.OCR.Enabled && totalTextLen < p.cfg.OCR.MinTextLen

	if needsOCR {
		ocrText, ocrBlocks, ocrErr := p.runOCR(ctx, filePath)
		if ocrErr == nil {
			doc.Metadata.IsOCR = true
			doc.Metadata.OCRLanguage = p.cfg.OCR.Languages
			if len(ocrText) > len(textPages) {
				textPages = ocrText
			} else {
				for i := range ocrText {
					if i < len(textPages) && textPages[i] == "" {
						textPages[i] = ocrText[i]
					} else if i >= len(textPages) {
						textPages = append(textPages, ocrText[i])
					}
				}
			}
			textBlocks = append(textBlocks, ocrBlocks...)
		}
	}

	if len(textPages) == 0 && doc.Metadata.PageCount > 0 {
		for i := 0; i < doc.Metadata.PageCount; i++ {
			textPages = append(textPages, "")
		}
	}

	doc.Metadata.PageCount = len(textPages)
	doc.Pages = make([]models.ParsedPage, len(textPages))

	for i, text := range textPages {
		pageBlocks := p.filterBlocksByPage(textBlocks, i+1)
		tables := p.detectTables(pageBlocks)
		doc.Pages[i] = models.ParsedPage{
			PageNum:    i + 1,
			Text:       text,
			TextBlocks: pageBlocks,
			Tables:     tables,
			Width:      p.estimatePageWidth(pageBlocks),
			Height:     p.estimatePageHeight(pageBlocks),
		}
		doc.TextBlocks = append(doc.TextBlocks, pageBlocks...)
		doc.Tables = append(doc.Tables, tables...)
		doc.FullText += text + "\n"
	}

	if doc.Metadata.PageCount == 0 {
		doc.Metadata.PageCount = len(doc.Pages)
	}

	return doc, nil
}

func (p *PDFParser) estimatePageWidth(blocks []models.TextBlock) float64 {
	maxW := 0.0
	for _, b := range blocks {
		if b.X+b.Width > maxW {
			maxW = b.X + b.Width
		}
	}
	if maxW <= 0 {
		return 595
	}
	return maxW
}

func (p *PDFParser) estimatePageHeight(blocks []models.TextBlock) float64 {
	maxH := 0.0
	for _, b := range blocks {
		if b.Y+b.Height > maxH {
			maxH = b.Y + b.Height
		}
	}
	if maxH <= 0 {
		return 842
	}
	return maxH
}

func (p *PDFParser) filterBlocksByPage(blocks []models.TextBlock, pageNum int) []models.TextBlock {
	var result []models.TextBlock
	for _, b := range blocks {
		if b.PageNum == pageNum {
			result = append(result, b)
		}
	}
	return result
}

func (p *PDFParser) extractMetadata(filePath string) (models.DocumentMetadata, error) {
	var meta models.DocumentMetadata
	f, err := os.Open(filePath)
	if err != nil {
		return meta, err
	}
	defer f.Close()

	pdfInfo, err := api.PDFInfo(f, filePath, nil, false, model.NewDefaultConfiguration())
	if err != nil {
		return meta, err
	}

	meta.Title = pdfInfo.Title
	meta.Author = pdfInfo.Author
	meta.Creator = pdfInfo.Creator
	meta.Producer = pdfInfo.Producer
	meta.CreatedAt = utils.ParsePDFDate(pdfInfo.CreationDate)
	meta.ModifiedAt = utils.ParsePDFDate(pdfInfo.ModificationDate)
	meta.PageCount = pdfInfo.PageCount

	return meta, nil
}

func (p *PDFParser) extractTextWithLayout(filePath string) ([]string, []models.TextBlock, error) {
	pythonExe := findPythonExecutable()
	if pythonExe != "" {
		if pages, blocks, err := p.extractWithPython(pythonExe, filePath); err == nil && len(pages) > 0 {
			return pages, blocks, nil
		}
	}

	f, err := os.Open(filePath)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	var textPages []string
	var textBlocks []models.TextBlock
	var mu sync.Mutex

	digestContent := func(rd io.Reader, pageNr int) error {
		data, err := io.ReadAll(rd)
		if err != nil {
			return err
		}
		content := string(data)

		text := extractReadableText(content)
		blocks := extractBlocksFromContent(content, pageNr)

		mu.Lock()
		for len(textPages) < pageNr {
			textPages = append(textPages, "")
		}
		if pageNr-1 < len(textPages) {
			if textPages[pageNr-1] == "" {
				textPages[pageNr-1] = text
			}
		} else {
			textPages = append(textPages, text)
		}
		textBlocks = append(textBlocks, blocks...)
		mu.Unlock()

		return nil
	}

	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationRelaxed

	err = api.ExtractContent(f, nil, digestContent, conf)
	if err != nil {
		if len(textPages) == 0 {
			fallbackText, fallbackBlocks, fbErr := p.extractFallback(filePath)
			if fbErr == nil {
				return fallbackText, fallbackBlocks, nil
			}
			return nil, nil, err
		}
	}

	return textPages, textBlocks, nil
}

func findPythonExecutable() string {
	candidates := []string{
		".venv/bin/python",
		".venv/bin/python3",
		"venv/bin/python",
		"venv/bin/python3",
	}
	cwd, _ := os.Getwd()
	for _, c := range candidates {
		p := filepath.Join(cwd, c)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	for _, exe := range []string{"python3", "python"} {
		if p, err := exec.LookPath(exe); err == nil {
			return p
		}
	}
	return ""
}

func (p *PDFParser) extractWithPython(pythonExe, filePath string) ([]string, []models.TextBlock, error) {
	script := fmt.Sprintf(`
import sys
import json
try:
    from pypdf import PdfReader
except ImportError:
    try:
        import PyPDF2 as PdfReader_mod
        class _Wrap:
            def PdfReader(self, p):
                return PdfReader_mod.PdfReader(p)
        PdfReader = _Wrap()
    except ImportError:
        sys.exit(2)
try:
    reader = PdfReader.PdfReader(r'%s')
except AttributeError:
    reader = PdfReader(r'%s')
result = {'pages': [], 'metadata': {}}
try:
    if reader.metadata:
        m = reader.metadata
        result['metadata'] = {
            'title': str(m.title or ''),
            'author': str(m.author or ''),
            'creator': str(m.creator or ''),
            'producer': str(m.producer or ''),
        }
except:
    pass
for i, page in enumerate(reader.pages):
    try:
        t = page.extract_text() or ''
    except:
        t = ''
    result['pages'].append(t)
sys.stdout.write(json.dumps(result, ensure_ascii=False))
`, filePath, filePath)
	cmd := exec.Command(pythonExe, "-c", script)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, nil, fmt.Errorf("python extract: %w: %s", err, stderr.String())
	}
	type pyResult struct {
		Pages    []string          `json:"pages"`
		Metadata map[string]string `json:"metadata"`
	}
	var res pyResult
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		return nil, nil, fmt.Errorf("parse python output: %w", err)
	}
	var pages []string
	var blocks []models.TextBlock
	for pIdx, part := range res.Pages {
		pages = append(pages, part)
		blocks = append(blocks, linesToBlocks(part, pIdx+1)...)
	}
	if len(pages) == 0 {
		return nil, nil, fmt.Errorf("python extracted empty pages")
	}
	return pages, blocks, nil
}

func (p *PDFParser) extractFallback(filePath string) ([]string, []models.TextBlock, error) {
	tmpDir, err := os.MkdirTemp("", "pdftxt-*")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(tmpDir)

	if _, err := exec.LookPath("pdftotext"); err == nil {
		outFile := filepath.Join(tmpDir, "output.txt")
		args := []string{"-layout", "-enc", "UTF-8", filePath, outFile}
		if err := exec.Command("pdftotext", args...).Run(); err == nil {
			data, err := os.ReadFile(outFile)
			if err == nil {
				lines := strings.Split(string(data), "\f")
				var pages []string
				var blocks []models.TextBlock
				for pIdx, l := range lines {
					pageText := strings.TrimSuffix(l, "\n")
					pages = append(pages, pageText)
					pageBlocks := linesToBlocks(pageText, pIdx+1)
					blocks = append(blocks, pageBlocks...)
				}
				return pages, blocks, nil
			}
		}
	}

	if _, err := exec.LookPath("python3"); err == nil {
		script := fmt.Sprintf(`
import sys
try:
    import PyPDF2
    reader = PyPDF2.PdfReader(r'%s')
    for i, page in enumerate(reader.pages):
        try:
            print(page.extract_text() or '')
        except:
            print('')
        if i < len(reader.pages) - 1:
            print('\f')
except ImportError:
    try:
        import pdfplumber
        with pdfplumber.open(r'%s') as pdf:
            for i, page in enumerate(pdf.pages):
                try:
                    print(page.extract_text() or '')
                except:
                    print('')
                if i < len(pdf.pages) - 1:
                    print('\f')
    except:
        sys.exit(1)
`, filePath, filePath)
		cmd := exec.Command("python3", "-c", script)
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		if err := cmd.Run(); err == nil {
			parts := strings.Split(stdout.String(), "\f")
			var pages []string
			var blocks []models.TextBlock
			for pIdx, part := range parts {
				pages = append(pages, part)
				blocks = append(blocks, linesToBlocks(part, pIdx+1)...)
			}
			if len(pages) > 0 {
				return pages, blocks, nil
			}
		}
	}

	return nil, nil, fmt.Errorf("all extraction methods failed")
}

func linesToBlocks(text string, pageNum int) []models.TextBlock {
	var blocks []models.TextBlock
	lines := strings.Split(text, "\n")
	y := 50.0
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			y += 14
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		x := 50.0 + float64(indent)*4
		cleanLine := strings.TrimSpace(line)
		blocks = append(blocks, models.TextBlock{
			Text:     cleanLine,
			X:        x,
			Y:        y,
			Width:    float64(len(cleanLine)) * 7,
			Height:   14,
			PageNum:  pageNum,
			FontSize: 12,
		})
		y += 14
	}
	return blocks
}

var (
	parenTextRe  = regexp.MustCompile(`\(([^)]*)\)`)
	hexTextRe    = regexp.MustCompile(`<([0-9A-Fa-f]+)>`)
	tfRe         = regexp.MustCompile(`([\d.]+)\s+\/?([A-Za-z0-9]+)\s+Tf`)
	tdRe         = regexp.MustCompile(`([-\d.]+)\s+([-\d.]+)\s+T[dD]`)
	tjRe         = regexp.MustCompile(`\(([^)]*)\)\s*Tj`)
	tjArrayRe    = regexp.MustCompile(`\[([^\]]*)\]\s*TJ`)
	starRe       = regexp.MustCompile(`T\*\s*$`)
	singleQuoteRe = regexp.MustCompile(`'$`)
	doubleQuoteRe = regexp.MustCompile(`([-\d.]+)\s+([-\d.]+)\s+"$`)
)

type pdfTextState struct {
	x, y          float64
	startX        float64
	fontSize      float64
	leading       float64
	textMatrix    [6]float64
	inTextObject  bool
}

func newPDFTextState() pdfTextState {
	return pdfTextState{
		textMatrix: [6]float64{1, 0, 0, 1, 0, 0},
		fontSize:   12,
		leading:    14,
	}
}

func (ts *pdfTextState) applyTd(dx, dy float64) {
	ts.textMatrix[4] += dx
	ts.textMatrix[5] += dy
	ts.x = ts.textMatrix[4]
	ts.y = ts.textMatrix[5]
}

func (ts *pdfTextState) newLine() {
	ts.textMatrix[5] -= ts.leading
	ts.textMatrix[4] = ts.startX
	ts.x = ts.startX
	ts.y = ts.textMatrix[5]
}

func extractReadableText(content string) string {
	var texts []string
	var curText strings.Builder

	ts := newPDFTextState()
	lastY := 842.0

	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")

	tokens := tokenizePDFContent(content)
	i := 0
	for i < len(tokens) {
		tok := tokens[i]

		switch tok {
		case "BT":
			ts = newPDFTextState()
			ts.inTextObject = true
		case "ET":
			ts.inTextObject = false
			if curText.Len() > 0 {
				texts = append(texts, strings.TrimSpace(curText.String()))
				curText.Reset()
			}
		case "Tf":
			if i >= 2 {
				if f, err := parseFloat(tokens[i-2]); err == nil {
					ts.fontSize = f
				}
			}
		case "Td":
			if i >= 2 {
				if dy, err := parseFloat(tokens[i-1]); err == nil {
					if dx, err := parseFloat(tokens[i-2]); err == nil {
						ts.applyTd(dx, dy)
						if ts.y != lastY && curText.Len() > 0 {
							texts = append(texts, strings.TrimSpace(curText.String()))
							curText.Reset()
						}
						lastY = ts.y
					}
				}
			}
		case "TD":
			if i >= 2 {
				if dy, err := parseFloat(tokens[i-1]); err == nil {
					ts.leading = -dy
					if dx, err := parseFloat(tokens[i-2]); err == nil {
						ts.applyTd(dx, dy)
						if ts.y != lastY && curText.Len() > 0 {
							texts = append(texts, strings.TrimSpace(curText.String()))
							curText.Reset()
						}
						lastY = ts.y
					}
				}
			}
		case "Tm":
			if i >= 6 {
				if e, err := parseFloat(tokens[i-2]); err == nil {
					if f, err := parseFloat(tokens[i-1]); err == nil {
						ts.textMatrix = [6]float64{0, 0, 0, 0, e, f}
						ts.x = e
						ts.y = f
						ts.startX = e
					}
				}
			}
		case "T*":
			ts.newLine()
			if curText.Len() > 0 {
				texts = append(texts, strings.TrimSpace(curText.String()))
				curText.Reset()
			}
		case "Tj":
			if i >= 1 {
				text := decodePDFTextToken(tokens[i-1])
				if text != "" {
					curText.WriteString(text)
				}
			}
		case "'":
			ts.newLine()
			if curText.Len() > 0 {
				texts = append(texts, strings.TrimSpace(curText.String()))
				curText.Reset()
			}
			if i >= 1 {
				text := decodePDFTextToken(tokens[i-1])
				if text != "" {
					curText.WriteString(text)
				}
			}
		case "\"":
			ts.newLine()
			if curText.Len() > 0 {
				texts = append(texts, strings.TrimSpace(curText.String()))
				curText.Reset()
			}
			if i >= 3 {
				text := decodePDFTextToken(tokens[i-3])
				if text != "" {
					curText.WriteString(text)
				}
			}
		case "TJ":
			if i >= 1 {
				arrText := decodePDFTJArray(tokens[i-1])
				if arrText != "" {
					curText.WriteString(arrText)
				}
			}
		}
		i++
	}

	if curText.Len() > 0 {
		texts = append(texts, strings.TrimSpace(curText.String()))
	}

	if len(texts) == 0 {
		texts = extractTextFallback(content)
	}

	cleaned := make([]string, 0, len(texts))
	for _, t := range texts {
		t = strings.TrimSpace(t)
		if t != "" {
			cleaned = append(cleaned, t)
		}
	}

	if len(cleaned) > 0 {
		return strings.Join(cleaned, "\n")
	}
	return ""
}

func tokenizePDFContent(content string) []string {
	var tokens []string
	var buf strings.Builder
	inParen := false
	inHex := false
	parenDepth := 0
	escaped := false

	for i := 0; i < len(content); i++ {
		c := content[i]

		if inParen {
			buf.WriteByte(c)
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '(' {
				parenDepth++
			} else if c == ')' {
				parenDepth--
				if parenDepth == 0 {
					inParen = false
					tokens = append(tokens, buf.String())
					buf.Reset()
				}
			}
			continue
		}

		if inHex {
			buf.WriteByte(c)
			if c == '>' {
				inHex = false
				tokens = append(tokens, buf.String())
				buf.Reset()
			}
			continue
		}

		if c == '(' {
			if buf.Len() > 0 {
				tokens = append(tokens, buf.String())
				buf.Reset()
			}
			inParen = true
			parenDepth = 1
			buf.WriteByte(c)
		} else if c == '<' {
			if i+1 < len(content) && content[i+1] == '<' {
				continue
			}
			if buf.Len() > 0 {
				tokens = append(tokens, buf.String())
				buf.Reset()
			}
			inHex = true
			buf.WriteByte(c)
		} else if c == '>' {
			if i+1 < len(content) && content[i+1] == '>' {
				i++
				continue
			}
		} else if c == '[' || c == ']' {
			if buf.Len() > 0 {
				tokens = append(tokens, buf.String())
				buf.Reset()
			}
			tokens = append(tokens, string(c))
		} else if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			if buf.Len() > 0 {
				tokens = append(tokens, buf.String())
				buf.Reset()
			}
		} else if c == '/' {
			if buf.Len() > 0 {
				tokens = append(tokens, buf.String())
				buf.Reset()
			}
			buf.WriteByte(c)
		} else {
			buf.WriteByte(c)
		}
	}

	if buf.Len() > 0 {
		tokens = append(tokens, buf.String())
	}

	return tokens
}

func parseFloat(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(s), 64)
}

func decodePDFTextToken(token string) string {
	if strings.HasPrefix(token, "(") && strings.HasSuffix(token, ")") {
		content := token[1 : len(token)-1]
		content = parseOctalEscapes(content)
		return decodePDFString(content)
	}
	if strings.HasPrefix(token, "<") && strings.HasSuffix(token, ">") {
		content := token[1 : len(token)-1]
		return decodeHexString(content)
	}
	return ""
}

func parseOctalEscapes(s string) string {
	var buf bytes.Buffer
	i := 0
	for i < len(s) {
		if s[i] == '\\' && i+3 < len(s) && s[i+1] >= '0' && s[i+1] <= '7' && s[i+2] >= '0' && s[i+2] <= '7' && s[i+3] >= '0' && s[i+3] <= '7' {
			octal := s[i+1 : i+4]
			val, err := strconv.ParseUint(octal, 8, 8)
			if err == nil {
				buf.WriteByte(byte(val))
			}
			i += 4
		} else if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n':
				buf.WriteByte('\n')
			case 'r':
				buf.WriteByte('\r')
			case 't':
				buf.WriteByte('\t')
			case 'b':
				buf.WriteByte('\b')
			case 'f':
				buf.WriteByte('\f')
			case '\\':
				buf.WriteByte('\\')
			case '(':
				buf.WriteByte('(')
			case ')':
				buf.WriteByte(')')
			default:
				buf.WriteByte(s[i+1])
			}
			i += 2
		} else {
			buf.WriteByte(s[i])
			i++
		}
	}
	return buf.String()
}

func decodeHexString(s string) string {
	s = strings.TrimSpace(s)
	if len(s)%2 != 0 {
		s = s + "0"
	}
	var buf bytes.Buffer
	for i := 0; i < len(s); i += 2 {
		b, err := strconv.ParseUint(s[i:i+2], 16, 8)
		if err == nil {
			buf.WriteByte(byte(b))
		}
	}
	result := buf.String()
	if strings.HasPrefix(result, "\xFE\xFF") {
		return decodeUTF16BE(result[2:])
	}
	return result
}

func decodeUTF16BE(s string) string {
	if len(s)%2 != 0 {
		s += "\x00"
	}
	runes := make([]rune, 0, len(s)/2)
	for i := 0; i < len(s); i += 2 {
		if i+1 >= len(s) {
			break
		}
		r := rune(s[i])<<8 | rune(s[i+1])
		runes = append(runes, r)
	}
	return string(runes)
}

func decodePDFTJArray(token string) string {
	if !strings.HasPrefix(token, "[") || !strings.HasSuffix(token, "]") {
		return ""
	}
	content := token[1 : len(token)-1]
	var result strings.Builder
	parts := strings.Split(content, " ")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, "(") && strings.HasSuffix(p, ")") {
			text := parseOctalEscapes(p[1 : len(p)-1])
			result.WriteString(decodePDFString(text))
		} else if strings.HasPrefix(p, "<") && strings.HasSuffix(p, ">") {
			result.WriteString(decodeHexString(p[1 : len(p)-1]))
		}
	}
	return result.String()
}

func extractTextFallback(content string) []string {
	var texts []string

	parenRe := regexp.MustCompile(`\(([^)]*)\)`)
	matches := parenRe.FindAllStringSubmatch(content, -1)
	for _, m := range matches {
		if len(m) > 1 {
			text := parseOctalEscapes(m[1])
			text = decodePDFString(text)
			text = strings.TrimSpace(text)
			if text != "" && len(text) > 1 && len(text) < 300 {
				if isLikelyText(text) {
					texts = append(texts, text)
				}
			}
		}
	}

	if len(texts) == 0 {
		hexRe := regexp.MustCompile(`<([0-9A-Fa-f]{20,})>`)
		matches := hexRe.FindAllStringSubmatch(content, -1)
		for _, m := range matches {
			if len(m) > 1 {
				t := decodeHexString(m[1])
				t = strings.TrimSpace(t)
				if t != "" && isLikelyText(t) {
					texts = append(texts, t)
				}
			}
		}
	}

	return texts
}

func isLikelyText(s string) bool {
	if len(s) == 0 {
		return false
	}
	printCount := 0
	for _, r := range s {
		if (r >= 32 && r <= 126) || (r >= 0x4E00 && r <= 0x9FFF) || r == '\n' || r == '\t' {
			printCount++
		}
	}
	return float64(printCount)/float64(len(s)) > 0.7
}

func decodePDFString(s string) string {
	if s == "" {
		return ""
	}

	if len(s) >= 2 && s[0] == 0xFE && s[1] == 0xFF {
		return decodeUTF16BE(s[2:])
	}

	nullCount := 0
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			nullCount++
		}
	}
	if len(s) > 0 && float64(nullCount)/float64(len(s)) > 0.3 {
		return decodeUTF16BE(s)
	}

	return s
}

func extractBlocksFromContent(content string, pageNr int) []models.TextBlock {
	var blocks []models.TextBlock

	pageHeight := 842.0

	ts := newPDFTextState()
	lastY := pageHeight

	tokens := tokenizePDFContent(content)
	i := 0
	var currentText strings.Builder
	var lastBlockX, lastBlockY float64

	for i < len(tokens) {
		tok := tokens[i]

		switch tok {
		case "BT":
			ts = newPDFTextState()
			ts.inTextObject = true
			currentText.Reset()
		case "ET":
			ts.inTextObject = false
			if currentText.Len() > 0 {
				text := strings.TrimSpace(currentText.String())
				if text != "" {
					w := float64(len(text)) * ts.fontSize * 0.5
					if w < 5 {
						w = 5
					}
					h := ts.fontSize * 1.2
					if h < 5 {
						h = 5
					}
					blocks = append(blocks, models.TextBlock{
						Text:     text,
						X:        ts.x,
						Y:        pageHeight - ts.y,
						Width:    w,
						Height:   h,
						PageNum:  pageNr,
						FontSize: ts.fontSize,
					})
					lastBlockX = ts.x
					lastBlockY = ts.y
				}
				currentText.Reset()
			}
		case "Tf":
			if i >= 2 {
				if f, err := parseFloat(tokens[i-2]); err == nil {
					ts.fontSize = f
				}
			}
		case "Td":
			if i >= 2 {
				if dy, err := parseFloat(tokens[i-1]); err == nil {
					if dx, err := parseFloat(tokens[i-2]); err == nil {
						ts.applyTd(dx, dy)
						if ts.y != lastY && currentText.Len() > 0 {
							text := strings.TrimSpace(currentText.String())
							if text != "" {
								w := float64(len(text)) * ts.fontSize * 0.5
								if w < 5 {
									w = 5
								}
								h := ts.fontSize * 1.2
								if h < 5 {
									h = 5
								}
								blocks = append(blocks, models.TextBlock{
									Text:     text,
									X:        lastBlockX,
									Y:        pageHeight - lastBlockY,
									Width:    w,
									Height:   h,
									PageNum:  pageNr,
									FontSize: ts.fontSize,
								})
								currentText.Reset()
							}
						}
						lastY = ts.y
					}
				}
			}
		case "TD":
			if i >= 2 {
				if dy, err := parseFloat(tokens[i-1]); err == nil {
					ts.leading = -dy
					if dx, err := parseFloat(tokens[i-2]); err == nil {
						ts.applyTd(dx, dy)
						if ts.y != lastY && currentText.Len() > 0 {
							text := strings.TrimSpace(currentText.String())
							if text != "" {
								w := float64(len(text)) * ts.fontSize * 0.5
								if w < 5 {
									w = 5
								}
								h := ts.fontSize * 1.2
								if h < 5 {
									h = 5
								}
								blocks = append(blocks, models.TextBlock{
									Text:     text,
									X:        lastBlockX,
									Y:        pageHeight - lastBlockY,
									Width:    w,
									Height:   h,
									PageNum:  pageNr,
									FontSize: ts.fontSize,
								})
								currentText.Reset()
							}
						}
						lastY = ts.y
					}
				}
			}
		case "Tm":
			if i >= 6 {
				if e, err := parseFloat(tokens[i-2]); err == nil {
					if f, err := parseFloat(tokens[i-1]); err == nil {
						ts.textMatrix = [6]float64{0, 0, 0, 0, e, f}
						ts.x = e
						ts.y = f
						ts.startX = e
					}
				}
			}
		case "T*":
			ts.newLine()
			if currentText.Len() > 0 {
				text := strings.TrimSpace(currentText.String())
				if text != "" {
					w := float64(len(text)) * ts.fontSize * 0.5
					if w < 5 {
						w = 5
					}
					h := ts.fontSize * 1.2
					if h < 5 {
						h = 5
					}
					blocks = append(blocks, models.TextBlock{
						Text:     text,
						X:        lastBlockX,
						Y:        pageHeight - lastBlockY,
						Width:    w,
						Height:   h,
						PageNum:  pageNr,
						FontSize: ts.fontSize,
					})
					currentText.Reset()
				}
			}
		case "Tj":
			if i >= 1 {
				text := decodePDFTextToken(tokens[i-1])
				if text != "" {
					currentText.WriteString(text)
					lastBlockX = ts.x
					lastBlockY = ts.y
				}
			}
		case "'":
			ts.newLine()
			if currentText.Len() > 0 {
				text := strings.TrimSpace(currentText.String())
				if text != "" {
					w := float64(len(text)) * ts.fontSize * 0.5
					if w < 5 {
						w = 5
					}
					h := ts.fontSize * 1.2
					if h < 5 {
						h = 5
					}
					blocks = append(blocks, models.TextBlock{
						Text:     text,
						X:        lastBlockX,
						Y:        pageHeight - lastBlockY,
						Width:    w,
						Height:   h,
						PageNum:  pageNr,
						FontSize: ts.fontSize,
					})
					currentText.Reset()
				}
			}
			if i >= 1 {
				text := decodePDFTextToken(tokens[i-1])
				if text != "" {
					currentText.WriteString(text)
					lastBlockX = ts.x
					lastBlockY = ts.y
				}
			}
		case "\"":
			ts.newLine()
			if currentText.Len() > 0 {
				text := strings.TrimSpace(currentText.String())
				if text != "" {
					w := float64(len(text)) * ts.fontSize * 0.5
					if w < 5 {
						w = 5
					}
					h := ts.fontSize * 1.2
					if h < 5 {
						h = 5
					}
					blocks = append(blocks, models.TextBlock{
						Text:     text,
						X:        lastBlockX,
						Y:        pageHeight - lastBlockY,
						Width:    w,
						Height:   h,
						PageNum:  pageNr,
						FontSize: ts.fontSize,
					})
					currentText.Reset()
				}
			}
			if i >= 3 {
				text := decodePDFTextToken(tokens[i-3])
				if text != "" {
					currentText.WriteString(text)
					lastBlockX = ts.x
					lastBlockY = ts.y
				}
			}
		case "TJ":
			if i >= 1 {
				arrText := decodePDFTJArray(tokens[i-1])
				if arrText != "" {
					currentText.WriteString(arrText)
					lastBlockX = ts.x
					lastBlockY = ts.y
				}
			}
		}
		i++
	}

	if currentText.Len() > 0 {
		text := strings.TrimSpace(currentText.String())
		if text != "" {
			w := float64(len(text)) * ts.fontSize * 0.5
			if w < 5 {
				w = 5
			}
			h := ts.fontSize * 1.2
			if h < 5 {
				h = 5
			}
			blocks = append(blocks, models.TextBlock{
				Text:     text,
				X:        ts.x,
				Y:        pageHeight - ts.y,
				Width:    w,
				Height:   h,
				PageNum:  pageNr,
				FontSize: ts.fontSize,
			})
		}
	}

	if len(blocks) == 0 {
		fbTexts := extractTextFallback(content)
		y := 50.0
		for _, t := range fbTexts {
			blocks = append(blocks, models.TextBlock{
				Text:     t,
				X:        50,
				Y:        y,
				Width:    float64(len(t)) * 7,
				Height:   14,
				PageNum:  pageNr,
				FontSize: 12,
			})
			y += 14
		}
	}

	return blocks
}

type ocrResult struct {
	text   string
	blocks []models.TextBlock
}

func (p *PDFParser) runOCR(ctx context.Context, filePath string) ([]string, []models.TextBlock, error) {
	ocrCtx, cancel := context.WithTimeout(ctx, time.Duration(p.cfg.OCR.TimeoutSec)*time.Second)
	defer cancel()

	tmpDir, err := os.MkdirTemp("", "pdfocr-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	err = convertPDFToImages(ocrCtx, filePath, tmpDir, p.cfg.OCR.DPI)
	if err != nil {
		return nil, nil, fmt.Errorf("convert pdf to images: %w", err)
	}

	entries, _ := os.ReadDir(tmpDir)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var mu sync.Mutex
	var wg sync.WaitGroup
	workerCount := p.cfg.Pipeline.Workers
	if workerCount > len(entries) {
		workerCount = len(entries)
	}
	if workerCount < 1 {
		workerCount = 1
	}

	type job struct {
		idx  int
		path string
	}
	jobs := make(chan job, len(entries))
	results := make([]ocrResult, len(entries))
	errs := make([]error, len(entries))

	for w := 0; w < workerCount; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				text, blocks, err := ocrImage(ocrCtx, j.path, p.cfg.OCR.Languages, j.idx+1)
				mu.Lock()
				results[j.idx] = ocrResult{text: text, blocks: blocks}
				errs[j.idx] = err
				mu.Unlock()
			}
		}()
	}

	for i, e := range entries {
		if !e.IsDir() {
			jobs <- job{idx: i, path: filepath.Join(tmpDir, e.Name())}
		}
	}
	close(jobs)
	wg.Wait()

	var textPages []string
	var textBlocks []models.TextBlock

	for i, r := range results {
		if errs[i] != nil {
			textPages = append(textPages, "")
			continue
		}
		textPages = append(textPages, r.text)
		textBlocks = append(textBlocks, r.blocks...)
	}

	return textPages, textBlocks, nil
}

func convertPDFToImages(ctx context.Context, pdfPath, outDir string, dpi int) error {
	if _, err := exec.LookPath("pdftoppm"); err == nil {
		args := []string{
			"-r", fmt.Sprintf("%d", dpi),
			"-png",
			pdfPath,
			filepath.Join(outDir, "page"),
		}
		cmd := exec.CommandContext(ctx, "pdftoppm", args...)
		return cmd.Run()
	}
	if _, err := exec.LookPath("convert"); err == nil {
		args := []string{
			"-density", fmt.Sprintf("%d", dpi),
			pdfPath,
			"-quality", "90",
			filepath.Join(outDir, "page_%04d.png"),
		}
		cmd := exec.CommandContext(ctx, "convert", args...)
		return cmd.Run()
	}
	return fmt.Errorf("no PDF-to-image converter found (install poppler-utils or ImageMagick)")
}

func ocrImage(ctx context.Context, imgPath, langs string, pageNum int) (string, []models.TextBlock, error) {
	if _, err := exec.LookPath("tesseract"); err != nil {
		return "", nil, fmt.Errorf("tesseract not found")
	}

	tmpOut := filepath.Join(filepath.Dir(imgPath), fmt.Sprintf("out_%d", pageNum))
	args := []string{imgPath, tmpOut, "-l", langs, "--psm", "6", "tsv"}
	cmd := exec.CommandContext(ctx, "tesseract", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", nil, fmt.Errorf("tesseract: %w: %s", err, stderr.String())
	}

	tsvPath := tmpOut + ".tsv"
	tsvFile, err := os.Open(tsvPath)
	if err != nil {
		return "", nil, fmt.Errorf("open tsv: %w", err)
	}
	defer tsvFile.Close()

	blocks, fullText := parseTSV(tsvFile, pageNum)

	txtPath := tmpOut + ".txt"
	if txtData, err := os.ReadFile(txtPath); err == nil {
		if len(txtData) > len(fullText) {
			fullText = string(txtData)
		}
	}

	return fullText, blocks, nil
}

func parseTSV(r io.Reader, pageNum int) ([]models.TextBlock, string) {
	var blocks []models.TextBlock
	var lines []string
	scanner := bufio.NewScanner(r)
	first := true
	for scanner.Scan() {
		if first {
			first = false
			continue
		}
		line := scanner.Text()
		parts := strings.Split(line, "\t")
		if len(parts) < 12 {
			continue
		}
		text := parts[11]
		if text == "" {
			continue
		}
		var left, top, width, height int
		fmt.Sscanf(parts[6], "%d", &left)
		fmt.Sscanf(parts[7], "%d", &top)
		fmt.Sscanf(parts[8], "%d", &width)
		fmt.Sscanf(parts[9], "%d", &height)

		lines = append(lines, text)
		blocks = append(blocks, models.TextBlock{
			Text:     text,
			X:        float64(left),
			Y:        float64(top),
			Width:    float64(width),
			Height:   float64(height),
			PageNum:  pageNum,
			FontSize: float64(height),
		})
	}
	return blocks, strings.Join(lines, " ")
}

func (p *PDFParser) detectTables(blocks []models.TextBlock) []models.Table {
	if len(blocks) < 6 {
		return nil
	}

	var tables []models.Table

	pageWidth := 0.0
	pageHeight := 0.0
	for _, b := range blocks {
		if b.X+b.Width > pageWidth {
			pageWidth = b.X + b.Width
		}
		if b.Y+b.Height > pageHeight {
			pageHeight = b.Y + b.Height
		}
	}
	if pageWidth <= 0 {
		pageWidth = 595
	}
	if pageHeight <= 0 {
		pageHeight = 842
	}

	xStrips := make(map[int][]models.TextBlock)
	stripWidth := 5.0
	for _, b := range blocks {
		strip := int(b.X / stripWidth)
		xStrips[strip] = append(xStrips[strip], b)
	}

	colGaps := p.findGaps(xStrips, pageWidth, stripWidth, 0.05)

	yStrips := make(map[int][]models.TextBlock)
	stripHeight := 5.0
	avgLineHeight := p.avgLineHeight(blocks)
	for _, b := range blocks {
		strip := int(b.Y / stripHeight)
		yStrips[strip] = append(yStrips[strip], b)
	}
	rowGapThreshold := avgLineHeight * 1.5
	rowGaps := p.findRowGaps(yStrips, pageHeight, stripHeight, rowGapThreshold)

	if len(colGaps) >= 2 && len(rowGaps) >= 2 {
		cols := p.gapsToBoundaries(colGaps, pageWidth)
		rows := p.gapsToBoundaries(rowGaps, pageHeight)

		table := models.Table{
			PageNum: blocks[0].PageNum,
			X:       cols[0],
			Y:       rows[0],
			Width:   cols[len(cols)-1] - cols[0],
			Height:  rows[len(rows)-1] - rows[0],
		}

		tableRows := len(rows) - 1
		tableCols := len(cols) - 1
		if tableRows > 0 && tableCols > 0 && tableRows*tableCols <= 500 {
			table.Rows = make([][]models.TableCell, tableRows)
			for r := 0; r < tableRows; r++ {
				table.Rows[r] = make([]models.TableCell, tableCols)
				for c := 0; c < tableCols; c++ {
					cell := models.TableCell{
						Row: r, Col: c,
						X: cols[c], Y: rows[r],
						Width:  cols[c+1] - cols[c],
						Height: rows[r+1] - rows[r],
					}
					for _, b := range blocks {
						if b.X >= cols[c]-2 && b.X+b.Width <= cols[c+1]+2 &&
							b.Y >= rows[r]-2 && b.Y+b.Height <= rows[r+1]+2 {
							if cell.Text != "" {
								cell.Text += " "
							}
							cell.Text += b.Text
						}
					}
					table.Rows[r][c] = cell
				}
			}

			nonEmpty := 0
			for _, row := range table.Rows {
				for _, cell := range row {
					if strings.TrimSpace(cell.Text) != "" {
						nonEmpty++
					}
				}
			}
			if nonEmpty >= 4 {
				tables = append(tables, table)
			}
		}
	}

	return tables
}

func (p *PDFParser) avgLineHeight(blocks []models.TextBlock) float64 {
	if len(blocks) == 0 {
		return 12
	}
	sum := 0.0
	for _, b := range blocks {
		if b.Height > 0 {
			sum += b.Height
		}
	}
	return sum / float64(len(blocks))
}

func (p *PDFParser) findGaps(strips map[int][]models.TextBlock, maxW, stripW, minRatio float64) []int {
	maxStrip := int(maxW / stripW)
	empty := make([]int, 0)
	for i := 0; i < maxStrip; i++ {
		if len(strips[i]) == 0 {
			empty = append(empty, i)
		}
	}
	if len(empty) == 0 {
		return nil
	}

	minGapStrips := int(minRatio * maxW / stripW)
	if minGapStrips < 2 {
		minGapStrips = 2
	}

	var gaps []int
	start := empty[0]
	for i := 1; i < len(empty); i++ {
		if empty[i] != empty[i-1]+1 {
			length := empty[i-1] - start + 1
			if length >= minGapStrips {
				mid := (start + empty[i-1]) / 2
				gaps = append(gaps, mid)
			}
			start = empty[i]
		}
	}
	length := empty[len(empty)-1] - start + 1
	if length >= minGapStrips {
		mid := (start + empty[len(empty)-1]) / 2
		gaps = append(gaps, mid)
	}
	return gaps
}

func (p *PDFParser) findRowGaps(strips map[int][]models.TextBlock, maxH, stripH, threshold float64) []int {
	maxStrip := int(maxH / stripH)
	empty := make([]int, 0)
	for i := 0; i < maxStrip; i++ {
		if len(strips[i]) == 0 {
			empty = append(empty, i)
		}
	}
	if len(empty) == 0 {
		return nil
	}

	minGapStrips := int(threshold / stripH)
	if minGapStrips < 1 {
		minGapStrips = 1
	}

	var gaps []int
	start := empty[0]
	for i := 1; i < len(empty); i++ {
		if empty[i] != empty[i-1]+1 {
			length := empty[i-1] - start + 1
			if length >= minGapStrips {
				mid := (start + empty[i-1]) / 2
				gaps = append(gaps, mid)
			}
			start = empty[i]
		}
	}
	length := empty[len(empty)-1] - start + 1
	if length >= minGapStrips {
		mid := (start + empty[len(empty)-1]) / 2
		gaps = append(gaps, mid)
	}
	return gaps
}

func (p *PDFParser) gapsToBoundaries(gaps []int, maxVal float64) []float64 {
	if len(gaps) == 0 {
		return []float64{0, maxVal}
	}
	boundaries := []float64{0}
	for _, g := range gaps {
		boundaries = append(boundaries, float64(g)*5)
	}
	boundaries = append(boundaries, maxVal)
	return boundaries
}

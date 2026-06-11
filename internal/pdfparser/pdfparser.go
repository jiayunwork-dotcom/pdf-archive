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
	parenTextRe = regexp.MustCompile(`\(([^)]*)\)`)
	tfRe        = regexp.MustCompile(`([\d.]+)\s+Tf`)
	tdRe        = regexp.MustCompile(`([-\d.]+)\s+([-\d.]+)\s+T[dD]`)
	tjRe        = regexp.MustCompile(`\(([^)]*)\)\s*Tj`)
	tjArrayRe   = regexp.MustCompile(`\[([^\]]*)\]\s*TJ`)
)

func extractReadableText(content string) string {
	var texts []string

	tjArrayRe := regexp.MustCompile(`\[([^\]]*)\]\s*TJ`)
	content = tjArrayRe.ReplaceAllStringFunc(content, func(s string) string {
		m := tjArrayRe.FindStringSubmatch(s)
		if len(m) < 2 {
			return s
		}
		inner := m[1]
		parts := parenTextRe.FindAllStringSubmatch(inner, -1)
		var combined string
		for _, p := range parts {
			if len(p) > 1 {
				combined += p[1]
			}
		}
		return "(" + combined + ") Tj"
	})

	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if strings.Contains(line, "Tj") {
			matches := tjRe.FindAllStringSubmatch(line, -1)
			for _, m := range matches {
				if len(m) > 1 {
					t := decodePDFString(m[1])
					if t != "" {
						texts = append(texts, t)
					}
				}
			}
		}
		if strings.Contains(line, "TJ") {
			matches := parenTextRe.FindAllStringSubmatch(line, -1)
			for _, m := range matches {
				if len(m) > 1 {
					t := decodePDFString(m[1])
					if t != "" {
						texts = append(texts, t)
					}
				}
			}
		}
	}

	if len(texts) > 0 {
		return strings.Join(texts, "\n")
	}

	return cleanContentStream(content)
}

func cleanContentStream(content string) string {
	content = regexp.MustCompile(`\b[0-9.]+\s+[A-Za-z]+\b`).ReplaceAllString(content, " ")
	content = strings.ReplaceAll(content, "Tj", "\n")
	content = strings.ReplaceAll(content, "TJ", "\n")
	content = strings.ReplaceAll(content, "ET", "\n\n")
	content = regexp.MustCompile(`[^[:print:]\n]`).ReplaceAllString(content, " ")
	content = strings.TrimSpace(content)
	return content
}

func decodePDFString(s string) string {
	s = strings.ReplaceAll(s, `\\n`, "\n")
	s = strings.ReplaceAll(s, `\r`, "\r")
	s = strings.ReplaceAll(s, `\t`, "\t")
	s = strings.ReplaceAll(s, `\\`, "\\")
	s = strings.ReplaceAll(s, `\(`, "(")
	s = strings.ReplaceAll(s, `\)`, ")")
	return s
}

func extractBlocksFromContent(content string, pageNr int) []models.TextBlock {
	var blocks []models.TextBlock

	pageHeight := 842.0

	var curX, curY float64 = 50, pageHeight
	var fontSize float64 = 12

	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if m := tfRe.FindStringSubmatch(line); len(m) > 1 {
			fmt.Sscanf(m[1], "%f", &fontSize)
			if fontSize < 1 {
				fontSize = 12
			}
		}
		if m := tdRe.FindStringSubmatch(line); len(m) > 2 {
			var dx, dy float64
			fmt.Sscanf(m[1], "%f", &dx)
			fmt.Sscanf(m[2], "%f", &dy)
			curX = dx
			curY -= dy
		}
		if strings.Contains(line, "Tj") || strings.Contains(line, "TJ") {
			matches := parenTextRe.FindAllStringSubmatch(line, -1)
			for _, m := range matches {
				if len(m) > 1 {
					text := decodePDFString(m[1])
					text = strings.TrimSpace(text)
					if text == "" {
						continue
					}
					w := float64(len(text)) * fontSize * 0.5
					if w < 5 {
						w = 5
					}
					h := fontSize * 1.2
					if h < 5 {
						h = 5
					}
					blocks = append(blocks, models.TextBlock{
						Text:     text,
						X:        curX,
						Y:        pageHeight - curY,
						Width:    w,
						Height:   h,
						PageNum:  pageNr,
						FontSize: fontSize,
					})
					curX += w
				}
			}
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

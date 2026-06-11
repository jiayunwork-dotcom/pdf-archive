package extractor

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"pdf-archive/internal/config"
	"pdf-archive/internal/utils"
	"pdf-archive/models"
)

type Extractor struct {
	cfg *config.Config
}

func New(cfg *config.Config) *Extractor {
	return &Extractor{cfg: cfg}
}

func (e *Extractor) Extract(doc *models.ParsedDocument, classResult *models.ClassificationResult) map[string]models.ExtractedField {
	results := make(map[string]models.ExtractedField)

	if doc == nil || classResult == nil {
		return results
	}

	docType := string(classResult.Type)
	typeCfg := e.cfg.GetDocTypeConfig(docType)
	if typeCfg == nil {
		return results
	}

	ocrFactor := 1.0
	if doc.Metadata.IsOCR {
		ocrFactor = e.cfg.OCR.ConfidenceThreshold
	}

	for _, rule := range typeCfg.ExtractRules {
		field := e.extractField(rule, doc, ocrFactor)
		if field.Confidence == 0 && len(rule.Alternatives) > 0 {
			for _, altRule := range rule.Alternatives {
				altField := e.extractField(altRule, doc, ocrFactor)
				if altField.Confidence > field.Confidence {
					altField.Name = rule.FieldName
					field = altField
					if field.Confidence >= 0.9 {
						break
					}
				}
			}
		}
		field.Name = rule.FieldName
		field.NeedReview = field.Confidence < e.cfg.Pipeline.LowConfThreshold
		if field.Value == nil && rule.Required {
			field.Error = "required field extraction failed"
		}
		results[rule.FieldName] = field
	}

	return results
}

func (e *Extractor) extractField(rule config.ExtractRule, doc *models.ParsedDocument, ocrFactor float64) models.ExtractedField {
	field := models.ExtractedField{
		Method:     rule.Strategy,
		Confidence: 0,
	}

	switch rule.Strategy {
	case "regex":
		return e.extractRegex(rule, doc, ocrFactor)
	case "keyword":
		return e.extractKeyword(rule, doc, ocrFactor)
	case "table_cell":
		return e.extractTableCell(rule, doc, ocrFactor)
	case "relative_pos":
		return e.extractRelativePos(rule, doc, ocrFactor)
	case "first_line":
		return e.extractFirstLine(rule, doc, ocrFactor)
	default:
		field.Error = fmt.Sprintf("unknown strategy: %s", rule.Strategy)
		return field
	}
}

func (e *Extractor) extractRegex(rule config.ExtractRule, doc *models.ParsedDocument, ocrFactor float64) models.ExtractedField {
	field := models.ExtractedField{
		Method:     "regex",
		Confidence: 0,
	}

	if rule.Pattern == "" {
		field.Error = "empty regex pattern"
		return field
	}

	re, err := regexp.Compile(rule.Pattern)
	if err != nil {
		field.Error = fmt.Sprintf("invalid regex: %v", err)
		return field
	}

	type matchInfo struct {
		text    string
		matches []string
		pageNum int
		pos     string
	}

	var bestMatch *matchInfo

	for pageIdx, page := range doc.Pages {
		fullText := page.Text
		matches := re.FindAllStringSubmatch(fullText, -1)
		if len(matches) == 0 {
			continue
		}

		for _, m := range matches {
			if len(m) < 2 {
				continue
			}
			if bestMatch == nil {
				bestMatch = &matchInfo{
					text:    m[0],
					matches: m,
					pageNum: pageIdx + 1,
				}
			}
		}
		if bestMatch != nil {
			break
		}
	}

	if bestMatch == nil {
		field.Error = "no regex match found"
		return field
	}

	groupIdx := rule.GroupIndex
	if groupIdx <= 0 || groupIdx >= len(bestMatch.matches) {
		groupIdx = 1
	}

	value := bestMatch.matches[groupIdx]
	field.Value = e.postProcess(value, rule.PostProcess)
	field.Confidence = 1.0 * ocrFactor
	field.PageNum = bestMatch.pageNum
	field.Position = bestMatch.pos

	return field
}

func (e *Extractor) extractKeyword(rule config.ExtractRule, doc *models.ParsedDocument, ocrFactor float64) models.ExtractedField {
	field := models.ExtractedField{
		Method:     "keyword",
		Confidence: 0,
	}

	if rule.Keyword == "" {
		field.Error = "empty keyword"
		return field
	}

	direction := rule.Direction
	if direction == "" {
		direction = "right"
	}
	distance := rule.Distance
	if distance <= 0 {
		distance = 200
	}

	keywordLower := strings.ToLower(rule.Keyword)
	var keywordBlock *models.TextBlock
	keywordPage := 0

	for pageIdx, page := range doc.Pages {
		for i := range page.TextBlocks {
			b := &page.TextBlocks[i]
			if strings.Contains(strings.ToLower(b.Text), keywordLower) {
				keywordBlock = b
				keywordPage = pageIdx + 1
				break
			}
		}
		if keywordBlock != nil {
			break
		}
	}

	if keywordBlock == nil {
		fullTextLower := strings.ToLower(doc.FullText)
		idx := strings.Index(fullTextLower, keywordLower)
		if idx < 0 {
			field.Error = "keyword not found"
			return field
		}

		end := idx + len(rule.Keyword)
		after := fullTextLower[end:]
		if len(after) > distance {
			after = after[:distance]
		}

		numberRe := regexp.MustCompile(`[\d,]+(?:\.\d+)?`)
		dateRe := regexp.MustCompile(`\d{4}[-/年]\d{1,2}[-/月]?\d{0,2}日?`)

		var match string
		if direction == "right" || direction == "down" || direction == "" {
			if dateMatch := dateRe.FindString(after); dateMatch != "" {
				match = dateMatch
			} else if numMatch := numberRe.FindString(after); numMatch != "" {
				match = numMatch
			} else {
				parts := strings.Fields(after)
				if len(parts) > 0 {
					match = parts[0]
				}
			}
		}

		if match != "" {
			field.Value = e.postProcess(match, rule.PostProcess)
			field.Confidence = 0.7 * ocrFactor
			field.Source = "fulltext_search"
			return field
		}
		field.Error = "could not extract value near keyword in fulltext"
		return field
	}

	var candidates []*models.TextBlock
	pageNum := keywordPage - 1
	if pageNum < len(doc.Pages) {
		for i := range doc.Pages[pageNum].TextBlocks {
			b := &doc.Pages[pageNum].TextBlocks[i]
			if b == keywordBlock {
				continue
			}

			switch direction {
			case "right":
				if isRightOf(b, keywordBlock, distance) {
					candidates = append(candidates, b)
				}
			case "down":
				if isBelow(b, keywordBlock, distance) {
					candidates = append(candidates, b)
				}
			case "left":
				if isLeftOf(b, keywordBlock, distance) {
					candidates = append(candidates, b)
				}
			case "any":
				if isNear(b, keywordBlock, distance) {
					candidates = append(candidates, b)
				}
			default:
				if isRightOf(b, keywordBlock, distance) || isBelow(b, keywordBlock, distance) {
					candidates = append(candidates, b)
				}
			}
		}
	}

	if len(candidates) == 0 {
		field.Error = "no nearby text blocks found"
		return field
	}

	sort.Slice(candidates, func(i, j int) bool {
		return distanceTo(candidates[i], keywordBlock) < distanceTo(candidates[j], keywordBlock)
	})

	for _, b := range candidates {
		text := strings.TrimSpace(b.Text)
		if text == "" {
			continue
		}
		field.Value = e.postProcess(text, rule.PostProcess)
		field.Confidence = 0.75 * ocrFactor
		field.PageNum = keywordPage
		field.Position = fmt.Sprintf("near(%.0f,%.0f)", b.X, b.Y)
		return field
	}

	field.Error = "no valid value in candidates"
	return field
}

func (e *Extractor) extractTableCell(rule config.ExtractRule, doc *models.ParsedDocument, ocrFactor float64) models.ExtractedField {
	field := models.ExtractedField{
		Method:     "table_cell",
		Confidence: 0,
	}

	if len(doc.Tables) == 0 {
		field.Error = "no tables found"
		return field
	}

	for _, table := range doc.Tables {
		if rule.HeaderMatch != "" {
			headerIdx := -1
			if len(table.Rows) > 0 {
				for colIdx, cell := range table.Rows[0] {
					if strings.Contains(strings.ToLower(cell.Text), strings.ToLower(rule.HeaderMatch)) {
						headerIdx = colIdx
						break
					}
				}
			}
			if headerIdx < 0 {
				continue
			}
			rowIdx := rule.TableRow
			if rowIdx <= 0 {
				rowIdx = 1
			}
			if rowIdx < len(table.Rows) && headerIdx < len(table.Rows[rowIdx]) {
				cell := table.Rows[rowIdx][headerIdx]
				if strings.TrimSpace(cell.Text) != "" {
					field.Value = e.postProcess(cell.Text, rule.PostProcess)
					field.Confidence = 0.85 * ocrFactor
					field.PageNum = table.PageNum
					field.Position = fmt.Sprintf("table_cell[%d][%d]", rowIdx, headerIdx)
					return field
				}
			}
		} else {
			rowIdx := rule.TableRow
			colIdx := rule.TableCol
			if rowIdx < 0 {
				rowIdx = len(table.Rows) + rowIdx
			}
			if colIdx < 0 && len(table.Rows) > 0 {
				colIdx = len(table.Rows[0]) + colIdx
			}
			if rowIdx >= 0 && rowIdx < len(table.Rows) {
				if colIdx >= 0 && colIdx < len(table.Rows[rowIdx]) {
					cell := table.Rows[rowIdx][colIdx]
					if strings.TrimSpace(cell.Text) != "" {
						field.Value = e.postProcess(cell.Text, rule.PostProcess)
						field.Confidence = 0.9 * ocrFactor
						field.PageNum = table.PageNum
						field.Position = fmt.Sprintf("table_cell[%d][%d]", rowIdx, colIdx)
						return field
					}
				}
			}
		}
	}

	field.Error = "target table cell not found or empty"
	return field
}

func (e *Extractor) extractRelativePos(rule config.ExtractRule, doc *models.ParsedDocument, ocrFactor float64) models.ExtractedField {
	return e.extractKeyword(rule, doc, ocrFactor)
}

func (e *Extractor) extractFirstLine(rule config.ExtractRule, doc *models.ParsedDocument, ocrFactor float64) models.ExtractedField {
	field := models.ExtractedField{
		Method:     "first_line",
		Confidence: 0,
	}

	if len(doc.Pages) == 0 {
		field.Error = "no pages"
		return field
	}

	for _, page := range doc.Pages {
		lines := strings.Split(page.Text, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" {
				field.Value = e.postProcess(line, rule.PostProcess)
				field.Confidence = 0.7 * ocrFactor
				field.Position = fmt.Sprintf("page_%d_first_line", page.PageNum)
				return field
			}
		}
	}

	field.Error = "no non-empty first line found"
	return field
}

func (e *Extractor) postProcess(value string, rules []config.PostProcessRule) interface{} {
	if len(rules) == 0 {
		return utils.CleanString(value)
	}

	result := value
	for _, rule := range rules {
		switch rule.Type {
		case "trim":
			result = strings.TrimSpace(result)
		case "date_format":
			result = utils.NormalizeDate(result)
		case "amount_number":
			if amount, err := utils.ParseAmount(result); err == nil {
				return amount
			}
		case "integer":
			re := regexp.MustCompile(`\d+`)
			if m := re.FindString(result); m != "" {
				if n, err := strconv.Atoi(m); err == nil {
					return n
				}
			}
		case "float":
			if n, err := utils.ParseAmount(result); err == nil {
				return n
			}
		case "clean":
			result = utils.CleanString(result)
		case "uppercase":
			result = strings.ToUpper(result)
		case "lowercase":
			result = strings.ToLower(result)
		case "remove_whitespace":
			result = strings.ReplaceAll(result, " ", "")
		case "sanitize_filename":
			result = utils.SanitizeFilename(result)
		case "pad_zero":
			if len(result) == 1 {
				result = "0" + result
			}
		}
	}
	return strings.TrimSpace(result)
}

func isRightOf(a, ref *models.TextBlock, maxDist int) bool {
	return a.X >= ref.X+ref.Width-5 &&
		math.Abs(a.Y-ref.Y) <= math.Max(ref.Height, a.Height)*1.5 &&
		a.X-ref.X <= float64(maxDist)
}

func isLeftOf(a, ref *models.TextBlock, maxDist int) bool {
	return a.X+a.Width <= ref.X+5 &&
		math.Abs(a.Y-ref.Y) <= math.Max(ref.Height, a.Height)*1.5 &&
		ref.X-(a.X+a.Width) <= float64(maxDist)
}

func isBelow(a, ref *models.TextBlock, maxDist int) bool {
	return a.Y >= ref.Y+ref.Height-2 &&
		a.Y-ref.Y <= float64(maxDist) &&
		math.Abs(a.X-ref.X) <= math.Max(ref.Width, a.Width)
}

func isNear(a, ref *models.TextBlock, maxDist int) bool {
	return distanceTo(a, ref) <= float64(maxDist)
}

func distanceTo(a, ref *models.TextBlock) float64 {
	ax := a.X + a.Width/2
	ay := a.Y + a.Height/2
	bx := ref.X + ref.Width/2
	by := ref.Y + ref.Height/2
	return math.Sqrt((ax-bx)*(ax-bx) + (ay-by)*(ay-by))
}

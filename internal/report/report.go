package report

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"pdf-archive/models"
)

type ReportData struct {
	Summary      *models.SummaryReport
	Results      []*models.DocumentResult
	InputDir     string
	ArchiveDir   string
	DryRun       bool
	GeneratedAt  time.Time
}

type docGroup struct {
	DocType string
	Items   []*models.DocumentResult
}

func GenerateHTML(data *ReportData) (string, error) {
	title := "PDF归档处理报告"
	if data.DryRun {
		title = "[DRY RUN] PDF归档处理报告"
	}

	groups := groupByType(data.Results)
	lowConfItems := filterLowConfidence(data.Results, 0.6)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>%s</title>
</head>
<body style="margin:0;padding:0;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Arial,'Noto Sans SC',sans-serif;background:#f5f7fa;color:#333;line-height:1.6;">
<div style="max-width:1200px;margin:0 auto;padding:20px;">
`, html.EscapeString(title)))

	writeHeader(&sb, title, data)
	writeStatsPanel(&sb, data)
	writeGroupedDetails(&sb, groups, data.InputDir)
	writeLowConfidenceSection(&sb, lowConfItems, data.InputDir)
	writeFooter(&sb, data)

	sb.WriteString(`</div>
</body>
</html>`)
	return sb.String(), nil
}

func writeHeader(sb *strings.Builder, title string, data *ReportData) {
	sb.WriteString(fmt.Sprintf(`
<div style="background:linear-gradient(135deg,#667eea 0%%,#764ba2 100%%);color:white;padding:30px;border-radius:12px;margin-bottom:24px;">
<h1 style="margin:0 0 8px 0;font-size:24px;">%s</h1>
<p style="margin:0;opacity:0.9;font-size:14px;">生成时间: %s | 输入目录: %s | 归档目录: %s</p>
</div>
`, html.EscapeString(title),
		data.GeneratedAt.Format("2006-01-02 15:04:05"),
		html.EscapeString(relPath(data.InputDir, data.InputDir)),
		html.EscapeString(relPath(data.ArchiveDir, data.ArchiveDir))))
}

func writeStatsPanel(sb *strings.Builder, data *ReportData) {
	s := data.Summary
	sb.WriteString(fmt.Sprintf(`
<div style="display:flex;flex-wrap:wrap;gap:16px;margin-bottom:24px;">
<div style="flex:1;min-width:140px;background:#e3f2fd;border-left:4px solid #2196f3;padding:16px 20px;border-radius:8px;">
<div style="font-size:28px;font-weight:bold;color:#1565c0;">%d</div>
<div style="font-size:13px;color:#555;">总文件数</div>
</div>
<div style="flex:1;min-width:140px;background:#e8f5e9;border-left:4px solid #4caf50;padding:16px 20px;border-radius:8px;">
<div style="font-size:28px;font-weight:bold;color:#2e7d32;">%d</div>
<div style="font-size:13px;color:#555;">成功数</div>
</div>
<div style="flex:1;min-width:140px;background:#ffebee;border-left:4px solid #f44336;padding:16px 20px;border-radius:8px;">
<div style="font-size:28px;font-weight:bold;color:#c62828;">%d</div>
<div style="font-size:13px;color:#555;">失败数</div>
</div>
<div style="flex:1;min-width:140px;background:#fff3e0;border-left:4px solid #ff9800;padding:16px 20px;border-radius:8px;">
<div style="font-size:28px;font-weight:bold;color:#e65100;">%d</div>
<div style="font-size:13px;color:#555;">需复核数</div>
</div>
</div>
`, s.TotalFiles, s.SuccessFiles, s.ErrorFiles, s.NeedReview))
}

func writeGroupedDetails(sb *strings.Builder, groups []docGroup, inputDir string) {
	sb.WriteString(`
<div style="background:white;border-radius:12px;padding:24px;margin-bottom:24px;box-shadow:0 2px 8px rgba(0,0,0,0.06);">
<h2 style="margin:0 0 20px 0;font-size:18px;border-bottom:2px solid #eee;padding-bottom:10px;">📋 按文档类型分组的详细列表</h2>
`)

	for _, g := range groups {
		displayName := g.DocType
		sb.WriteString(fmt.Sprintf(`
<h3 style="margin:20px 0 12px 0;font-size:16px;color:#444;">%s (%d 个文件)</h3>
<table style="width:100%%;border-collapse:collapse;font-size:13px;">
<thead>
<tr style="background:#f8f9fa;">
<th style="padding:10px 12px;text-align:left;border-bottom:2px solid #dee2e6;white-space:nowrap;">文件名</th>
<th style="padding:10px 12px;text-align:center;border-bottom:2px solid #dee2e6;white-space:nowrap;">分类置信度</th>
<th style="padding:10px 12px;text-align:left;border-bottom:2px solid #dee2e6;">提取字段</th>
<th style="padding:10px 12px;text-align:left;border-bottom:2px solid #dee2e6;white-space:nowrap;">归档路径</th>
</tr>
</thead>
<tbody>
`, html.EscapeString(displayName), len(g.Items)))

		for i, item := range g.Items {
			bg := ""
			if i%2 == 1 {
				bg = "background:#fafbfc;"
			}
			fileName := html.EscapeString(filepath.Base(item.FilePath))
			confidence := ""
			if item.Classification != nil {
				confidence = fmt.Sprintf("%.2f", item.Classification.Confidence)
			}
			confStyle := ""
			if item.Classification != nil && item.Classification.Confidence < 0.6 {
				confStyle = "color:#e65100;font-weight:bold;"
			}

			fieldsHTML := renderFields(item.Fields)
			archivePath := html.EscapeString(relPath(item.ArchivePath, inputDir))

			sb.WriteString(fmt.Sprintf(`
<tr style="%s">
<td style="padding:8px 12px;border-bottom:1px solid #eee;">%s</td>
<td style="padding:8px 12px;border-bottom:1px solid #eee;text-align:center;%s">%s</td>
<td style="padding:8px 12px;border-bottom:1px solid #eee;">%s</td>
<td style="padding:8px 12px;border-bottom:1px solid #eee;font-size:12px;color:#666;">%s</td>
</tr>
`, bg, fileName, confStyle, confidence, fieldsHTML, archivePath))
		}

		sb.WriteString(`</tbody></table>`)
	}

	if len(groups) == 0 {
		sb.WriteString(`<p style="color:#999;text-align:center;padding:20px;">暂无处理结果</p>`)
	}

	sb.WriteString(`</div>`)
}

func writeLowConfidenceSection(sb *strings.Builder, items []*models.DocumentResult, inputDir string) {
	sb.WriteString(fmt.Sprintf(`
<div style="background:white;border-radius:12px;padding:24px;margin-bottom:24px;box-shadow:0 2px 8px rgba(0,0,0,0.06);border-left:4px solid #ff9800;">
<h2 style="margin:0 0 20px 0;font-size:18px;border-bottom:2px solid #eee;padding-bottom:10px;">⚠️ 低置信度文件清单 (置信度 &lt; 0.6)</h2>
`))

	if len(items) == 0 {
		sb.WriteString(`<p style="color:#999;text-align:center;padding:20px;">无低置信度文件</p>`)
	} else {
		sb.WriteString(`
<table style="width:100%;border-collapse:collapse;font-size:13px;">
<thead>
<tr style="background:#fff3e0;">
<th style="padding:10px 12px;text-align:left;border-bottom:2px solid #ffe0b2;">文件名</th>
<th style="padding:10px 12px;text-align:center;border-bottom:2px solid #ffe0b2;">置信度</th>
<th style="padding:10px 12px;text-align:left;border-bottom:2px solid #ffe0b2;">分类类型</th>
<th style="padding:10px 12px;text-align:left;border-bottom:2px solid #ffe0b2;">归档路径</th>
</tr>
</thead>
<tbody>
`)
		for i, item := range items {
			bg := ""
			if i%2 == 1 {
				bg = "background:#fffbf0;"
			}
			fileName := html.EscapeString(filepath.Base(item.FilePath))
			confidence := ""
			docType := ""
			if item.Classification != nil {
				confidence = fmt.Sprintf("%.2f", item.Classification.Confidence)
				docType = string(item.Classification.Type)
			}
			archivePath := html.EscapeString(relPath(item.ArchivePath, inputDir))

			sb.WriteString(fmt.Sprintf(`
<tr style="%s">
<td style="padding:8px 12px;border-bottom:1px solid #ffe0b2;">%s</td>
<td style="padding:8px 12px;border-bottom:1px solid #ffe0b2;text-align:center;color:#e65100;font-weight:bold;">%s</td>
<td style="padding:8px 12px;border-bottom:1px solid #ffe0b2;">%s</td>
<td style="padding:8px 12px;border-bottom:1px solid #ffe0b2;font-size:12px;color:#666;">%s</td>
</tr>
`, bg, fileName, confidence, html.EscapeString(docType), archivePath))
		}
		sb.WriteString(`</tbody></table>`)
	}

	sb.WriteString(`</div>`)
}

func writeFooter(sb *strings.Builder, data *ReportData) {
	sb.WriteString(fmt.Sprintf(`
<div style="text-align:center;color:#999;font-size:12px;padding:20px 0;">
<p>pdf-archive 处理报告 | 总耗时: %dms | 生成于 %s</p>
</div>
`, data.Summary.TotalDuration, data.GeneratedAt.Format("2006-01-02 15:04:05")))
}

func renderFields(fields map[string]models.ExtractedField) string {
	if len(fields) == 0 {
		return `<span style="color:#999;">-</span>`
	}

	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var parts []string
	for _, k := range keys {
		f := fields[k]
		valHTML := ""
		if f.Value == nil {
			valHTML = `<span style="color:#999;font-style:italic;">未提取</span>`
		} else {
			valStr := fmt.Sprintf("%v", f.Value)
			if len(valStr) > 60 {
				valStr = valStr[:60] + "..."
			}
			valHTML = html.EscapeString(valStr)
		}
		parts = append(parts, fmt.Sprintf(`<span style="display:inline-block;margin:1px 4px 1px 0;"><b>%s</b>: %s</span>`, html.EscapeString(k), valHTML))
	}
	return strings.Join(parts, "")
}

func groupByType(results []*models.DocumentResult) []docGroup {
	m := make(map[string][]*models.DocumentResult)
	var typeOrder []string
	seen := make(map[string]bool)

	for _, r := range results {
		t := "unknown"
		if r.Classification != nil {
			t = string(r.Classification.Type)
		}
		if !seen[t] {
			seen[t] = true
			typeOrder = append(typeOrder, t)
		}
		m[t] = append(m[t], r)
	}

	var groups []docGroup
	for _, t := range typeOrder {
		groups = append(groups, docGroup{DocType: t, Items: m[t]})
	}
	return groups
}

func filterLowConfidence(results []*models.DocumentResult, threshold float64) []*models.DocumentResult {
	var out []*models.DocumentResult
	for _, r := range results {
		if r.Classification != nil && r.Classification.Confidence < threshold {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Classification.Confidence < out[j].Classification.Confidence
	})
	return out
}

func relPath(path, base string) string {
	if path == "" {
		return "-"
	}
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return path
	}
	return rel
}

func WriteReport(data *ReportData) (string, error) {
	html, err := GenerateHTML(data)
	if err != nil {
		return "", fmt.Errorf("generate html: %w", err)
	}

	filename := fmt.Sprintf("report_%s.html", data.GeneratedAt.Format("20060102_150405"))
	outPath := filepath.Join(data.ArchiveDir, filename)

	dir := filepath.Dir(outPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create report dir: %w", err)
	}

	if err := os.WriteFile(outPath, []byte(html), 0644); err != nil {
		return "", fmt.Errorf("write report: %w", err)
	}

	return outPath, nil
}

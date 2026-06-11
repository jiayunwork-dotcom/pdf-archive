package archiver

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"pdf-archive/internal/config"
	"pdf-archive/internal/utils"
	"pdf-archive/models"
)

type Archiver struct {
	cfg *config.Config
}

func New(cfg *config.Config) *Archiver {
	return &Archiver{cfg: cfg}
}

type ArchiveResult struct {
	Success   bool
	TargetPath string
	Mode      string
	Error     string
	Unsorted  bool
}

func (a *Archiver) Archive(
	srcPath string,
	fields map[string]models.ExtractedField,
	classResult *models.ClassificationResult,
	dryRun bool,
) ArchiveResult {
	result := ArchiveResult{
		Mode: a.cfg.Archive.Mode,
	}

	variables := a.buildVariables(fields, classResult)

	template := a.getArchiveTemplate(classResult)
	a.fillSmartDefaults(variables, classResult)

	targetRelPath, err := a.renderTemplate(template, variables)
	if err != nil || targetRelPath == "" {
		result.Unsorted = true
		targetRelPath = filepath.Join(a.cfg.Archive.UnsortedDir, filepath.Base(srcPath))
	}

	if !a.validateVariablesSmart(variables, template, classResult) {
		result.Unsorted = true
		targetRelPath = filepath.Join(a.cfg.Archive.UnsortedDir, filepath.Base(srcPath))
	}

	targetPath := filepath.Join(a.cfg.Archive.TargetDir, targetRelPath)
	targetPath = filepath.Clean(targetPath)

	targetDir := filepath.Dir(targetPath)
	if !dryRun {
		if err := utils.EnsureDir(targetDir); err != nil {
			result.Error = fmt.Sprintf("create dir: %v", err)
			return result
		}
	}

	if a.cfg.Archive.ConflictPolicy == "rename" {
		targetPath = utils.ResolveConflict(targetPath)
	}

	result.TargetPath = targetPath

	if dryRun {
		result.Success = true
		return result
	}

	switch a.cfg.Archive.Mode {
	case "copy":
		err = copyFile(srcPath, targetPath)
	case "move":
		err = os.Rename(srcPath, targetPath)
		if err != nil {
			err = copyFile(srcPath, targetPath)
			if err == nil {
				os.Remove(srcPath)
			}
		}
	case "link":
		err = os.Link(srcPath, targetPath)
	default:
		err = fmt.Errorf("unknown mode: %s", a.cfg.Archive.Mode)
	}

	if err != nil {
		result.Error = err.Error()
		return result
	}

	result.Success = true
	return result
}

func (a *Archiver) ComputeTargetPath(srcPath string, fields map[string]models.ExtractedField, classResult *models.ClassificationResult) string {
	variables := a.BuildVariablesMap(fields, classResult)
	template := a.getArchiveTemplate(classResult)
	a.fillSmartDefaults(variables, classResult)

	targetRelPath, err := a.renderTemplate(template, variables)
	if err != nil || targetRelPath == "" {
		return filepath.Clean(filepath.Join(a.cfg.Archive.TargetDir, a.cfg.Archive.UnsortedDir, filepath.Base(srcPath)))
	}

	if !a.validateVariablesSmart(variables, template, classResult) {
		return filepath.Clean(filepath.Join(a.cfg.Archive.TargetDir, a.cfg.Archive.UnsortedDir, filepath.Base(srcPath)))
	}

	targetPath := filepath.Join(a.cfg.Archive.TargetDir, targetRelPath)
	targetPath = filepath.Clean(targetPath)
	return targetPath
}

func (a *Archiver) BuildVariablesMap(fields map[string]models.ExtractedField, classResult *models.ClassificationResult) map[string]string {
	return a.buildVariables(fields, classResult)
}

func (a *Archiver) getArchiveTemplate(classResult *models.ClassificationResult) string {
	if classResult != nil {
		typeCfg := a.cfg.GetDocTypeConfig(string(classResult.Type))
		if typeCfg != nil && typeCfg.PathTemplate != "" {
			return typeCfg.PathTemplate
		}
	}
	return a.cfg.Archive.PathTemplate
}

func (a *Archiver) fillSmartDefaults(vars map[string]string, classResult *models.ClassificationResult) {
	if _, ok := vars["year"]; !ok {
		vars["year"] = "unknown"
	}
	if _, ok := vars["month"]; !ok {
		vars["month"] = "unknown"
	}
	docNoFields := map[string][]string{
		"invoice":  {"invoice_no", "发票号码", "invoice_number", "invoice_num"},
		"contract": {"contract_no", "合同编号", "contract_id", "contract_number"},
		"resume":   {"candidate_name", "姓名", "name"},
		"report":   {"report_no", "报告编号", "report_id", "report_number"},
		"notice":   {"notice_no", "文号", "notice_id"},
	}
	noField := "doc_no"
	if classResult != nil {
		docType := string(classResult.Type)
		if fields, ok := docNoFields[docType]; ok {
			for _, f := range fields {
				if v, ok := vars[f]; ok && v != "" {
					vars[noField] = v
					break
				}
			}
			if _, ok := vars["invoice_no"]; !ok {
				if v, ok := vars[noField]; ok && v != "" {
					vars["invoice_no"] = v
				}
			}
		}
	}
}

func (a *Archiver) validateVariablesSmart(vars map[string]string, template string, classResult *models.ClassificationResult) bool {
	matches := varRe.FindAllStringSubmatch(template, -1)
	requiredFields := make(map[string]bool)
	optionalFields := map[string]bool{
		"year":  true,
		"month": true,
	}
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		name := strings.TrimSpace(m[1])
		requiredFields[name] = true
	}
	for name := range requiredFields {
		v, ok := vars[name]
		if !ok || v == "" {
			if optionalFields[name] {
				continue
			}
			if classResult != nil {
				docType := string(classResult.Type)
				if (docType == "contract" || docType == "resume" || docType == "report" || docType == "notice") && name == "invoice_no" {
					continue
				}
			}
			return false
		}
	}
	return true
}

func (a *Archiver) buildVariables(fields map[string]models.ExtractedField, classResult *models.ClassificationResult) map[string]string {
	vars := make(map[string]string)

	if classResult != nil {
		vars["type"] = string(classResult.Type)
		vars["type_display"] = string(classResult.Type)
		vars["confidence"] = fmt.Sprintf("%.2f", classResult.Confidence)
	}

	for name, field := range fields {
		if field.Value == nil {
			continue
		}
		switch v := field.Value.(type) {
		case string:
			vars[name] = utils.SanitizeFilename(v)
		case float64:
			vars[name] = fmt.Sprintf("%.2f", v)
		case int:
			vars[name] = fmt.Sprintf("%d", v)
		default:
			vars[name] = utils.SanitizeFilename(fmt.Sprintf("%v", v))
		}
	}

	for _, k := range []string{"date", "开票日期", "create_date", "created_at"} {
		if v, ok := vars[k]; ok && v != "" {
			year, month := utils.ExtractYearMonth(v)
			if year != "" {
				vars["year"] = year
			}
			if month != "" {
				vars["month"] = month
			}
			break
		}
	}

	if dateStr := a.extractDateFromFields(fields); dateStr != "" {
		year, month := utils.ExtractYearMonth(dateStr)
		if _, ok := vars["year"]; !ok && year != "" {
			vars["year"] = year
		}
		if _, ok := vars["month"]; !ok && month != "" {
			vars["month"] = month
		}
	}

	if _, ok := vars["vendor_name"]; !ok {
		for _, k := range []string{"vendor", "seller", "销售方", "销售方名称", "supplier", "company"} {
			if v, ok := vars[k]; ok && v != "" {
				vars["vendor_name"] = v
				break
			}
		}
	}

	if _, ok := vars["invoice_no"]; !ok {
		for _, k := range []string{"发票号码", "invoice_number", "invoice_num", "no", "number"} {
			if v, ok := vars[k]; ok && v != "" {
				vars["invoice_no"] = v
				break
			}
		}
	}

	if _, ok := vars["contract_no"]; !ok {
		for _, k := range []string{"合同编号", "contract_id", "contract_number"} {
			if v, ok := vars[k]; ok && v != "" {
				vars["contract_no"] = v
				break
			}
		}
	}

	return vars
}

func (a *Archiver) extractDateFromFields(fields map[string]models.ExtractedField) string {
	dateFieldNames := []string{
		"开票日期", "date", "issue_date", "create_date", "contract_date",
		"签订日期", "签发日期", "report_date", "检测日期",
	}
	for _, name := range dateFieldNames {
		if f, ok := fields[name]; ok && f.Value != nil {
			if s, ok := f.Value.(string); ok {
				return s
			}
		}
	}
	return ""
}

var varRe = regexp.MustCompile(`\{([^}]+)\}`)

func (a *Archiver) renderTemplate(template string, vars map[string]string) (string, error) {
	if template == "" {
		return "", fmt.Errorf("empty template")
	}

	result := varRe.ReplaceAllStringFunc(template, func(m string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(m, "{"), "}")
		name = strings.TrimSpace(name)
		if v, ok := vars[name]; ok && v != "" {
			return v
		}
		return ""
	})

	result = filepath.Clean(result)
	result = strings.ReplaceAll(result, "//", "/")
	return result, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = out.ReadFrom(in)
	return err
}

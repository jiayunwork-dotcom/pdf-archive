package dispatcher

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"pdf-archive/internal/archiver"
	"pdf-archive/internal/config"
	"pdf-archive/internal/storage"
	"pdf-archive/internal/utils"
	"pdf-archive/models"

	"gopkg.in/yaml.v3"
)

var knownFields = map[string]bool{
	"doc_type": true, "confidence": true,
	"invoice_no": true, "amount": true, "date": true,
	"file_size": true, "page_count": true, "file_name": true,
	"file_path": true, "md5": true, "archive_path": true,
	"year": true, "month": true, "type": true, "type_display": true,
	"vendor_name": true, "contract_no": true, "doc_no": true,
	"发票号码": true, "发票代码": true, "开票日期": true,
	"税号": true, "金额": true, "税额": true,
	"销售方名称": true, "购买方名称": true,
	"合同编号": true, "甲方": true, "乙方": true,
	"签订日期": true, "合同金额": true, "合同标题": true,
	"report_no": true, "notice_no": true,
	"candidate_name": true,
}

var validOperators = map[string]bool{
	"eq": true, "ne": true, "gt": true, "lt": true,
	"gte": true, "lte": true, "contains": true,
	"starts_with": true, "ends_with": true, "regex": true,
	"in": true, "not_in": true, "between": true,
}

type Dispatcher struct {
	cfg      *config.Config
	store    *storage.Storage
	archiver *archiver.Archiver
	rules    []models.DispatchRuleConfig
	dryRun   bool
}

func New(cfg *config.Config, store *storage.Storage, rulesPath string, dryRun bool) (*Dispatcher, error) {
	rules, err := LoadRules(rulesPath)
	if err != nil {
		return nil, fmt.Errorf("加载规则配置失败: %w", err)
	}

	allFields := buildKnownFields(cfg)
	for k := range allFields {
		knownFields[k] = true
	}

	if errs := ValidateRules(rules); len(errs) > 0 {
		return nil, fmt.Errorf("规则校验失败:\n  %s", strings.Join(errs, "\n  "))
	}

	sort.Slice(rules, func(i, j int) bool {
		return rules[i].Priority < rules[j].Priority
	})

	return &Dispatcher{
		cfg:      cfg,
		store:    store,
		archiver: archiver.New(cfg),
		rules:    rules,
		dryRun:   dryRun,
	}, nil
}

func LoadRules(path string) ([]models.DispatchRuleConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rc models.DispatchRulesConfig
	if err := yaml.Unmarshal(data, &rc); err != nil {
		return nil, fmt.Errorf("解析YAML失败: %w", err)
	}
	return rc.Rules, nil
}

func buildKnownFields(cfg *config.Config) map[string]bool {
	fields := make(map[string]bool)
	for _, dt := range cfg.DocTypes {
		for _, r := range dt.ExtractRules {
			fields[r.FieldName] = true
		}
	}
	return fields
}

func ValidateRules(rules []models.DispatchRuleConfig) []string {
	var errs []string
	for i, rule := range rules {
		if rule.Name == "" {
			errs = append(errs, fmt.Sprintf("规则#%d: name不能为空", i+1))
		}
		errs = append(errs, validateConditions(rule.Name, rule.Conditions)...)
		errs = append(errs, validateActions(rule.Name, rule.Actions)...)
	}
	return errs
}

func validateConditions(ruleName string, conditions []models.DispatchCondition) []string {
	var errs []string
	for j, c := range conditions {
		if len(c.OrGroup) > 0 {
			errs = append(errs, validateConditions(ruleName, c.OrGroup)...)
			continue
		}
		if c.Field == "" {
			errs = append(errs, fmt.Sprintf("规则'%s'条件#%d: field不能为空", ruleName, j+1))
			continue
		}
		if !knownFields[c.Field] {
			errs = append(errs, fmt.Sprintf("规则'%s'条件#%d: 未知字段'%s'", ruleName, j+1, c.Field))
		}
		if c.Operator == "" {
			errs = append(errs, fmt.Sprintf("规则'%s'条件#%d: operator不能为空", ruleName, j+1))
			continue
		}
		if !validOperators[c.Operator] {
			errs = append(errs, fmt.Sprintf("规则'%s'条件#%d: 不合法的operator'%s'", ruleName, j+1, c.Operator))
			continue
		}
		errs = append(errs, validateValue(ruleName, j+1, c.Operator, c.Value)...)
	}
	return errs
}

func validateActions(ruleName string, actions []models.DispatchAction) []string {
	var errs []string
	validTypes := map[string]bool{
		"move_to": true, "copy_to": true, "tag": true,
		"notify": true, "set_field": true, "skip": true,
	}
	for j, a := range actions {
		if a.Type == "" {
			errs = append(errs, fmt.Sprintf("规则'%s'动作#%d: type不能为空", ruleName, j+1))
			continue
		}
		if !validTypes[a.Type] {
			errs = append(errs, fmt.Sprintf("规则'%s'动作#%d: 不合法的动作类型'%s'", ruleName, j+1, a.Type))
		}
		if (a.Type == "move_to" || a.Type == "copy_to") && a.Target == "" {
			errs = append(errs, fmt.Sprintf("规则'%s'动作#%d: %s动作必须指定target", ruleName, j+1, a.Type))
		}
		if a.Type == "tag" && a.Tag == "" {
			errs = append(errs, fmt.Sprintf("规则'%s'动作#%d: tag动作必须指定tag", ruleName, j+1))
		}
		if a.Type == "set_field" && a.Field == "" {
			errs = append(errs, fmt.Sprintf("规则'%s'动作#%d: set_field动作必须指定field", ruleName, j+1))
		}
	}
	return errs
}

func validateValue(ruleName string, idx int, op string, val interface{}) []string {
	var errs []string
	switch op {
	case "between":
		arr, ok := val.([]interface{})
		if !ok || len(arr) != 2 {
			errs = append(errs, fmt.Sprintf("规则'%s'条件#%d: between的value必须是二元数组", ruleName, idx))
		} else {
			for k, v := range arr {
				if !isNumeric(v) {
					errs = append(errs, fmt.Sprintf("规则'%s'条件#%d: between的value[%d]必须为数字", ruleName, idx, k))
				}
			}
		}
	case "in", "not_in":
		if _, ok := val.([]interface{}); !ok {
			errs = append(errs, fmt.Sprintf("规则'%s'条件#%d: %s的value必须是数组", ruleName, idx, op))
		}
	case "gt", "lt", "gte", "lte":
		if !isNumeric(val) {
			errs = append(errs, fmt.Sprintf("规则'%s'条件#%d: %s的value必须为数字", ruleName, idx, op))
		}
	case "regex":
		if s, ok := val.(string); ok {
			if _, err := regexp.Compile(s); err != nil {
				errs = append(errs, fmt.Sprintf("规则'%s'条件#%d: regex无效: %v", ruleName, idx, err))
			}
		}
	}
	return errs
}

func isNumeric(v interface{}) bool {
	switch v.(type) {
	case int, int64, float64, json.Number:
		return true
	case string:
		return false
	default:
		return false
	}
}

type DispatchOptions struct {
	FilterType string
	FilterRule string
}

func (d *Dispatcher) Run(opts DispatchOptions) (*models.DispatchSummary, error) {
	docs, err := d.store.ListAllDocuments()
	if err != nil {
		return nil, fmt.Errorf("查询文档失败: %w", err)
	}

	if len(docs) == 0 {
		return &models.DispatchSummary{}, nil
	}

	summary := &models.DispatchSummary{
		Details: make([]models.DispatchDocResult, 0, len(docs)),
	}

	hasDefaultRule := false
	for _, r := range d.rules {
		if r.Name == "default" {
			hasDefaultRule = true
			break
		}
	}
	defaultRule := models.DispatchRuleConfig{
		Name:     "default",
		Priority: 999999,
		Conditions: []models.DispatchCondition{},
		Actions: []models.DispatchAction{
			{Type: "tag", Tag: "unmatched"},
			{Type: "notify", Message: "该文件未命中任何规则"},
		},
	}
	if !hasDefaultRule {
		d.rules = append(d.rules, defaultRule)
	}

	for _, doc := range docs {
		if opts.FilterType != "" && doc.DocType != opts.FilterType {
			continue
		}

		ctx := d.buildContext(doc)

		rule, matched := d.matchRules(ctx, opts.FilterRule)
		if !matched {
			for i := range d.rules {
				if d.rules[i].Name == "default" {
					rule = &d.rules[i]
					break
				}
			}
		}

		docResult := models.DispatchDocResult{
			FileID:   doc.FileID,
			FileName: filepath.Base(doc.FilePath),
			Matched:  matched,
		}

		if rule != nil {
			docResult.RuleName = rule.Name
			actions := d.executeActions(*rule, doc, ctx)
			docResult.Actions = actions
			for _, a := range actions {
				if a.Error != "" {
					docResult.Errors = append(docResult.Errors, a.Error)
					summary.Errors++
				}
			}
		}

		if docResult.Matched {
			summary.Matched++
		} else {
			summary.Unmatched++
		}

		summary.Total++
		summary.Details = append(summary.Details, docResult)
	}

	return summary, nil
}

func (d *Dispatcher) buildContext(doc storage.DoneDocument) *models.DispatchDocContext {
	fileSize := int64(0)
	if fi, err := os.Stat(doc.FilePath); err == nil {
		fileSize = fi.Size()
	}

	ctx := &models.DispatchDocContext{
		FileID:      doc.FileID,
		FilePath:    doc.FilePath,
		MD5:         doc.MD5,
		DocType:     doc.DocType,
		Confidence:  doc.Confidence,
		Fields:      doc.Fields,
		ArchivePath: doc.ArchivePath,
		Tags:        doc.Tags,
		FileSize:    fileSize,
		FileName:    filepath.Base(doc.FilePath),
	}

	if doc.ArchivePath != "" {
		if fi, err := os.Stat(doc.ArchivePath); err == nil {
			ctx.FileSize = fi.Size()
		}
	}

	return ctx
}

func (d *Dispatcher) matchRules(ctx *models.DispatchDocContext, filterRule string) (*models.DispatchRuleConfig, bool) {
	for i := range d.rules {
		rule := &d.rules[i]
		if filterRule != "" && rule.Name != filterRule && rule.Name != "default" {
			continue
		}
		if rule.Name == "default" {
			continue
		}
		if d.matchConditions(rule.Conditions, ctx) {
			return rule, true
		}
	}
	return nil, false
}

func (d *Dispatcher) matchConditions(conditions []models.DispatchCondition, ctx *models.DispatchDocContext) bool {
	for _, c := range conditions {
		if len(c.OrGroup) > 0 {
			if !d.matchOrGroup(c.OrGroup, ctx) {
				return false
			}
			continue
		}
		if !d.matchCondition(c, ctx) {
			return false
		}
	}
	return true
}

func (d *Dispatcher) matchOrGroup(conditions []models.DispatchCondition, ctx *models.DispatchDocContext) bool {
	for _, c := range conditions {
		if len(c.OrGroup) > 0 {
			if d.matchOrGroup(c.OrGroup, ctx) {
				return true
			}
			continue
		}
		if d.matchCondition(c, ctx) {
			return true
		}
	}
	return false
}

func (d *Dispatcher) matchCondition(c models.DispatchCondition, ctx *models.DispatchDocContext) bool {
	fieldVal := d.getFieldValue(c.Field, ctx)
	return EvaluateCondition(c.Operator, c.Value, fieldVal)
}

func (d *Dispatcher) getFieldValue(field string, ctx *models.DispatchDocContext) string {
	switch field {
	case "doc_type":
		return ctx.DocType
	case "confidence":
		return fmt.Sprintf("%.6f", ctx.Confidence)
	case "file_size":
		return fmt.Sprintf("%d", ctx.FileSize)
	case "page_count":
		return fmt.Sprintf("%d", ctx.PageCount)
	case "file_name":
		return ctx.FileName
	case "file_path":
		return ctx.FilePath
	case "md5":
		return ctx.MD5
	case "archive_path":
		return ctx.ArchivePath
	default:
		if f, ok := ctx.Fields[field]; ok && f.Value != nil {
			return fmt.Sprintf("%v", f.Value)
		}
		return ""
	}
}

func EvaluateCondition(op string, ruleValue interface{}, fieldValue string) bool {
	switch op {
	case "eq":
		return evalEq(ruleValue, fieldValue)
	case "ne":
		return !evalEq(ruleValue, fieldValue)
	case "gt":
		return evalNumericCompare(ruleValue, fieldValue, func(fv, rv float64) bool { return fv > rv })
	case "lt":
		return evalNumericCompare(ruleValue, fieldValue, func(fv, rv float64) bool { return fv < rv })
	case "gte":
		return evalNumericCompare(ruleValue, fieldValue, func(fv, rv float64) bool { return fv >= rv })
	case "lte":
		return evalNumericCompare(ruleValue, fieldValue, func(fv, rv float64) bool { return fv <= rv })
	case "contains":
		rv := toString(ruleValue)
		return strings.Contains(fieldValue, rv)
	case "starts_with":
		rv := toString(ruleValue)
		return strings.HasPrefix(fieldValue, rv)
	case "ends_with":
		rv := toString(ruleValue)
		return strings.HasSuffix(fieldValue, rv)
	case "regex":
		rv := toString(ruleValue)
		re, err := regexp.Compile(rv)
		if err != nil {
			return false
		}
		return re.MatchString(fieldValue)
	case "in":
		arr, ok := ruleValue.([]interface{})
		if !ok {
			return false
		}
		for _, v := range arr {
			if evalEq(v, fieldValue) {
				return true
			}
		}
		return false
	case "not_in":
		arr, ok := ruleValue.([]interface{})
		if !ok {
			return false
		}
		for _, v := range arr {
			if evalEq(v, fieldValue) {
				return false
			}
		}
		return true
	case "between":
		arr, ok := ruleValue.([]interface{})
		if !ok || len(arr) != 2 {
			return false
		}
		fv, err := toFloat64(fieldValue)
		if err != nil {
			return false
		}
		minVal, err1 := toFloat64FromInterface(arr[0])
		maxVal, err2 := toFloat64FromInterface(arr[1])
		if err1 != nil || err2 != nil {
			return false
		}
		return fv >= minVal && fv <= maxVal
	}
	return false
}

func evalEq(ruleValue interface{}, fieldValue string) bool {
	rvStr := toString(ruleValue)
	ruleNum, ruleErr := toFloat64(rvStr)
	fieldNum, fieldErr := toFloat64(fieldValue)
	if ruleErr == nil && fieldErr == nil {
		return ruleNum == fieldNum
	}
	return rvStr == fieldValue
}

func evalNumericCompare(ruleValue interface{}, fieldValue string, cmp func(float64, float64) bool) bool {
	rv, err1 := toFloat64FromInterface(ruleValue)
	fv, err2 := toFloat64(fieldValue)
	if err1 != nil || err2 != nil {
		return false
	}
	return cmp(fv, rv)
}

func toFloat64(s string) (float64, error) {
	var f float64
	_, err := fmt.Sscanf(s, "%f", &f)
	if err != nil {
		return 0, err
	}
	return f, nil
}

func toFloat64FromInterface(v interface{}) (float64, error) {
	switch n := v.(type) {
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case float64:
		return n, nil
	case json.Number:
		return n.Float64()
	case string:
		return toFloat64(n)
	default:
		return toFloat64(fmt.Sprintf("%v", v))
	}
}

func toString(v interface{}) string {
	switch val := v.(type) {
	case string:
		return val
	case int:
		return fmt.Sprintf("%d", val)
	case int64:
		return fmt.Sprintf("%d", val)
	case float64:
		if val == float64(int64(val)) {
			return fmt.Sprintf("%d", int64(val))
		}
		return fmt.Sprintf("%v", val)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func (d *Dispatcher) executeActions(rule models.DispatchRuleConfig, doc storage.DoneDocument, ctx *models.DispatchDocContext) []models.DispatchActionResult {
	var results []models.DispatchActionResult
	for _, action := range rule.Actions {
		result := d.executeAction(action, doc, ctx)
		results = append(results, result)
	}
	return results
}

func (d *Dispatcher) executeAction(action models.DispatchAction, doc storage.DoneDocument, ctx *models.DispatchDocContext) models.DispatchActionResult {
	switch action.Type {
	case "move_to":
		return d.actionMoveTo(action, doc, ctx)
	case "copy_to":
		return d.actionCopyTo(action, doc, ctx)
	case "tag":
		return d.actionTag(action, doc)
	case "notify":
		return d.actionNotify(action, doc)
	case "set_field":
		return d.actionSetField(action, doc)
	case "skip":
		return models.DispatchActionResult{
			ActionType: "skip",
			Detail:     "跳过该文档",
		}
	default:
		return models.DispatchActionResult{
			ActionType: action.Type,
			Error:      fmt.Sprintf("未知的动作类型: %s", action.Type),
		}
	}
}

func (d *Dispatcher) actionMoveTo(action models.DispatchAction, doc storage.DoneDocument, ctx *models.DispatchDocContext) models.DispatchActionResult {
	targetPath, err := d.resolvePath(action.Target, ctx)
	if err != nil {
		return models.DispatchActionResult{ActionType: "move_to", Error: fmt.Sprintf("解析路径失败: %v", err)}
	}

	if d.dryRun {
		return models.DispatchActionResult{
			ActionType: "move_to",
			Detail:     fmt.Sprintf("[DRY RUN] 将移动到: %s", targetPath),
		}
	}

	srcPath := doc.ArchivePath
	if srcPath == "" {
		srcPath = doc.FilePath
	}

	if _, err := os.Stat(srcPath); os.IsNotExist(err) {
		return models.DispatchActionResult{ActionType: "move_to", Error: fmt.Sprintf("源文件不存在: %s", srcPath)}
	}

	targetDir := filepath.Dir(targetPath)
	if err := utils.EnsureDir(targetDir); err != nil {
		return models.DispatchActionResult{ActionType: "move_to", Error: fmt.Sprintf("创建目录失败: %v", err)}
	}

	if d.cfg.Archive.ConflictPolicy == "rename" {
		targetPath = utils.ResolveConflict(targetPath)
	}

	if err := os.Rename(srcPath, targetPath); err != nil {
		if err := copyFileOp(srcPath, targetPath); err != nil {
			return models.DispatchActionResult{ActionType: "move_to", Error: fmt.Sprintf("移动文件失败: %v", err)}
		}
		os.Remove(srcPath)
	}

	if err := d.store.UpdateArchivePath(doc.FileID, targetPath); err != nil {
		return models.DispatchActionResult{ActionType: "move_to", Detail: targetPath, Error: fmt.Sprintf("更新索引失败: %v", err)}
	}

	return models.DispatchActionResult{ActionType: "move_to", Detail: targetPath}
}

func (d *Dispatcher) actionCopyTo(action models.DispatchAction, doc storage.DoneDocument, ctx *models.DispatchDocContext) models.DispatchActionResult {
	targetPath, err := d.resolvePath(action.Target, ctx)
	if err != nil {
		return models.DispatchActionResult{ActionType: "copy_to", Error: fmt.Sprintf("解析路径失败: %v", err)}
	}

	if d.dryRun {
		return models.DispatchActionResult{
			ActionType: "copy_to",
			Detail:     fmt.Sprintf("[DRY RUN] 将复制到: %s", targetPath),
		}
	}

	srcPath := doc.ArchivePath
	if srcPath == "" {
		srcPath = doc.FilePath
	}

	if _, err := os.Stat(srcPath); os.IsNotExist(err) {
		return models.DispatchActionResult{ActionType: "copy_to", Error: fmt.Sprintf("源文件不存在: %s", srcPath)}
	}

	targetDir := filepath.Dir(targetPath)
	if err := utils.EnsureDir(targetDir); err != nil {
		return models.DispatchActionResult{ActionType: "copy_to", Error: fmt.Sprintf("创建目录失败: %v", err)}
	}

	if d.cfg.Archive.ConflictPolicy == "rename" {
		targetPath = utils.ResolveConflict(targetPath)
	}

	if err := copyFileOp(srcPath, targetPath); err != nil {
		return models.DispatchActionResult{ActionType: "copy_to", Error: fmt.Sprintf("复制文件失败: %v", err)}
	}

	return models.DispatchActionResult{ActionType: "copy_to", Detail: targetPath}
}

func (d *Dispatcher) actionTag(action models.DispatchAction, doc storage.DoneDocument) models.DispatchActionResult {
	if action.Tag == "" {
		return models.DispatchActionResult{ActionType: "tag", Error: "tag动作未指定标签名"}
	}

	currentTags := doc.Tags
	if currentTags == "" {
		currentTags = action.Tag
	} else {
		existing := strings.Split(currentTags, ",")
		found := false
		for _, t := range existing {
			if strings.TrimSpace(t) == action.Tag {
				found = true
				break
			}
		}
		if !found {
			currentTags = currentTags + "," + action.Tag
		}
	}

	if d.dryRun {
		return models.DispatchActionResult{
			ActionType: "tag",
			Detail:     fmt.Sprintf("[DRY RUN] 将添加标签: %s -> %s", action.Tag, currentTags),
		}
	}

	if err := d.store.UpdateTags(doc.FileID, currentTags); err != nil {
		return models.DispatchActionResult{ActionType: "tag", Error: fmt.Sprintf("更新标签失败: %v", err)}
	}

	return models.DispatchActionResult{ActionType: "tag", Detail: fmt.Sprintf("添加标签: %s", action.Tag)}
}

func (d *Dispatcher) actionNotify(action models.DispatchAction, doc storage.DoneDocument) models.DispatchActionResult {
	msg := action.Message
	if msg == "" {
		msg = fmt.Sprintf("文档 %s 触发通知", filepath.Base(doc.FilePath))
	}
	return models.DispatchActionResult{ActionType: "notify", Detail: msg}
}

func (d *Dispatcher) actionSetField(action models.DispatchAction, doc storage.DoneDocument) models.DispatchActionResult {
	if action.Field == "" {
		return models.DispatchActionResult{ActionType: "set_field", Error: "set_field动作未指定字段名"}
	}

	if d.dryRun {
		return models.DispatchActionResult{
			ActionType: "set_field",
			Detail:     fmt.Sprintf("[DRY RUN] 将设置字段 %s = %s", action.Field, action.Value),
		}
	}

	if err := d.store.SetField(doc.FileID, action.Field, action.Value); err != nil {
		return models.DispatchActionResult{ActionType: "set_field", Error: fmt.Sprintf("设置字段失败: %v", err)}
	}

	return models.DispatchActionResult{ActionType: "set_field", Detail: fmt.Sprintf("设置 %s = %s", action.Field, action.Value)}
}

func (d *Dispatcher) resolvePath(template string, ctx *models.DispatchDocContext) (string, error) {
	classResult := &models.ClassificationResult{
		Type:       models.DocType(ctx.DocType),
		Confidence: ctx.Confidence,
	}
	variables := d.archiver.BuildVariablesMap(ctx.Fields, classResult)

	if _, ok := variables["year"]; !ok {
		variables["year"] = "unknown"
	}
	if _, ok := variables["month"]; !ok {
		variables["month"] = "unknown"
	}

	varRe := regexp.MustCompile(`\{([^}]+)\}`)
	result := varRe.ReplaceAllStringFunc(template, func(m string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(m, "{"), "}")
		name = strings.TrimSpace(name)
		if v, ok := variables[name]; ok && v != "" {
			return v
		}
		return ""
	})

	result = filepath.Clean(result)
	result = strings.ReplaceAll(result, "//", "/")

	if !filepath.IsAbs(result) {
		result = filepath.Join(d.cfg.Archive.TargetDir, result)
	}

	return result, nil
}

func copyFileOp(src, dst string) error {
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

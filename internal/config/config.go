package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"gopkg.in/yaml.v3"
)

type ExtractRule struct {
	FieldName    string                 `yaml:"field_name"`
	Description  string                 `yaml:"description,omitempty"`
	Strategy     string                 `yaml:"strategy"`
	Pattern      string                 `yaml:"pattern,omitempty"`
	GroupIndex   int                    `yaml:"group_index,omitempty"`
	Keyword      string                 `yaml:"keyword,omitempty"`
	Direction    string                 `yaml:"direction,omitempty"`
	Distance     int                    `yaml:"distance,omitempty"`
	TableRow     int                    `yaml:"table_row,omitempty"`
	TableCol     int                    `yaml:"table_col,omitempty"`
	HeaderMatch  string                 `yaml:"header_match,omitempty"`
	PostProcess  []PostProcessRule      `yaml:"post_process,omitempty"`
	Required     bool                   `yaml:"required,omitempty"`
	Default      interface{}            `yaml:"default,omitempty"`
	Alternatives []ExtractRule          `yaml:"alternatives,omitempty"`
}

type PostProcessRule struct {
	Type   string `yaml:"type"`
	Format string `yaml:"format,omitempty"`
}

type DocTypeConfig struct {
	Type         string         `yaml:"type"`
	DisplayName  string         `yaml:"display_name"`
	Keywords     []string       `yaml:"keywords"`
	MinMatches   int            `yaml:"min_matches,omitempty"`
	PathTemplate string         `yaml:"path_template,omitempty"`
	ExtractRules []ExtractRule  `yaml:"extract_rules"`
}

type PipelineStages struct {
	Scan      bool `yaml:"scan"`
	Parse     bool `yaml:"parse"`
	Classify  bool `yaml:"classify"`
	Extract   bool `yaml:"extract"`
	Validate  bool `yaml:"validate"`
	Archive   bool `yaml:"archive"`
	Report    bool `yaml:"report"`
}

type ArchiveConfig struct {
	TargetDir      string `yaml:"target_dir"`
	PathTemplate   string `yaml:"path_template"`
	UnsortedDir    string `yaml:"unsorted_dir"`
	Mode           string `yaml:"mode"`
	ConflictPolicy string `yaml:"conflict_policy"`
}

type OCRConfig struct {
	Enabled         bool   `yaml:"enabled"`
	Command         string `yaml:"command"`
	Languages       string `yaml:"languages"`
	TimeoutSec      int    `yaml:"timeout_sec"`
	DPI             int    `yaml:"dpi"`
	MinTextLen      int    `yaml:"min_text_len"`
	ConfidenceThreshold float64 `yaml:"confidence_threshold"`
}

type ClassifierConfig struct {
	EnableML       bool     `yaml:"enable_ml"`
	RuleFirst      bool     `yaml:"rule_first"`
	Threshold      float64  `yaml:"threshold"`
	MLModelPath    string   `yaml:"ml_model_path"`
	MinTrainSamples int     `yaml:"min_train_samples"`
	StopWords      []string `yaml:"stop_words,omitempty"`
}

type StorageConfig struct {
	DBPath          string `yaml:"db_path"`
	IndexFields     []string `yaml:"index_fields,omitempty"`
	EnableFullText  bool   `yaml:"enable_full_text"`
}

type PipelineConfig struct {
	InputDir          string `yaml:"input_dir"`
	Workers           int    `yaml:"workers"`
	Recursive         bool   `yaml:"recursive"`
	DryRun            bool   `yaml:"dry_run"`
	Verbose           bool   `yaml:"verbose"`
	TimeoutPerFileSec int    `yaml:"timeout_per_file_sec"`
	MaxFiles          int    `yaml:"max_files"`
	LargePageCount    int    `yaml:"large_page_count"`
	BatchPageSize     int    `yaml:"batch_page_size"`
	ResumeFromDB      bool   `yaml:"resume_from_db"`
	LowConfThreshold  float64 `yaml:"low_conf_threshold"`
}

type Config struct {
	ConfigPath    string            `yaml:"-"`
	BaseConfigs   []string          `yaml:"base_configs,omitempty"`
	AppName       string            `yaml:"app_name"`
	LogLevel      string            `yaml:"log_level"`
	LogDir        string            `yaml:"log_dir"`
	Pipeline      PipelineConfig    `yaml:"pipeline"`
	Stages        PipelineStages    `yaml:"stages"`
	OCR           OCRConfig         `yaml:"ocr"`
	Classifier    ClassifierConfig  `yaml:"classifier"`
	Archive       ArchiveConfig     `yaml:"archive"`
	Storage       StorageConfig     `yaml:"storage"`
	DocTypes      []DocTypeConfig   `yaml:"doc_types"`
}

var defaultDocTypes = []DocTypeConfig{
	{
		Type:        "invoice",
		DisplayName: "发票",
		Keywords:    []string{"发票", "税号", "价税合计", "开票日期", "纳税人识别号", "发票号码", "发票代码"},
		MinMatches:  2,
	},
	{
		Type:        "contract",
		DisplayName: "合同",
		Keywords:    []string{"合同", "甲方", "乙方", "签订日期", "合同编号", "违约责任", "权利义务"},
		MinMatches:  2,
	},
	{
		Type:        "resume",
		DisplayName: "简历",
		Keywords:    []string{"教育经历", "工作经验", "技能", "求职意向", "个人简介", "项目经验"},
		MinMatches:  2,
	},
	{
		Type:        "report",
		DisplayName: "检测报告",
		Keywords:    []string{"检测报告", "检测项", "标准值", "实测值", "判定", "检验结论", "测试报告"},
		MinMatches:  2,
	},
	{
		Type:        "notice",
		DisplayName: "通知公告",
		Keywords:    []string{"通知", "公告", "发文机关", "文号", "签发", "印发"},
		MinMatches:  2,
	},
}

func DefaultConfig() *Config {
	return &Config{
		AppName:  "pdf-archive",
		LogLevel: "info",
		LogDir:   "./logs",
		Pipeline: PipelineConfig{
			Workers:           runtime.NumCPU(),
			Recursive:         true,
			DryRun:            false,
			Verbose:           false,
			TimeoutPerFileSec: 120,
			MaxFiles:          10000,
			LargePageCount:    200,
			BatchPageSize:     50,
			ResumeFromDB:      true,
			LowConfThreshold:  0.6,
		},
		Stages: PipelineStages{
			Scan:     true,
			Parse:    true,
			Classify: true,
			Extract:  true,
			Validate: true,
			Archive:  true,
			Report:   true,
		},
		OCR: OCRConfig{
			Enabled:         true,
			Command:         "tesseract",
			Languages:       "chi_sim+eng",
			TimeoutSec:      300,
			DPI:             300,
			MinTextLen:      50,
			ConfidenceThreshold: 0.7,
		},
		Classifier: ClassifierConfig{
			EnableML:       true,
			RuleFirst:      true,
			Threshold:      0.5,
			MinTrainSamples: 10,
		},
		Archive: ArchiveConfig{
			TargetDir:      "./archive",
			PathTemplate:   "",
			UnsortedDir:    "",
			Mode:           "copy",
			ConflictPolicy: "rename",
		},
		Storage: StorageConfig{
			DBPath:         "./pdf-archive.db",
			EnableFullText: true,
		},
		DocTypes: defaultDocTypes,
	}
}

func Load(path string) (*Config, error) {
	cfg := DefaultConfig()
	if path != "" {
		cfg.ConfigPath = path
		loaded := &Config{}
		if err := loadYAML(path, loaded); err != nil {
			return nil, fmt.Errorf("load config %s: %w", path, err)
		}
		mergeConfigs(cfg, loaded)
	}
	if err := cfg.loadBaseConfigs(); err != nil {
		return nil, err
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func loadYAML(path string, out *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, out)
}

func (c *Config) loadBaseConfigs() error {
	if len(c.BaseConfigs) == 0 {
		return nil
	}
	baseDir := "."
	if c.ConfigPath != "" {
		baseDir = filepath.Dir(c.ConfigPath)
	}
	for _, bc := range c.BaseConfigs {
		bcPath := bc
		if !filepath.IsAbs(bcPath) {
			bcPath = filepath.Join(baseDir, bcPath)
		}
		data, err := os.ReadFile(bcPath)
		if err != nil {
			return fmt.Errorf("read base config %s: %w", bcPath, err)
		}
		base := &Config{}
		if err := yaml.Unmarshal(data, base); err != nil {
			return fmt.Errorf("parse base config %s: %w", bcPath, err)
		}
		if len(base.DocTypes) == 0 {
			var dtCfg DocTypeConfig
			if err := yaml.Unmarshal(data, &dtCfg); err == nil && dtCfg.Type != "" {
				base.DocTypes = []DocTypeConfig{dtCfg}
			}
		}
		mergeConfigs(c, base)
	}
	return nil
}

func mergeConfigs(target *Config, base *Config) {
	if target.Pipeline.Workers == 0 {
		target.Pipeline.Workers = base.Pipeline.Workers
	}
	if target.Pipeline.TimeoutPerFileSec == 0 {
		target.Pipeline.TimeoutPerFileSec = base.Pipeline.TimeoutPerFileSec
	}
	if target.Pipeline.MaxFiles == 0 {
		target.Pipeline.MaxFiles = base.Pipeline.MaxFiles
	}
	if target.Pipeline.InputDir == "" {
		target.Pipeline.InputDir = base.Pipeline.InputDir
	}
	if !target.Pipeline.Recursive && base.Pipeline.Recursive {
		target.Pipeline.Recursive = base.Pipeline.Recursive
	}
	if !target.Pipeline.DryRun && base.Pipeline.DryRun {
		target.Pipeline.DryRun = base.Pipeline.DryRun
	}
	if target.Pipeline.LowConfThreshold == 0 {
		target.Pipeline.LowConfThreshold = base.Pipeline.LowConfThreshold
	}
	if target.Archive.TargetDir == "" {
		target.Archive.TargetDir = base.Archive.TargetDir
	}
	if target.Archive.PathTemplate == "" {
		target.Archive.PathTemplate = base.Archive.PathTemplate
	}
	if target.Archive.Mode == "" {
		target.Archive.Mode = base.Archive.Mode
	}
	if target.Archive.UnsortedDir == "" {
		target.Archive.UnsortedDir = base.Archive.UnsortedDir
	}
	if target.Storage.DBPath == "" {
		target.Storage.DBPath = base.Storage.DBPath
	}
	if target.OCR.Languages == "" {
		target.OCR.Languages = base.OCR.Languages
	}
	if !target.OCR.Enabled && base.OCR.Enabled {
		target.OCR.Enabled = base.OCR.Enabled
	}
	if target.Classifier.MinTrainSamples == 0 {
		target.Classifier.MinTrainSamples = base.Classifier.MinTrainSamples
	}
	if target.Classifier.Threshold == 0 {
		target.Classifier.Threshold = base.Classifier.Threshold
	}
	if target.LogDir == "" {
		target.LogDir = base.LogDir
	}
	if target.LogLevel == "" {
		target.LogLevel = base.LogLevel
	}
	if target.AppName == "" {
		target.AppName = base.AppName
	}
	if len(target.BaseConfigs) == 0 && len(base.BaseConfigs) > 0 {
		target.BaseConfigs = base.BaseConfigs
	}

	existingTypes := make(map[string]int)
	for idx, dt := range target.DocTypes {
		existingTypes[dt.Type] = idx
	}
	for _, dt := range base.DocTypes {
		if idx, ok := existingTypes[dt.Type]; ok {
			if len(dt.ExtractRules) > 0 {
				target.DocTypes[idx].ExtractRules = dt.ExtractRules
			}
			if len(dt.Keywords) > 0 {
				target.DocTypes[idx].Keywords = dt.Keywords
			}
			if dt.DisplayName != "" {
				target.DocTypes[idx].DisplayName = dt.DisplayName
			}
			if dt.MinMatches > 0 {
				target.DocTypes[idx].MinMatches = dt.MinMatches
			}
			if dt.PathTemplate != "" {
				target.DocTypes[idx].PathTemplate = dt.PathTemplate
			}
		} else {
			target.DocTypes = append(target.DocTypes, dt)
		}
	}
}

func (c *Config) applyDefaults() {
	if c.Pipeline.Workers <= 0 {
		c.Pipeline.Workers = runtime.NumCPU()
	}
	if c.Pipeline.TimeoutPerFileSec <= 0 {
		c.Pipeline.TimeoutPerFileSec = 120
	}
	if c.Pipeline.MaxFiles <= 0 {
		c.Pipeline.MaxFiles = 10000
	}
	if c.Pipeline.LargePageCount <= 0 {
		c.Pipeline.LargePageCount = 200
	}
	if c.Pipeline.BatchPageSize <= 0 {
		c.Pipeline.BatchPageSize = 50
	}
	if c.Pipeline.LowConfThreshold <= 0 || c.Pipeline.LowConfThreshold > 1 {
		c.Pipeline.LowConfThreshold = 0.6
	}
	if c.OCR.Languages == "" {
		c.OCR.Languages = "chi_sim+eng"
	}
	if c.OCR.DPI <= 0 {
		c.OCR.DPI = 300
	}
	if c.OCR.TimeoutSec <= 0 {
		c.OCR.TimeoutSec = 300
	}
	if c.Classifier.Threshold <= 0 || c.Classifier.Threshold > 1 {
		c.Classifier.Threshold = 0.5
	}
	if c.Classifier.MinTrainSamples <= 0 {
		c.Classifier.MinTrainSamples = 10
	}
	if c.Archive.Mode == "" {
		c.Archive.Mode = "copy"
	}
	if c.Archive.UnsortedDir == "" {
		c.Archive.UnsortedDir = "unsorted"
	}
	if c.Archive.PathTemplate == "" {
		c.Archive.PathTemplate = "{type}/{year}/{month}/{type}_{invoice_no}.pdf"
	}
	if len(c.DocTypes) == 0 {
		c.DocTypes = defaultDocTypes
	}
}

func (c *Config) Validate() error {
	if c.Archive.Mode != "copy" && c.Archive.Mode != "move" && c.Archive.Mode != "link" {
		return fmt.Errorf("archive.mode must be one of: copy, move, link")
	}
	if c.Pipeline.TimeoutPerFileSec < 10 {
		return fmt.Errorf("pipeline.timeout_per_file_sec must be >= 10")
	}
	if c.Pipeline.Workers < 1 {
		return fmt.Errorf("pipeline.workers must be >= 1")
	}
	return nil
}

func (c *Config) GetDocTypeConfig(docType string) *DocTypeConfig {
	for i := range c.DocTypes {
		if c.DocTypes[i].Type == docType {
			return &c.DocTypes[i]
		}
	}
	return nil
}

func (c *Config) Save(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

package models

import (
	"encoding/json"
	"time"
)

type DocType string

const (
	DocTypeInvoice      DocType = "invoice"
	DocTypeContract     DocType = "contract"
	DocTypeResume       DocType = "resume"
	DocTypeReport       DocType = "report"
	DocTypeNotice       DocType = "notice"
	DocTypeUnknown      DocType = "unknown"
	DocTypeUnsorted     DocType = "unsorted"
)

type TextBlock struct {
	Text     string    `json:"text"`
	X        float64   `json:"x"`
	Y        float64   `json:"y"`
	Width    float64   `json:"width"`
	Height   float64   `json:"height"`
	PageNum  int       `json:"page_num"`
	FontSize float64   `json:"font_size"`
	Bold     bool      `json:"bold"`
}

type Table struct {
	PageNum int          `json:"page_num"`
	Rows    [][]TableCell `json:"rows"`
	X       float64      `json:"x"`
	Y       float64      `json:"y"`
	Width   float64      `json:"width"`
	Height  float64      `json:"height"`
}

type TableCell struct {
	Text     string  `json:"text"`
	Row      int     `json:"row"`
	Col      int     `json:"col"`
	RowSpan  int     `json:"row_span"`
	ColSpan  int     `json:"col_span"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Width    float64 `json:"width"`
	Height   float64 `json:"height"`
}

type ImageInfo struct {
	PageNum  int    `json:"page_num"`
	Index    int    `json:"index"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Format   string `json:"format"`
	ColorSpace string `json:"color_space"`
}

type DocumentMetadata struct {
	Title       string    `json:"title"`
	Author      string    `json:"author"`
	Creator     string    `json:"creator"`
	Producer    string    `json:"producer"`
	CreatedAt   time.Time `json:"created_at"`
	ModifiedAt  time.Time `json:"modified_at"`
	PageCount   int       `json:"page_count"`
	IsOCR       bool      `json:"is_ocr"`
	OCRLanguage string    `json:"ocr_language"`
}

type ParsedDocument struct {
	FilePath     string           `json:"file_path"`
	FileSize     int64            `json:"file_size"`
	MD5          string           `json:"md5"`
	Metadata     DocumentMetadata `json:"metadata"`
	Pages        []ParsedPage     `json:"pages"`
	FullText     string           `json:"full_text"`
	TextBlocks   []TextBlock      `json:"text_blocks"`
	Tables       []Table          `json:"tables"`
	Images       []ImageInfo      `json:"images"`
	ParseError   string           `json:"parse_error,omitempty"`
}

type ParsedPage struct {
	PageNum    int         `json:"page_num"`
	Width      float64     `json:"width"`
	Height     float64     `json:"height"`
	TextBlocks []TextBlock `json:"text_blocks"`
	Text       string      `json:"text"`
	Tables     []Table     `json:"tables"`
}

type ClassificationResult struct {
	Type       DocType `json:"type"`
	Confidence float64 `json:"confidence"`
	Method     string  `json:"method"`
	RuleHit    string  `json:"rule_hit,omitempty"`
	Scores     map[DocType]float64 `json:"scores,omitempty"`
}

type ExtractedField struct {
	Name       string      `json:"name"`
	Value      interface{} `json:"value"`
	Confidence float64     `json:"confidence"`
	Method     string      `json:"method"`
	Source     string      `json:"source,omitempty"`
	PageNum    int         `json:"page_num,omitempty"`
	Position   string      `json:"position,omitempty"`
	NeedReview bool        `json:"need_review"`
	Error      string      `json:"error,omitempty"`
}

type DocumentResult struct {
	FileID          string                 `json:"file_id"`
	FilePath        string                 `json:"file_path"`
	MD5             string                 `json:"md5"`
	Parsed          *ParsedDocument        `json:"parsed,omitempty"`
	Classification  *ClassificationResult  `json:"classification,omitempty"`
	Fields          map[string]ExtractedField `json:"fields"`
	Validation      *ValidationReport      `json:"validation,omitempty"`
	ArchivePath     string                 `json:"archive_path,omitempty"`
	ArchiveMode     string                 `json:"archive_mode,omitempty"`
	Status          string                 `json:"status"`
	ErrorMessage    string                 `json:"error_message,omitempty"`
	ProcessedAt     time.Time              `json:"processed_at"`
	DurationMs      int64                  `json:"duration_ms"`
}

func (dr *DocumentResult) ToJSON() (string, error) {
	b, err := json.Marshal(dr)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (dr *DocumentResult) FieldsJSON() (string, error) {
	b, err := json.Marshal(dr.Fields)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

type ValidationReport struct {
	TotalFields     int     `json:"total_fields"`
	SuccessCount    int     `json:"success_count"`
	LowConfCount    int     `json:"low_conf_count"`
	FailedCount     int     `json:"failed_count"`
	NeedReviewCount int     `json:"need_review_count"`
	OverallScore    float64 `json:"overall_score"`
	FieldDetails    map[string]FieldValidation `json:"field_details"`
}

type FieldValidation struct {
	FieldName   string  `json:"field_name"`
	Success     bool    `json:"success"`
	Confidence  float64 `json:"confidence"`
	NeedReview  bool    `json:"need_review"`
	Message     string  `json:"message,omitempty"`
}

type PipelineStatus string

const (
	StatusPending    PipelineStatus = "pending"
	StatusProcessing PipelineStatus = "processing"
	StatusDone       PipelineStatus = "done"
	StatusError      PipelineStatus = "error"
)

type PipelineRecord struct {
	FileID       string         `json:"file_id"`
	FilePath     string         `json:"file_path"`
	MD5          string         `json:"md5"`
	Status       PipelineStatus `json:"status"`
	ErrorMessage string         `json:"error_message,omitempty"`
	ProcessedAt  time.Time      `json:"processed_at"`
	DocType      DocType        `json:"doc_type,omitempty"`
	ArchivePath  string         `json:"archive_path,omitempty"`
}

type SummaryReport struct {
	TotalFiles     int            `json:"total_files"`
	ProcessedFiles int            `json:"processed_files"`
	SuccessFiles   int            `json:"success_files"`
	ErrorFiles     int            `json:"error_files"`
	SkippedFiles   int            `json:"skipped_files"`
	DocTypeCount   map[DocType]int `json:"doc_type_count"`
	NeedReview     int            `json:"need_review"`
	TotalDuration  int64          `json:"total_duration_ms"`
	AvgDuration    int64          `json:"avg_duration_ms"`
	ArchiveCount   int            `json:"archive_count"`
	UnsortedCount  int            `json:"unsorted_count"`
	GeneratedAt    time.Time      `json:"generated_at"`
	ReportPath     string         `json:"report_path,omitempty"`
}

package search

import (
	"fmt"
	"strings"
	"time"

	"pdf-archive/internal/storage"
)

type SearchService struct {
	store *storage.Storage
}

func New(store *storage.Storage) *SearchService {
	return &SearchService{store: store}
}

type Query struct {
	DocType    string
	FieldName  string
	FieldValue string
	FileName   string
	DateFrom   string
	DateTo     string
	Limit      int
	Offset     int
}

type Result struct {
	TotalCount  int
	Results     []storage.SearchResult
	QueryTimeMs int64
}

func (s *SearchService) Search(q Query) (*Result, error) {
	start := time.Now()
	sq := storage.SearchQuery{
		DocType:    q.DocType,
		FieldName:  q.FieldName,
		FieldValue: q.FieldValue,
		FileName:   q.FileName,
		Limit:      q.Limit,
		Offset:     q.Offset,
	}
	if q.DateFrom != "" {
		if t, err := parseDate(q.DateFrom); err == nil {
			sq.DateFrom = t
		}
	}
	if q.DateTo != "" {
		if t, err := parseDate(q.DateTo); err == nil {
			sq.DateTo = t
		}
	}

	if sq.Limit <= 0 {
		sq.Limit = 100
	}

	results, err := s.store.Search(sq)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	return &Result{
		TotalCount:  len(results),
		Results:     results,
		QueryTimeMs: time.Since(start).Milliseconds(),
	}, nil
}

func parseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	formats := []string{
		"2006-01-02", "2006/01/02", "2006-01-02 15:04:05",
		"2006-01", "2006",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid date: %s", s)
}

func (s *SearchService) FormatResult(r storage.SearchResult, verbose bool) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📄 %s\n", r.FilePath))
	sb.WriteString(fmt.Sprintf("   类型: %s | 置信度: %.2f\n", r.DocType, r.Confidence))
	if r.ArchivePath != "" {
		sb.WriteString(fmt.Sprintf("   归档: %s\n", r.ArchivePath))
	}
	if !r.ProcessedAt.IsZero() {
		sb.WriteString(fmt.Sprintf("   处理时间: %s\n", r.ProcessedAt.Format("2006-01-02 15:04:05")))
	}
	if verbose && len(r.Fields) > 0 {
		sb.WriteString("   字段:\n")
		names := make([]string, 0, len(r.Fields))
		for k := range r.Fields {
			names = append(names, k)
		}
		sortStrings(names)
		for _, n := range names {
			f := r.Fields[n]
			flag := ""
			if f.NeedReview {
				flag = " ⚠️"
			}
			sb.WriteString(fmt.Sprintf("     %s: %v (%.2f)%s\n", n, f.Value, f.Confidence, flag))
		}
	}
	return sb.String()
}

func sortStrings(s []string) {
	for i := 0; i < len(s); i++ {
		for j := i + 1; j < len(s); j++ {
			if s[i] > s[j] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

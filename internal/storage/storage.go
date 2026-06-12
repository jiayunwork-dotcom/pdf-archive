package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"pdf-archive/models"

	_ "modernc.org/sqlite"
)

type Storage struct {
	db *sql.DB
}

func New(path string) (*Storage, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode=WAL&_pragma=busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	s := &Storage{db: db}
	if err := s.initSchema(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Storage) Close() error {
	return s.db.Close()
}

func (s *Storage) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS pipeline_records (
		file_id TEXT PRIMARY KEY,
		file_path TEXT NOT NULL,
		md5 TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending',
		error_message TEXT,
		processed_at DATETIME,
		doc_type TEXT,
		archive_path TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_pipeline_md5 ON pipeline_records(md5);
	CREATE INDEX IF NOT EXISTS idx_pipeline_status ON pipeline_records(status);
	CREATE INDEX IF NOT EXISTS idx_pipeline_path ON pipeline_records(file_path);

	CREATE TABLE IF NOT EXISTS documents (
		file_id TEXT PRIMARY KEY,
		file_path TEXT NOT NULL,
		md5 TEXT NOT NULL,
		doc_type TEXT,
		confidence REAL DEFAULT 0,
		fields_json TEXT,
		full_text TEXT,
		archive_path TEXT,
		status TEXT,
		processed_at DATETIME,
		duration_ms INTEGER DEFAULT 0,
		tags TEXT DEFAULT ''
	);
	CREATE INDEX IF NOT EXISTS idx_docs_type ON documents(doc_type);
	CREATE INDEX IF NOT EXISTS idx_docs_md5 ON documents(md5);
	CREATE INDEX IF NOT EXISTS idx_docs_path ON documents(file_path);

	CREATE TABLE IF NOT EXISTS classifiers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		doc_type TEXT NOT NULL,
		model_data TEXT NOT NULL,
		trained_at DATETIME,
		sample_count INTEGER DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_classifiers_type ON classifiers(doc_type);
	`
	_, err := s.db.Exec(schema)
	if err != nil {
		return err
	}
	return s.migrate()
}

func (s *Storage) migrate() error {
	var colCount int
	row := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('documents') WHERE name='tags'`)
	row.Scan(&colCount)
	if colCount == 0 {
		_, err := s.db.Exec(`ALTER TABLE documents ADD COLUMN tags TEXT DEFAULT ''`)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Storage) GetRecord(md5 string) (*models.PipelineRecord, error) {
	row := s.db.QueryRow(`SELECT file_id, file_path, md5, status, error_message, processed_at, doc_type, archive_path 
		FROM pipeline_records WHERE md5 = ?`, md5)
	var r models.PipelineRecord
	var processedAt sql.NullTime
	var errMsg, docType, archivePath sql.NullString
	err := row.Scan(&r.FileID, &r.FilePath, &r.MD5, &r.Status, &errMsg, &processedAt, &docType, &archivePath)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if errMsg.Valid {
		r.ErrorMessage = errMsg.String
	}
	if processedAt.Valid {
		r.ProcessedAt = processedAt.Time
	}
	if docType.Valid {
		r.DocType = models.DocType(docType.String)
	}
	if archivePath.Valid {
		r.ArchivePath = archivePath.String
	}
	return &r, nil
}

func (s *Storage) UpsertRecord(r *models.PipelineRecord) error {
	_, err := s.db.Exec(`INSERT INTO pipeline_records (file_id, file_path, md5, status, error_message, processed_at, doc_type, archive_path)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(file_id) DO UPDATE SET
			status = excluded.status,
			error_message = excluded.error_message,
			processed_at = excluded.processed_at,
			doc_type = excluded.doc_type,
			archive_path = excluded.archive_path`,
		r.FileID, r.FilePath, r.MD5, r.Status, r.ErrorMessage, r.ProcessedAt, string(r.DocType), r.ArchivePath)
	return err
}

func (s *Storage) IsProcessed(md5 string) (bool, error) {
	row := s.db.QueryRow(`SELECT COUNT(*) FROM pipeline_records WHERE md5 = ? AND status = 'done'`, md5)
	var count int
	err := row.Scan(&count)
	return count > 0, err
}

func (s *Storage) SaveDocument(result *models.DocumentResult) error {
	fieldsJSON, err := result.FieldsJSON()
	if err != nil {
		fieldsJSON = "{}"
	}
	fullText := ""
	if result.Parsed != nil {
		fullText = result.Parsed.FullText
		if len(fullText) > 100000 {
			fullText = fullText[:100000]
		}
	}
	confidence := 0.0
	docType := ""
	if result.Classification != nil {
		confidence = result.Classification.Confidence
		docType = string(result.Classification.Type)
	}
	_, err = s.db.Exec(`INSERT INTO documents (file_id, file_path, md5, doc_type, confidence, fields_json, full_text, archive_path, status, processed_at, duration_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(file_id) DO UPDATE SET
			doc_type = excluded.doc_type,
			confidence = excluded.confidence,
			fields_json = excluded.fields_json,
			full_text = excluded.full_text,
			archive_path = excluded.archive_path,
			status = excluded.status,
			processed_at = excluded.processed_at,
			duration_ms = excluded.duration_ms`,
		result.FileID, result.FilePath, result.MD5, docType, confidence, fieldsJSON, fullText,
		result.ArchivePath, result.Status, result.ProcessedAt, result.DurationMs)
	return err
}

type SearchQuery struct {
	DocType    string
	FieldName  string
	FieldValue string
	FileName   string
	DateFrom   time.Time
	DateTo     time.Time
	Limit      int
	Offset     int
}

type SearchResult struct {
	FileID       string
	FilePath     string
	MD5          string
	DocType      string
	Confidence   float64
	Fields       map[string]models.ExtractedField
	ArchivePath  string
	Status       string
	ProcessedAt  time.Time
}

func (s *Storage) Search(q SearchQuery) ([]SearchResult, error) {
	query := `SELECT file_id, file_path, md5, doc_type, confidence, fields_json, archive_path, status, processed_at FROM documents WHERE 1=1`
	args := []interface{}{}
	argIdx := 1

	if q.DocType != "" {
		query += fmt.Sprintf(" AND doc_type = $%d", argIdx)
		args = append(args, q.DocType)
		argIdx++
	}
	if q.FileName != "" {
		query += fmt.Sprintf(" AND file_path LIKE $%d", argIdx)
		args = append(args, "%"+q.FileName+"%")
		argIdx++
	}
	if q.FieldName != "" && q.FieldValue != "" {
		query += fmt.Sprintf(" AND json_extract(fields_json, '$.' || $%d || '.value') LIKE $%d", argIdx, argIdx+1)
		args = append(args, q.FieldName, "%"+q.FieldValue+"%")
		argIdx += 2
	}
	if !q.DateFrom.IsZero() {
		query += fmt.Sprintf(" AND processed_at >= $%d", argIdx)
		args = append(args, q.DateFrom)
		argIdx++
	}
	if !q.DateTo.IsZero() {
		query += fmt.Sprintf(" AND processed_at <= $%d", argIdx)
		args = append(args, q.DateTo)
		argIdx++
	}
	query += " ORDER BY processed_at DESC"
	if q.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", q.Limit)
	}
	if q.Offset > 0 {
		query += fmt.Sprintf(" OFFSET %d", q.Offset)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var fieldsJSON string
		var processedAt sql.NullTime
		var docType, archivePath, status sql.NullString
		err := rows.Scan(&r.FileID, &r.FilePath, &r.MD5, &docType, &r.Confidence, &fieldsJSON, &archivePath, &status, &processedAt)
		if err != nil {
			return nil, err
		}
		if docType.Valid {
			r.DocType = docType.String
		}
		if archivePath.Valid {
			r.ArchivePath = archivePath.String
		}
		if status.Valid {
			r.Status = status.String
		}
		if processedAt.Valid {
			r.ProcessedAt = processedAt.Time
		}
		r.Fields = make(map[string]models.ExtractedField)
		if fieldsJSON != "" {
			_ = json.Unmarshal([]byte(fieldsJSON), &r.Fields)
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (s *Storage) Stats() (map[string]int, error) {
	stats := make(map[string]int)
	row := s.db.QueryRow(`SELECT COUNT(*) FROM pipeline_records WHERE status = 'done'`)
	var done int
	row.Scan(&done)
	stats["done"] = done

	row = s.db.QueryRow(`SELECT COUNT(*) FROM pipeline_records WHERE status = 'error'`)
	var errCount int
	row.Scan(&errCount)
	stats["error"] = errCount

	row = s.db.QueryRow(`SELECT COUNT(*) FROM pipeline_records WHERE status = 'processing'`)
	var proc int
	row.Scan(&proc)
	stats["processing"] = proc

	row = s.db.QueryRow(`SELECT COUNT(*) FROM documents`)
	var docs int
	row.Scan(&docs)
	stats["documents"] = docs

	rows, err := s.db.Query(`SELECT doc_type, COUNT(*) FROM documents GROUP BY doc_type`)
	if err != nil {
		return stats, nil
	}
	defer rows.Close()
	for rows.Next() {
		var t string
		var c int
		if err := rows.Scan(&t, &c); err == nil && t != "" {
			stats["type_"+t] = c
		}
	}
	return stats, nil
}

func (s *Storage) SaveClassifier(docType string, modelData interface{}, sampleCount int) error {
	data, err := json.Marshal(modelData)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO classifiers (doc_type, model_data, trained_at, sample_count)
		VALUES (?, ?, ?, ?)
		ON CONFLICT DO NOTHING`,
		docType, string(data), time.Now(), sampleCount)
	return err
}

func (s *Storage) GetClassifier(docType string) (string, int, error) {
	row := s.db.QueryRow(`SELECT model_data, sample_count FROM classifiers WHERE doc_type = ? ORDER BY trained_at DESC LIMIT 1`, docType)
	var modelData string
	var count int
	err := row.Scan(&modelData, &count)
	if err == sql.ErrNoRows {
		return "", 0, nil
	}
	return modelData, count, err
}

type DoneDocument struct {
	FileID       string
	FilePath     string
	MD5          string
	DocType      string
	Confidence   float64
	Fields       map[string]models.ExtractedField
	ArchivePath  string
	Tags         string
}

func (s *Storage) ListDoneDocuments() ([]DoneDocument, error) {
	rows, err := s.db.Query(`SELECT file_id, file_path, md5, doc_type, confidence, fields_json, archive_path, COALESCE(tags,'')
		FROM documents WHERE status = 'done'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []DoneDocument
	for rows.Next() {
		var d DoneDocument
		var fieldsJSON string
		var docType, archivePath sql.NullString
		err := rows.Scan(&d.FileID, &d.FilePath, &d.MD5, &docType, &d.Confidence, &fieldsJSON, &archivePath, &d.Tags)
		if err != nil {
			return nil, err
		}
		if docType.Valid {
			d.DocType = docType.String
		}
		if archivePath.Valid {
			d.ArchivePath = archivePath.String
		}
		d.Fields = make(map[string]models.ExtractedField)
		if fieldsJSON != "" {
			_ = json.Unmarshal([]byte(fieldsJSON), &d.Fields)
		}
		results = append(results, d)
	}
	return results, rows.Err()
}

func (s *Storage) UpdateArchivePath(fileID, newArchivePath string) error {
	_, err := s.db.Exec(`UPDATE documents SET archive_path = ? WHERE file_id = ?`, newArchivePath, fileID)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE pipeline_records SET archive_path = ? WHERE file_id = ?`, newArchivePath, fileID)
	return err
}

func (s *Storage) GetAllClassifiers() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT doc_type, model_data FROM classifiers WHERE (doc_type, trained_at) IN (SELECT doc_type, MAX(trained_at) FROM classifiers GROUP BY doc_type)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]string)
	for rows.Next() {
		var t, d string
		if err := rows.Scan(&t, &d); err == nil {
			result[t] = d
		}
	}
	return result, nil
}

func (s *Storage) GetTags(fileID string) (string, error) {
	row := s.db.QueryRow(`SELECT COALESCE(tags,'') FROM documents WHERE file_id = ?`, fileID)
	var tags string
	err := row.Scan(&tags)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return tags, err
}

func (s *Storage) UpdateTags(fileID, tags string) error {
	_, err := s.db.Exec(`UPDATE documents SET tags = ? WHERE file_id = ?`, tags, fileID)
	return err
}

func (s *Storage) DeleteField(fileID, fieldName string) error {
	row := s.db.QueryRow(`SELECT fields_json FROM documents WHERE file_id = ?`, fileID)
	var fieldsJSON string
	if err := row.Scan(&fieldsJSON); err != nil {
		return err
	}
	fields := make(map[string]models.ExtractedField)
	if fieldsJSON != "" {
		_ = json.Unmarshal([]byte(fieldsJSON), &fields)
	}
	delete(fields, fieldName)
	updatedJSON, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE documents SET fields_json = ? WHERE file_id = ?`, string(updatedJSON), fileID)
	return err
}

func (s *Storage) GetDocument(fileID string) (*DoneDocument, error) {
	row := s.db.QueryRow(`SELECT file_id, file_path, md5, doc_type, confidence, fields_json, archive_path, COALESCE(tags,'')
		FROM documents WHERE file_id = ?`, fileID)
	var d DoneDocument
	var fieldsJSON string
	var docType, archivePath sql.NullString
	err := row.Scan(&d.FileID, &d.FilePath, &d.MD5, &docType, &d.Confidence, &fieldsJSON, &archivePath, &d.Tags)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if docType.Valid {
		d.DocType = docType.String
	}
	if archivePath.Valid {
		d.ArchivePath = archivePath.String
	}
	d.Fields = make(map[string]models.ExtractedField)
	if fieldsJSON != "" {
		_ = json.Unmarshal([]byte(fieldsJSON), &d.Fields)
	}
	return &d, nil
}

func (s *Storage) SetField(fileID, fieldName string, value interface{}) error {
	row := s.db.QueryRow(`SELECT fields_json FROM documents WHERE file_id = ?`, fileID)
	var fieldsJSON string
	if err := row.Scan(&fieldsJSON); err != nil {
		return err
	}
	fields := make(map[string]models.ExtractedField)
	if fieldsJSON != "" {
		_ = json.Unmarshal([]byte(fieldsJSON), &fields)
	}
	fields[fieldName] = models.ExtractedField{
		Name:       fieldName,
		Value:      value,
		Confidence: 1.0,
		Method:     "dispatch",
	}
	updatedJSON, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE documents SET fields_json = ? WHERE file_id = ?`, string(updatedJSON), fileID)
	return err
}

func (s *Storage) ListAllDocuments() ([]DoneDocument, error) {
	rows, err := s.db.Query(`SELECT file_id, file_path, md5, doc_type, confidence, fields_json, archive_path, COALESCE(tags,'')
		FROM documents`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []DoneDocument
	for rows.Next() {
		var d DoneDocument
		var fieldsJSON string
		var docType, archivePath sql.NullString
		err := rows.Scan(&d.FileID, &d.FilePath, &d.MD5, &docType, &d.Confidence, &fieldsJSON, &archivePath, &d.Tags)
		if err != nil {
			return nil, err
		}
		if docType.Valid {
			d.DocType = docType.String
		}
		if archivePath.Valid {
			d.ArchivePath = archivePath.String
		}
		d.Fields = make(map[string]models.ExtractedField)
		if fieldsJSON != "" {
			_ = json.Unmarshal([]byte(fieldsJSON), &d.Fields)
		}
		results = append(results, d)
	}
	return results, rows.Err()
}

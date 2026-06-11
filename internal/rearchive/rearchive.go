package rearchive

import (
	"fmt"
	"os"
	"path/filepath"

	"pdf-archive/internal/archiver"
	"pdf-archive/internal/config"
	"pdf-archive/internal/storage"
	"pdf-archive/internal/utils"
	"pdf-archive/models"
)

type RearchiveResult struct {
	Moved   int
	Skipped int
	Missing int
	Details []RearchiveDetail
}

type RearchiveDetail struct {
	FileID    string
	FileName  string
	OldPath   string
	NewPath   string
	Status    string
	Error     string
}

type Rearchiver struct {
	cfg      *config.Config
	store    *storage.Storage
	archiver *archiver.Archiver
	dryRun   bool
}

func New(cfg *config.Config, store *storage.Storage, dryRun bool) *Rearchiver {
	return &Rearchiver{
		cfg:      cfg,
		store:    store,
		archiver: archiver.New(cfg),
		dryRun:   dryRun,
	}
}

func (r *Rearchiver) Run() (*RearchiveResult, error) {
	docs, err := r.store.ListDoneDocuments()
	if err != nil {
		return nil, fmt.Errorf("查询已归档文档失败: %w", err)
	}

	if len(docs) == 0 {
		return &RearchiveResult{}, nil
	}

	result := &RearchiveResult{
		Details: make([]RearchiveDetail, 0, len(docs)),
	}

	for _, doc := range docs {
		detail := r.processDocument(doc)
		result.Details = append(result.Details, detail)

		switch detail.Status {
		case "moved":
			result.Moved++
		case "skipped":
			result.Skipped++
		case "missing":
			result.Missing++
		}
	}

	return result, nil
}

func (r *Rearchiver) processDocument(doc storage.DoneDocument) RearchiveDetail {
	detail := RearchiveDetail{
		FileID:   doc.FileID,
		FileName: filepath.Base(doc.FilePath),
		OldPath:  doc.ArchivePath,
	}

	if doc.ArchivePath == "" {
		detail.Status = "skipped"
		return detail
	}

	if _, err := os.Stat(doc.ArchivePath); os.IsNotExist(err) {
		detail.Status = "missing"
		return detail
	}

	classResult := &models.ClassificationResult{
		Type:       models.DocType(doc.DocType),
		Confidence: doc.Confidence,
	}

	newPath := r.archiver.ComputeTargetPath(doc.ArchivePath, doc.Fields, classResult)

	cleanOld := filepath.Clean(doc.ArchivePath)
	cleanNew := filepath.Clean(newPath)

	if cleanOld == cleanNew {
		detail.Status = "skipped"
		detail.NewPath = newPath
		return detail
	}

	if r.cfg.Archive.ConflictPolicy == "rename" {
		newPath = utils.ResolveConflict(newPath)
	}

	detail.NewPath = newPath

	if r.dryRun {
		detail.Status = "moved"
		return detail
	}

	targetDir := filepath.Dir(newPath)
	if err := utils.EnsureDir(targetDir); err != nil {
		detail.Status = "skipped"
		detail.Error = fmt.Sprintf("创建目录失败: %v", err)
		return detail
	}

	if err := os.Rename(doc.ArchivePath, newPath); err != nil {
		if err := copyFile(doc.ArchivePath, newPath); err != nil {
			detail.Status = "skipped"
			detail.Error = fmt.Sprintf("移动文件失败: %v", err)
			return detail
		}
		os.Remove(doc.ArchivePath)
	}

	if err := r.store.UpdateArchivePath(doc.FileID, newPath); err != nil {
		detail.Status = "moved"
		detail.Error = fmt.Sprintf("更新索引失败: %v", err)
		return detail
	}

	detail.Status = "moved"
	return detail
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

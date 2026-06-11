package pipeline

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"pdf-archive/internal/archiver"
	"pdf-archive/internal/classifier"
	"pdf-archive/internal/config"
	"pdf-archive/internal/extractor"
	"pdf-archive/internal/pdfparser"
	"pdf-archive/internal/storage"
	"pdf-archive/internal/utils"
	"pdf-archive/internal/validator"
	"pdf-archive/models"
)

type Pipeline struct {
	cfg        *config.Config
	parser     *pdfparser.PDFParser
	classifier *classifier.Classifier
	extractor  *extractor.Extractor
	validator  *validator.Validator
	archiver   *archiver.Archiver
	store      *storage.Storage

	mu          sync.Mutex
	summary     *models.SummaryReport
	processed   int64
	errors      int64
	success     int64
	skipped     int64
	needReview  int64
	archived    int64
	unsorted    int64
	typeCount   map[models.DocType]int64
	startTime   time.Time
}

func New(cfg *config.Config) (*Pipeline, error) {
	store, err := storage.New(cfg.Storage.DBPath)
	if err != nil {
		return nil, fmt.Errorf("init storage: %w", err)
	}

	return &Pipeline{
		cfg:        cfg,
		parser:     pdfparser.New(cfg),
		classifier: classifier.New(cfg, store),
		extractor:  extractor.New(cfg),
		validator:  validator.New(cfg),
		archiver:   archiver.New(cfg),
		store:      store,
		typeCount:  make(map[models.DocType]int64),
		summary: &models.SummaryReport{
			DocTypeCount: make(map[models.DocType]int),
		},
	}, nil
}

func (p *Pipeline) Close() error {
	if p.store != nil {
		return p.store.Close()
	}
	return nil
}

type fileJob struct {
	Path    string
	MD5     string
	Size    int64
	Retries int
}

func (p *Pipeline) Run(ctx context.Context, inputDir string) (*models.SummaryReport, error) {
	p.startTime = time.Now()

	if inputDir == "" {
		inputDir = p.cfg.Pipeline.InputDir
	}
	if inputDir == "" {
		return nil, fmt.Errorf("input directory not specified")
	}

	if _, err := os.Stat(inputDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("input directory does not exist: %s", inputDir)
	}

	p.logInfo("扫描目录: %s (recursive=%v)", inputDir, p.cfg.Pipeline.Recursive)
	files, err := utils.FindPDFs(inputDir, p.cfg.Pipeline.Recursive)
	if err != nil {
		return nil, fmt.Errorf("scan directory: %w", err)
	}

	if len(files) == 0 {
		p.logInfo("未找到PDF文件")
		return p.buildSummary(), nil
	}

	p.summary.TotalFiles = len(files)
	p.logInfo("找到 %d 个PDF文件", len(files))

	if len(files) > p.cfg.Pipeline.MaxFiles {
		return nil, fmt.Errorf("文件数量 %d 超过最大限制 %d，请分批执行", len(files), p.cfg.Pipeline.MaxFiles)
	}

	var jobs []fileJob
	skippedResume := 0
	for _, f := range files {
		size, _ := utils.FileSize(f)
		md5, err := utils.FileMD5(f)
		if err != nil {
			p.logWarn("计算MD5失败 %s: %v", f, err)
			continue
		}

		if p.cfg.Pipeline.ResumeFromDB {
			done, err := p.store.IsProcessed(md5)
			if err == nil && done {
				skippedResume++
				continue
			}
		}

		jobs = append(jobs, fileJob{Path: f, MD5: md5, Size: size})
	}

	if skippedResume > 0 {
		p.skipped = int64(skippedResume)
		p.logInfo("跳过已处理的 %d 个文件", skippedResume)
	}

	p.summary.SkippedFiles = skippedResume

	if len(jobs) == 0 {
		p.logInfo("没有需要处理的新文件")
		return p.buildSummary(), nil
	}

	p.runWorkers(ctx, jobs)

	return p.buildSummary(), nil
}

func (p *Pipeline) runWorkers(ctx context.Context, jobs []fileJob) {
	workerCount := p.cfg.Pipeline.Workers
	if workerCount <= 0 {
		workerCount = 1
	}
	if workerCount > len(jobs) {
		workerCount = len(jobs)
	}

	p.logInfo("启动 %d 个worker处理 %d 个文件", workerCount, len(jobs))

	jobChan := make(chan fileJob, len(jobs))
	var wg sync.WaitGroup
	var fileLocks sync.Map

	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for job := range jobChan {
				if ctx.Err() != nil {
					return
				}
				if _, loaded := fileLocks.LoadOrStore(job.MD5, true); loaded {
					continue
				}
				p.processFileWithTimeout(ctx, workerID, job)
				fileLocks.Delete(job.MD5)
			}
		}(i)
	}

	for _, j := range jobs {
		jobChan <- j
	}
	close(jobChan)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		p.logWarn("Pipeline被取消")
	case <-done:
	}
}

func (p *Pipeline) processFileWithTimeout(ctx context.Context, workerID int, job fileJob) {
	timeout := time.Duration(p.cfg.Pipeline.TimeoutPerFileSec) * time.Second
	fileCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	done := make(chan *models.DocumentResult, 1)
	errChan := make(chan error, 1)

	go func() {
		result, err := p.processSingleFile(fileCtx, job)
		if err != nil {
			errChan <- err
			return
		}
		done <- result
	}()

	select {
	case <-fileCtx.Done():
		p.recordError(job, fmt.Errorf("timeout after %v", timeout))
		p.logWarn("[W%d] 处理超时: %s", workerID, job.Path)
	case err := <-errChan:
		p.recordError(job, err)
		p.logWarn("[W%d] 处理错误 %s: %v", workerID, job.Path, err)
	case result := <-done:
		p.recordResult(job, result)
		if p.cfg.Pipeline.Verbose {
			p.logInfo("[W%d] 完成: %s -> %s (%.2f)", workerID, job.Path,
				result.Classification.Type, result.Classification.Confidence)
		}
	}
}

func (p *Pipeline) processSingleFile(ctx context.Context, job fileJob) (*models.DocumentResult, error) {
	start := time.Now()
	result := &models.DocumentResult{
		FileID:   job.MD5,
		FilePath: job.Path,
		MD5:      job.MD5,
		Fields:   make(map[string]models.ExtractedField),
		Status:   "processing",
	}

	p.updateRecord(job, models.StatusProcessing, "", "", "")

	var parsed *models.ParsedDocument
	var err error

	if p.cfg.Stages.Parse {
		parsed, err = p.parser.Parse(ctx, job.Path)
		if err != nil {
			return nil, fmt.Errorf("parse: %w", err)
		}
		result.Parsed = parsed
	}

	var classResult *models.ClassificationResult
	if p.cfg.Stages.Classify && parsed != nil {
		classResult = p.classifier.Classify(parsed)
		result.Classification = classResult
	}

	if p.cfg.Stages.Extract && parsed != nil && classResult != nil {
		fields := p.extractor.Extract(parsed, classResult)
		result.Fields = fields

		if p.cfg.Stages.Validate {
			typeCfg := p.cfg.GetDocTypeConfig(string(classResult.Type))
			validation := p.validator.Validate(fields, typeCfg)
			result.Validation = validation
		}
	}

	if p.cfg.Stages.Archive {
		mode := p.cfg.Archive.Mode
		if p.cfg.Pipeline.DryRun {
			mode = "dry_run"
		}
		ar := p.archiver.Archive(job.Path, result.Fields, result.Classification, p.cfg.Pipeline.DryRun)
		if ar.Success {
			result.ArchivePath = ar.TargetPath
			result.ArchiveMode = mode
			p.mu.Lock()
			if ar.Unsorted {
				p.unsorted++
			} else {
				p.archived++
			}
			p.mu.Unlock()
		} else if !p.cfg.Pipeline.DryRun {
			p.logWarn("归档失败 %s: %s", job.Path, ar.Error)
		}
	}

	result.Status = "done"
	result.ProcessedAt = time.Now()
	result.DurationMs = time.Since(start).Milliseconds()

	if p.store != nil {
		if err := p.store.SaveDocument(result); err != nil {
			p.logWarn("保存文档索引失败: %v", err)
		}
	}

	archivePath := result.ArchivePath
	docType := ""
	if result.Classification != nil {
		docType = string(result.Classification.Type)
	}
	p.updateRecord(job, models.StatusDone, "", docType, archivePath)

	return result, nil
}

func (p *Pipeline) updateRecord(job fileJob, status models.PipelineStatus, errMsg, docType, archivePath string) {
	if p.store == nil {
		return
	}
	rec := &models.PipelineRecord{
		FileID:       job.MD5,
		FilePath:     job.Path,
		MD5:          job.MD5,
		Status:       status,
		ErrorMessage: errMsg,
		ProcessedAt:  time.Now(),
	}
	if docType != "" {
		rec.DocType = models.DocType(docType)
	}
	if archivePath != "" {
		rec.ArchivePath = archivePath
	}
	if err := p.store.UpsertRecord(rec); err != nil {
		p.logWarn("更新状态失败: %v", err)
	}
}

func (p *Pipeline) recordError(job fileJob, err error) {
	atomic.AddInt64(&p.errors, 1)
	atomic.AddInt64(&p.processed, 1)
	p.updateRecord(job, models.StatusError, err.Error(), "", "")
}

func (p *Pipeline) recordResult(job fileJob, result *models.DocumentResult) {
	atomic.AddInt64(&p.processed, 1)
	if result.Classification != nil {
		if result.Classification.Confidence >= p.cfg.Classifier.Threshold {
			atomic.AddInt64(&p.success, 1)
		}
		p.mu.Lock()
		p.typeCount[result.Classification.Type]++
		p.mu.Unlock()
	}
	if result.Validation != nil && result.Validation.NeedReviewCount > 0 {
		atomic.AddInt64(&p.needReview, 1)
	}
}

func (p *Pipeline) buildSummary() *models.SummaryReport {
	p.summary.ProcessedFiles = int(atomic.LoadInt64(&p.processed))
	p.summary.SuccessFiles = int(atomic.LoadInt64(&p.success))
	p.summary.ErrorFiles = int(atomic.LoadInt64(&p.errors))
	p.summary.SkippedFiles += int(atomic.LoadInt64(&p.skipped))
	p.summary.NeedReview = int(atomic.LoadInt64(&p.needReview))
	p.summary.ArchiveCount = int(atomic.LoadInt64(&p.archived))
	p.summary.UnsortedCount = int(atomic.LoadInt64(&p.unsorted))
	p.mu.Lock()
	for k, v := range p.typeCount {
		p.summary.DocTypeCount[k] = int(v)
	}
	p.mu.Unlock()
	p.summary.TotalDuration = time.Since(p.startTime).Milliseconds()
	if p.summary.ProcessedFiles > 0 {
		p.summary.AvgDuration = p.summary.TotalDuration / int64(p.summary.ProcessedFiles)
	}
	p.summary.GeneratedAt = time.Now()
	return p.summary
}

func (p *Pipeline) logInfo(format string, args ...interface{}) {
	log.Printf("[INFO] "+format, args...)
}

func (p *Pipeline) logWarn(format string, args ...interface{}) {
	log.Printf("[WARN] "+format, args...)
}

func (p *Pipeline) Stats() (map[string]int, error) {
	if p.store == nil {
		return nil, fmt.Errorf("storage not initialized")
	}
	return p.store.Stats()
}

func (p *Pipeline) TrainClassifier(samples map[string][]string) error {
	return p.classifier.Train(samples)
}

func (p *Pipeline) GetStore() *storage.Storage {
	return p.store
}

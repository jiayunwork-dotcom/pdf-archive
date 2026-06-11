package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"pdf-archive/internal/classifier"
	"pdf-archive/internal/config"
	"pdf-archive/internal/pipeline"
	"pdf-archive/internal/rearchive"
	"pdf-archive/internal/search"
	"pdf-archive/internal/storage"
	"pdf-archive/internal/utils"
	"pdf-archive/models"

	"github.com/spf13/cobra"
)

var (
	cfgFile    string
	workers    int
	dryRun     bool
	verbose    bool
	inputDir   string
	outputDir  string
	recursive  bool
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "pdf-archive",
		Short: "批量PDF文档结构化信息提取与智能归档工具",
		Long: `pdf-archive 是一个批量PDF文档结构化信息提取与智能归档的自动化Pipeline工具,
支持:
- 文本PDF和扫描件PDF(OCR)解析
- 文档自动分类(规则+TF-IDF朴素贝叶斯)
- 结构化字段提取(正则/关键词/坐标/表格)
- 提取结果校验与置信度评估
- 按规则自动归档
- SQLite索引与检索
- Pipeline断点续跑与并行处理`,
		SilenceUsage: true,
	}

	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "配置文件路径 (YAML)")
	rootCmd.PersistentFlags().IntVarP(&workers, "workers", "w", 0, "并行worker数 (默认CPU核数)")
	rootCmd.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "只分析不实际归档")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "详细日志模式")

	rootCmd.AddCommand(runCmd())
	rootCmd.AddCommand(statusCmd())
	rootCmd.AddCommand(searchCmd())
	rootCmd.AddCommand(configCmd())
	rootCmd.AddCommand(trainCmd())
	rootCmd.AddCommand(rearchiveCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func loadConfig() (*config.Config, error) {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return nil, err
	}
	if workers > 0 {
		cfg.Pipeline.Workers = workers
	}
	if dryRun {
		cfg.Pipeline.DryRun = dryRun
	}
	if verbose {
		cfg.Pipeline.Verbose = verbose
	}
	if inputDir != "" {
		cfg.Pipeline.InputDir = inputDir
	}
	if outputDir != "" {
		cfg.Archive.TargetDir = outputDir
	}
	if recursive {
		cfg.Pipeline.Recursive = recursive
	}
	return cfg, nil
}

func runCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "执行Pipeline处理PDF文件",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("加载配置失败: %w", err)
			}

			dir := cfg.Pipeline.InputDir
			if len(args) > 0 {
				dir = args[0]
			}
			if dir == "" {
				return fmt.Errorf("请指定输入目录")
			}

			printBanner("执行PDF处理Pipeline")
			fmt.Printf("📁 输入目录: %s\n", dir)
			fmt.Printf("📦 归档目录: %s\n", cfg.Archive.TargetDir)
			fmt.Printf("🗄️  数据库: %s\n", cfg.Storage.DBPath)
			fmt.Printf("⚙️  Workers: %d, 超时: %ds, 最大文件: %d\n",
				cfg.Pipeline.Workers, cfg.Pipeline.TimeoutPerFileSec, cfg.Pipeline.MaxFiles)
			if cfg.Pipeline.DryRun {
				fmt.Println("🔍 DRY RUN模式 - 不会实际移动/复制文件")
			}
			fmt.Println()

			pipe, err := pipeline.New(cfg)
			if err != nil {
				return fmt.Errorf("初始化Pipeline失败: %w", err)
			}
			defer pipe.Close()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				sig := <-sigCh
				fmt.Printf("\n⚠️  收到信号 %s, 正在优雅关闭...\n", sig)
				cancel()
			}()

			start := time.Now()
			summary, err := pipe.Run(ctx, dir)
			elapsed := time.Since(start)

			if err != nil {
				return fmt.Errorf("Pipeline执行失败: %w", err)
			}

			printSummary(summary, elapsed)
			return nil
		},
	}
	cmd.Flags().StringVarP(&inputDir, "input", "i", "", "输入目录(覆盖配置)")
	cmd.Flags().StringVarP(&outputDir, "output", "o", "", "输出归档目录(覆盖配置)")
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "递归扫描子目录")
	return cmd
}

func statusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "查看处理进度与统计信息",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("加载配置失败: %w", err)
			}

			store, err := storage.New(cfg.Storage.DBPath)
			if err != nil {
				return fmt.Errorf("打开数据库失败: %w", err)
			}
			defer store.Close()

			stats, err := store.Stats()
			if err != nil {
				return fmt.Errorf("查询统计失败: %w", err)
			}

			printBanner("Pipeline状态")
			fmt.Printf("🗄️  数据库: %s\n\n", cfg.Storage.DBPath)
			fmt.Println("📊 处理统计:")
			fmt.Printf("   ✅ 已完成: %d\n", stats["done"])
			fmt.Printf("   ❌ 失败:   %d\n", stats["error"])
			fmt.Printf("   🔄 处理中: %d\n", stats["processing"])
			fmt.Printf("   📄 索引文档: %d\n", stats["documents"])

			typeLabels := map[string]string{
				"invoice":  "🧾 发票",
				"contract": "📝 合同",
				"resume":   "📋 简历",
				"report":   "🔬 检测报告",
				"notice":   "📢 通知公告",
				"unknown":  "❓ 未知",
			}

			fmt.Println("\n📂 文档类型分布:")
			hasTypes := false
			for t, label := range typeLabels {
				if c, ok := stats["type_"+t]; ok && c > 0 {
					hasTypes = true
					fmt.Printf("   %s: %d\n", label, c)
				}
			}
			for k, v := range stats {
				if strings.HasPrefix(k, "type_") {
					t := strings.TrimPrefix(k, "type_")
					if _, ok := typeLabels[t]; !ok && v > 0 {
						hasTypes = true
						fmt.Printf("   %s: %d\n", t, v)
					}
				}
			}
			if !hasTypes {
				fmt.Println("   (暂无分类文档)")
			}

			return nil
		},
	}
	return cmd
}

func searchCmd() *cobra.Command {
	var (
		docType    string
		fieldName  string
		fieldValue string
		fileName   string
		dateFrom   string
		dateTo     string
		limit      int
		offset     int
		showFields bool
	)

	cmd := &cobra.Command{
		Use:   "search",
		Short: "检索已索引的文档",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("加载配置失败: %w", err)
			}

			store, err := storage.New(cfg.Storage.DBPath)
			if err != nil {
				return fmt.Errorf("打开数据库失败: %w", err)
			}
			defer store.Close()

			svc := search.New(store)

			q := search.Query{
				DocType:    docType,
				FieldName:  fieldName,
				FieldValue: fieldValue,
				FileName:   fileName,
				DateFrom:   dateFrom,
				DateTo:     dateTo,
				Limit:      limit,
				Offset:     offset,
			}

			printBanner("文档检索")
			printQuery(q)

			result, err := svc.Search(q)
			if err != nil {
				return fmt.Errorf("检索失败: %w", err)
			}

			fmt.Printf("\n✅ 找到 %d 条结果 (耗时 %dms)\n\n", result.TotalCount, result.QueryTimeMs)

			for i, r := range result.Results {
				fmt.Printf("[%d] %s", i+1, svc.FormatResult(r, showFields))
				fmt.Println()
			}

			if result.TotalCount == 0 {
				fmt.Println("  没有匹配的文档。")
			}

			return nil
		},
	}
	cmd.Flags().StringVarP(&docType, "type", "t", "", "按文档类型过滤 (invoice/contract/resume/report/notice)")
	cmd.Flags().StringVar(&fieldName, "field-name", "", "字段名")
	cmd.Flags().StringVar(&fieldValue, "field-value", "", "字段值(支持模糊匹配)")
	cmd.Flags().StringVarP(&fileName, "name", "n", "", "按文件名过滤")
	cmd.Flags().StringVar(&dateFrom, "from", "", "起始日期 (YYYY-MM-DD)")
	cmd.Flags().StringVar(&dateTo, "to", "", "结束日期 (YYYY-MM-DD)")
	cmd.Flags().IntVarP(&limit, "limit", "l", 50, "返回数量限制")
	cmd.Flags().IntVar(&offset, "offset", 0, "偏移量")
	cmd.Flags().BoolVarP(&showFields, "fields", "f", false, "显示详细字段")
	return cmd
}

func configCmd() *cobra.Command {
	var (
		generateDefault bool
		checkOnly       bool
	)

	cmd := &cobra.Command{
		Use:   "config",
		Short: "配置文件管理与校验",
		RunE: func(cmd *cobra.Command, args []string) error {
			if generateDefault {
				cfg := config.DefaultConfig()
				outPath := "config.default.yaml"
				if len(args) > 0 {
					outPath = args[0]
				}
				if err := cfg.Save(outPath); err != nil {
					return fmt.Errorf("保存配置失败: %w", err)
				}
				fmt.Printf("✅ 已生成默认配置文件: %s\n", outPath)
				return nil
			}

			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("❌ 配置校验失败: %w", err)
			}

			printBanner("配置校验结果")
			fmt.Println("✅ 配置文件有效")
			fmt.Printf("📄 配置路径: %s\n", cfg.ConfigPath)
			fmt.Printf("📁 输入目录: %s\n", cfg.Pipeline.InputDir)
			fmt.Printf("📦 归档目录: %s\n", cfg.Archive.TargetDir)
			fmt.Printf("🗄️  数据库: %s\n", cfg.Storage.DBPath)
			fmt.Printf("⚙️  Workers: %d, OCR: %v, 语言: %s\n",
				cfg.Pipeline.Workers, cfg.OCR.Enabled, cfg.OCR.Languages)
			fmt.Printf("📝 归档模板: %s\n", cfg.Archive.PathTemplate)
			fmt.Printf("📚 支持文档类型: %d 种\n", len(cfg.DocTypes))
			for _, dt := range cfg.DocTypes {
				fmt.Printf("   - %s (%s): %d条提取规则, %d个关键词\n",
					dt.DisplayName, dt.Type, len(dt.ExtractRules), len(dt.Keywords))
			}

			stages := []struct {
				name string
				ok   bool
			}{
				{"扫描", cfg.Stages.Scan},
				{"解析", cfg.Stages.Parse},
				{"分类", cfg.Stages.Classify},
				{"提取", cfg.Stages.Extract},
				{"校验", cfg.Stages.Validate},
				{"归档", cfg.Stages.Archive},
				{"报告", cfg.Stages.Report},
			}
			fmt.Println("\n🔧 Pipeline 阶段:")
			for _, s := range stages {
				flag := "✅"
				if !s.ok {
					flag = "⏭️  (跳过)"
				}
				fmt.Printf("   %s: %s\n", s.name, flag)
			}

			if !checkOnly {
				fmt.Println("\n💾 完整配置JSON:")
				data, _ := json.MarshalIndent(cfg, "", "  ")
				if len(data) > 2000 {
					fmt.Println(string(data[:2000]) + "\n... (截断)")
				} else {
					fmt.Println(string(data))
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&generateDefault, "generate", false, "生成默认配置文件")
	cmd.Flags().BoolVar(&checkOnly, "check", false, "仅检查配置有效性")
	return cmd
}

func trainCmd() *cobra.Command {
	var (
		annotationDir string
		outputModel   string
	)

	cmd := &cobra.Command{
		Use:   "train",
		Short: "使用标注数据训练分类器",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("加载配置失败: %w", err)
			}

			dir := annotationDir
			if len(args) > 0 {
				dir = args[0]
			}
			if dir == "" {
				return fmt.Errorf("请指定标注数据目录")
			}

			printBanner("训练文档分类器")
			fmt.Printf("📁 标注目录: %s\n", dir)
			fmt.Printf("📋 最少样本数: %d (每种类型)\n", cfg.Classifier.MinTrainSamples)
			fmt.Println()

			samples, err := loadTrainingSamples(dir)
			if err != nil {
				return fmt.Errorf("加载训练样本失败: %w", err)
			}

			totalTypeCount := 0
			sufficientTypes := 0
			for t, texts := range samples {
				ok := len(texts) >= cfg.Classifier.MinTrainSamples
				status := "✅"
				if !ok {
					status = "❌ 不足"
				} else {
					sufficientTypes++
				}
				totalTypeCount++
				fmt.Printf("   %s: %d 个样本 %s\n", t, len(texts), status)
			}

			if totalTypeCount == 0 {
				return fmt.Errorf("未找到任何标注样本")
			}

			if sufficientTypes == 0 {
				fmt.Println("\n⚠️  没有任何类型达到最少样本数，将使用纯规则模式分类")
			} else {
				pipe, err := pipeline.New(cfg)
				if err != nil {
					return fmt.Errorf("初始化Pipeline失败: %w", err)
				}
				defer pipe.Close()

				if err := pipe.TrainClassifier(samples); err != nil {
					return fmt.Errorf("训练失败: %w", err)
				}

				fmt.Println("\n✅ 分类器训练完成，模型已保存到数据库")
			}

			if outputModel != "" {
				data, err := json.MarshalIndent(samples, "", "  ")
				if err == nil {
					os.WriteFile(outputModel, data, 0644)
					fmt.Printf("💾 训练数据已导出到: %s\n", outputModel)
				}
			}

			return nil
		},
	}
	cmd.Flags().StringVarP(&annotationDir, "annotations", "a", "", "标注数据目录")
	cmd.Flags().StringVarP(&outputModel, "output", "o", "", "导出训练数据到JSON文件")
	return cmd
}

func loadTrainingSamples(root string) (map[string][]string, error) {
	samples := make(map[string][]string)

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		docType := entry.Name()
		typeDir := filepath.Join(root, docType)

		files, err := utils.FindPDFs(typeDir, true)
		if err != nil {
			continue
		}

		textFileDir := filepath.Join(root, docType+"_txt")
		var textFiles []string
		if _, err := os.Stat(textFileDir); err == nil {
			txts, _ := utils.FindPDFs(textFileDir, true)
			for _, t := range txts {
				if strings.HasSuffix(strings.ToLower(t), ".txt") {
					textFiles = append(textFiles, t)
				}
			}
		}

		count := len(files)
		if count < len(textFiles) {
			count = len(textFiles)
		}
		if count == 0 {
			continue
		}

		for i, f := range files {
			if i >= count {
				break
			}
			data, err := os.ReadFile(f)
			if err == nil {
				samples[docType] = append(samples[docType], string(data))
			}
		}

		for _, t := range textFiles {
			if len(samples[docType]) >= count {
				break
			}
			data, err := os.ReadFile(t)
			if err == nil {
				samples[docType] = append(samples[docType], string(data))
			}
		}
	}

	txtPattern := filepath.Join(root, "*.txt")
	matches, _ := filepath.Glob(txtPattern)
	for _, m := range matches {
		base := strings.TrimSuffix(filepath.Base(m), ".txt")
		parts := strings.SplitN(base, "_", 2)
		if len(parts) == 2 {
			t := parts[0]
			data, err := os.ReadFile(m)
			if err == nil {
				samples[t] = append(samples[t], string(data))
			}
		}
	}

	return samples, nil
}

func rearchiveCmd() *cobra.Command {
	var rearchiveDryRun bool

	cmd := &cobra.Command{
		Use:   "rearchive",
		Short: "批量重归档: 按最新配置重新计算归档路径并迁移文件",
		Long: `对已索引的文档重新计算归档路径并执行文件迁移。
只处理 status=done 的文档记录, 其他状态跳过。
支持 --dry-run 参数预览将要执行的变更而不实际移动。`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("加载配置失败: %w", err)
			}

			if rearchiveDryRun {
				cfg.Pipeline.DryRun = true
			}

			printBanner("批量重归档")
			fmt.Printf("📦 归档目录: %s\n", cfg.Archive.TargetDir)
			fmt.Printf("🗄️  数据库: %s\n", cfg.Storage.DBPath)
			fmt.Printf("📝 归档模板: %s\n", cfg.Archive.PathTemplate)
			if cfg.Pipeline.DryRun {
				fmt.Println("🔍 DRY RUN模式 - 不会实际移动文件")
			}
			fmt.Println()

			store, err := storage.New(cfg.Storage.DBPath)
			if err != nil {
				return fmt.Errorf("打开数据库失败: %w", err)
			}
			defer store.Close()

			ra := rearchive.New(cfg, store, cfg.Pipeline.DryRun)
			result, err := ra.Run()
			if err != nil {
				return fmt.Errorf("重归档执行失败: %w", err)
			}

			printRearchiveResult(result, cfg.Pipeline.DryRun)
			return nil
		},
	}
	cmd.Flags().BoolVar(&rearchiveDryRun, "dry-run", false, "预览变更,不实际移动文件")
	return cmd
}

func printRearchiveResult(r *rearchive.RearchiveResult, isDryRun bool) {
	mode := ""
	if isDryRun {
		mode = " [DRY RUN]"
	}
	printBanner(fmt.Sprintf("重归档完成%s", mode))

	fmt.Printf("📊 汇总:\n")
	fmt.Printf("   ✅ 已迁移:  %d\n", r.Moved)
	fmt.Printf("   ⏭️  跳过:    %d\n", r.Skipped)
	fmt.Printf("   ❓ 缺失:    %d\n", r.Missing)

	if len(r.Details) == 0 {
		fmt.Println("\n   (无已归档文档)")
		return
	}

	movedDetails := []rearchive.RearchiveDetail{}
	for _, d := range r.Details {
		if d.Status == "moved" {
			movedDetails = append(movedDetails, d)
		}
	}

	if len(movedDetails) > 0 {
		fmt.Printf("\n📋 变更明细%s:\n", mode)
		fmt.Printf("   %-30s  %-40s  →  %s\n", "文件名", "旧路径", "新路径")
		fmt.Printf("   %s\n", strings.Repeat("─", 100))
		for _, d := range movedDetails {
			oldRel := d.OldPath
			newRel := d.NewPath
			fmt.Printf("   %-30s  %-40s  →  %s\n", d.FileName, oldRel, newRel)
		}
	}

	missingDetails := []rearchive.RearchiveDetail{}
	for _, d := range r.Details {
		if d.Status == "missing" {
			missingDetails = append(missingDetails, d)
		}
	}

	if len(missingDetails) > 0 {
		fmt.Printf("\n❓ 缺失文件 (源文件不存在):\n")
		for _, d := range missingDetails {
			fmt.Printf("   %s (原路径: %s)\n", d.FileName, d.OldPath)
		}
	}
}

func printBanner(title string) {
	w := 60
	fmt.Println()
	fmt.Println(strings.Repeat("═", w))
	fmt.Printf("  %s\n", title)
	fmt.Println(strings.Repeat("═", w))
}

func printQuery(q search.Query) {
	parts := []string{}
	if q.DocType != "" {
		parts = append(parts, "类型="+q.DocType)
	}
	if q.FileName != "" {
		parts = append(parts, "文件名~="+q.FileName)
	}
	if q.FieldName != "" {
		parts = append(parts, fmt.Sprintf("%s~=%s", q.FieldName, q.FieldValue))
	}
	if q.DateFrom != "" || q.DateTo != "" {
		parts = append(parts, fmt.Sprintf("日期[%s~%s]", q.DateFrom, q.DateTo))
	}
	if len(parts) == 0 {
		fmt.Println("  条件: (全部)")
	} else {
		fmt.Println("  条件:", strings.Join(parts, ", "))
	}
}

func printSummary(s *models.SummaryReport, elapsed time.Duration) {
	printBanner("处理完成 - 汇总报告")

	fmt.Printf("⏱️  总耗时: %s (平均 %.0fms/文件)\n", elapsed, float64(s.AvgDuration))
	fmt.Printf("📄 总文件数: %d\n\n", s.TotalFiles)

	fmt.Println("📊 处理统计:")
	fmt.Printf("   ✅ 成功处理:  %d\n", s.SuccessFiles)
	fmt.Printf("   ⚠️  需人工复核: %d\n", s.NeedReview)
	fmt.Printf("   ❌ 失败:      %d\n", s.ErrorFiles)
	fmt.Printf("   ⏭️  跳过:      %d\n", s.SkippedFiles)
	fmt.Printf("   📦 已归档:    %d\n", s.ArchiveCount)
	fmt.Printf("   📂 待分拣:    %d\n\n", s.UnsortedCount)

	typeLabels := map[models.DocType]string{
		models.DocTypeInvoice:  "🧾 发票",
		models.DocTypeContract: "📝 合同",
		models.DocTypeResume:   "📋 简历",
		models.DocTypeReport:   "🔬 检测报告",
		models.DocTypeNotice:   "📢 通知公告",
		models.DocTypeUnknown:  "❓ 未知",
	}

	fmt.Println("📂 文档类型分布:")
	hasAny := false
	for t, label := range typeLabels {
		if c, ok := s.DocTypeCount[t]; ok && c > 0 {
			hasAny = true
			barLen := 0
			total := s.SuccessFiles
			if total > 0 {
				barLen = int(float64(c) / float64(total) * 30)
			}
			bar := strings.Repeat("█", barLen)
			fmt.Printf("   %s: %4d  %s\n", label, c, bar)
		}
	}
	for t, c := range s.DocTypeCount {
		if _, ok := typeLabels[t]; !ok && c > 0 {
			hasAny = true
			fmt.Printf("   %s: %d\n", t, c)
		}
	}
	if !hasAny {
		fmt.Println("   (无分类数据)")
	}

	successRate := 0.0
	if s.TotalFiles > 0 {
		successRate = float64(s.SuccessFiles) / float64(s.TotalFiles-s.SkippedFiles) * 100
	}
	fmt.Printf("\n📈 处理成功率: %.1f%%\n", successRate)

	if s.NeedReview > 0 {
		fmt.Printf("\n⚠️  有 %d 个文档需要人工复核，请检查归档目录中的文件。\n", s.NeedReview)
	}

	if s.ReportPath != "" {
		fmt.Printf("\n📄 处理报告: %s\n", s.ReportPath)
	}
}

var _ = runtime.NumCPU
var _ = classifier.Tokenize

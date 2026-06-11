package classifier

import (
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"

	"pdf-archive/internal/config"
	"pdf-archive/internal/storage"
	"pdf-archive/models"
)

type Classifier struct {
	cfg        *config.Config
	store      *storage.Storage
	naiveBayes *NaiveBayes
	ruleFirst  bool
	mu         sync.RWMutex
}

type NaiveBayes struct {
	Vocabulary   map[string]bool            `json:"vocabulary"`
	WordCounts   map[string]map[string]int  `json:"word_counts"`
	DocCounts    map[string]int             `json:"doc_counts"`
	TotalDocs    int                        `json:"total_docs"`
	TotalWords   map[string]int             `json:"total_words"`
}

func NewNaiveBayes() *NaiveBayes {
	return &NaiveBayes{
		Vocabulary: make(map[string]bool),
		WordCounts: make(map[string]map[string]int),
		DocCounts:  make(map[string]int),
		TotalWords: make(map[string]int),
	}
}

func New(cfg *config.Config, store *storage.Storage) *Classifier {
	c := &Classifier{
		cfg:       cfg,
		store:     store,
		ruleFirst: cfg.Classifier.RuleFirst,
	}
	c.naiveBayes = NewNaiveBayes()
	c.loadModels()
	return c
}

func (c *Classifier) loadModels() {
	if c.store == nil {
		return
	}
	modelsMap, err := c.store.GetAllClassifiers()
	if err != nil || len(modelsMap) == 0 {
		return
	}
	for _, data := range modelsMap {
		nb := NewNaiveBayes()
		if err := json.Unmarshal([]byte(data), nb); err == nil {
			c.mu.Lock()
			c.naiveBayes = nb
			c.mu.Unlock()
			break
		}
	}
}

func (c *Classifier) Classify(doc *models.ParsedDocument) *models.ClassificationResult {
	result := &models.ClassificationResult{
		Type:       models.DocTypeUnknown,
		Confidence: 0.0,
		Scores:     make(map[models.DocType]float64),
	}

	if doc == nil || strings.TrimSpace(doc.FullText) == "" {
		return result
	}

	text := strings.ToLower(doc.FullText)

	if c.ruleFirst {
		ruleResult := c.ruleClassify(text)
		if ruleResult.Type != models.DocTypeUnknown {
			for k, v := range ruleResult.Scores {
				result.Scores[k] = v
			}
			mlResult := c.mlClassify(text)
			for k, v := range mlResult.Scores {
				if existing, ok := result.Scores[k]; ok {
					result.Scores[k] = (existing + v) / 2
				} else {
					result.Scores[k] = v * 0.4
				}
			}
			result.Type = ruleResult.Type
			result.Confidence = ruleResult.Confidence
			result.Method = "rule+ml"
			result.RuleHit = ruleResult.RuleHit
			return result
		}
	}

	mlResult := c.mlClassify(text)
	if mlResult.Type != models.DocTypeUnknown {
		return mlResult
	}

	if !c.ruleFirst {
		ruleResult := c.ruleClassify(text)
		if ruleResult.Type != models.DocTypeUnknown {
			return ruleResult
		}
	}

	return result
}

func (c *Classifier) ruleClassify(text string) *models.ClassificationResult {
	result := &models.ClassificationResult{
		Type:   models.DocTypeUnknown,
		Scores: make(map[models.DocType]float64),
	}

	bestType := models.DocTypeUnknown
	bestCount := 0
	bestRuleHit := ""

	for _, dt := range c.cfg.DocTypes {
		count := 0
		weightedCount := 0.0
		matchedKeywords := make([]string, 0)
		minMatches := dt.MinMatches
		if minMatches <= 0 {
			minMatches = 1
		}

		highValueKeywords := map[string]bool{}
		for _, hv := range getHighValueKeywords(dt.Type) {
			highValueKeywords[strings.ToLower(hv)] = true
		}

		for _, kw := range dt.Keywords {
			lowerKw := strings.ToLower(kw)
			n := strings.Count(text, lowerKw)
			if n > 0 {
				weight := 1.0
				if highValueKeywords[lowerKw] {
					weight = 2.5
				} else if len(lowerKw) >= 4 {
					weight = 1.5
				}
				count += n
				weightedCount += float64(n) * weight
				matchedKeywords = append(matchedKeywords, kw)
			}
		}

		docType := models.DocType(dt.Type)
		if count >= minMatches {
			maxPossible := 0.0
			for _, kw := range dt.Keywords {
				lowerKw := strings.ToLower(kw)
				w := 1.0
				if highValueKeywords[lowerKw] {
					w = 2.5
				} else if len(lowerKw) >= 4 {
					w = 1.5
				}
				maxPossible += w
			}
			score := weightedCount / math.Max(maxPossible, 1)
			if score > 1 {
				score = 1.0
			}
			uniqueRatio := float64(len(matchedKeywords)) / float64(minMatches)
			if uniqueRatio > 1 {
				uniqueRatio = 1
			}
			score = 0.6*score + 0.4*uniqueRatio
			if score > 1 {
				score = 1.0
			}
			result.Scores[docType] = score
			if count > bestCount {
				bestCount = count
				bestType = docType
				bestRuleHit = strings.Join(matchedKeywords, ",")
			}
		}
	}

	if bestType != models.DocTypeUnknown {
		confidence := result.Scores[bestType]
		if confidence < 0.5 && bestCount >= 3 {
			confidence = math.Max(confidence, 0.7)
		}
		if confidence > 0.9 {
			confidence = 0.95
		}
		result.Type = bestType
		result.Confidence = confidence
		result.Method = "rule"
		result.RuleHit = bestRuleHit
	}

	return result
}

func getHighValueKeywords(docType string) []string {
	switch docType {
	case "resume":
		return []string{"教育经历", "工作经验", "工作经历", "Education", "Experience", "Skills", "技能", "求职意向"}
	case "invoice":
		return []string{"发票", "价税合计", "纳税人识别号", "invoice", "VAT", "Tax Invoice"}
	case "contract":
		return []string{"合同", "甲方", "乙方", "合同编号", "违约责任", "Contract", "Party A", "Party B", "Agreement"}
	case "report":
		return []string{"检测报告", "检测项", "标准值", "实测值", "判定", "Test Report", "Inspection"}
	case "notice":
		return []string{"通知", "公告", "发文机关", "文号", "签发", "Notice", "Announcement", "Circular"}
	default:
		return nil
	}
}

func (c *Classifier) mlClassify(text string) *models.ClassificationResult {
	result := &models.ClassificationResult{
		Type:   models.DocTypeUnknown,
		Scores: make(map[models.DocType]float64),
	}

	c.mu.RLock()
	nb := c.naiveBayes
	totalDocs := nb.TotalDocs
	c.mu.RUnlock()

	if totalDocs < c.cfg.Classifier.MinTrainSamples*len(c.cfg.DocTypes) {
		return result
	}

	tokens := Tokenize(text)
	if len(tokens) == 0 {
		return result
	}

	c.mu.RLock()
	vocabSize := len(nb.Vocabulary)
	bestClass := ""
	bestLogProb := math.Inf(-1)

	for _, dtCfg := range c.cfg.DocTypes {
		clsName := dtCfg.Type
		docCount := nb.DocCounts[clsName]
		if docCount == 0 {
			continue
		}

		logProb := math.Log(float64(docCount) / float64(totalDocs))
		classTotalWords := nb.TotalWords[clsName]
		classWordCounts := nb.WordCounts[clsName]

		for _, token := range tokens {
			if !nb.Vocabulary[token] {
				continue
			}
			count := 0
			if classWordCounts != nil {
				count = classWordCounts[token]
			}
			logProb += math.Log(float64(count+1) / float64(classTotalWords+vocabSize))
		}

		docType := models.DocType(clsName)
		result.Scores[docType] = math.Exp(logProb)
		if logProb > bestLogProb {
			bestLogProb = logProb
			bestClass = clsName
		}
	}
	c.mu.RUnlock()

	if bestClass != "" {
		scores := make([]float64, 0, len(result.Scores))
		for _, s := range result.Scores {
			scores = append(scores, s)
		}
		sort.Float64s(scores)
		sum := 0.0
		for _, s := range scores {
			sum += s
		}

		confidence := 0.0
		if sum > 0 {
			confidence = result.Scores[models.DocType(bestClass)] / sum
		}
		if len(scores) >= 2 {
			second := scores[len(scores)-2]
			first := scores[len(scores)-1]
			if first > second && second > 0 {
				margin := (first - second) / second
				confidence = confidence * (1 + math.Min(margin, 1)*0.3)
			}
		}

		if confidence > c.cfg.Classifier.Threshold {
			if confidence > 1 {
				confidence = 1.0
			}
			result.Type = models.DocType(bestClass)
			result.Confidence = confidence
			result.Method = "tfidf_naive_bayes"
		}
	}

	return result
}

func (c *Classifier) Train(samples map[string][]string) error {
	if len(samples) == 0 {
		return nil
	}

	for _, texts := range samples {
		if len(texts) < c.cfg.Classifier.MinTrainSamples {
			continue
		}
	}

	c.mu.Lock()
	nb := NewNaiveBayes()

	totalSamples := 0
	for className, texts := range samples {
		nb.DocCounts[className] = len(texts)
		nb.WordCounts[className] = make(map[string]int)
		nb.TotalWords[className] = 0
		totalSamples += len(texts)

		for _, text := range texts {
			tokens := Tokenize(text)
			seen := make(map[string]bool)
			for _, t := range tokens {
				nb.Vocabulary[t] = true
				nb.WordCounts[className][t]++
				nb.TotalWords[className]++
				seen[t] = true
			}
		}
	}
	nb.TotalDocs = totalSamples
	c.naiveBayes = nb
	c.mu.Unlock()

	if c.store != nil {
		total := 0
		for _, n := range nb.DocCounts {
			total += n
		}
		return c.store.SaveClassifier("all", nb, total)
	}
	return nil
}

var (
	wordRe = regexp.MustCompile(`[\p{Han}]+|[a-zA-Z0-9_]+`)
)

func Tokenize(text string) []string {
	text = strings.ToLower(text)
	matches := wordRe.FindAllString(text, -1)
	if matches == nil {
		return nil
	}

	chineseTokens := make([]string, 0)
	for _, m := range matches {
		if isChinese(m) {
			if len(m) >= 2 {
				chineseTokens = append(chineseTokens, m)
			}
			if len(m) >= 2 {
				for i := 0; i < len(m)-1; i += 3 {
					end := i + 6
					if end > len(m) {
						end = len(m)
					}
					if end-i >= 6 {
						chineseTokens = append(chineseTokens, m[i:end])
					}
				}
			}
		} else {
			if len(m) >= 2 {
				chineseTokens = append(chineseTokens, m)
			}
		}
	}

	return chineseTokens
}

func isChinese(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

func GetAllDocTypes() []models.DocType {
	return []models.DocType{
		models.DocTypeInvoice,
		models.DocTypeContract,
		models.DocTypeResume,
		models.DocTypeReport,
		models.DocTypeNotice,
	}
}

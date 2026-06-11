package validator

import (
	"pdf-archive/internal/config"
	"pdf-archive/models"
)

type Validator struct {
	cfg *config.Config
}

func New(cfg *config.Config) *Validator {
	return &Validator{cfg: cfg}
}

func (v *Validator) Validate(fields map[string]models.ExtractedField, typeCfg *config.DocTypeConfig) *models.ValidationReport {
	report := &models.ValidationReport{
		FieldDetails: make(map[string]models.FieldValidation),
	}

	if typeCfg == nil {
		return report
	}

	requiredFields := make(map[string]bool)
	for _, rule := range typeCfg.ExtractRules {
		if rule.Required {
			requiredFields[rule.FieldName] = true
		}
	}

	totalConfidence := 0.0
	fieldCount := 0

	for fieldName, field := range fields {
		report.TotalFields++
		fieldCount++

		fv := models.FieldValidation{
			FieldName:  fieldName,
			Confidence: field.Confidence,
		}

		isRequired := requiredFields[fieldName]

		if field.Value == nil {
			report.FailedCount++
			fv.Success = false
			if field.Error != "" {
				fv.Message = field.Error
			} else {
				fv.Message = "value is null"
			}
			if isRequired {
				report.NeedReviewCount++
				fv.NeedReview = true
			}
		} else {
			if field.Confidence >= v.cfg.Pipeline.LowConfThreshold {
				report.SuccessCount++
				fv.Success = true
			} else {
				report.LowConfCount++
				fv.Success = true
				fv.NeedReview = true
				fv.Message = "low confidence"
				report.NeedReviewCount++
			}
			if field.NeedReview {
				if !fv.NeedReview {
					fv.NeedReview = true
					report.NeedReviewCount++
				}
			}
			totalConfidence += field.Confidence
		}

		report.FieldDetails[fieldName] = fv
	}

	if fieldCount > 0 {
		report.OverallScore = totalConfidence / float64(fieldCount)
	} else {
		report.OverallScore = 0
	}

	return report
}

func (v *Validator) NeedReview(report *models.ValidationReport) bool {
	if report == nil {
		return false
	}
	return report.NeedReviewCount > 0 || report.FailedCount > 0
}

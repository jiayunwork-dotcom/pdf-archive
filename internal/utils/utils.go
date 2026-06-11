package utils

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func FileMD5(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func FileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func IsPDF(path string) bool {
	return strings.ToLower(filepath.Ext(path)) == ".pdf"
}

func FindPDFs(root string, recursive bool) ([]string, error) {
	var files []string
	if recursive {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() && IsPDF(path) {
				files = append(files, path)
			}
			return nil
		})
		return files, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() && IsPDF(filepath.Join(root, e.Name())) {
			files = append(files, filepath.Join(root, e.Name()))
		}
	}
	return files, nil
}

func EnsureDir(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return os.MkdirAll(path, 0755)
	}
	return nil
}

func SanitizeFilename(name string) string {
	reg := regexp.MustCompile(`[\\/:*?"<>|]+`)
	name = reg.ReplaceAllString(name, "_")
	name = strings.TrimSpace(name)
	if len(name) > 200 {
		name = name[:200]
	}
	if name == "" {
		name = "untitled"
	}
	return name
}

func ResolveConflict(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for i := 1; i < 10000; i++ {
		candidate := fmt.Sprintf("%s_%03d%s", base, i, ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	return path
}

func NormalizeDate(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}

	formats := []string{
		"2006-01-02", "2006/01/02", "2006.01.02",
		"2006年01月02日", "2006年1月2日",
		"2006年01月", "2006年1月",
		"2006-01", "2006/01",
		"02/01/2006", "01/02/2006",
	}

	for _, f := range formats {
		if t, err := time.Parse(f, input); err == nil {
			return t.Format("2006-01")
		}
	}

	yearRe := regexp.MustCompile(`(\d{4})`)
	monthRe := regexp.MustCompile(`(\d{1,2})\s*月`)
	yearMatch := yearRe.FindStringSubmatch(input)
	monthMatch := monthRe.FindStringSubmatch(input)
	if yearMatch != nil {
		year := yearMatch[1]
		month := "01"
		if monthMatch != nil {
			m, _ := strconv.Atoi(monthMatch[1])
			month = fmt.Sprintf("%02d", m)
		}
		return fmt.Sprintf("%s-%s", year, month)
	}

	return ""
}

func ParseAmount(input string) (float64, error) {
	input = strings.TrimSpace(input)
	input = strings.ReplaceAll(input, "¥", "")
	input = strings.ReplaceAll(input, "￥", "")
	input = strings.ReplaceAll(input, "$", "")
	input = strings.ReplaceAll(input, ",", "")
	input = strings.ReplaceAll(input, " ", "")
	re := regexp.MustCompile(`-?\d+(\.\d+)?`)
	match := re.FindString(input)
	if match == "" {
		return 0, fmt.Errorf("no amount found in: %s", input)
	}
	return strconv.ParseFloat(match, 64)
}

func CleanString(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\u3000", " ")
	repl := strings.NewReplacer(
		"\r\n", " ",
		"\r", " ",
		"\n", " ",
		"\t", " ",
	)
	s = repl.Replace(s)
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return strings.TrimSpace(s)
}

func ExtractYearMonth(dateStr string) (string, string) {
	norm := NormalizeDate(dateStr)
	if norm == "" {
		return "", ""
	}
	parts := strings.Split(norm, "-")
	if len(parts) >= 2 {
		return parts[0], parts[1]
	}
	return "", ""
}

func ParsePDFDate(s string) time.Time {
	if len(s) < 2 {
		return time.Time{}
	}
	s = strings.TrimPrefix(s, "D:")
	if len(s) >= 14 {
		year, _ := strconv.Atoi(s[0:4])
		month, _ := strconv.Atoi(s[4:6])
		day, _ := strconv.Atoi(s[6:8])
		hour, _ := strconv.Atoi(s[8:10])
		min, _ := strconv.Atoi(s[10:12])
		sec, _ := strconv.Atoi(s[12:14])
		if month < 1 || month > 12 {
			month = 1
		}
		if day < 1 || day > 31 {
			day = 1
		}
		return time.Date(year, time.Month(month), day, hour, min, sec, 0, time.Local)
	}
	return time.Time{}
}

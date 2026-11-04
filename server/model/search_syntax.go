package model

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	searchMaxKeywordRunes = 200
	searchMaxTagRunes     = 50
	searchMaxFolderRunes  = 32
)

// searchSyntax 是搜索框的解析结果：冒号前缀 token 作为过滤条件，其余为全文关键词。
// 语法：tag:标签、folder:文件夹、sort:updated|created|title、since:YYYY-MM-DD、until:YYYY-MM-DD。
// 无法识别的 token 原样回退为关键词，不报错也不静默丢弃。
type searchSyntax struct {
	Keyword string
	Tags    []string
	Folder  string
	Since   string
	Until   string
	SortBy  string
}

func parseSearchSyntax(raw string) searchSyntax {
	var out searchSyntax
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return out
	}
	if utf8.RuneCountInString(raw) > searchMaxKeywordRunes {
		raw = string([]rune(raw)[:searchMaxKeywordRunes])
	}

	seen := make(map[string]bool)
	var keyword []string
	for _, token := range tokenizeSearch(raw) {
		key, val, ok := splitSearchToken(token)
		if !ok {
			keyword = append(keyword, token)
			continue
		}
		switch key {
		case "tag":
			val = strings.ReplaceAll(val, " ", "")
			if val == "" || utf8.RuneCountInString(val) > searchMaxTagRunes {
				keyword = append(keyword, token)
				continue
			}
			if !seen[val] {
				seen[val] = true
				out.Tags = append(out.Tags, val)
			}
		case "folder":
			if val == "" || utf8.RuneCountInString(val) > searchMaxFolderRunes {
				keyword = append(keyword, token)
				continue
			}
			out.Folder = val
		case "sort":
			if canonical := canonicalSearchSort(val); canonical != "" {
				out.SortBy = canonical
			} else {
				keyword = append(keyword, token)
			}
		case "since", "until":
			if !validSearchDate(val) {
				keyword = append(keyword, token)
				continue
			}
			if key == "since" {
				out.Since = val
			} else {
				out.Until = val
			}
		default:
			keyword = append(keyword, token)
		}
	}
	out.Keyword = strings.TrimSpace(strings.Join(keyword, " "))
	if utf8.RuneCountInString(out.Keyword) > searchMaxKeywordRunes {
		out.Keyword = string([]rune(out.Keyword)[:searchMaxKeywordRunes])
	}
	return out
}

// tokenizeSearch 按空白切分，双引号内的空白不切（支持 folder:"项目 资料"）。
func tokenizeSearch(raw string) []string {
	var tokens []string
	var b strings.Builder
	inQuote := false
	flush := func() {
		if b.Len() > 0 {
			tokens = append(tokens, b.String())
			b.Reset()
		}
	}
	for _, r := range raw {
		switch {
		case r == '"':
			inQuote = !inQuote
			b.WriteRune(r)
		case unicode.IsSpace(r) && !inQuote:
			flush()
		default:
			b.WriteRune(r)
		}
	}
	flush()
	return tokens
}

func splitSearchToken(token string) (key, val string, ok bool) {
	i := strings.Index(token, ":")
	if i <= 0 {
		return "", "", false
	}
	key = strings.ToLower(token[:i])
	val = strings.Trim(strings.TrimSpace(token[i+1:]), `"`)
	if val == "" {
		return "", "", false
	}
	return key, val, true
}

func canonicalSearchSort(val string) string {
	switch strings.ToLower(val) {
	case "updated", "updated_at", "modified":
		return "updated_at"
	case "created", "created_at":
		return "created_at"
	case "title", "name":
		return "title"
	}
	return ""
}

func validSearchDate(val string) bool {
	_, err := time.Parse("2006-01-02", val)
	return err == nil
}

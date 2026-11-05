package search

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kailas-cloud/vecdex/internal/domain/search/result"
)

// Keep the ranked IDs and metadata even when the text budget is small. Share
// remaining bytes across remaining hits so one long source cannot starve all
// later excerpts. Truncation never splits a UTF-8 rune.
func limitSnippets(results []result.Result, budget int, query string) []result.Result {
	for i := range results {
		allowance := budget / (len(results) - i)
		text := results[i].Content()
		if len(text) > allowance {
			text = matchingExcerpt(text, query, allowance)
		}
		budget -= len(text)
		results[i] = results[i].WithSnippet(text)
	}
	return results
}

func matchingExcerpt(text, query string, budget int) string {
	if budget == 0 {
		return ""
	}
	start := 0
	if matchStart, matchEnd := snippetMatch(text, query); matchStart >= 0 {
		left := min(budget/4, max(0, budget-(matchEnd-matchStart)))
		start = max(0, matchStart-left)
		if start+budget > len(text) {
			start = max(0, len(text)-budget)
		}
	}
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	end := min(len(text), start+budget)
	for end > start && end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[start:end]
}

// Prefer the complete phrase; otherwise locate the longest matching term.
// Bound term scans independently of query size. Lowercasing can change byte
// lengths (e.g. İ → i), so translate rune offsets back into the original text.
func snippetMatch(text, query string) (start, end int) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return -1, -1
	}
	lower := strings.ToLower(text)
	position, term := strings.Index(lower, query), query
	if position < 0 {
		terms := strings.FieldsFunc(query, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
		})
		term = ""
		for _, candidate := range terms[:min(len(terms), 16)] {
			if len(candidate) <= len(term) {
				continue
			}
			if found := strings.Index(lower, candidate); found >= 0 {
				position, term = found, candidate
			}
		}
	}
	if position < 0 {
		return -1, -1
	}
	startRune := utf8.RuneCountInString(lower[:position])
	endRune := startRune + utf8.RuneCountInString(term)
	start, end, count := 0, len(text), 0
	for offset := range text {
		if count == startRune {
			start = offset
		}
		if count == endRune {
			end = offset
			break
		}
		count++
	}
	return start, end
}

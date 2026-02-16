package redis

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/redis/rueidis"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain/search/filter"
)

// SearchKNN runs a KNN vector similarity search via FT.SEARCH.
func (s *Store) SearchKNN(ctx context.Context, q *db.KNNQuery) (*db.SearchResult, error) {
	if q.IndexName == "" {
		return nil, fmt.Errorf("index name is required")
	}
	if len(q.Vector) == 0 {
		return nil, fmt.Errorf("vector is required")
	}
	if q.K <= 0 {
		return nil, fmt.Errorf("k must be positive")
	}

	filterStr := buildFilter(q.Filters)

	knnPart := fmt.Sprintf("[KNN %d @vector $BLOB]", q.K)
	var queryStr string
	if filterStr != "" {
		queryStr = fmt.Sprintf("(%s)=>%s", filterStr, knnPart)
	} else {
		queryStr = fmt.Sprintf("*=>%s", knnPart)
	}

	args := []string{q.IndexName, queryStr}

	if len(q.ReturnFields) > 0 {
		args = append(args, "RETURN", strconv.Itoa(len(q.ReturnFields)))
		args = append(args, q.ReturnFields...)
	}

	args = append(args, "PARAMS", "2", "BLOB", vectorToBytes(q.Vector), "DIALECT", "2")

	cmd := s.b().Arbitrary("FT.SEARCH").Args(args...).Build()
	raw, err := s.do(ctx, cmd).ToArray()
	if err != nil {
		return nil, &db.Error{Op: db.OpSearch, Err: err}
	}

	return parseKNNResult(raw, q.RawScores)
}

// SearchBM25 runs a BM25 text search via FT.SEARCH.
func (s *Store) SearchBM25(ctx context.Context, q *db.TextQuery) (*db.SearchResult, error) {
	if q.IndexName == "" {
		return nil, fmt.Errorf("index name is required")
	}
	if q.Query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if q.TopK <= 0 {
		return nil, fmt.Errorf("topK must be positive")
	}

	filterStr := buildFilter(q.Filters)

	escaped := escapeQuery(q.Query)
	textPart := fmt.Sprintf("@__content:(%s)", escaped)

	var queryStr string
	if filterStr != "" {
		queryStr = fmt.Sprintf("%s %s", filterStr, textPart)
	} else {
		queryStr = textPart
	}

	args := []string{q.IndexName, queryStr}

	if len(q.ReturnFields) > 0 {
		args = append(args, "RETURN", strconv.Itoa(len(q.ReturnFields)))
		args = append(args, q.ReturnFields...)
	}

	args = append(args,
		"WITHSCORES",
		"LIMIT", "0", strconv.Itoa(q.TopK),
		"DIALECT", "2",
	)

	cmd := s.b().Arbitrary("FT.SEARCH").Args(args...).Build()
	raw, err := s.do(ctx, cmd).ToArray()
	if err != nil {
		return nil, &db.Error{Op: db.OpSearch, Err: err}
	}

	return parseBM25Result(raw)
}

// SearchList performs paginated search via FT.SEARCH.
func (s *Store) SearchList(
	ctx context.Context, index, query string, offset, limit int, fields []string,
) (*db.SearchResult, error) {
	args := []string{index, query, "LIMIT", strconv.Itoa(offset), strconv.Itoa(limit)}

	if len(fields) > 0 {
		args = append(args, "RETURN", strconv.Itoa(len(fields)))
		args = append(args, fields...)
	}

	cmd := s.b().Arbitrary("FT.SEARCH").Args(args...).Build()
	raw, err := s.do(ctx, cmd).ToArray()
	if err != nil {
		return nil, &db.Error{Op: db.OpSearch, Err: err}
	}

	return parseListResult(raw)
}

// SearchCount returns document count via FT.SEARCH with LIMIT 0 0.
func (s *Store) SearchCount(ctx context.Context, index, query string) (int, error) {
	cmd := s.b().Arbitrary("FT.SEARCH").Args(index, query, "LIMIT", "0", "0").Build()
	raw, err := s.do(ctx, cmd).ToArray()
	if err != nil {
		return 0, &db.Error{Op: db.OpSearch, Err: err}
	}
	if len(raw) == 0 {
		return 0, nil
	}
	total, err := raw[0].AsInt64()
	if err != nil {
		return 0, fmt.Errorf("parse count: %w", err)
	}
	return int(total), nil
}

// --- Result parsing ---

func parseKNNResult(raw []rueidis.RedisMessage, rawScores bool) (*db.SearchResult, error) {
	if len(raw) == 0 {
		return &db.SearchResult{}, nil
	}

	total, err := raw[0].AsInt64()
	if err != nil {
		return nil, fmt.Errorf("parse total: %w", err)
	}
	if total == 0 {
		return &db.SearchResult{}, nil
	}

	entries := make([]db.SearchEntry, 0, total)
	// 2-stride: [total, key1, fields1, key2, fields2, ...]
	for i := 1; i+1 < len(raw); i += 2 {
		key, err := raw[i].ToString()
		if err != nil {
			continue
		}

		fields, err := raw[i+1].ToArray()
		if err != nil {
			continue
		}

		entry := db.SearchEntry{
			Key:    key,
			Fields: parseFieldPairs(fields),
		}

		if scoreStr, ok := entry.Fields["__vector_score"]; ok {
			if s, err := strconv.ParseFloat(scoreStr, 64); err == nil {
				if rawScores {
					entry.Score = s
				} else {
					entry.Score = max(0, 1.0-s) // cosine distance → similarity, clamped to [0,1]
				}
			}
			delete(entry.Fields, "__vector_score")
		}

		entries = append(entries, entry)
	}

	return &db.SearchResult{Total: int(total), Entries: entries}, nil
}

func parseBM25Result(raw []rueidis.RedisMessage) (*db.SearchResult, error) {
	if len(raw) == 0 {
		return &db.SearchResult{}, nil
	}

	total, err := raw[0].AsInt64()
	if err != nil {
		return nil, fmt.Errorf("parse total: %w", err)
	}
	if total == 0 {
		return &db.SearchResult{}, nil
	}

	entries := make([]db.SearchEntry, 0, total)
	// 3-stride: [total, key1, score1, fields1, key2, score2, fields2, ...]
	for i := 1; i+2 < len(raw); i += 3 {
		key, err := raw[i].ToString()
		if err != nil {
			continue
		}

		scoreStr, err := raw[i+1].ToString()
		if err != nil {
			continue
		}
		score, err := strconv.ParseFloat(scoreStr, 64)
		if err != nil {
			continue
		}

		fields, err := raw[i+2].ToArray()
		if err != nil {
			continue
		}

		entries = append(entries, db.SearchEntry{
			Key:    key,
			Score:  score,
			Fields: parseFieldPairs(fields),
		})
	}

	return &db.SearchResult{Total: int(total), Entries: entries}, nil
}

func parseListResult(raw []rueidis.RedisMessage) (*db.SearchResult, error) {
	if len(raw) == 0 {
		return &db.SearchResult{}, nil
	}

	total, err := raw[0].AsInt64()
	if err != nil {
		return nil, fmt.Errorf("parse total: %w", err)
	}
	if total == 0 {
		return &db.SearchResult{}, nil
	}

	entries := make([]db.SearchEntry, 0, total)
	// 2-stride: [total, key1, fields1, key2, fields2, ...]
	for i := 1; i+1 < len(raw); i += 2 {
		key, err := raw[i].ToString()
		if err != nil {
			continue
		}

		fields, err := raw[i+1].ToArray()
		if err != nil {
			continue
		}

		entries = append(entries, db.SearchEntry{
			Key:    key,
			Fields: parseFieldPairs(fields),
		})
	}

	return &db.SearchResult{Total: int(total), Entries: entries}, nil
}

func parseFieldPairs(fields []rueidis.RedisMessage) map[string]string {
	m := make(map[string]string, len(fields)/2)
	for j := 0; j+1 < len(fields); j += 2 {
		name, err := fields[j].ToString()
		if err != nil {
			continue
		}
		value, err := fields[j+1].ToString()
		if err != nil {
			continue
		}
		m[name] = value
	}
	return m
}

// --- Filter building ---

// buildFilter translates filter.Expression into an FT.SEARCH pre-filter query string.
func buildFilter(expr filter.Expression) string {
	if expr.IsEmpty() {
		return ""
	}

	var parts []string

	for _, cond := range expr.Must() {
		parts = append(parts, buildCondition(cond))
	}

	if shouldParts := buildShouldGroup(expr.Should()); shouldParts != "" {
		parts = append(parts, shouldParts)
	}

	for _, cond := range expr.MustNot() {
		parts = append(parts, "-"+buildCondition(cond))
	}

	return strings.Join(parts, " ")
}

func buildCondition(cond filter.Condition) string {
	if cond.IsMatch() {
		return buildTagFilter(cond.Key(), cond.Match())
	}
	if cond.IsRange() {
		return buildNumericFilter(cond.Key(), *cond.Range())
	}
	return ""
}

func buildShouldGroup(conditions []filter.Condition) string {
	if len(conditions) == 0 {
		return ""
	}
	parts := make([]string, 0, len(conditions))
	for _, cond := range conditions {
		parts = append(parts, buildCondition(cond))
	}
	return "(" + strings.Join(parts, " | ") + ")"
}

func buildTagFilter(key, value string) string {
	escaped := tagEscaper.Replace(value)
	return fmt.Sprintf("@%s:{%s}", key, escaped)
}

func buildNumericFilter(key string, r filter.Range) string {
	minBound := "-inf"
	maxBound := "+inf"

	if r.GT() != nil {
		minBound = fmt.Sprintf("(%g", *r.GT())
	} else if r.GTE() != nil {
		minBound = fmt.Sprintf("%g", *r.GTE())
	}

	if r.LT() != nil {
		maxBound = fmt.Sprintf("(%g", *r.LT())
	} else if r.LTE() != nil {
		maxBound = fmt.Sprintf("%g", *r.LTE())
	}

	return fmt.Sprintf("@%s:[%s %s]", key, minBound, maxBound)
}

// --- Query helpers ---

var tagEscaper = strings.NewReplacer(
	",", "\\,",
	".", "\\.",
	"<", "\\<",
	">", "\\>",
	"{", "\\{",
	"}", "\\}",
	"\"", "\\\"",
	"'", "\\'",
	":", "\\:",
	";", "\\;",
	"!", "\\!",
	"@", "\\@",
	"#", "\\#",
	"$", "\\$",
	"%", "\\%",
	"^", "\\^",
	"&", "\\&",
	"*", "\\*",
	"(", "\\(",
	")", "\\)",
	"-", "\\-",
	"+", "\\+",
	"=", "\\=",
	"~", "\\~",
	" ", "\\ ",
)

func escapeQuery(s string) string {
	return queryEscaper.Replace(s)
}

var queryEscaper = strings.NewReplacer(
	`\`, `\\`,
	`'`, `\'`,
	`"`, `\"`,
	`@`, `\@`,
	`{`, `\{`,
	`}`, `\}`,
	`(`, `\(`,
	`)`, `\)`,
	`|`, `\|`,
	`-`, `\-`,
	`~`, `\~`,
	`*`, `\*`,
	`[`, `\[`,
	`]`, `\]`,
	`!`, `\!`,
	`%`, `\%`,
	`^`, `\^`,
	`$`, `\$`,
	`<`, `\<`,
	`>`, `\>`,
	`=`, `\=`,
	`;`, `\;`,
	`+`, `\+`,
)

func vectorToBytes(v []float32) string {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return string(buf)
}

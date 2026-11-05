package valkey

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

	filterStr := withExpiryFilter(buildFilter(q.Filters), q.NotExpiredAt)
	if q.ExcludeParentID != "" {
		filterStr = strings.TrimSpace(filterStr + " -" + buildTagFilter("__parent_id", q.ExcludeParentID))
	}

	knnPart := fmt.Sprintf("[KNN %d @vector $BLOB]", q.K)
	var queryStr string
	if filterStr != "" {
		queryStr = fmt.Sprintf("(%s)=>%s", filterStr, knnPart)
	} else {
		queryStr = fmt.Sprintf("*=>%s", knnPart)
	}

	// A successful search must cover every shard with one index/topology view.
	args := []string{q.IndexName, queryStr, "ALLSHARDS", "CONSISTENT"}

	if len(q.ReturnFields) > 0 {
		args = append(args, "RETURN", strconv.Itoa(len(q.ReturnFields)))
		args = append(args, q.ReturnFields...)
	}

	// K controls candidate ranking, while LIMIT controls response cardinality.
	// Omitting LIMIT silently truncates FT.SEARCH to its ten-row default.
	args = append(args, "LIMIT", "0", strconv.Itoa(q.K),
		"PARAMS", "2", "BLOB", vectorToBytes(q.Vector), "DIALECT", "2")

	cmd := s.searchCommand(args)
	raw, err := s.do(ctx, cmd).ToArray()
	if err != nil {
		return nil, &db.Error{Op: db.OpSearch, Err: err}
	}

	return parseKNNResult(raw)
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

	filterStr := withExpiryFilter(buildFilter(q.Filters), q.NotExpiredAt)

	escaped := escapeQuery(q.Query)
	textPart := escaped

	var queryStr string
	if filterStr != "" {
		queryStr = fmt.Sprintf("%s %s", filterStr, textPart)
	} else {
		queryStr = textPart
	}

	// A successful search must cover every shard with one index/topology view.
	args := []string{q.IndexName, queryStr, "ALLSHARDS", "CONSISTENT"}

	if len(q.ReturnFields) > 0 {
		args = append(args, "RETURN", strconv.Itoa(len(q.ReturnFields)))
		args = append(args, q.ReturnFields...)
	}
	args = append(args, "LIMIT", "0", strconv.Itoa(q.TopK), "DIALECT", "2")

	cmd := s.searchCommand(args)
	raw, err := s.do(ctx, cmd).ToArray()
	if err != nil {
		return nil, &db.Error{Op: db.OpSearch, Err: err}
	}

	return parseBM25Result(raw)
}

func withExpiryFilter(filters string, now int64) string {
	if now <= 0 {
		return filters
	}
	expiry := fmt.Sprintf("(@__expires_at:[0 0] | @__expires_at:[(%d +inf])", now)
	return strings.TrimSpace(filters + " " + expiry)
}

// A routing slot selects a primary coordinator without creating a data key.
// Rotating it avoids pinning a collection's entire query load to its index-name
// slot. The actual index argument and distributed consistency checks are intact.
func (s *Store) searchCommand(args []string) rueidis.Completed {
	route := "vecdex:query-route:" + strconv.FormatUint(s.queryRoute.Add(1), 10)
	return s.b().Arbitrary("FT.SEARCH").Args(args...).ReadOnly().SetSlot(route)
}

// --- Result parsing ---

func parseKNNResult(raw []rueidis.RedisMessage) (*db.SearchResult, error) {
	result, err := parseSearchResult(raw)
	if err != nil {
		return nil, err
	}
	for i := range result.Entries {
		entry := &result.Entries[i]
		if value, ok := entry.Fields["__vector_score"]; ok {
			distance, err := strconv.ParseFloat(value, 64)
			if err != nil || math.IsNaN(distance) || math.IsInf(distance, 0) {
				return nil, fmt.Errorf("invalid vector distance for %q", entry.Key)
			}
			// FLOAT32 cosine dot products can round slightly above one.
			entry.Score = min(1, max(0, 1.0-distance))
			delete(entry.Fields, "__vector_score")
		}
	}
	return result, nil
}

func parseBM25Result(raw []rueidis.RedisMessage) (*db.SearchResult, error) {
	result, err := parseSearchResult(raw)
	if err != nil {
		return nil, err
	}
	for i := range result.Entries {
		result.Entries[i].Score = rankScore(i, result.Total)
	}
	return result, nil
}

// Reject malformed rows as a whole. Skipping a key or field would turn a
// protocol error into a successful, silently incomplete search response.
func parseSearchResult(raw []rueidis.RedisMessage) (*db.SearchResult, error) {
	if len(raw) == 0 || len(raw)%2 != 1 {
		return nil, fmt.Errorf("invalid search response shape")
	}
	total, err := raw[0].AsInt64()
	rows := (len(raw) - 1) / 2
	if err != nil || total < 0 || total < int64(rows) || (total > 0 && rows == 0) {
		return nil, fmt.Errorf("invalid search response total")
	}
	entries := make([]db.SearchEntry, 0, rows)
	for i := 1; i < len(raw); i += 2 {
		key, err := raw[i].ToString()
		if err != nil || key == "" {
			return nil, fmt.Errorf("invalid search key at row %d", (i-1)/2)
		}
		fields, err := raw[i+1].ToArray()
		if err != nil {
			return nil, fmt.Errorf("invalid search fields for %q: %w", key, err)
		}
		values, err := parseFieldPairs(fields)
		if err != nil {
			return nil, fmt.Errorf("invalid search fields for %q: %w", key, err)
		}
		entries = append(entries, db.SearchEntry{Key: key, Fields: values})
	}
	return &db.SearchResult{Total: int(total), Entries: entries}, nil
}

func rankScore(rank, total int) float64 {
	if total <= 0 {
		return 0
	}
	score := float64(total-rank) / float64(total)
	if score <= 0 {
		return 1.0 / float64(rank+1)
	}
	return score
}

func parseFieldPairs(fields []rueidis.RedisMessage) (map[string]string, error) {
	if len(fields)%2 != 0 {
		return nil, fmt.Errorf("unpaired field name")
	}
	values := make(map[string]string, len(fields)/2)
	for i := 0; i < len(fields); i += 2 {
		if !fields[i].IsString() || !fields[i+1].IsString() {
			return nil, fmt.Errorf("field names and values must be strings")
		}
		name, err := fields[i].ToString()
		if err != nil {
			return nil, fmt.Errorf("invalid field name: %w", err)
		}
		value, err := fields[i+1].ToString()
		if err != nil {
			return nil, fmt.Errorf("invalid value for %q: %w", name, err)
		}
		values[name] = value
	}
	return values, nil
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
	`:`, `\:`,
	`,`, `\,`,
	`.`, `\.`,
	`/`, `\/`,
	`&`, `\&`,
	`#`, `\#`,
	`?`, `\?`,
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

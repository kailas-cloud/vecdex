package valkey

import (
	"context"
	"errors"
	"testing"

	"github.com/redis/rueidis"
	"github.com/redis/rueidis/mock"
	"go.uber.org/mock/gomock"

	"github.com/kailas-cloud/vecdex/internal/db"
	"github.com/kailas-cloud/vecdex/internal/domain/search/filter"
)

// --- client.go tests ---

func TestPing_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mock.NewClient(ctrl)

	c.EXPECT().
		Do(gomock.Any(), mock.Match("PING")).
		Return(mock.Result(mock.RedisString("PONG")))

	s := NewStoreForTest(c)
	if err := s.Ping(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPing_Error(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mock.NewClient(ctrl)

	c.EXPECT().
		Do(gomock.Any(), mock.Match("PING")).
		Return(mock.ErrorResult(context.DeadlineExceeded))

	s := NewStoreForTest(c)
	if err := s.Ping(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestContainsIgnoreCase(t *testing.T) {
	tests := []struct {
		s, sub string
		want   bool
	}{
		{"Index Already Exists", "index already exists", true},
		{"UNKNOWN INDEX NAME", "unknown index name", true},
		{"short", "longer than input", false},
		{"", "", true},
	}
	for _, tc := range tests {
		got := containsIgnoreCase(tc.s, tc.sub)
		if got != tc.want {
			t.Errorf("containsIgnoreCase(%q, %q) = %v, want %v", tc.s, tc.sub, got, tc.want)
		}
	}
}

// --- hash.go tests ---

func TestHSet_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mock.NewClient(ctrl)

	c.EXPECT().
		Do(gomock.Any(), mock.MatchFn(func(cmd []string) bool {
			return cmd[0] == "HSET" && cmd[1] == "mykey"
		})).
		Return(mock.Result(mock.RedisInt64(1)))

	s := NewStoreForTest(c)
	if err := s.HSet(context.Background(), "mykey", map[string]string{"f": "v"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHGetAll_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mock.NewClient(ctrl)

	c.EXPECT().
		Do(gomock.Any(), mock.Match("HGETALL", "mykey")).
		Return(mock.Result(mock.RedisMap(map[string]rueidis.RedisMessage{
			"f1": mock.RedisString("v1"),
		})))

	s := NewStoreForTest(c)
	m, err := s.HGetAll(context.Background(), "mykey")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m["f1"] != "v1" {
		t.Errorf("unexpected map: %v", m)
	}
}

func TestHSetMulti_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mock.NewClient(ctrl)

	c.EXPECT().
		DoMulti(gomock.Any(), gomock.Any()).
		Return([]rueidis.RedisResult{
			mock.Result(mock.RedisInt64(2)),
			mock.Result(mock.RedisInt64(2)),
		})

	s := NewStoreForTest(c)
	err := s.HSetMulti(context.Background(), []db.HashSetItem{
		{Key: "k1", Fields: map[string]string{"f1": "v1"}},
		{Key: "k2", Fields: map[string]string{"f2": "v2"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHSetMulti_Empty(t *testing.T) {
	s := NewStoreForTest(nil)
	if err := s.HSetMulti(context.Background(), nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHGetAllMulti_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mock.NewClient(ctrl)

	c.EXPECT().
		DoMulti(gomock.Any(), gomock.Any()).
		Return([]rueidis.RedisResult{
			mock.Result(mock.RedisMap(map[string]rueidis.RedisMessage{
				"f": mock.RedisString("a"),
			})),
		})

	s := NewStoreForTest(c)
	results, err := s.HGetAllMulti(context.Background(), []string{"k1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0]["f"] != "a" {
		t.Errorf("unexpected results: %v", results)
	}
}

func TestHGetAllMulti_Empty(t *testing.T) {
	s := NewStoreForTest(nil)
	results, err := s.HGetAllMulti(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results != nil {
		t.Errorf("expected nil, got %v", results)
	}
}

func TestDel_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mock.NewClient(ctrl)

	c.EXPECT().
		Do(gomock.Any(), mock.Match("DEL", "mykey")).
		Return(mock.Result(mock.RedisInt64(1)))

	s := NewStoreForTest(c)
	if err := s.Del(context.Background(), "mykey"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExists_True(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mock.NewClient(ctrl)

	c.EXPECT().
		Do(gomock.Any(), mock.Match("EXISTS", "mykey")).
		Return(mock.Result(mock.RedisInt64(1)))

	s := NewStoreForTest(c)
	exists, err := s.Exists(context.Background(), "mykey")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("expected true")
	}
}

func TestExists_False(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mock.NewClient(ctrl)

	c.EXPECT().
		Do(gomock.Any(), mock.Match("EXISTS", "mykey")).
		Return(mock.Result(mock.RedisInt64(0)))

	s := NewStoreForTest(c)
	exists, err := s.Exists(context.Background(), "mykey")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Error("expected false")
	}
}

func TestScan_SinglePage(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mock.NewClient(ctrl)

	c.EXPECT().
		Do(gomock.Any(), mock.MatchFn(func(cmd []string) bool {
			return cmd[0] == "SCAN"
		})).
		Return(mock.Result(mock.RedisArray(
			mock.RedisInt64(0),
			mock.RedisArray(mock.RedisString("key1"), mock.RedisString("key2")),
		)))

	s := NewStoreForTest(c)
	keys, err := s.Scan(context.Background(), "prefix:*")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
}

// --- kv.go tests ---

func TestGet_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mock.NewClient(ctrl)

	c.EXPECT().
		Do(gomock.Any(), mock.Match("GET", "mykey")).
		Return(mock.Result(mock.RedisBlobString("value")))

	s := NewStoreForTest(c)
	data, err := s.Get(context.Background(), "mykey")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "value" {
		t.Errorf("unexpected data: %s", data)
	}
}

func TestGet_NotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mock.NewClient(ctrl)

	c.EXPECT().
		Do(gomock.Any(), mock.Match("GET", "mykey")).
		Return(mock.Result(mock.RedisNil()))

	s := NewStoreForTest(c)
	_, err := s.Get(context.Background(), "mykey")
	if !errors.Is(err, db.ErrKeyNotFound) {
		t.Errorf("expected ErrKeyNotFound, got %v", err)
	}
}

func TestSet_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mock.NewClient(ctrl)

	c.EXPECT().
		Do(gomock.Any(), mock.Match("SET", "mykey", "myvalue")).
		Return(mock.Result(mock.RedisString("OK")))

	s := NewStoreForTest(c)
	if err := s.Set(context.Background(), "mykey", []byte("myvalue")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestIncrBy_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mock.NewClient(ctrl)

	c.EXPECT().
		Do(gomock.Any(), mock.Match("INCRBY", "counter", "5")).
		Return(mock.Result(mock.RedisInt64(5)))

	s := NewStoreForTest(c)
	if err := s.IncrBy(context.Background(), "counter", 5); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExpire_WithNX(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mock.NewClient(ctrl)

	c.EXPECT().
		Do(gomock.Any(), mock.MatchFn(func(cmd []string) bool {
			if cmd[0] != "EXPIRE" {
				return false
			}
			for _, arg := range cmd {
				if arg == "NX" {
					return true
				}
			}
			return false
		})).
		Return(mock.Result(mock.RedisInt64(1)))

	s := NewStoreForTest(c)
	if err := s.Expire(context.Background(), "mykey", 300*1e9, true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- index.go tests ---

func TestCreateIndex_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mock.NewClient(ctrl)

	c.EXPECT().
		Do(gomock.Any(), mock.MatchFn(func(cmd []string) bool {
			return cmd[0] == "FT.CREATE"
		})).
		Return(mock.Result(mock.RedisString("OK")))

	s := NewStoreForTest(c)
	idx := &db.IndexDefinition{
		Name:        "test:idx",
		StorageType: db.StorageJSON,
		Prefixes:    []string{"test:"},
		Fields: []db.IndexField{
			{Name: "$.field", Type: db.IndexFieldTag},
		},
	}
	if err := s.CreateIndex(context.Background(), idx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreateIndex_AlreadyExists(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mock.NewClient(ctrl)

	c.EXPECT().
		Do(gomock.Any(), mock.MatchFn(func(cmd []string) bool {
			return cmd[0] == "FT.CREATE"
		})).
		Return(mock.Result(mock.RedisError("Index already exists")))

	s := NewStoreForTest(c)
	idx := &db.IndexDefinition{
		Name:   "test:idx",
		Fields: []db.IndexField{{Name: "f", Type: db.IndexFieldTag}},
	}
	err := s.CreateIndex(context.Background(), idx)
	if !errors.Is(err, db.ErrIndexExists) {
		t.Errorf("expected ErrIndexExists, got %v", err)
	}
}

func TestDropIndex_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mock.NewClient(ctrl)

	c.EXPECT().
		Do(gomock.Any(), mock.Match("FT.DROPINDEX", "test:idx")).
		Return(mock.Result(mock.RedisString("OK")))

	s := NewStoreForTest(c)
	if err := s.DropIndex(context.Background(), "test:idx"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDropIndex_NotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mock.NewClient(ctrl)

	c.EXPECT().
		Do(gomock.Any(), mock.Match("FT.DROPINDEX", "test:idx")).
		Return(mock.Result(mock.RedisError("Unknown Index name")))

	s := NewStoreForTest(c)
	err := s.DropIndex(context.Background(), "test:idx")
	if !errors.Is(err, db.ErrIndexNotFound) {
		t.Errorf("expected ErrIndexNotFound, got %v", err)
	}
}

func TestIndexExists_True(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mock.NewClient(ctrl)

	c.EXPECT().
		Do(gomock.Any(), mock.Match("FT.INFO", "test:idx")).
		Return(mock.Result(mock.RedisArray(mock.RedisString("index_name"))))

	s := NewStoreForTest(c)
	exists, err := s.IndexExists(context.Background(), "test:idx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("expected true")
	}
}

func TestIndexExists_False(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mock.NewClient(ctrl)

	c.EXPECT().
		Do(gomock.Any(), mock.Match("FT.INFO", "test:idx")).
		Return(mock.Result(mock.RedisError("Unknown Index name")))

	s := NewStoreForTest(c)
	exists, err := s.IndexExists(context.Background(), "test:idx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Error("expected false")
	}
}

// --- Valkey-specific: SupportsTextSearch returns false ---

func TestSupportsTextSearch_False(t *testing.T) {
	s := &Store{}
	if s.SupportsTextSearch(context.Background()) {
		t.Error("Valkey store should NOT support text search")
	}
}

// --- search.go tests ---

func TestSearchKNN_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mock.NewClient(ctrl)

	c.EXPECT().
		Do(gomock.Any(), mock.MatchFn(func(cmd []string) bool {
			return cmd[0] == "FT.SEARCH"
		})).
		Return(mock.Result(mock.RedisArray(
			mock.RedisInt64(1),
			mock.RedisString("doc:1"),
			mock.RedisArray(
				mock.RedisString("__vector_score"),
				mock.RedisString("0.2"),
				mock.RedisString("__content"),
				mock.RedisString("hello"),
			),
		)))

	s := NewStoreForTest(c)
	result, err := s.SearchKNN(context.Background(), &db.KNNQuery{
		IndexName: "idx",
		Vector:    []float32{0.1, 0.2},
		K:         10,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("expected total 1, got %d", result.Total)
	}
	// cosine distance 0.2 maps to similarity 0.8
	if result.Entries[0].Score < 0.79 || result.Entries[0].Score > 0.81 {
		t.Errorf("expected score ~0.8, got %f", result.Entries[0].Score)
	}
}

func TestSearchKNN_Validation(t *testing.T) {
	s := &Store{}
	ctx := context.Background()

	_, err := s.SearchKNN(ctx, &db.KNNQuery{Vector: []float32{0.1}, K: 10})
	if err == nil {
		t.Error("expected error for empty index name")
	}

	_, err = s.SearchKNN(ctx, &db.KNNQuery{IndexName: "idx", K: 10})
	if err == nil {
		t.Error("expected error for empty vector")
	}

	_, err = s.SearchKNN(ctx, &db.KNNQuery{IndexName: "idx", Vector: []float32{0.1}, K: 0})
	if err == nil {
		t.Error("expected error for k=0")
	}
}

func TestSearchBM25_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mock.NewClient(ctrl)

	c.EXPECT().
		Do(gomock.Any(), mock.MatchFn(func(cmd []string) bool {
			return cmd[0] == "FT.SEARCH"
		})).
		Return(mock.Result(mock.RedisArray(
			mock.RedisInt64(1),
			mock.RedisString("doc:1"),
			mock.RedisString("0.7"),
			mock.RedisArray(
				mock.RedisString("__content"),
				mock.RedisString("text"),
			),
		)))

	s := NewStoreForTest(c)
	result, err := s.SearchBM25(context.Background(), &db.TextQuery{
		IndexName: "idx",
		Query:     "test",
		TopK:      10,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("expected total 1, got %d", result.Total)
	}
}

func TestSearchBM25_Validation(t *testing.T) {
	s := &Store{}
	ctx := context.Background()

	_, err := s.SearchBM25(ctx, &db.TextQuery{Query: "test", TopK: 10})
	if err == nil {
		t.Error("expected error for empty index name")
	}

	_, err = s.SearchBM25(ctx, &db.TextQuery{IndexName: "idx", TopK: 10})
	if err == nil {
		t.Error("expected error for empty query")
	}

	_, err = s.SearchBM25(ctx, &db.TextQuery{IndexName: "idx", Query: "test", TopK: 0})
	if err == nil {
		t.Error("expected error for topK=0")
	}
}

// --- Valkey-specific: SearchList fallback to SCAN for query="*" ---

func TestSearchList_WildcardFallback(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mock.NewClient(ctrl)

	// SCAN returns keys
	c.EXPECT().
		Do(gomock.Any(), mock.MatchFn(func(cmd []string) bool {
			return cmd[0] == "SCAN"
		})).
		Return(mock.Result(mock.RedisArray(
			mock.RedisInt64(0),
			mock.RedisArray(mock.RedisString("vecdex:col:doc1"), mock.RedisString("vecdex:col:doc2")),
		)))

	// HGETALL for each key
	c.EXPECT().
		Do(gomock.Any(), mock.Match("HGETALL", "vecdex:col:doc1")).
		Return(mock.Result(mock.RedisMap(map[string]rueidis.RedisMessage{
			"__content": mock.RedisString("hello"),
		})))

	c.EXPECT().
		Do(gomock.Any(), mock.Match("HGETALL", "vecdex:col:doc2")).
		Return(mock.Result(mock.RedisMap(map[string]rueidis.RedisMessage{
			"__content": mock.RedisString("world"),
		})))

	s := NewStoreForTest(c)
	result, err := s.SearchList(context.Background(), "vecdex:col:idx", "*", 0, 10, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 2 {
		t.Fatalf("expected total 2, got %d", result.Total)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result.Entries))
	}
	if result.Entries[0].Fields["__content"] != "hello" {
		t.Errorf("unexpected content: %s", result.Entries[0].Fields["__content"])
	}
}

func TestSearchList_NonWildcard(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mock.NewClient(ctrl)

	c.EXPECT().
		Do(gomock.Any(), mock.MatchFn(func(cmd []string) bool {
			return cmd[0] == "FT.SEARCH"
		})).
		Return(mock.Result(mock.RedisArray(
			mock.RedisInt64(1),
			mock.RedisString("doc:1"),
			mock.RedisArray(mock.RedisString("f"), mock.RedisString("v")),
		)))

	s := NewStoreForTest(c)
	result, err := s.SearchList(context.Background(), "idx", "@tag:{val}", 0, 10, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("expected total 1, got %d", result.Total)
	}
}

// --- Valkey-specific: SearchCount fallback to SCAN for query="*" ---

func TestSearchCount_WildcardFallback(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mock.NewClient(ctrl)

	c.EXPECT().
		Do(gomock.Any(), mock.MatchFn(func(cmd []string) bool {
			return cmd[0] == "SCAN"
		})).
		Return(mock.Result(mock.RedisArray(
			mock.RedisInt64(0),
			mock.RedisArray(mock.RedisString("k1"), mock.RedisString("k2"), mock.RedisString("k3")),
		)))

	s := NewStoreForTest(c)
	count, err := s.SearchCount(context.Background(), "vecdex:col:idx", "*")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3, got %d", count)
	}
}

func TestSearchCount_NonWildcard(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mock.NewClient(ctrl)

	c.EXPECT().
		Do(gomock.Any(), mock.MatchFn(func(cmd []string) bool {
			return cmd[0] == "FT.SEARCH"
		})).
		Return(mock.Result(mock.RedisArray(mock.RedisInt64(5))))

	s := NewStoreForTest(c)
	count, err := s.SearchCount(context.Background(), "idx", "@tag:{val}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 5 {
		t.Errorf("expected 5, got %d", count)
	}
}

// --- Valkey-specific: indexToKeyPrefix ---

func TestIndexToKeyPrefix(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"vecdex:myCollection:idx", "vecdex:myCollection:"},
		{"other:index", "other:index:"},
	}
	for _, tc := range tests {
		got := indexToKeyPrefix(tc.input)
		if got != tc.want {
			t.Errorf("indexToKeyPrefix(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// --- Filter building tests ---

func TestBuildFilter_Empty(t *testing.T) {
	result := buildFilter(filter.Expression{})
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestBuildFilter_MustTag(t *testing.T) {
	cond, _ := filter.NewMatch("category", "electronics")
	expr, _ := filter.NewExpression([]filter.Condition{cond}, nil, nil)

	result := buildFilter(expr)
	if result != `@category:{electronics}` {
		t.Errorf("unexpected filter: %q", result)
	}
}

func TestBuildFilter_Should(t *testing.T) {
	cond1, _ := filter.NewMatch("color", "red")
	cond2, _ := filter.NewMatch("color", "blue")
	expr, _ := filter.NewExpression(nil, []filter.Condition{cond1, cond2}, nil)

	result := buildFilter(expr)
	if result != `(@color:{red} | @color:{blue})` {
		t.Errorf("unexpected filter: %q", result)
	}
}

func TestBuildFilter_MustNot(t *testing.T) {
	cond, _ := filter.NewMatch("status", "deleted")
	expr, _ := filter.NewExpression(nil, nil, []filter.Condition{cond})

	result := buildFilter(expr)
	if result != `-@status:{deleted}` {
		t.Errorf("unexpected filter: %q", result)
	}
}

func TestBuildNumericFilter_GTE_LTE(t *testing.T) {
	gte := 10.0
	lte := 100.0
	rng, _ := filter.NewRangeFilter(nil, &gte, nil, &lte)
	result := buildNumericFilter("price", rng)
	if result != `@price:[10 100]` {
		t.Errorf("unexpected filter: %q", result)
	}
}

func TestEscapeQuery(t *testing.T) {
	input := `hello @user`
	escaped := escapeQuery(input)
	if escaped != `hello \@user` {
		t.Errorf("unexpected escaped: %q", escaped)
	}
}

func TestVectorToBytes(t *testing.T) {
	v := []float32{1.0}
	b := vectorToBytes(v)
	if len(b) != 4 {
		t.Fatalf("expected 4 bytes, got %d", len(b))
	}
}

// --- buildCreateArgs / buildFieldArgs ---

func TestBuildCreateArgs_Validation(t *testing.T) {
	_, err := buildCreateArgs(&db.IndexDefinition{Name: "", Fields: []db.IndexField{{Name: "f", Type: db.IndexFieldTag}}})
	if err == nil {
		t.Error("expected error for empty name")
	}

	_, err = buildCreateArgs(&db.IndexDefinition{Name: "test"})
	if err == nil {
		t.Error("expected error for empty fields")
	}
}

func TestBuildFieldArgs_AllTypes(t *testing.T) {
	tests := []struct {
		name  string
		field db.IndexField
		want  string
	}{
		{"tag", db.IndexField{Name: "f", Type: db.IndexFieldTag}, "TAG"},
		{"numeric", db.IndexField{Name: "f", Type: db.IndexFieldNumeric}, "NUMERIC"},
		{"text", db.IndexField{Name: "f", Type: db.IndexFieldText}, "TEXT"},
		{"vector", db.IndexField{Name: "f", Type: db.IndexFieldVector, VectorDim: 128, VectorAlgo: db.VectorHNSW}, "VECTOR"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args, err := buildFieldArgs(&tc.field)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			found := false
			for _, a := range args {
				if a == tc.want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected %q in args %v", tc.want, args)
			}
		})
	}
}

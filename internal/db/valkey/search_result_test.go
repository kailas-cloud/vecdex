package valkey

import (
	"testing"

	"github.com/redis/rueidis"
	"github.com/redis/rueidis/mock"

	"github.com/kailas-cloud/vecdex/internal/db"
)

func TestSearchRejectsMalformedRepliesWithoutPartialResults(t *testing.T) {
	valid := []rueidis.RedisMessage{
		mock.RedisInt64(2), mock.RedisString("first"),
		mock.RedisArray(mock.RedisString("content"), mock.RedisString("valid")),
	}
	cases := map[string][]rueidis.RedisMessage{
		"empty":          {},
		"negative total": {mock.RedisInt64(-1)},
		"wrong total":    {mock.RedisString("two")},
		"missing rows":   {mock.RedisInt64(2)},
		"missing fields": append(append([]rueidis.RedisMessage{}, valid...), mock.RedisString("second")),
		"invalid key": append(append([]rueidis.RedisMessage{}, valid...),
			mock.RedisArray(), mock.RedisArray()),
		"invalid fields": append(append([]rueidis.RedisMessage{}, valid...),
			mock.RedisString("second"), mock.RedisString("invalid")),
		"unpaired field": append(append([]rueidis.RedisMessage{}, valid...),
			mock.RedisString("second"), mock.RedisArray(mock.RedisString("content"))),
		"invalid value": append(append([]rueidis.RedisMessage{}, valid...),
			mock.RedisString("second"), mock.RedisArray(mock.RedisString("content"), mock.RedisArray())),
		"zero total with row": {mock.RedisInt64(0), mock.RedisString("first"), mock.RedisArray()},
	}
	parsers := map[string]func([]rueidis.RedisMessage) (*db.SearchResult, error){
		"keyword": parseBM25Result, "semantic": parseKNNResult,
	}
	for mode, parse := range parsers {
		for name, raw := range cases {
			t.Run(mode+"/"+name, func(t *testing.T) {
				if result, err := parse(raw); err == nil || result != nil {
					t.Fatalf("malformed reply returned partial success: result=%+v err=%v", result, err)
				}
			})
		}
	}
}

func TestSearchReplyCanContainFewerRowsThanTotal(t *testing.T) {
	result, err := parseBM25Result([]rueidis.RedisMessage{
		mock.RedisInt64(100), mock.RedisString("first"), mock.RedisArray(),
	})
	if err != nil || result.Total != 100 || len(result.Entries) != 1 {
		t.Fatalf("valid LIMIT response rejected: result=%+v err=%v", result, err)
	}
}

func TestKNNRejectsInvalidDistances(t *testing.T) {
	for _, distance := range []string{"invalid", "NaN", "+Inf", "-Inf"} {
		t.Run(distance, func(t *testing.T) {
			result, err := parseKNNResult([]rueidis.RedisMessage{
				mock.RedisInt64(1), mock.RedisString("first"),
				mock.RedisArray(mock.RedisString("__vector_score"), mock.RedisString(distance)),
			})
			if err == nil || result != nil {
				t.Fatalf("invalid distance returned success: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestKNNAcceptsFiniteCosineRoundoff(t *testing.T) {
	for distance, score := range map[string]float64{"-0.00000011920929": 1, "2.00000011920929": 0} {
		result, err := parseKNNResult([]rueidis.RedisMessage{
			mock.RedisInt64(1), mock.RedisString("first"),
			mock.RedisArray(mock.RedisString("__vector_score"), mock.RedisString(distance)),
		})
		if err != nil || result.Entries[0].Score != score {
			t.Fatalf("cosine roundoff %s: result=%+v err=%v", distance, result, err)
		}
	}
}

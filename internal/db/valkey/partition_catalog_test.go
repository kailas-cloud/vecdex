package valkey

import (
	"context"
	"testing"

	"github.com/redis/rueidis"
	"github.com/redis/rueidis/mock"
	"go.uber.org/mock/gomock"
)

func TestPartitionCatalogPipelinesRoutedKeys(t *testing.T) {
	client := mock.NewClient(gomock.NewController(t))
	client.EXPECT().DoMulti(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, commands ...rueidis.Completed) []rueidis.RedisResult {
			for i, command := range commands {
				expected := client.B().Get().Key([]string{"ids:{first}", "ids:{second}"}[i]).Build()
				if command.Slot() != expected.Slot() {
					t.Fatal("catalog command did not carry its key slot")
				}
			}
			return []rueidis.RedisResult{
				mock.Result(mock.RedisArray(mock.RedisString("a"), mock.RedisString("c"))),
				mock.Result(mock.RedisArray(mock.RedisString("b"))),
			}
		})
	pages, err := NewStoreForTest(client).CatalogIDPages(
		context.Background(), []string{"ids:{first}", "ids:{second}"}, "", 8,
	)
	if err != nil || len(pages) != 2 || len(pages[0]) != 2 || pages[1][0] != "b" {
		t.Fatalf("pages=%v err=%v", pages, err)
	}
}

func TestPartitionCatalogCountsFailOnUnavailableShard(t *testing.T) {
	client := mock.NewClient(gomock.NewController(t))
	client.EXPECT().DoMulti(gomock.Any(), gomock.Any(), gomock.Any()).Return([]rueidis.RedisResult{
		mock.Result(mock.RedisInt64(10)), mock.Result(mock.RedisError("CLUSTERDOWN shard unavailable")),
	})
	counts, err := NewStoreForTest(client).CatalogCounts(context.Background(), []string{"ids:{a}", "ids:{b}"})
	if err == nil || counts != nil {
		t.Fatalf("partial count returned as success: %v, %v", counts, err)
	}
}

func TestLiveCountsFailOnUnavailableShard(t *testing.T) {
	client := mock.NewClient(gomock.NewController(t))
	client.EXPECT().DoMulti(gomock.Any(), gomock.Any(), gomock.Any()).Return([]rueidis.RedisResult{
		mock.Result(mock.RedisInt64(10)), mock.Result(mock.RedisError("CLUSTERDOWN shard unavailable")),
	})
	counts, err := NewStoreForTest(client).LiveCounts(
		context.Background(), []string{"deadlines:{a}", "deadlines:{b}"}, 1000,
	)
	if err == nil || counts != nil {
		t.Fatalf("partial live count returned as success: %v, %v", counts, err)
	}
}

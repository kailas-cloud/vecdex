package valkey

import (
	"context"
	"slices"
	"testing"

	"github.com/redis/rueidis"
	"github.com/redis/rueidis/mock"
	"go.uber.org/mock/gomock"

	"github.com/kailas-cloud/vecdex/internal/db"
)

// FT names are not data keys, but their slots select a primary connection.
// Unkeyed arbitrary commands can select replicas in rueidis cluster mode.
func TestIndexCommandsRouteToPrimary(t *testing.T) {
	for _, operation := range []string{"create", "drop", "info"} {
		t.Run(operation, func(t *testing.T) {
			client := mock.NewClient(gomock.NewController(t))
			client.EXPECT().Do(gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ context.Context, command rueidis.Completed) rueidis.RedisResult {
					expected := client.B().Get().Key("collection:index").Build()
					if command.Slot() != expected.Slot() {
						t.Fatal("FT command has no primary routing slot")
					}
					return mock.Result(mock.RedisString("OK"))
				})
			store := NewStoreForTest(client)
			var err error
			ctx := context.Background()
			switch operation {
			case "create":
				err = store.CreateIndex(
					ctx,
					&db.IndexDefinition{
						Name:   "collection:index",
						Fields: []db.IndexField{{Name: "text", Type: db.IndexFieldText}},
					},
				)
			case "drop":
				err = store.DropIndex(ctx, "collection:index")
			case "info":
				_, err = store.IndexExists(ctx, "collection:index")
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSearchDistributesCoordinatorRoutingAndRequiresCompleteResults(t *testing.T) {
	client := mock.NewClient(gomock.NewController(t))
	slots := make(map[uint16]bool)
	unroutedCommand := client.B().Arbitrary("FT.SEARCH").Args("collection:index", "*").ReadOnly()
	unrouted := unroutedCommand.Slot()
	client.EXPECT().Do(gomock.Any(), gomock.Any()).Times(64).DoAndReturn(
		func(_ context.Context, command rueidis.Completed) rueidis.RedisResult {
			if command.Slot() == unrouted || !command.IsReadOnly() {
				t.Fatal("search has no primary routing slot or is not marked read-only")
			}
			slots[command.Slot()] = true
			args := command.Commands()
			if args[1] != "collection:index" {
				t.Fatal("coordinator selection changed the logical index name")
			}
			for _, required := range []string{"ALLSHARDS", "CONSISTENT"} {
				if !slices.Contains(args, required) {
					t.Fatalf("search permits incomplete results: %v", args)
				}
			}
			return mock.Result(mock.RedisArray(mock.RedisInt64(0)))
		})
	store := NewStoreForTest(client)
	for range 32 {
		if _, err := store.SearchKNN(context.Background(), &db.KNNQuery{
			IndexName: "collection:index", Vector: []float32{1}, K: 10,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.SearchBM25(context.Background(), &db.TextQuery{
			IndexName: "collection:index", Query: "source", TopK: 10,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(slots) < 32 {
		t.Fatalf("one collection's queries reached only %d routing slots", len(slots))
	}
}

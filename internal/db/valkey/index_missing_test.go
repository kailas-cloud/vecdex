package valkey

import (
	"context"
	"testing"

	"github.com/redis/rueidis/mock"
	"go.uber.org/mock/gomock"
)

func TestIndexProbeRecognizesSearch12MissingIndex(t *testing.T) {
	client := mock.NewClient(gomock.NewController(t))
	client.EXPECT().Do(gomock.Any(), mock.Match("FT.INFO", "missing")).
		Return(mock.Result(mock.RedisError("Index with name 'missing' not found in database 0")))
	exists, err := NewStoreForTest(client).IndexExists(context.Background(), "missing")
	if err != nil || exists {
		t.Fatalf("exists=%v err=%v", exists, err)
	}
}

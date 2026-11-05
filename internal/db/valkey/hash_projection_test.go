package valkey

import (
	"context"
	"testing"

	"github.com/redis/rueidis"
	"github.com/redis/rueidis/mock"
	"go.uber.org/mock/gomock"
)

func TestHashProjectionKeepsMetadataAndVersionInOneCommand(t *testing.T) {
	client := mock.NewClient(gomock.NewController(t))
	client.EXPECT().DoMulti(gomock.Any(),
		mock.Match("HMGET", "present", "__chunk_version", "__source_metadata"),
		mock.Match("HMGET", "missing", "__chunk_version", "__source_metadata")).Return([]rueidis.RedisResult{
		mock.Result(mock.RedisArray(mock.RedisString("version"), mock.RedisString(`{"nested":true}`))),
		mock.Result(mock.RedisArray(mock.RedisNil(), mock.RedisNil())),
	})
	rows, err := NewStoreForTest(client).HGetFieldsMulti(context.Background(),
		[]string{"present", "missing"}, []string{"__chunk_version", "__source_metadata"})
	if err != nil || rows[0][0] != "version" || rows[0][1] != `{"nested":true}` ||
		rows[1][0] != "" || rows[1][1] != "" {
		t.Fatalf("projection: %v %v", rows, err)
	}
}

func TestHashProjectionRejectsMisalignedRows(t *testing.T) {
	client := mock.NewClient(gomock.NewController(t))
	client.EXPECT().DoMulti(gomock.Any(), gomock.Any()).Return([]rueidis.RedisResult{
		mock.Result(mock.RedisArray(mock.RedisString("version"))),
	})
	if _, err := NewStoreForTest(client).HGetFieldsMulti(context.Background(),
		[]string{"source"}, []string{"__chunk_version", "__source_metadata"}); err == nil {
		t.Fatal("accepted a projection missing its metadata column")
	}
}

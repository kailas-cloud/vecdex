package valkey

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/redis/rueidis"
	"github.com/redis/rueidis/mock"
	"go.uber.org/mock/gomock"
)

func TestTransactionRetryRequiresProvenRejection(t *testing.T) {
	cases := []struct {
		name    string
		replies []rueidis.RedisResult
		retry   bool
	}{
		{"redirect at exec", []rueidis.RedisResult{
			mock.Result(mock.RedisString("OK")),
			mock.Result(mock.RedisString("QUEUED")),
			mock.Result(mock.RedisError("MOVED 42 127.0.0.1:7001")),
		}, true},
		{"queue abort", []rueidis.RedisResult{
			mock.Result(mock.RedisString("OK")),
			mock.Result(mock.RedisError("TRYAGAIN slot migrating")),
			mock.Result(mock.RedisError("EXECABORT Transaction discarded because of previous errors.")),
		}, true},
		{"lost exec even with queue rejection", []rueidis.RedisResult{
			mock.Result(mock.RedisString("OK")),
			mock.Result(mock.RedisError("MOVED 42 127.0.0.1:7001")),
			mock.ErrorResult(context.DeadlineExceeded),
		}, false},
		{"permanent queue error", []rueidis.RedisResult{
			mock.Result(mock.RedisString("OK")),
			mock.Result(mock.RedisError("OOM command not allowed")),
			mock.Result(mock.RedisError("EXECABORT Transaction discarded because of previous errors.")),
		}, false},
		{"runtime error may partially commit", []rueidis.RedisResult{
			mock.Result(mock.RedisString("OK")),
			mock.Result(mock.RedisString("QUEUED")),
			mock.Result(mock.RedisArray(mock.RedisError("WRONGTYPE invalid key"))),
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := mock.NewDedicatedClient(gomock.NewController(t))
			client.EXPECT().DoMulti(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(tc.replies)
			_, committed, err := execTransaction(context.Background(), client, []rueidis.Completed{
				client.B().Set().Key("key").Value("value").Build(),
			})
			if committed || err == nil || canRetryTransaction(err) != tc.retry {
				t.Fatalf("committed=%v err=%v retry=%v, want %v", committed, err, canRetryTransaction(err), tc.retry)
			}
			if tc.name == "permanent queue error" && !strings.Contains(err.Error(), "OOM command not allowed") {
				t.Fatalf("transaction lost the queue rejection cause: %v", err)
			}
		})
	}
	if rejectedTransaction(errors.New("MOVED 42 host:7001"), nil) {
		t.Fatal("text matching must not turn a non-server error into a safe replay")
	}
}

func TestTransactionDoesNotRetryMixedQueueRejections(t *testing.T) {
	client := mock.NewDedicatedClient(gomock.NewController(t))
	client.EXPECT().DoMulti(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(
		[]rueidis.RedisResult{
			mock.Result(mock.RedisString("OK")),
			mock.Result(mock.RedisError("TRYAGAIN slot migrating")),
			mock.Result(mock.RedisError("OOM command not allowed")),
			mock.Result(mock.RedisError("EXECABORT Transaction discarded because of previous errors.")),
		})
	_, committed, err := execTransaction(context.Background(), client, []rueidis.Completed{
		client.B().Set().Key("key").Value("value").Build(),
		client.B().Set().Key("other").Value("value").Build(),
	})
	if committed || err == nil || canRetryTransaction(err) || !strings.Contains(err.Error(), "OOM") {
		t.Fatalf("mixed rejection was replayed or lost: committed=%v err=%v", committed, err)
	}
}

func TestWatchRedirectCanBeRetried(t *testing.T) {
	client := mock.NewDedicatedClient(gomock.NewController(t))
	client.EXPECT().Do(gomock.Any(), mock.Match("WATCH", "key")).
		Return(mock.Result(mock.RedisError("ASK 42 127.0.0.1:7001")))
	if err := watchKeys(context.Background(), client, "key"); !canRetryTransaction(err) {
		t.Fatalf("expected safe retry for WATCH: %v", err)
	}
}

package vecdex

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type sdkRoundTripper func(*http.Request) (*http.Response, error)

func (transport sdkRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return transport(req)
}

func TestHTTPWriteTransportFailureIsNotReplayed(t *testing.T) {
	lostReply := errors.New("connection lost after write")
	calls := 0
	client, err := NewHTTP("http://vecdex.test", "", WithHTTPClient(&http.Client{
		Transport: sdkRoundTripper(func(_ *http.Request) (*http.Response, error) {
			calls++
			return nil, lostReply
		}),
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.Documents("memories").Upsert(context.Background(), Document{ID: "one", Content: "source"})
	if !errors.Is(err, lostReply) || calls != 1 {
		t.Fatalf("ambiguous write replayed or lost its cause: calls=%d err=%v", calls, err)
	}
}

func TestHTTPWriteRedirectIsNotReplayed(t *testing.T) {
	calls := 0
	client, err := NewHTTP("http://vecdex.test", "", WithHTTPClient(&http.Client{
		Transport: sdkRoundTripper(func(_ *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: http.StatusTemporaryRedirect, Body: http.NoBody,
				Header: http.Header{"Location": []string{"http://vecdex.test/other"}}}, nil
		}),
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.Documents("memories").Upsert(context.Background(), Document{ID: "one", Content: "source"})
	var failure *HTTPError
	if !errors.As(err, &failure) || failure.StatusCode != http.StatusTemporaryRedirect || calls != 1 {
		t.Fatalf("redirect replayed a write: calls=%d err=%v", calls, err)
	}
}

func TestHTTPContextCancellationIsRetained(t *testing.T) {
	client, err := NewHTTP("http://vecdex.test", "", WithHTTPClient(&http.Client{
		Transport: sdkRoundTripper(func(req *http.Request) (*http.Response, error) {
			<-req.Context().Done()
			return nil, req.Context().Err()
		}),
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Documents("memories").Get(ctx, "source")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("HTTP SDK lost cancellation: %v", err)
	}
}

func TestHTTPSearchRejectsAnIncompleteSourceProjection(t *testing.T) {
	client, err := NewHTTP("http://vecdex.test", "", WithHTTPClient(&http.Client{
		Transport: sdkRoundTripper(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{
				"items":[
					{"id":"valid","version":"v1","score":0.9,"snippet":"ok","metadata":{}},
					{"id":"invalid","score":0.8,"snippet":"missing version","metadata":{}}
				],"limit":10,"total":2}`))}, nil
		}),
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	response, err := client.Search("memories").Query(context.Background(), "memory", nil)
	if err == nil || len(response.Results) != 0 {
		t.Fatal("a malformed response became a successful partial search")
	}
}

func TestHTTPHealthRetainsUnhealthyComponentChecks(t *testing.T) {
	client, err := NewHTTP("http://vecdex.test", "", WithHTTPClient(&http.Client{
		Transport: sdkRoundTripper(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusServiceUnavailable,
				Body: io.NopCloser(strings.NewReader(`{"status":"error","checks":{"valkey":"error"}}`))}, nil
		}),
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	status := client.Health(context.Background())
	if status.Status != "error" || status.Checks["valkey"] != "error" {
		t.Fatalf("HTTP health lost the component that failed readiness: %+v", status)
	}
}

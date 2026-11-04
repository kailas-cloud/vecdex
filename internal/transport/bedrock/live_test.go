package bedrock

import (
	"context"
	"os"
	"testing"
)

// TestLiveBedrock is opt-in and performs one small paid inference request.
// Supply AWS_BEARER_TOKEN_BEDROCK and AWS_REGION explicitly; no AWS credential chain.
func TestLiveBedrock(t *testing.T) {
	if os.Getenv("VECDEX_BEDROCK_LIVE") != "1" {
		t.Skip("set VECDEX_BEDROCK_LIVE=1 to invoke real Bedrock")
	}
	region := os.Getenv("AWS_REGION")
	if region == "" {
		t.Fatal("AWS_REGION is required for the live test")
	}
	model := os.Getenv("VECDEX_BEDROCK_MODEL")
	if model == "" {
		model = "cohere.embed-v4:0"
	}
	e, err := NewEmbedder(
		&Config{
			APIKey:     os.Getenv("AWS_BEARER_TOKEN_BEDROCK"),
			Region:     region,
			Model:      model,
			Dimensions: 1024,
			Purpose:    "search_document",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.Embed(context.Background(), "Проверка векторного поиска. Vector search smoke test.")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Embedding) != 1024 {
		t.Fatalf("dimensions %d", len(r.Embedding))
	}
	t.Logf("dimensions=%d tokens=%d usage_unknown=%t", len(r.Embedding), r.TotalTokens, r.UsageUnknown)
}

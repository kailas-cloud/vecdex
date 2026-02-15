// Example demonstrating the vecdex Document API.
//
// Creates a "months" collection, uploads 12 documents (months),
// searches for "winter" and prints the results.
//
// Run: go run ./examples/months/
// Requires a running vecdex instance on localhost:8080.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

const baseURL = "http://localhost:8080/api/v1"

type month struct {
	id      string
	content string
	season  string
}

var months = []month{
	{
		"january",
		"January is the first month of the year. It is cold and snowy in the northern hemisphere.",
		"winter",
	},
	{
		"february",
		"February is the shortest month. Winter is still strong, with freezing temperatures.",
		"winter",
	},
	{
		"march",
		"March marks the beginning of spring. Snow melts, days grow longer, and nature awakens.",
		"spring",
	},
	{
		"april",
		"April brings warm rains and blooming flowers. Spring is in full swing with green fields.",
		"spring",
	},
	{
		"may",
		"May is late spring. Trees are fully green, temperatures are pleasant.",
		"spring",
	},
	{
		"june",
		"June is the start of summer. Long sunny days, warm weather, and vacations begin.",
		"summer",
	},
	{
		"july",
		"July is the hottest month of summer. Beach trips and outdoor adventures everywhere.",
		"summer",
	},
	{
		"august",
		"August is late summer. The heat persists but evenings start to cool.",
		"summer",
	},
	{
		"september",
		"September marks the beginning of autumn. Leaves change color and temperatures drop.",
		"autumn",
	},
	{
		"october",
		"October is the heart of autumn. Forests turn golden and red, the air becomes crisp.",
		"autumn",
	},
	{
		"november",
		"November is late autumn approaching winter. Trees are bare, frost appears.",
		"autumn",
	},
	{
		"december",
		"December is the last month. Winter arrives with snow, holidays, and celebrations.",
		"winter",
	},
}

func main() {
	// 1. Create collection
	fmt.Println("=== Creating collection 'months' ===")
	createCollection()

	// 2. Upload documents
	fmt.Println("\n=== Adding 12 month documents ===")
	for _, m := range months {
		createDocument(m)
	}

	// 3. Search
	fmt.Println("\n=== Searching: \"winter\" ===")
	search("winter", nil, 5)

	fmt.Println("\n=== Searching: \"hot sunny beach\" ===")
	search("hot sunny beach", nil, 3)

	fmt.Println("\n=== Searching: \"cold\" with filter season=winter ===")
	search("cold", map[string]string{"season": "winter"}, 5)

	// 4. Cleanup
	fmt.Println("\n=== Cleanup: deleting collection ===")
	deleteCollection()
}

func createCollection() {
	body := map[string]interface{}{
		"type": "json",
		"fields": []map[string]string{
			{"name": "season", "type": "tag"},
		},
	}
	resp := doRequest("POST", baseURL+"/months", body)
	switch resp.StatusCode {
	case 201:
		fmt.Println("  Collection created (201)")
	case 409:
		fmt.Println("  Collection already exists (409), continuing...")
	default:
		printBody("  Error", resp)
	}
	_ = resp.Body.Close()
}

func createDocument(m month) {
	body := map[string]interface{}{
		"content": m.content,
		"tags":    map[string]string{"season": m.season},
	}
	resp := doRequest("POST", fmt.Sprintf("%s/months/%s", baseURL, m.id), body)
	switch resp.StatusCode {
	case 201:
		fmt.Printf("  + %s (%s)\n", m.id, m.season)
	case 409:
		fmt.Printf("  ~ %s already exists\n", m.id)
	default:
		printBody(fmt.Sprintf("  ! %s error", m.id), resp)
	}
	_ = resp.Body.Close()
}

func search(query string, filters map[string]string, k int) {
	body := map[string]interface{}{
		"query": query,
		"k":     k,
	}
	if filters != nil {
		body["filters"] = filters
	}

	resp := doRequest("POST", baseURL+"/months/search", body)

	if resp.StatusCode != 200 {
		printBody("  Search error", resp)
		_ = resp.Body.Close()
		return
	}

	var result struct {
		Results []struct {
			ID      string            `json:"id"`
			Score   float64           `json:"score"`
			Content string            `json:"content"`
			Tags    map[string]string `json:"tags"`
		} `json:"results"`
		Total int `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		_ = resp.Body.Close()
		log.Fatalf("Failed to decode search results: %v", err)
	}
	_ = resp.Body.Close()

	fmt.Printf("  Found %d results:\n", result.Total)
	for i, r := range result.Results {
		// Truncate content for readability
		content := r.Content
		if len(content) > 80 {
			content = content[:80] + "..."
		}
		fmt.Printf("  %d. %-10s (score: %.4f, season: %s)\n     %s\n",
			i+1, r.ID, r.Score, r.Tags["season"], content)
	}
}

func deleteCollection() {
	req, _ := http.NewRequestWithContext(context.Background(), "DELETE", baseURL+"/months", http.NoBody)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("DELETE failed: %v", err)
	}
	fmt.Printf("  Deleted (%d)\n", resp.StatusCode)
	_ = resp.Body.Close()
}

func doRequest(method, url string, body interface{}) *http.Response {
	data, err := json.Marshal(body)
	if err != nil {
		log.Fatalf("Marshal failed: %v", err)
	}

	req, err := http.NewRequestWithContext(context.Background(), method, url, bytes.NewReader(data))
	if err != nil {
		log.Fatalf("NewRequest failed: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Request failed: %v\nIs vecdex running on localhost:8080?\n", err)
		os.Exit(1)
	}
	return resp
}

func printBody(prefix string, resp *http.Response) {
	body, _ := io.ReadAll(resp.Body)
	fmt.Printf("%s (%d): %s\n", prefix, resp.StatusCode, body)
}

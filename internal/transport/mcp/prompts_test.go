package mcp

import (
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExactlyApprovedPromptsAcrossRolesAndProtocols(t *testing.T) {
	h, _, _ := fixture(t)
	for _, version := range []string{legacyVersion, latestVersion} {
		for _, role := range []string{"read", "write", "admin"} {
			t.Run(version+"/"+role, func(t *testing.T) {
				wire, _ := callRPC(t, h, Principal{"owner", role}, version, "prompts/list", nil)
				result := wire["result"].(map[string]any)
				prompts := result["prompts"].([]any)
				if len(prompts) != 2 || result["nextCursor"] != nil {
					t.Fatalf("unexpected prompt catalog: %#v", result)
				}
				names := map[string]bool{}
				for _, value := range prompts {
					names[value.(map[string]any)["name"].(string)] = true
				}
				if !names["resume_context"] || !names["review_memory"] {
					t.Fatalf("wrong prompt names: %#v", names)
				}
			})
		}
	}
}

func TestApprovedPromptWorkflowsAndOwnership(t *testing.T) {
	h, _, cols := fixture(t)
	for _, tc := range []struct {
		name         string
		instructions []string
	}{
		{"resume_context",
			[]string{"context_get",
				"verify_pins:true",
				"memory_read",
				"current evidence",
				"continue the user's task"}},

		{"review_memory", []string{"context_get", "verify_pins:true", "memory_read",
			"contradictions", "propose corrections"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire, _ := callRPC(t, h, Principal{"owner", "read"}, latestVersion, "prompts/get", map[string]any{
				"name": tc.name, "arguments": map[string]any{"context_id": "context-1", "topic": "quoted \"topic\""},
			})
			result := wire["result"].(map[string]any)
			message := result["messages"].([]any)[0].(map[string]any)
			text := message["content"].(map[string]any)["text"].(string)
			for _, instruction := range tc.instructions {
				if !strings.Contains(text, instruction) {
					t.Fatalf("missing workflow step %q: %s", instruction, text)
				}
			}
			if !strings.Contains(text, `"topic":"quoted \"topic\""`) {
				t.Fatalf("task data was not JSON quoted: %s", text)
			}
			if !validCompletionRef(&sdk.CompleteReference{Type: "ref/prompt", Name: tc.name}, "read") {
				t.Fatal("read principal cannot complete approved prompt")
			}
		})
	}
	// Resuming must still work when the old collection generation has gone stale.
	cols.mu.Lock()
	cols.col = cols.col.WithGeneration("gen-2")
	cols.mu.Unlock()
	wire, _ := callRPC(t, h, Principal{"owner", "read"}, latestVersion, "prompts/get", map[string]any{
		"name": "resume_context", "arguments": map[string]any{"context_id": "context-1"},
	})
	if wire["error"] != nil {
		t.Fatalf("stale context cannot be resumed for inspection: %#v", wire)
	}
	if err := h.checkPromptContext(WithPrincipal(t.Context(), Principal{"other", "read"}), "context-1"); err == nil {
		t.Fatal("prompt accepted another principal's context")
	}
	for _, old := range []string{"memory_recall", "memory_capture", "memory_review"} {
		if validCompletionRef(&sdk.CompleteReference{Type: "ref/prompt",
			Name: old},
			"admin") {
			t.Fatalf("old prompt alias retained: %s",
				old)
		}
	}
}

// Command fixture serves the production MCP transport with inert domain services.
// It binds loopback only and injects a test principal. Never deploy this command.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kailas-cloud/vecdex/internal/skills"
	"github.com/kailas-cloud/vecdex/internal/transport/mcp"
)

func main() {
	if err := serve(); err != nil {
		log.Fatal(err)
	}
}

func serve() error {
	assets := make([]mcp.Skill, 0, 4)
	for _, s := range skills.All() {
		assets = append(assets, mcp.Skill{Name: s.Name, Description: s.Description, Content: s.Content, Files: s.Files})
	}
	handler, err := mcp.New(mcp.Dependencies{
		Collections: collections{}, Documents: documents{}, Search: search{}, Contexts: contexts{},
	}, mcp.Options{Version: "conformance-fixture", Skills: assets})
	if err != nil {
		return fmt.Errorf("new MCP handler: %w", err)
	}
	defer func() { _ = handler.Close() }()
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer func() { _ = listener.Close() }()
	url := "http://" + listener.Addr().String() + "/mcp"
	if ready := os.Getenv("VECDEX_CONFORMANCE_READY_FILE"); ready != "" {
		//nolint:gosec // Test runner controls this output path; it is not request input.
		if err := os.WriteFile(ready, []byte(url), 0o600); err != nil {
			return fmt.Errorf("write fixture URL: %w", err)
		}
	}
	fmt.Println(url)
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		ctx := mcp.WithPrincipal(r.Context(), mcp.Principal{KeyID: "conformance", Role: "admin"})
		handler.ServeHTTP(w, r.WithContext(ctx))
	})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve fixture: %w", err)
	}
	return nil
}

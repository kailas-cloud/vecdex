package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunReportsBindFailureIntegration(t *testing.T) {
	addr := os.Getenv("VECDEX_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("set VECDEX_TEST_VALKEY_ADDR to an isolated Valkey fixture")
	}
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(context.Background(), "tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	t.Setenv("ENV", "local")
	if err := os.Mkdir("config", 0o700); err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf(`http:
  port: %s
valkey:
  addrs: [%q]
storage:
  key_prefix: "bind-failure-%s:"
auth:
  tokens:
    - principal_id: test
      role: admin
      key: test-key
embedding:
  providers:
    stub:
      api_key: test-key
      base_url: "http://127.0.0.1:1/v1/"
  vectorizers:
    json:
      provider: stub
      model: stub
      dimensions: 1024
`, port, addr, port)
	if err := os.WriteFile(filepath.Join("config", "local.yaml"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	err = run()
	if err == nil || !strings.Contains(err.Error(), "serve HTTP:") {
		t.Fatalf("run() = %v; want a listener failure after engine cleanup", err)
	}
}

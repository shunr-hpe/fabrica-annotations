// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

// Package sqlite contains integration tests that run the generated sqlite
// build of the storage-annotations user-service end to end.
//
// The service under test is produced by `make generate-sqlite`, which writes
// it to ../../build/user-service-sqlite. Override that location with the
// USER_SERVICE_SQLITE_DIR environment variable.
package sqlite

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// aliceBody is the request used by both POSTs. The username (and name) are
// identical so the second create must be rejected on the uniqueness
// constraint declared in user-service-structs/user.go.
const aliceBody = `{
  "metadata": { "name": "alice" },
  "spec": {
    "username": "alice",
    "email": "alice@example.com",
    "password": "s3cretpass",
    "fullName": "Alice Example",
    "role": "admin",
    "active": true
  }
}`

// TestUserUniqueness verifies that creating two users with the same username
// is rejected and that the user list only grows by one entry.
//
// NOTE: This test currently FAILS. It exposes a known Fabrica bug where the
// dedicated-storage path does not enforce the uniqueness constraint. The fix
// lives on the shunr-hpe/dedicated-resources branch; do not work around the
// bug here.
func TestUserUniqueness(t *testing.T) {
	base, stop := startServer(t)
	defer stop()

	if code, body := postUser(t, base, aliceBody); code != http.StatusCreated {
		t.Fatalf("first POST /users: expected 201 Created, got %d: %s", code, body)
	}

	if code, body := postUser(t, base, aliceBody); code == http.StatusCreated {
		t.Errorf("second POST /users with duplicate username succeeded (status %d); expected it to fail: %s", code, body)
	}

	users := listUsers(t, base)
	if len(users) != 1 {
		t.Errorf("expected exactly 1 user after duplicate create, got %d", len(users))
	}
}

// startServer builds and launches the sqlite user-service on a free port using
// its own temp working directory and sqlite database, then waits for /health.
func startServer(t *testing.T) (base string, stop func()) {
	t.Helper()

	dir := serviceDir()
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		t.Skipf("generated sqlite user-service not found at %s; run 'make generate-sqlite' first (%v)", dir, err)
	}

	work := t.TempDir()
	exe := filepath.Join(work, "server")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}

	build := exec.Command("go", "build", "-o", exe, "./cmd/server")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("failed to build ./cmd/server in %s: %v\n%s", dir, err, out)
	}

	port := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, exe, "serve",
		"--port", strconv.Itoa(port),
		"--database-url", "file:./user-service.db?_fk=1")
	cmd.Dir = work

	var logs bytes.Buffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("failed to start server: %v", err)
	}

	base = fmt.Sprintf("http://127.0.0.1:%d", port)
	stop = func() {
		cancel()
		_ = cmd.Wait()
	}

	if err := waitForHealth(base, 30*time.Second); err != nil {
		stop()
		t.Fatalf("server did not become healthy: %v\nserver output:\n%s", err, logs.String())
	}

	return base, stop
}

// serviceDir returns the location of the generated sqlite user-service.
func serviceDir() string {
	if d := os.Getenv("USER_SERVICE_SQLITE_DIR"); d != "" {
		return d
	}
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "build", "user-service-sqlite")
}

// freePort returns an available TCP port on the loopback interface.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find a free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// waitForHealth polls GET /health until it returns 200 or the timeout elapses.
func waitForHealth(base string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(base + "/health")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timed out after %s", timeout)
}

// postUser POSTs body to /users and returns the status code and response body.
func postUser(t *testing.T, base, body string) (int, []byte) {
	t.Helper()
	resp, err := http.Post(base+"/users", "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("POST /users failed: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

// listUsers GETs /users and returns the decoded list.
func listUsers(t *testing.T, base string) []json.RawMessage {
	t.Helper()
	resp, err := http.Get(base + "/users")
	if err != nil {
		t.Fatalf("GET /users failed: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /users returned %d: %s", resp.StatusCode, data)
	}
	var users []json.RawMessage
	if err := json.Unmarshal(data, &users); err != nil {
		t.Fatalf("failed to decode user list: %v\nbody: %s", err, data)
	}
	return users
}

// Copyright © 2026 OpenCHAMI a Series of LF Projects, LLC
//
// SPDX-License-Identifier: MIT

// Package postgres contains integration tests that run the generated postgres
// build of the storage-annotations user-service end to end.
//
// The service under test is produced by `make generate-postgres`, which writes
// it to ../../build/user-service-postgres. Override that location with the
// USER_SERVICE_POSTGRES_DIR environment variable.
//
// The test needs a postgres database. There are two modes:
//
//   - Automated: if USER_SERVICE_POSTGRES_URL is unset, the test starts a
//     postgres:16 container in Docker (on port 5432 when free, otherwise a free
//     port) and removes it when the test finishes.
//   - Stepped: set USER_SERVICE_POSTGRES_URL to an already-running postgres
//     (e.g. `make postgres-up`), and the test uses it without touching Docker.
package postgres

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

// pgContainer is the name of the Docker container the test manages. It differs
// from the stepped-mode container (user-pg) so the two never collide.
const pgContainer = "user-pg-test"

// aliceBody builds a create request for the given username. Both POSTs in a run
// use the same username so the second create must be rejected on the uniqueness
// constraint declared in user-service-structs/user.go.
func aliceBody(username string) string {
	return fmt.Sprintf(`{
  "metadata": { "name": %q },
  "spec": {
    "username": %q,
    "email": %q,
    "password": "s3cretpass",
    "fullName": "Alice Example",
    "role": "admin",
    "active": true
  }
}`, username, username, username+"@example.com")
}

// TestUserUniqueness verifies that creating two users with the same username is
// rejected and that only one such user ends up stored.
//
// NOTE: This test currently FAILS. It exposes a known Fabrica bug where the
// dedicated-storage path does not enforce the uniqueness constraint. The fix
// lives on the shunr-hpe/dedicated-resources branch; do not work around the
// bug here.
func TestUserUniqueness(t *testing.T) {
	dbURL, stopDB := pgConnURL(t)
	defer stopDB()

	base, stopServer := startServer(t, dbURL)
	defer stopServer()

	// Unique per run so the test is independent of any existing data in a
	// shared/stepped database. Keep it alphanumeric to satisfy validation.
	username := fmt.Sprintf("alice%d", time.Now().UnixNano())
	body := aliceBody(username)

	if code, b := postUser(t, base, body); code != http.StatusCreated {
		t.Fatalf("first POST /users: expected 201 Created, got %d: %s", code, b)
	}

	if code, b := postUser(t, base, body); code == http.StatusCreated {
		t.Errorf("second POST /users with duplicate username succeeded (status %d); expected it to fail: %s", code, b)
	}

	if n := countUsersWithUsername(t, base, username); n != 1 {
		t.Errorf("expected exactly 1 user with username %q, got %d", username, n)
	}
}

// pgConnURL returns a postgres connection URL and a stop function. When
// USER_SERVICE_POSTGRES_URL is set it is used as-is; otherwise a postgres
// container is started and removed by the stop function.
func pgConnURL(t *testing.T) (string, func()) {
	t.Helper()
	if u := os.Getenv("USER_SERVICE_POSTGRES_URL"); u != "" {
		return u, func() {}
	}
	return startPostgres(t)
}

// startPostgres launches a postgres:16 container on port 5432 (or a free port)
// and waits until it accepts connections.
func startPostgres(t *testing.T) (string, func()) {
	t.Helper()

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found; set USER_SERVICE_POSTGRES_URL to use an external postgres")
	}

	port := pickPostgresPort(t)

	// Remove any stale container left behind by a crashed run.
	_ = exec.Command("docker", "rm", "-f", pgContainer).Run()

	run := exec.Command("docker", "run", "--rm", "-d",
		"--name", pgContainer,
		"-p", fmt.Sprintf("%d:5432", port),
		"-e", "POSTGRES_USER=postgres",
		"-e", "POSTGRES_PASSWORD=postgres",
		"-e", "POSTGRES_DB=user_service",
		"postgres:16")
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("failed to start postgres container: %v\n%s", err, out)
	}

	stop := func() { _ = exec.Command("docker", "rm", "-f", pgContainer).Run() }

	if err := waitForPostgres(pgContainer, 90*time.Second); err != nil {
		logs, _ := exec.Command("docker", "logs", pgContainer).CombinedOutput()
		stop()
		t.Fatalf("postgres did not become ready: %v\ncontainer logs:\n%s", err, logs)
	}

	url := fmt.Sprintf("postgres://postgres:postgres@localhost:%d/user_service?sslmode=disable", port)
	return url, stop
}

// pickPostgresPort prefers 5432 when it is free, otherwise an ephemeral port.
func pickPostgresPort(t *testing.T) int {
	t.Helper()
	if portFree(5432) {
		return 5432
	}
	return freePort(t)
}

// portFree reports whether the given TCP port is available on loopback.
func portFree(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

// waitForPostgres polls `pg_isready` inside the container until it succeeds.
func waitForPostgres(container string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cmd := exec.Command("docker", "exec", container, "pg_isready", "-U", "postgres", "-d", "user_service")
		if err := cmd.Run(); err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out after %s", timeout)
}

// startServer builds and launches the postgres user-service on a free port,
// pointed at dbURL, then waits for /health.
func startServer(t *testing.T, dbURL string) (base string, stop func()) {
	t.Helper()

	dir := serviceDir()
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		t.Skipf("generated postgres user-service not found at %s; run 'make generate-postgres' first (%v)", dir, err)
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
		"--database-url", dbURL)
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

// serviceDir returns the location of the generated postgres user-service.
func serviceDir() string {
	if d := os.Getenv("USER_SERVICE_POSTGRES_DIR"); d != "" {
		return d
	}
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "build", "user-service-postgres")
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

// countUsersWithUsername GETs /users and counts entries whose spec.username
// matches the given value.
func countUsersWithUsername(t *testing.T, base, username string) int {
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

	var users []struct {
		Spec struct {
			Username string `json:"username"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(data, &users); err != nil {
		t.Fatalf("failed to decode user list: %v\nbody: %s", err, data)
	}

	n := 0
	for _, u := range users {
		if u.Spec.Username == username {
			n++
		}
	}
	return n
}

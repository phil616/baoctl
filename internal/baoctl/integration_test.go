package baoctl

import (
	"context"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// scripts/test-integration.sh provides a disposable loopback-only dev server.
// Production code never permits HTTP or a supplied admin token.
func TestOpenBao271Integration(t *testing.T) {
	address := os.Getenv("BAOCTL_TEST_ADDRESS")
	if address == "" {
		t.Skip("run scripts/test-integration.sh for OpenBao 2.7.1 integration")
	}
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
		t.Fatal("integration target must be disposable loopback HTTP")
	}
	b, err := os.ReadFile(os.Getenv("BAOCTL_TEST_TOKEN_FILE"))
	if err != nil {
		t.Fatal("test token file missing")
	}
	root := strings.TrimSpace(string(b))
	c := &Client{address: address, http: &http.Client{Timeout: 10 * time.Second}}
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, setup := range []struct {
		path string
		body any
	}{
		{"sys/auth/approle", map[string]any{"type": "approle"}},
		{"sys/auth/userpass", map[string]any{"type": "userpass"}},
		{"sys/mounts/kv", map[string]any{"type": "kv", "options": map[string]string{"version": "2"}}},
		{"sys/policies/acl/test-admin", map[string]string{"policy": `path "*" { capabilities = ["create", "read", "update", "delete", "list", "sudo"] }`}},
		{"auth/userpass/users/test-admin", map[string]any{"password": "fictional-integration-password", "token_policies": []string{"test-admin"}}},
	} {
		must(c.call(ctx, "POST", setup.path, root, setup.body, false, nil))
	}
	for _, app := range []string{"app-a", "app-b", "app-other"} {
		must(c.call(ctx, "POST", "kv/data/prod/apps/"+app+"/env", root, map[string]any{"data": map[string]string{"PASSWORD": "fictional-test-value", "SPECIAL": `  $foo "x" 'y' \ # = end  `, "EMPTY": ""}}, false, nil))
	}
	t.Run("EmptyMachineThenFirstApp", func(t *testing.T) {
		dir, s := testState(t)
		s.Apps = []string{}
		if err := saveState(dir, s); err != nil {
			t.Fatal(err)
		}
		if err := reconcile(ctx, c, root, dir, s); err != nil {
			t.Fatal(err)
		}
		firstRole, firstSecret := s.RoleID, s.SecretID
		if !s.Ready || len(s.Apps) != 0 {
			t.Fatal("empty machine not ready")
		}
		if err := reconcile(ctx, c, root, dir, s); err != nil {
			t.Fatal(err)
		}
		token, err := c.machineLogin(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		_, readErr := c.readKV(ctx, token, "app-a")
		if err := c.revoke(token); err != nil {
			t.Fatal(err)
		}
		if !isStatus(readErr, 403) {
			t.Fatal("empty machine can read application KV")
		}
		// A missing Policy must still be treated as drift for an empty machine.
		if err := c.call(ctx, "DELETE", "sys/policies/acl/"+s.role(), root, nil, false, nil); err != nil {
			t.Fatal(err)
		}
		if err := reconcile(ctx, c, root, dir, s); err == nil {
			t.Fatal("deleted empty-machine policy recreated")
		}
		if err := c.call(ctx, "PUT", "sys/policies/acl/"+s.role(), root, map[string]string{"policy": policy(s, s.Apps)}, false, nil); err != nil {
			t.Fatal(err)
		}
		s.Apps = []string{"app-a"}
		s.Ready = false
		if err := saveState(dir, s); err != nil {
			t.Fatal(err)
		}
		if err := reconcile(ctx, c, root, dir, s); err != nil {
			t.Fatal(err)
		}
		if s.RoleID != firstRole || s.SecretID != firstSecret {
			t.Fatal("adding first app replaced machine credentials")
		}
		if err := syncApp(ctx, c, s, "app-a", 1, io.Discard); err != nil {
			t.Fatal(err)
		}
	})
	dir, s := testState(t)
	admin, err := c.login(ctx, "auth/userpass/login/test-admin", map[string]string{"password": "fictional-integration-password"})
	must(err)
	must(reconcile(ctx, c, admin.Auth.Token, dir, s))
	firstSecret, firstRole := s.SecretID, s.RoleID
	must(reconcile(ctx, c, admin.Auth.Token, dir, s))
	if s.SecretID != firstSecret || s.RoleID != firstRole {
		t.Fatal("repeat init rotated credentials")
	}
	must(c.revoke(admin.Auth.Token))
	// Closing the bootstrap admin session must not invalidate machine credentials.
	must(syncApp(ctx, c, s, "app-a", 1, io.Discard))
	machine, err := c.machineLogin(ctx, s)
	must(err)
	_, err = c.readKV(ctx, machine, "app-other")
	if !isStatus(err, 403) {
		t.Fatal("unrelated application was not denied")
	}
	err = c.call(ctx, "POST", "kv/data/prod/apps/app-a/env", machine, map[string]any{"data": map[string]string{"BAD": "x"}}, false, nil)
	if !isStatus(err, 403) {
		t.Fatal("machine can modify KV")
	}
	err = c.call(ctx, "PUT", "sys/policies/acl/evil", machine, map[string]string{"policy": `path "*" { capabilities=["read"] }`}, false, nil)
	if !isStatus(err, 403) {
		t.Fatal("machine can modify policy")
	}
	must(c.revoke(machine))
	_, err = c.readKV(ctx, machine, "app-a")
	if !isStatus(err, 403) {
		t.Fatal("revoked token remains usable")
	}
	// Verify Docker's actual --env-file parser without printing test values.
	cmd := exec.Command("docker", "run", "--rm", "--network", "none", "--env-file", filepath.Join(s.OutputDir, "app-a.env"), "--entrypoint", "/bin/sh", "openbao/openbao:2.7.1", "-c", `test "$PASSWORD" = 'fictional-test-value' && test "$SPECIAL" = '  $foo "x" '\''y'\'' \ # = end  ' && test "${EMPTY+x}:$EMPTY" = 'x:'`)
	if err := cmd.Run(); err != nil {
		t.Fatal("Docker env round trip failed", err)
	}
	// Add another app while preserving the existing identity and app permissions.
	s.Ready = false
	s.Apps = append(s.Apps, "app-b")
	slices.Sort(s.Apps)
	must(saveState(dir, s))
	must(reconcile(ctx, c, root, dir, s))
	if s.SecretID != firstSecret {
		t.Fatal("app add rotated identity")
	}
	must(syncApp(ctx, c, s, "app-b", 1, io.Discard))
	must(syncApp(ctx, c, s, "app-a", 1, io.Discard))
	// Simulate an interruption after policy write, before local commit.
	s.Ready = false
	s.Apps = append(s.Apps, "app-other")
	must(saveState(dir, s))
	must(c.call(ctx, "PUT", "sys/policies/acl/"+s.role(), root, map[string]string{"policy": policy(s, s.Apps)}, false, nil))
	must(reconcile(ctx, c, root, dir, s))
	if !s.Ready {
		t.Fatal("pending intent not recovered")
	}
	// External changes must never be silently overwritten.
	must(c.call(ctx, "PUT", "sys/policies/acl/"+s.role(), root, map[string]string{"policy": policy(s, s.Apps) + "\n# external change\n"}, false, nil))
	if err := reconcile(ctx, c, root, dir, s); err == nil {
		t.Fatal("policy drift overwritten")
	}
	t.Log("verified: userpass bootstrap, wrapping, independent machine login, idempotency, add/recovery, least privilege, revoke, Docker env literal values, policy drift")
	t.Run("CLIRealTerminal", func(t *testing.T) { testCLIWithTerminal(t, address) })
}

func testCLIWithTerminal(t *testing.T, address string) {
	t.Helper()
	upstream, _ := url.Parse(address)
	proxy := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(upstream))
	defer proxy.Close()
	base, err := os.MkdirTemp("", "baoctl-cli-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: proxy.Certificate().Raw})
	if err := os.WriteFile(filepath.Join(base, "ca.pem"), cert, 0600); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", filepath.Join(base, "baoctl"), "../../cmd/baoctl")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if err := build.Run(); err != nil {
		t.Fatal("CLI build failed", err)
	}
	start := exec.Command("docker", "run", "-d", "--rm", "--log-driver=none", "--network", "host", "--user", "0", "-v", base+":/test:ro", "-e", "SSL_CERT_FILE=/test/ca.pem", "--entrypoint", "/bin/sh", "openbao/openbao:2.7.1", "-c", "sleep 120")
	raw, err := start.Output()
	if err != nil {
		t.Fatal("CLI test container failed", err)
	}
	id := strings.TrimSpace(string(raw))
	defer func() {
		if err := exec.Command("docker", "rm", "-f", id).Run(); err != nil {
			t.Error("test container cleanup failed")
		}
	}()
	pty := exec.Command("python3", "../../scripts/test-tty.py", id, proxy.URL)
	// Python reports only fixed assertions, never the terminal transcript.
	if output, err := pty.CombinedOutput(); err != nil {
		t.Fatalf("terminal integration: %v; %s", err, output)
	}
	t.Log("real CLI: HTTPS init, no password echo, re-init, app add, sync, doctor, no-TTY refusal")
}

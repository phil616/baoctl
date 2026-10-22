package baoctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseOptionalApps(t *testing.T) {
	for _, raw := range []string{"", "   "} {
		apps, err := parseApps(raw)
		if err != nil || len(apps) != 0 {
			t.Fatal("blank applications must be accepted")
		}
	}
	apps, err := parseApps("worker, my-api")
	if err != nil || !slices.Equal(apps, []string{"my-api", "worker"}) {
		t.Fatal("nonempty apps parsed incorrectly")
	}
	for _, raw := range []string{",", "my-api,", ",worker", "my-api,,worker", "my-api,my-api", "../app"} {
		if _, err := parseApps(raw); err == nil {
			t.Fatalf("invalid app list accepted: %q", raw)
		}
	}
}

func TestEmptyMachineState(t *testing.T) {
	dir, s := testState(t)
	s.Targets = []Target{}
	s.AppliedTargets = nil
	s.Ready = true
	s.RoleID, s.SecretID = "fictional-role", "fictional-secret"
	if err := saveState(dir, s); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadState(dir)
	if err != nil || !loaded.Ready || len(loaded.Targets) != 0 {
		t.Fatal("empty ready machine failed to round trip", err)
	}
	if strings.Contains(policy(s, s.Targets), "kv/") {
		t.Fatal("empty machine has KV permissions")
	}
	if err := syncTarget(context.Background(), nil, s, appTarget("app-a"), 0, io.Discard); err == nil {
		t.Fatal("unregistered app accepted")
	}
	var out bytes.Buffer
	if err := status(s, &out); err != nil || !strings.Contains(out.String(), "尚未登记应用") {
		t.Fatal("empty status is unclear", err)
	}
}

func testState(t *testing.T) (string, *State) {
	t.Helper()
	base, err := os.MkdirTemp("", "baoctl-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	dir := filepath.Join(base, "state")
	s := &State{Schema: 2, Address: "https://example.test", Machine: "test-machine", ID: "0123456789abcdef", OutputDir: filepath.Join(base, "env"), Targets: []Target{appTarget("app-a")}}
	if err := saveState(dir, s); err != nil {
		t.Fatal(err)
	}
	return dir, s
}

func TestEnvDockerFormat(t *testing.T) {
	got, err := renderEnv(map[string]any{"EMPTY": "", "SPECIAL": `  $foo "x" 'y' \ # = end  `, "UNICODE": "中文"})
	if err != nil {
		t.Fatal(err)
	}
	want := "EMPTY=\nSPECIAL=  $foo \"x\" 'y' \\ # = end  \nUNICODE=中文\n"
	if string(got) != want {
		t.Fatal("format changed literal values")
	}
	for _, tc := range []map[string]any{
		{}, {"A": "line\nINJECT=bad"}, {"A": "\r"}, {"A": "\x00"}, {"A": "\t"}, {"A": true}, {"A": nil}, {"A": 1.0}, {"A": map[string]any{}}, {"BAD=KEY": "x"}, {"1BAD": "x"}, {"A": string([]byte{0xff})}, {"A": strings.Repeat("x", 60001)},
	} {
		if _, err := renderEnv(tc); err == nil {
			t.Fatal("invalid env accepted")
		}
	}
}

func TestStateAndFileSafety(t *testing.T) {
	dir, s := testState(t)
	s.SecretID = "fictional-secret"
	if err := saveState(dir, s); err != nil {
		t.Fatal(err)
	}
	r, err := loadState(dir)
	if err != nil || r.SecretID != s.SecretID {
		t.Fatal("round trip failed", err)
	}
	if err := checkFile(filepath.Join(dir, "state.json")); err != nil {
		t.Fatal(err)
	}
	lock, err := lockState(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if second, err := lockState(dir); err == nil {
		second.Close()
		t.Fatal("concurrent operation accepted")
	}
	if err := secureDir(s.OutputDir, true); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(s.OutputDir, "app-a.env")
	if err := os.Symlink(filepath.Join(dir, "state.json"), target); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(target, []byte("BAD=x\n")); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(dir, "state.json"), target); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(target, []byte("BAD=x\n")); err == nil {
		t.Fatal("hard link accepted")
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "state.json"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadState(dir); err == nil {
		t.Fatal("public credentials accepted")
	}
	if err := os.Chmod(s.OutputDir, 0777); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(target, []byte("BAD=x\n")); err == nil {
		t.Fatal("unsafe directory accepted")
	}
}

func TestClientRedactionAndRedirect(t *testing.T) {
	leaked := false
	dest := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer dest.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "redirect") {
			http.Redirect(w, r, dest.URL, http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(403)
		fmt.Fprint(w, `{"errors":["fictional-password-and-token"]}`)
	}))
	defer server.Close()
	c, err := NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = server.Client().Transport
	err = c.call(context.Background(), "POST", "login", "fictional-token", nil, false, nil)
	if err == nil || strings.Contains(err.Error(), "fictional") {
		t.Fatal("unsafe API error")
	}
	err = c.call(context.Background(), "POST", "redirect", "fictional-token", nil, false, nil)
	if !isStatus(err, 307) || leaked {
		t.Fatal("redirect followed")
	}
	for _, address := range []string{"http://example.test", "https://user:password@example.test", "https://example.test/path", "https://example.test?token=x"} {
		if _, err := NewClient(address); err == nil {
			t.Fatal("unsafe URL accepted")
		}
	}
}

func mockSync(t *testing.T, s *State, readStatus, revokeStatus int, values map[string]any, policies []string) (*Client, *int) {
	t.Helper()
	return mockTargetSync(t, s, appTarget("app-a"), readStatus, revokeStatus, values, policies)
}

func mockTargetSync(t *testing.T, s *State, target Target, readStatus, revokeStatus int, values map[string]any, policies []string) (*Client, *int) {
	t.Helper()
	revoked := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/approle/login":
			json.NewEncoder(w).Encode(map[string]any{"auth": map[string]any{"client_token": "fictional-machine-token", "policies": policies, "lease_duration": 300}})
		case "/v1/auth/token/revoke-self":
			revoked++
			w.WriteHeader(revokeStatus)
		case "/v1/sys/health":
			fmt.Fprint(w, `{"initialized":true,"sealed":false}`)
		case "/v1/" + target.kvPath():
			if readStatus != 200 {
				w.WriteHeader(readStatus)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"data": values, "metadata": map[string]any{"version": 2}}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	c, _ := NewClient(server.URL)
	c.http.Transport = server.Client().Transport
	return c, &revoked
}

func TestSyncFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		read, revoke, expect int
		data                 map[string]any
		extraPolicy          bool
	}{
		{"denied", 403, 204, 0, map[string]any{"PASSWORD": "fictional-new"}, false},
		{"server down", 503, 204, 0, map[string]any{}, false},
		{"invalid data", 200, 204, 0, map[string]any{"PASSWORD": "bad\nINJECT=x"}, false},
		{"version mismatch", 200, 204, 3, map[string]any{"PASSWORD": "fictional-new"}, false},
		{"revoke failure", 200, 503, 0, map[string]any{"PASSWORD": "fictional-new"}, false},
		{"excess privileges", 200, 204, 0, map[string]any{"PASSWORD": "fictional-new"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, s := testState(t)
			s.Ready = true
			s.AppliedTargets = s.Targets
			s.RoleID = "fictional-role"
			s.SecretID = "fictional-secret"
			path := filepath.Join(s.OutputDir, "app-a.env")
			if err := atomicWrite(path, []byte("PASSWORD=fictional-old\n")); err != nil {
				t.Fatal(err)
			}
			policies := []string{s.role()}
			if tc.extraPolicy {
				policies = append(policies, "platform-admin")
			}
			c, revoked := mockSync(t, s, tc.read, tc.revoke, tc.data, policies)
			var out bytes.Buffer
			if err := syncTarget(context.Background(), c, s, appTarget("app-a"), tc.expect, &out); err == nil {
				t.Fatal("expected failure")
			}
			b, _ := os.ReadFile(path)
			if string(b) != "PASSWORD=fictional-old\n" {
				t.Fatal("old env replaced on failure")
			}
			if *revoked != 1 {
				t.Fatal("token cleanup missing")
			}
			if strings.Contains(out.String(), "fictional") {
				t.Fatal("output contains secret")
			}
		})
	}
}

func TestSyncSuccessAndUnregistered(t *testing.T) {
	_, s := testState(t)
	s.Ready = true
	s.AppliedTargets = s.Targets
	s.RoleID = "fictional-role"
	s.SecretID = "fictional-secret"
	c, revoked := mockSync(t, s, 200, 204, map[string]any{"PASSWORD": "fictional-new"}, []string{s.role()})
	var out bytes.Buffer
	if err := syncTarget(context.Background(), c, s, appTarget("app-a"), 2, &out); err != nil {
		t.Fatal(err)
	}
	if *revoked != 1 || strings.Contains(out.String(), "fictional-new") {
		t.Fatal("unsafe success")
	}
	if err := checkFile(filepath.Join(s.OutputDir, "app-a.env")); err != nil {
		t.Fatal(err)
	}
	if err := syncTarget(context.Background(), c, s, appTarget("other"), 0, io.Discard); err == nil {
		t.Fatal("unregistered app accepted")
	}
	if *revoked != 1 {
		t.Fatal("unregistered request reached server")
	}
}

func TestPolicyIsExactAndNoDefaults(t *testing.T) {
	_, s := testState(t)
	p := policy(s, s.Targets)
	if strings.Contains(p, "*") || strings.Contains(p, "platform-admin") || !strings.Contains(p, `kv/data/prod/apps/app-a/env`) {
		t.Fatal("policy too broad")
	}
	r := expectedRole(s)
	if !r.matches(s) {
		t.Fatal("expected role rejected")
	}
	r.NoDefault = false
	if r.matches(s) {
		t.Fatal("default policy accepted")
	}
}

func TestMalformedResponseDoesNotLeak(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "fictional-secret") }))
	defer server.Close()
	c, _ := NewClient(server.URL)
	c.http.Transport = server.Client().Transport
	var kv KV
	err := c.call(context.Background(), "GET", "test", "", nil, false, &kv)
	if err == nil || strings.Contains(err.Error(), "fictional") {
		t.Fatal("malformed response leaked")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = c.call(ctx, "GET", "test", "", nil, false, nil)
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatal("cancellation not handled")
	}
}

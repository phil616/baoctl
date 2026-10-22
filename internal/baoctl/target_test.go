package baoctl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func fileValues(data []byte, name string) map[string]any {
	digest := sha256.Sum256(data)
	return map[string]any{"B64": base64.StdEncoding.EncodeToString(data), "FILENAME": name, "SHA256": hex.EncodeToString(digest[:])}
}

func TestFileDecode(t *testing.T) {
	for _, data := range [][]byte{[]byte("hello"), {}, {0, 255, '\n', '\r', '\t', 128}} {
		for _, quotes := range []string{"", "'", "\"", "'\""} {
			values := fileValues(data, "中文.txt")
			values["B64"] = quotes + values["B64"].(string) + quotes
			values["SHA256"] = strings.ToUpper(values["SHA256"].(string))
			name, got, err := decodeFile(values)
			if err != nil || name != "中文.txt" || !bytes.Equal(got, data) {
				t.Fatal("file did not round trip", err)
			}
		}
	}
	// Quote characters are discarded even when only one side is present.
	values := fileValues([]byte("hello"), "hello.txt")
	values["B64"] = "'aGVs\"bG8="
	if _, data, err := decodeFile(values); err != nil || string(data) != "hello" {
		t.Fatal("unmatched quotes not discarded", err)
	}
}

func TestTargetSyncPathsAndIsolation(t *testing.T) {
	for _, classified := range []string{"prod", "test", "dev"} {
		for _, typename := range []string{"apps", "files"} {
			t.Run(classified+"/"+typename, func(t *testing.T) {
				_, s := testState(t)
				target := Target{"app-a", classified, typename}
				s.Targets, s.AppliedTargets = []Target{target}, []Target{target}
				s.Ready, s.RoleID, s.SecretID = true, "fictional-role", "fictional-secret"
				data := []byte("hello\n\x00\xff")
				values := fileValues(data, "hello.txt")
				path := filepath.Join(s.OutputDir, "app-a", classified, "hello.txt")
				if typename == "apps" {
					values = map[string]any{"MODE": classified}
					data = []byte("MODE=" + classified + "\n")
					suffix := map[string]string{"prod": "", "test": ".test", "dev": ".development"}[classified]
					path = filepath.Join(s.OutputDir, "app-a.env"+suffix)
				}
				c, revoked := mockTargetSync(t, s, target, 200, 204, values, []string{s.role()})
				for i := 0; i < 2; i++ {
					if err := syncTarget(context.Background(), c, s, target, 2, io.Discard); err != nil {
						t.Fatal(err)
					}
				}
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, data) || *revoked != 2 {
					t.Fatal("incorrect output or token cleanup", err)
				}
				if err := checkFile(path); err != nil {
					t.Fatal(err)
				}
				if err := secureDir(filepath.Dir(path), false); err != nil {
					t.Fatal(err)
				}
				var out bytes.Buffer
				if err := status(s, &out); err != nil || !strings.Contains(out.String(), path) || !strings.Contains(out.String(), target.String()) {
					t.Fatal("status missing classified output", err)
				}
				for _, other := range []Target{{"app-a", "test", "apps"}, {"app-a", "prod", "files"}, {"app-a", "dev", "files"}} {
					if other == target {
						continue
					}
					if err := syncTarget(context.Background(), nil, s, other, 0, io.Discard); err == nil {
						t.Fatal("another environment/type accepted without registration")
					}
				}
			})
		}
	}
}

func TestFileSyncFailurePreservesOutput(t *testing.T) {
	for _, scenario := range []string{"base64", "digest mismatch", "digest format", "missing", "extra", "type", "filename", "version", "read", "revoke", "symlink file", "symlink directory"} {
		t.Run(scenario, func(t *testing.T) {
			_, s := testState(t)
			target := Target{"app-a", "test", "files"}
			s.Targets, s.AppliedTargets = []Target{target}, []Target{target}
			s.Ready, s.RoleID, s.SecretID = true, "fictional-role", "fictional-secret"
			path := filepath.Join(s.OutputDir, "app-a", "test", "hello.txt")
			old := []byte("fictional-old")
			if err := atomicWrite(path, old); err != nil {
				t.Fatal(err)
			}
			values := fileValues([]byte("fictional-new"), "hello.txt")
			readStatus, revokeStatus, version := 200, 204, 2
			switch scenario {
			case "base64":
				values["B64"] = "fictional-invalid-@@@"
			case "digest mismatch":
				values["SHA256"] = strings.Repeat("0", 64)
			case "digest format":
				values["SHA256"] = "fictional-invalid"
			case "missing":
				delete(values, "SHA256")
			case "extra":
				values["EXTRA"] = "fictional-extra"
			case "type":
				values["B64"] = true
			case "filename":
				values["FILENAME"] = "../../fictional-escape"
			case "version":
				version = 3
			case "read":
				readStatus = 403
			case "revoke":
				revokeStatus = 503
			case "symlink file":
				original := filepath.Join(s.OutputDir, "original")
				if err := os.Rename(path, original); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(original, path); err != nil {
					t.Fatal(err)
				}
			case "symlink directory":
				original := filepath.Join(s.OutputDir, "original")
				if err := os.Rename(filepath.Dir(path), original); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(original, filepath.Dir(path)); err != nil {
					t.Fatal(err)
				}
			}
			c, revoked := mockTargetSync(t, s, target, readStatus, revokeStatus, values, []string{s.role()})
			var out bytes.Buffer
			err := syncTarget(context.Background(), c, s, target, version, &out)
			if err == nil || strings.Contains(err.Error()+out.String(), "fictional") {
				t.Fatal("expected redacted failure")
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, old) || *revoked != 1 {
				t.Fatal("old output replaced or cleanup missing", err)
			}
		})
	}
}

func TestRejectUnsafeFilenames(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../escape", "/tmp/escape", "sub/file", `sub\file`, "bad\x00", "bad\nname", ".baoctl-temp", strings.Repeat("x", 256), string([]byte{0xff})} {
		if _, _, err := decodeFile(fileValues([]byte("hello"), name)); err == nil {
			t.Fatalf("unsafe filename accepted: %q", name)
		}
	}
}

func TestDoctorChecksFileDigestWithoutWriting(t *testing.T) {
	for _, valid := range []bool{true, false} {
		_, s := testState(t)
		target := Target{"app-a", "dev", "files"}
		s.Targets, s.AppliedTargets = []Target{target}, []Target{target}
		s.Ready, s.RoleID, s.SecretID = true, "fictional-role", "fictional-secret"
		if err := secureDir(s.OutputDir, true); err != nil {
			t.Fatal(err)
		}
		values := fileValues([]byte("hello"), "hello.txt")
		if !valid {
			values["SHA256"] = strings.Repeat("0", 64)
		}
		c, revoked := mockTargetSync(t, s, target, 200, 204, values, []string{s.role()})
		err := doctor(context.Background(), c, s, io.Discard)
		if (err == nil) != valid || *revoked != 1 {
			t.Fatal("doctor failed validation or cleanup", err)
		}
		entries, err := os.ReadDir(s.OutputDir)
		if err != nil || len(entries) != 0 {
			t.Fatal("doctor wrote output", err)
		}
	}
}

func TestLegacyStateMigration(t *testing.T) {
	for _, stage := range []string{"empty", "ready", "pending"} {
		t.Run(stage, func(t *testing.T) {
			dir, s := testState(t)
			legacy := map[string]any{
				"schema": 1, "address": s.Address, "machine": s.Machine, "id": s.ID,
				"output_dir": s.OutputDir, "role_id": "fictional-role", "secret_id": "fictional-secret",
				"apps": []string{"app-a"}, "applied_apps": []string{"app-a"}, "ready": true,
			}
			if stage == "empty" {
				legacy["apps"], legacy["applied_apps"] = []string{}, []string{}
			} else if stage == "pending" {
				legacy["apps"], legacy["ready"] = []string{"app-a", "app-b"}, false
			}
			data, _ := json.Marshal(legacy)
			if err := atomicWrite(filepath.Join(dir, "state.json"), data); err != nil {
				t.Fatal(err)
			}
			loaded, err := loadState(dir)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Schema != 2 || loaded.RoleID != "fictional-role" || loaded.SecretID != "fictional-secret" || loaded.Ready != (stage != "pending") {
				t.Fatal("legacy identity or pending intent lost")
			}
			wantPolicy := "# Managed by baoctl; identity=" + s.ID + "\npath \"auth/token/revoke-self\" {\n  capabilities = [\"update\"]\n}\n"
			for _, name := range legacy["apps"].([]string) {
				wantPolicy += "path \"kv/data/prod/apps/" + name + "/env\" {\n  capabilities = [\"read\"]\n}\n"
			}
			if policy(loaded, loaded.Targets) != wantPolicy {
				t.Fatal("legacy policy changed during migration")
			}
			if err := saveState(dir, loaded); err != nil {
				t.Fatal(err)
			}
			roundTrip, err := loadState(dir)
			if err != nil || !slices.Equal(loaded.Targets, roundTrip.Targets) || !slices.Equal(loaded.AppliedTargets, roundTrip.AppliedTargets) {
				t.Fatal("migrated state did not round trip", err)
			}
		})
	}
}

func TestTargetValidationAndPolicy(t *testing.T) {
	_, s := testState(t)
	s.Targets = []Target{{"app-a", "test", "apps"}, {"app-a", "dev", "files"}}
	p := policy(s, s.Targets)
	if strings.Contains(p, "*") || strings.Contains(p, "prod/") || !strings.Contains(p, "kv/data/test/apps/app-a/env") || !strings.Contains(p, "kv/data/dev/files/app-a/env") {
		t.Fatal("policy does not match exact environment/type")
	}
	for _, target := range []Target{{"app-a", "production", "apps"}, {"app-a", "prod", "file"}, {"../escape", "test", "files"}} {
		s.Targets = []Target{target}
		if err := saveState(filepath.Join(s.OutputDir, "invalid-state"), s); err == nil {
			t.Fatal("invalid target state saved")
		}
		if _, err := (*Client)(nil).readKV(context.Background(), "", target); err == nil {
			t.Fatal("invalid target reached network")
		}
	}
	s.Targets = []Target{appTarget("app-a"), appTarget("app-a")}
	if err := s.validate(); err == nil {
		t.Fatal("duplicate target accepted")
	}
}

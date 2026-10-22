package baoctl

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Target identifies one independently authorized KV secret.
type Target struct {
	Name       string `json:"name"`
	Classified string `json:"classified"`
	Typename   string `json:"typename"`
}

func appTarget(name string) Target { return Target{name, "prod", "apps"} }

func targetFlags(f *flag.FlagSet, name string) *Target {
	target := appTarget(name)
	f.StringVar(&target.Classified, "classified", "prod", "环境：prod、test、dev")
	f.StringVar(&target.Typename, "typename", "apps", "类型：apps、files")
	return &target
}

func sortTargets(targets []Target) {
	slices.SortFunc(targets, func(a, b Target) int { return strings.Compare(a.String(), b.String()) })
}

// File names come from KV at sync time. Status inspects the private directory
// locally, including previously synced names, without authenticating to Bao.
func localOutputPaths(base string, target Target) ([]string, error) {
	dir := target.outputDir(base)
	if err := secureDir(dir, false); err != nil {
		return nil, err
	}
	if target.Typename == "apps" {
		return []string{filepath.Join(dir, target.envFilename())}, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		paths = append(paths, filepath.Join(dir, entry.Name()))
	}
	return paths, nil
}

func (t Target) validate() error {
	if err := validName(t.Name); err != nil {
		return err
	}
	if t.Classified != "prod" && t.Classified != "test" && t.Classified != "dev" {
		return errors.New("--classified 必须是 prod、test 或 dev")
	}
	if t.Typename != "apps" && t.Typename != "files" {
		return errors.New("--typename 必须是 apps 或 files")
	}
	return nil
}

func (t Target) String() string { return t.Classified + "/" + t.Typename + "/" + t.Name }
func (t Target) kvPath() string { return "kv/data/" + t.String() + "/env" }

func (t Target) outputDir(base string) string {
	if t.Typename == "files" {
		return filepath.Join(base, t.Name, t.Classified)
	}
	return base
}

func (t Target) envFilename() string {
	suffix := map[string]string{"prod": "", "test": ".test", "dev": ".development"}[t.Classified]
	return t.Name + ".env" + suffix
}

// render returns a safe basename and validated content, without touching disk.
func (t Target) render(values map[string]any) (string, []byte, error) {
	if err := t.validate(); err != nil {
		return "", nil, err
	}
	if t.Typename == "files" {
		return decodeFile(values)
	}
	data, err := renderEnv(values)
	return t.envFilename(), data, err
}

func decodeFile(values map[string]any) (string, []byte, error) {
	if len(values) != 3 {
		return "", nil, errors.New("文件类 KV 必须且只能包含 B64、FILENAME、SHA256 三个字符串字段")
	}
	b64, bok := values["B64"].(string)
	name, nok := values["FILENAME"].(string)
	digest, sok := values["SHA256"].(string)
	if !bok || !nok || !sok {
		return "", nil, errors.New("文件类 KV 必须包含 B64、FILENAME、SHA256 三个字符串字段")
	}
	if name == "" || name == "." || name == ".." || len(name) > 255 || !utf8.ValidString(name) || strings.ContainsAny(name, "/\\") || strings.ContainsFunc(name, unicode.IsControl) || strings.HasPrefix(name, ".baoctl-") {
		return "", nil, errors.New("FILENAME 必须是有效的单个文件名，不能包含路径或控制字符，内容已隐藏")
	}
	want, err := hex.DecodeString(digest)
	if err != nil || len(want) != sha256.Size {
		return "", nil, errors.New("SHA256 必须是 64 位十六进制摘要，内容已隐藏")
	}
	// Quotes are not part of the Base64 alphabet. Accept values copied with
	// single/double quotes, including unmatched quotes, without shell parsing.
	b64 = strings.TrimSpace(strings.NewReplacer("'", "", "\"", "").Replace(b64))
	data, err := base64.StdEncoding.Strict().DecodeString(b64)
	if err != nil {
		return "", nil, errors.New("B64 不是有效的 Base64，内容已隐藏")
	}
	actual := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(actual[:]), digest) {
		return "", nil, errors.New("文件 SHA256 校验失败，已阻止写入")
	}
	return name, data, nil
}

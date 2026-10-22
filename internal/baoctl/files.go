package baoctl

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

func validName(s string) error {
	if !namePattern.MatchString(s) {
		return errors.New("名称必须为 1–63 位小写字母、数字、下划线或连字符，且以字母或数字开头")
	}
	return nil
}

type State struct {
	Schema         int      `json:"schema"`
	Address        string   `json:"address"`
	Machine        string   `json:"machine"`
	ID             string   `json:"id"`
	OutputDir      string   `json:"output_dir"`
	Targets        []Target `json:"targets"`
	AppliedTargets []Target `json:"applied_targets"`
	Ready          bool     `json:"ready"`
	RoleID         string   `json:"role_id,omitempty"`
	SecretID       string   `json:"secret_id,omitempty"`
	SecretAccessor string   `json:"secret_accessor,omitempty"`
}

func (s *State) role() string { return "baoctl-" + s.Machine + "-" + s.ID }
func (s *State) validate() error {
	if s.Schema != 2 {
		return errors.New("不支持的本地状态版本")
	}
	if err := validateAddress(s.Address); err != nil {
		return err
	}
	if err := validName(s.Machine); err != nil {
		return err
	}
	if matched, _ := regexp.MatchString(`^[a-f0-9]{16}$`, s.ID); !matched {
		return errors.New("本地身份标识无效")
	}
	if !filepath.IsAbs(s.OutputDir) || filepath.Clean(s.OutputDir) != s.OutputDir || s.OutputDir == "/" {
		return errors.New("输出目录必须为规范绝对路径")
	}
	for _, prefix := range []string{"/etc/openbao", "/etc/openbao-agent", "/run/openbao-agent"} {
		if s.OutputDir == prefix || strings.HasPrefix(s.OutputDir, prefix+"/") {
			return errors.New("不能复用现有 OpenBao/Agent 目录")
		}
	}
	seen := map[Target]bool{}
	for _, app := range s.Targets {
		if err := app.validate(); err != nil {
			return err
		}
		if seen[app] {
			return errors.New("登记目标重复")
		}
		seen[app] = true
	}
	applied := map[Target]bool{}
	for _, app := range s.AppliedTargets {
		if applied[app] {
			return errors.New("已授权目标重复")
		}
		applied[app] = true
		if !seen[app] {
			return errors.New("状态不一致：已授权目标不在登记列表中")
		}
	}
	if s.Ready && (s.RoleID == "" || s.SecretID == "" || !slices.Equal(s.Targets, s.AppliedTargets)) {
		return errors.New("本地就绪状态与凭据/授权不一致")
	}
	return nil
}

// Validate every ancestor before accessing a secret. Sticky /tmp is allowed for
// isolated tests; the actual private directory must be owned and mode 0700.
func secureDir(path string, create bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return errors.New("目录必须为规范绝对路径，且不能是根目录")
	}
	current := "/"
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) && create {
			if err = os.Mkdir(current, 0700); err != nil {
				return fmt.Errorf("创建受保护目录失败: %w", err)
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("路径包含符号链接或非目录对象")
		}
		st := info.Sys().(*syscall.Stat_t)
		if st.Uid != 0 && st.Uid != uint32(os.Geteuid()) {
			return errors.New("目录不属于当前用户或 root")
		}
		if info.Mode().Perm()&0022 != 0 && !(info.Mode()&os.ModeSticky != 0 && i < len(parts)-1 && st.Uid == 0) {
			return errors.New("目录允许其他用户写入")
		}
		if i == len(parts)-1 && (info.Mode().Perm() != 0700 || st.Uid != uint32(os.Geteuid())) {
			return errors.New("私有目录必须由当前用户所有且权限为 0700，请先核对目录用途")
		}
	}
	return nil
}

func checkFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	st := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
		return errors.New("文件必须为当前用户所有的普通文件、权限 0600，且不能是链接")
	}
	return nil
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := secureDir(dir, true); err != nil {
		return err
	}
	if err := checkFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(dir, ".baoctl-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func readPrivate(path string) ([]byte, error) {
	if err := secureDir(filepath.Dir(path), false); err != nil {
		return nil, err
	}
	if err := checkFile(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxResponse+1))
	if len(b) > maxResponse {
		return nil, errors.New("本地文件超过大小限制")
	}
	return b, err
}
func loadState(dir string) (*State, error) {
	b, err := readPrivate(filepath.Join(dir, "state.json"))
	if err != nil {
		return nil, err
	}
	var s State
	if json.Unmarshal(b, &s) != nil {
		return nil, errors.New("本地状态 JSON 无效，内容已隐藏")
	}
	if s.Schema == 1 {
		// Preserve ordering so existing managed policies remain byte-identical.
		var legacy struct {
			Apps        []string `json:"apps"`
			AppliedApps []string `json:"applied_apps"`
		}
		if json.Unmarshal(b, &legacy) != nil || len(s.Targets) != 0 || len(s.AppliedTargets) != 0 {
			return nil, errors.New("旧版本地状态无效，内容已隐藏")
		}
		for _, name := range legacy.Apps {
			s.Targets = append(s.Targets, appTarget(name))
		}
		for _, name := range legacy.AppliedApps {
			s.AppliedTargets = append(s.AppliedTargets, appTarget(name))
		}
		s.Schema = 2
	}
	if err := s.validate(); err != nil {
		return nil, err
	}
	return &s, nil
}
func saveState(dir string, s *State) error {
	if err := s.validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return errors.New("无法编码本地状态")
	}
	return atomicWrite(filepath.Join(dir, "state.json"), append(b, '\n'))
}

// A stable lock inode serializes all CLI commands on this machine.
func lockState(dir string) (*os.File, error) {
	if err := secureDir(dir, true); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, ".lock")
	if err := checkFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("已有 baoctl 操作正在运行，请等待完成后重试")
	}
	return f, nil
}

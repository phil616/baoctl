package baoctl

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/term"
)

// Release builds set Version from the tag using the Go linker's -X flag.
var Version = "0.1.0"
const usage = `baoctl — 本机 OpenBao 配置同步（Linux amd64）

用法：baoctl [--state-dir /etc/baoctl] <命令>

  init [--address URL] [--machine NAME] [--apps app1,app2]
       [--username admin] [--output-dir /run/baoctl]
       交互初始化机器身份或恢复接入；应用可留空，--apps 可选
  app add <APP> [--username admin]
       交互授权本机读取一个现有应用，保留已有授权
  sync <APP> [--expect-version N]
       自动认证、拉取最新 KV v2 配置、原子生成 env；失败返回非零
  status
       仅查看本地登记及文件状态，不证明配置是最新版本
  doctor
       检查连接、机器认证、所有登记应用读取及格式，不写 env
  version

初始化/授权的密码仅从终端隐藏输入，不接受密码参数或环境变量。
除 help/version 外需 root。固定 KV 路径：kv/prod/apps/<APP>/env。
sync 输出：/run/baoctl/<APP>.env（默认），root:root 0600。
`

func flags(name string, w io.Writer) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	// flag's default errors echo unknown arguments, which may contain secrets.
	f.SetOutput(io.Discard)
	f.Usage = func() { fmt.Fprint(w, usage) }
	return f
}
func parse(f *flag.FlagSet, args []string) error {
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return errors.New("命令参数无效，运行 baoctl help 查看用法")
	}
	return nil
}

func Run(ctx context.Context, args []string, out, errOut io.Writer) error {
	global := flags("baoctl", out)
	dir := global.String("state-dir", "/etc/baoctl", "私有状态目录")
	if err := parse(global, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	args = global.Args()
	if len(args) == 0 || args[0] == "help" {
		fmt.Fprint(out, usage)
		return nil
	}
	if args[0] == "version" {
		fmt.Fprintln(out, Version)
		return nil
	}
	if !slices.Contains([]string{"init", "app", "sync", "status", "doctor"}, args[0]) {
		return errors.New("未知命令，运行 baoctl help 查看用法")
	}
	if os.Geteuid() != 0 {
		return errors.New("此命令需要 root，请使用 sudo baoctl")
	}
	// Do not create state directories for read-only commands on uninitialized hosts.
	if args[0] != "init" {
		if err := secureDir(*dir, false); err != nil {
			return errors.New("本机状态目录不可用，请先运行 init 或检查目录权限")
		}
	}
	lock, err := lockState(*dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	switch args[0] {
	case "init":
		return initialize(ctx, *dir, args[1:], out)
	case "app":
		if len(args) < 3 || args[1] != "add" {
			return errors.New("用法：baoctl app add <APP> [--username admin]")
		}
		if err := validName(args[2]); err != nil {
			return err
		}
		f := flags("app add", out)
		username := f.String("username", "admin", "管理员用户名")
		if err := parse(f, args[3:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if f.NArg() != 0 {
			return errors.New("app add 含多余参数")
		}
		s, err := loadState(*dir)
		if err != nil {
			return err
		}
		if !s.Ready {
			return errors.New("存在未完成接入，请先运行 init 恢复")
		}
		return withAdmin(ctx, s, *username, out, func(c *Client, token string) error {
			if !slices.Contains(s.Apps, args[2]) {
				// Validate before saving intent to avoid pending state on simple typos.
				kv, err := c.readKV(ctx, token, args[2])
				if err != nil {
					return err
				}
				if _, err = renderEnv(kv.Data.Data); err != nil {
					return err
				}
				s.Apps = append(s.Apps, args[2])
				slices.Sort(s.Apps)
				s.Ready = false
				if err := saveState(*dir, s); err != nil {
					return err
				}
			}
			if err := reconcile(ctx, c, token, *dir, s); err != nil {
				return err
			}
			fmt.Fprintf(out, "应用 %s 已授权；运行 baoctl sync %s 生成文件\n", args[2], args[2])
			return nil
		})
	case "sync":
		if len(args) < 2 {
			return errors.New("用法：baoctl sync <APP> [--expect-version N]")
		}
		if err := validName(args[1]); err != nil {
			return err
		}
		f := flags("sync", out)
		version := f.Int("expect-version", 0, "要求当前 KV 版本")
		if err := parse(f, args[2:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if f.NArg() != 0 || *version < 0 {
			return errors.New("sync 参数无效")
		}
		s, err := loadState(*dir)
		if err != nil {
			return err
		}
		c, err := NewClient(s.Address)
		if err != nil {
			return err
		}
		return syncApp(ctx, c, s, args[1], *version, out)
	case "status", "doctor":
		if len(args) != 1 {
			return errors.New("status/doctor 不接受额外参数")
		}
		s, err := loadState(*dir)
		if err != nil {
			return err
		}
		if args[0] == "status" {
			return status(s, out)
		}
		c, err := NewClient(s.Address)
		if err != nil {
			return err
		}
		return doctor(ctx, c, s, out)
	}
	return errors.New("未实现的命令")
}

type terminal struct {
	f      *os.File
	reader *bufio.Reader
	ctx    context.Context
}

func openTerminal(ctx context.Context) (*terminal, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil || !term.IsTerminal(int(f.Fd())) {
		if f != nil {
			f.Close()
		}
		return nil, errors.New("管理操作需要交互终端；SSH 请使用 -t，CI 只调用 sync")
	}
	return &terminal{f, bufio.NewReader(f), ctx}, nil
}
func (t *terminal) line(label, fallback string) (string, error) {
	if fallback != "" {
		fmt.Fprintf(t.f, "%s [%s]: ", label, fallback)
	} else {
		fmt.Fprintf(t.f, "%s: ", label)
	}
	type lineResult struct {
		line string
		err  error
	}
	ch := make(chan lineResult, 1)
	go func() { line, err := t.reader.ReadString('\n'); ch <- lineResult{line, err} }()
	var line string
	var err error
	select {
	case <-t.ctx.Done():
		return "", errors.New("终端输入已取消")
	case r := <-ch:
		line, err = r.line, r.err
	}
	if err != nil {
		return "", errors.New("终端输入中断")
	}
	line = strings.TrimSpace(line)
	if line == "" {
		line = fallback
	}
	return line, nil
}
func (t *terminal) password(ctx context.Context) (string, error) {
	fd := int(t.f.Fd())
	state, err := term.GetState(fd)
	if err != nil {
		return "", errors.New("无法读取终端状态")
	}
	defer term.Restore(fd, state)
	fmt.Fprint(t.f, "管理员密码（不回显）: ")
	type result struct {
		b   []byte
		err error
	}
	ch := make(chan result, 1)
	go func() { b, err := term.ReadPassword(fd); ch <- result{b, err} }()
	select {
	case <-ctx.Done():
		return "", errors.New("密码输入已取消")
	case r := <-ch:
		fmt.Fprintln(t.f)
		if r.err != nil {
			return "", errors.New("密码读取失败")
		}
		defer clear(r.b)
		if len(r.b) == 0 {
			return "", errors.New("密码不能为空")
		}
		return string(r.b), nil
	}
}

func initialize(ctx context.Context, dir string, args []string, out io.Writer) error {
	f := flags("init", out)
	address := f.String("address", "", "Server HTTPS 地址")
	machine := f.String("machine", "", "机器名称")
	apps := f.String("apps", "", "可选的逗号分隔应用名；留空仅初始化机器")
	username := f.String("username", "admin", "管理员用户名")
	output := f.String("output-dir", "", "env 输出目录")
	if err := parse(f, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return errors.New("init 含多余参数")
	}
	appsProvided := false
	f.Visit(func(f *flag.Flag) {
		if f.Name == "apps" {
			appsProvided = true
		}
	})
	s, err := loadState(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	newState := s == nil
	if s != nil {
		if *address != "" || *machine != "" || appsProvided || *output != "" {
			return errors.New("本机已登记；恢复请直接运行 init，新增应用请用 app add")
		}
		fmt.Fprintf(out, "核对/恢复本机 %s，身份 %s\n", s.Machine, s.role())
	} else {
		t, err := openTerminal(ctx)
		if err != nil {
			return err
		}
		defer t.f.Close()
		if *address == "" {
			*address, err = t.line("OpenBao 地址", "https://openbao.example.com")
			if err != nil {
				return err
			}
		}
		if *machine == "" {
			hostname, _ := os.Hostname()
			*machine, err = t.line("机器名称", strings.ToLower(hostname))
			if err != nil {
				return err
			}
		}
		if !appsProvided {
			*apps, err = t.line("本机应用（可留空；多个用逗号分隔，填写时需已在 Web UI 创建）", "")
			if err != nil {
				return err
			}
		}
		if *output == "" {
			*output = "/run/baoctl"
		}
		var id [8]byte
		if _, err = rand.Read(id[:]); err != nil {
			return errors.New("生成机器标识失败")
		}
		s = &State{Schema: 1, Address: strings.TrimRight(*address, "/"), Machine: *machine, ID: hex.EncodeToString(id[:]), OutputDir: *output}
		s.Apps, err = parseApps(*apps)
		if err != nil {
			return err
		}
		if err := s.validate(); err != nil {
			return err
		}
		if err := secureDir(s.OutputDir, true); err != nil {
			return err
		}
		if len(s.Apps) == 0 {
			fmt.Fprintf(t.f, "将初始化本机 %s 的机器身份，暂不授予任何应用读取权限。\n", s.Machine)
		} else {
			fmt.Fprintf(t.f, "将接入本机 %s，为 %s 创建只读身份；不操作现有 Agent。\n", s.Machine, strings.Join(s.Apps, ", "))
		}
		yes, err := t.line("继续？输入 yes", "")
		if err != nil {
			return err
		}
		if yes != "yes" {
			return errors.New("操作已取消")
		}
	}
	return withAdmin(ctx, s, *username, out, func(c *Client, token string) error {
		if newState {
			// Do not pin a typo or an inaccessible Server into local state.
			for _, app := range s.Apps {
				kv, err := c.readKV(ctx, token, app)
				if err != nil {
					return fmt.Errorf("应用 %s 预检失败: %w", app, err)
				}
				if _, err := renderEnv(kv.Data.Data); err != nil {
					return err
				}
			}
			if err := saveState(dir, s); err != nil {
				return err
			}
		}
		if err := reconcile(ctx, c, token, dir, s); err != nil {
			return fmt.Errorf("接入未完成；修复原因后运行 baoctl init 恢复: %w", err)
		}
		if len(s.Apps) == 0 {
			fmt.Fprintln(out, "本机身份已初始化并验证，尚未登记应用。创建应用 KV 后，用 baoctl app add <APP> 授权。")
		} else {
			fmt.Fprintln(out, "本机身份与所有应用读取权限已验证。用 baoctl sync <APP> 生成 env。")
		}
		return nil
	})
}

func parseApps(raw string) ([]string, error) {
	apps := []string{}
	if strings.TrimSpace(raw) == "" {
		return apps, nil
	}
	seen := map[string]bool{}
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if err := validName(name); err != nil {
			return nil, err
		}
		if seen[name] {
			return nil, errors.New("应用列表重复")
		}
		seen[name] = true
		apps = append(apps, name)
	}
	slices.Sort(apps)
	return apps, nil
}

func withAdmin(ctx context.Context, s *State, username string, out io.Writer, fn func(*Client, string) error) (result error) {
	if ok, _ := regexp.MatchString(`^[A-Za-z0-9_.@-]{1,128}$`, username); !ok {
		return errors.New("管理员用户名格式无效")
	}
	t, err := openTerminal(ctx)
	if err != nil {
		return err
	}
	defer t.f.Close()
	var password string
	username, err = t.line("管理员用户名", username)
	if err != nil {
		return err
	}
	if ok, _ := regexp.MatchString(`^[A-Za-z0-9_.@-]{1,128}$`, username); !ok {
		return errors.New("管理员用户名格式无效")
	}
	c, err := NewClient(s.Address)
	if err != nil {
		return err
	}
	if err := c.health(ctx); err != nil {
		return err
	}
	password, err = t.password(ctx)
	if err != nil {
		return err
	}
	r, err := c.login(ctx, "auth/userpass/login/"+url.PathEscape(username), map[string]string{"password": password})
	password = ""
	if err != nil {
		return err
	}
	defer func() {
		if err := c.revoke(r.Auth.Token); err != nil {
			result = errors.Join(result, fmt.Errorf("临时管理员会话撤销失败，请在可信终端检查会话；密码未保存: %w", err))
		}
	}()
	return fn(c, r.Auth.Token)
}

func status(s *State, out io.Writer) error {
	fmt.Fprintf(out, "机器：%s\nServer：%s\n身份：%s\n接入完成：%t\n", s.Machine, s.Address, s.role(), s.Ready)
	if len(s.Apps) == 0 {
		fmt.Fprintln(out, "尚未登记应用；创建应用 KV 后，用 baoctl app add <APP> 授权。")
	}
	for _, app := range s.Apps {
		path := filepath.Join(s.OutputDir, app+".env")
		if err := secureDir(s.OutputDir, false); err != nil {
			fmt.Fprintf(out, "%s：输出目录不存在或权限异常\n", app)
			continue
		}
		if err := checkFile(path); err != nil {
			fmt.Fprintf(out, "%s：文件不存在或权限异常\n", app)
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s：文件存在，写入时间 %s，%s\n", app, info.ModTime().Format("2006-01-02T15:04:05Z07:00"), path)
	}
	fmt.Fprintln(out, "本地文件存在不代表最新配置；发布前必须成功执行 sync。")
	return nil
}

func doctor(ctx context.Context, c *Client, s *State, out io.Writer) (result error) {
	if !s.Ready {
		return errors.New("接入未完成，请运行 init 恢复")
	}
	if err := c.health(ctx); err != nil {
		return err
	}
	fmt.Fprintln(out, "Server：可达，已初始化且未封印")
	if len(s.Apps) > 0 {
		if err := secureDir(s.OutputDir, false); err != nil {
			return errors.New("输出目录缺失或权限异常；重启后请先运行 sync 生成运行时目录")
		}
	}
	token, err := c.machineLogin(ctx, s)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, c.revoke(token)) }()
	fmt.Fprintln(out, "机器认证：通过，Token 策略与有效期符合预期")
	if len(s.Apps) == 0 {
		fmt.Fprintln(out, "尚未登记应用；未执行应用配置检查。")
	}
	for _, app := range s.Apps {
		kv, err := c.readKV(ctx, token, app)
		if err != nil {
			return fmt.Errorf("应用 %s: %w", app, err)
		}
		if _, err := renderEnv(kv.Data.Data); err != nil {
			return fmt.Errorf("应用 %s: %w", app, err)
		}
		fmt.Fprintf(out, "%s：读取及格式通过，KV 版本 %d\n", app, kv.Data.Metadata.Version)
	}
	fmt.Fprintln(out, "未修改 env，也未检查 Docker 业务健康。")
	return nil
}

package baoctl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func policy(s *State, apps []Target) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Managed by baoctl; identity=%s\n", s.ID)
	b.WriteString("path \"auth/token/revoke-self\" {\n  capabilities = [\"update\"]\n}\n")
	for _, app := range apps {
		fmt.Fprintf(&b, "path \"%s\" {\n  capabilities = [\"read\"]\n}\n", app.kvPath())
	}
	return b.String()
}

type roleData struct {
	Bind           bool     `json:"bind_secret_id"`
	SecretTTL      int      `json:"secret_id_ttl"`
	SecretUses     int      `json:"secret_id_num_uses"`
	Policies       []string `json:"token_policies"`
	NoDefault      bool     `json:"token_no_default_policy"`
	TTL            int      `json:"token_ttl"`
	MaxTTL         int      `json:"token_max_ttl"`
	ExplicitMaxTTL int      `json:"token_explicit_max_ttl"`
	Period         int      `json:"token_period"`
	Uses           int      `json:"token_num_uses"`
	Type           string   `json:"token_type"`
	SecretCIDRs    []string `json:"secret_id_bound_cidrs"`
	TokenCIDRs     []string `json:"token_bound_cidrs"`
	LocalSecretIDs bool     `json:"local_secret_ids"`
}

func expectedRole(s *State) roleData {
	return roleData{Bind: true, Policies: []string{s.role()}, NoDefault: true, TTL: 300, MaxTTL: 600, ExplicitMaxTTL: 600, Type: "service"}
}
func (r roleData) matches(s *State) bool {
	e := expectedRole(s)
	return r.Bind && r.SecretTTL == 0 && r.SecretUses == 0 && slices.Equal(r.Policies, e.Policies) && r.NoDefault && r.TTL == e.TTL && r.MaxTTL == e.MaxTTL && r.ExplicitMaxTTL == e.ExplicitMaxTTL && r.Period == 0 && r.Uses == 0 && r.Type == e.Type && len(r.SecretCIDRs) == 0 && len(r.TokenCIDRs) == 0 && !r.LocalSecretIDs
}

func (c *Client) machineLogin(ctx context.Context, s *State) (string, error) {
	r, err := c.login(ctx, "auth/approle/login", map[string]string{"role_id": s.RoleID, "secret_id": s.SecretID})
	if err != nil {
		return "", err
	}
	if !slices.Equal(r.Auth.Policies, []string{s.role()}) || r.Auth.Lease <= 0 || r.Auth.Lease > 600 {
		cleanup := c.revoke(r.Auth.Token)
		return "", errors.Join(errors.New("机器 Token 的策略或有效期超出预期，已阻止同步"), cleanup)
	}
	return r.Auth.Token, nil
}

// Reconcile only state belonging to this installation. Pending intent is saved
// before writes, so interruptions can be resumed without replacing identities.
func reconcile(ctx context.Context, c *Client, admin, dir string, s *State) error {
	if err := c.health(ctx); err != nil {
		return err
	}
	for _, app := range s.Targets {
		kv, err := c.readKV(ctx, admin, app)
		if err != nil {
			return fmt.Errorf("目标 %s 预检失败（请先在 Web UI 创建其 KV v2 配置）: %w", app, err)
		}
		if _, _, err = app.render(kv.Data.Data); err != nil {
			return fmt.Errorf("目标 %s: %w", app, err)
		}
	}
	policyPath := "sys/policies/acl/" + s.role()
	var existing struct {
		Data struct {
			Policy string `json:"policy"`
		} `json:"data"`
	}
	err := c.call(ctx, "GET", policyPath, admin, nil, false, &existing)
	if err != nil && !isStatus(err, 404) {
		return err
	}
	missing := isStatus(err, 404)
	wanted := policy(s, s.Targets)
	if !missing && existing.Data.Policy != wanted && existing.Data.Policy != policy(s, s.AppliedTargets) {
		return errors.New("服务端 Policy 已被外部修改，拒绝覆盖；请核对本机权限")
	}
	if missing && (s.RoleID != "" || len(s.AppliedTargets) > 0) {
		return errors.New("已管理的 Policy 被删除，拒绝自动重建")
	}
	// Inspect the role before any policy mutation.
	rolePath := "auth/approle/role/" + s.role()
	var role struct {
		Data roleData `json:"data"`
	}
	rerr := c.call(ctx, "GET", rolePath, admin, nil, false, &role)
	if rerr != nil && !isStatus(rerr, 404) {
		return rerr
	}
	if rerr == nil && !role.Data.matches(s) {
		return errors.New("服务端 AppRole 与工具管理的权限/生命周期不一致，拒绝覆盖")
	}
	if isStatus(rerr, 404) && s.RoleID != "" {
		return errors.New("已登记的 AppRole 被删除，拒绝自动重建")
	}
	if missing || existing.Data.Policy != wanted {
		if err := c.call(ctx, "PUT", policyPath, admin, map[string]string{"policy": wanted}, false, nil); err != nil {
			return err
		}
	}
	if isStatus(rerr, 404) {
		if err := c.call(ctx, "POST", rolePath, admin, expectedRole(s), false, nil); err != nil {
			return err
		}
	}
	// Read back both objects rather than treating successful writes as acceptance.
	if err := c.call(ctx, "GET", policyPath, admin, nil, false, &existing); err != nil {
		return err
	}
	if existing.Data.Policy != wanted {
		return errors.New("Policy 写后核对失败")
	}
	if err := c.call(ctx, "GET", rolePath, admin, nil, false, &role); err != nil {
		return err
	}
	if !role.Data.matches(s) {
		return errors.New("AppRole 写后核对失败")
	}
	var rid struct {
		Data struct {
			ID string `json:"role_id"`
		} `json:"data"`
	}
	if err := c.call(ctx, "GET", rolePath+"/role-id", admin, nil, false, &rid); err != nil {
		return err
	}
	if rid.Data.ID == "" {
		return errors.New("AppRole 未返回 RoleID")
	}
	if s.RoleID != "" && s.RoleID != rid.Data.ID {
		return errors.New("RoleID 已被外部修改，拒绝替换本地凭据")
	}
	s.RoleID = rid.Data.ID
	if s.SecretID == "" {
		var wrap struct {
			Info struct {
				Token string `json:"token"`
				Path  string `json:"creation_path"`
			} `json:"wrap_info"`
		}
		if err := c.call(ctx, "POST", rolePath+"/secret-id", admin, map[string]any{}, true, &wrap); err != nil {
			return err
		}
		if wrap.Info.Token == "" || wrap.Info.Path != rolePath+"/secret-id" {
			return errors.New("SecretID wrapping 响应不符合预期")
		}
		var secret struct {
			Data struct {
				ID       string `json:"secret_id"`
				Accessor string `json:"secret_id_accessor"`
			} `json:"data"`
		}
		if err := c.call(ctx, "POST", "sys/wrapping/unwrap", wrap.Info.Token, nil, false, &secret); err != nil {
			return err
		}
		if secret.Data.ID == "" || secret.Data.Accessor == "" {
			return errors.New("SecretID 响应缺少凭据或 accessor")
		}
		s.SecretID = secret.Data.ID
		s.SecretAccessor = secret.Data.Accessor
		// Persist credentials before verification; retry reuses them.
		if err := saveState(dir, s); err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cleanup := c.call(cleanupCtx, "POST", rolePath+"/secret-id-accessor/destroy", admin, map[string]string{"secret_id_accessor": s.SecretAccessor}, false, nil)
			return errors.Join(err, cleanup)
		}
	}
	token, err := c.machineLogin(ctx, s)
	if err != nil {
		return err
	}
	var verifyErr error
	for _, app := range s.Targets {
		var kv KV
		kv, verifyErr = c.readKV(ctx, token, app)
		if verifyErr == nil {
			_, _, verifyErr = app.render(kv.Data.Data)
		}
		if verifyErr != nil {
			verifyErr = fmt.Errorf("目标 %s 机器身份验证失败: %w", app, verifyErr)
			break
		}
	}
	if err := errors.Join(verifyErr, c.revoke(token)); err != nil {
		return err
	}
	s.AppliedTargets = slices.Clone(s.Targets)
	s.Ready = true
	return saveState(dir, s)
}

func syncTarget(ctx context.Context, c *Client, s *State, app Target, expectVersion int, out io.Writer) error {
	if !s.Ready {
		return errors.New("本机接入尚未完成，请交互运行 baoctl init 恢复")
	}
	if !slices.Contains(s.Targets, app) {
		return errors.New("目标未在本机登记，请由管理员运行 app add 并指定相同分类")
	}
	if err := secureDir(s.OutputDir, true); err != nil {
		return err
	}
	token, err := c.machineLogin(ctx, s)
	if err != nil {
		return err
	}
	kv, readErr := c.readKV(ctx, token, app)
	// Revoke before publishing the output file. Failure leaves old output untouched.
	if err := errors.Join(readErr, c.revoke(token)); err != nil {
		return err
	}
	if expectVersion > 0 && kv.Data.Metadata.Version != expectVersion {
		return errors.New("当前 KV 版本与 --expect-version 不符，已阻止发布")
	}
	name, data, err := app.render(kv.Data.Data)
	if err != nil {
		return err
	}
	path := filepath.Join(app.outputDir(s.OutputDir), name)
	if err := atomicWrite(path, data); err != nil {
		return err
	}
	fmt.Fprintf(out, "已同步 %s：KV 版本 %d，%d 个字段，%s\n", app, kv.Data.Metadata.Version, len(kv.Data.Data), path)
	return nil
}

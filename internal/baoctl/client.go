package baoctl

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponse = 2 << 20

// APIError intentionally excludes response bodies: server errors can echo input.
type APIError struct{ Status int }

func (e *APIError) Error() string {
	switch e.Status {
	case 403:
		return "OpenBao 拒绝访问 (403)：检查凭据或权限"
	case 404:
		return "OpenBao 对象不存在 (404)"
	case 429:
		return "OpenBao 请求受限或节点不可用 (429)"
	case 503:
		return "OpenBao 不可用或尚未解封 (503)"
	default:
		return fmt.Sprintf("OpenBao 请求失败 (HTTP %d)，响应内容已隐藏", e.Status)
	}
}
func isStatus(err error, status int) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == status
}

type Client struct {
	address string
	http    *http.Client
}

func validateAddress(address string) error {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.Opaque != "" {
		return errors.New("Server 地址必须是无账号、查询参数及路径的 HTTPS 地址")
	}
	return nil
}

func NewClient(address string) (*Client, error) {
	if err := validateAddress(address); err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return &Client{strings.TrimRight(address, "/"), &http.Client{
		Transport: transport, Timeout: 20 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *Client) call(ctx context.Context, method, path, token string, body any, wrap bool, out any) error {
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return errors.New("无法编码请求")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.address+"/v1/"+path, bytes.NewReader(raw))
	if err != nil {
		return errors.New("无法创建请求")
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	if wrap {
		req.Header.Set("X-Vault-Wrap-TTL", "5m")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return errors.New("请求已取消或超时")
		}
		return errors.New("连接 OpenBao 失败：检查 DNS、TLS、网络和服务状态")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{resp.StatusCode}
	}
	if out == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil || len(data) > maxResponse {
		return errors.New("OpenBao 响应读取失败或超过大小限制")
	}
	if err := json.Unmarshal(data, out); err != nil {
		return errors.New("OpenBao 响应格式无效，内容已隐藏")
	}
	return nil
}

type authResponse struct {
	Auth struct {
		Token    string   `json:"client_token"`
		Policies []string `json:"policies"`
		Lease    int      `json:"lease_duration"`
	} `json:"auth"`
}

func (c *Client) login(ctx context.Context, path string, body any) (authResponse, error) {
	var r authResponse
	err := c.call(ctx, "POST", path, "", body, false, &r)
	if err == nil && r.Auth.Token == "" {
		err = errors.New("登录未返回有效 Token")
	}
	return r, err
}

func (c *Client) revoke(token string) error {
	// Cleanup still runs after Ctrl-C or a failed deployment request.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return c.call(ctx, "POST", "auth/token/revoke-self", token, nil, false, nil)
}

func (c *Client) health(ctx context.Context) error {
	var r struct {
		Initialized bool `json:"initialized"`
		Sealed      bool `json:"sealed"`
	}
	if err := c.call(ctx, "GET", "sys/health?standbyok=true&perfstandbyok=true", "", nil, false, &r); err != nil {
		return err
	}
	if !r.Initialized || r.Sealed {
		return errors.New("Server 未初始化或尚未解封")
	}
	return nil
}

type KV struct {
	Data struct {
		Data     map[string]any `json:"data"`
		Metadata struct {
			Version      int    `json:"version"`
			Destroyed    bool   `json:"destroyed"`
			DeletionTime string `json:"deletion_time"`
		} `json:"metadata"`
	} `json:"data"`
}

func (c *Client) readKV(ctx context.Context, token string, target Target) (KV, error) {
	var r KV
	if err := target.validate(); err != nil {
		return r, err
	}
	err := c.call(ctx, "GET", target.kvPath(), token, nil, false, &r)
	if err == nil && (r.Data.Data == nil || r.Data.Metadata.Version < 1 || r.Data.Metadata.Destroyed || r.Data.Metadata.DeletionTime != "") {
		err = errors.New("目标 KV 为空、已删除或缺少有效版本")
	}
	return r, err
}

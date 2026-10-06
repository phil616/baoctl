# 贡献指南

感谢参与 baoctl。可以通过 Issue 提交问题和功能需求，也可以通过 Pull Request 改进代码、测试或文档。

## 提交问题

请说明操作系统、CPU 架构、`baoctl version`、OpenBao 版本、复现步骤、预期结果和实际结果。使用虚构应用及测试变量复现。

不要附上 `state.json`、真实 env、密码、SecretID、Token 或完整未脱敏日志。如果发现可能导致秘密泄漏或越权的问题，请先通过仓库提供的私密安全报告渠道联系维护者；如该渠道不可用，可先请求私密联系方式，不在公开 Issue 中发布敏感细节。

## 本地开发

构建与单元测试需要 Go 1.25+、make；集成测试额外需要 Docker、Python 3 和 Linux amd64 环境。

```bash
make check
make build
bash scripts/test-integration.sh
```

`make check` 运行静态检查与竞态测试；集成脚本启动临时 OpenBao 2.7.1、使用独立测试凭据并清理测试容器，不应指向生产 Server。

| 路径 | 用途 |
| --- | --- |
| `cmd/baoctl/` | CLI 程序入口 |
| `internal/baoctl/` | 命令、OpenBao API、身份配置和文件处理 |
| `scripts/` | 安装和集成测试辅助脚本 |
| `docs/` | 使用、运维、设计与发布文档 |
| `.github/workflows/` | 标签发版工作流 |

## 提交改动

保持改动围绕一个明确问题。Go 代码使用 `gofmt`；根据行为变化补充测试，涉及认证、权限或文件输出时验证失败路径。仅修改文档时检查链接与示例语法即可。

新增命令或修改用户可见行为时，同步更新对应文档。示例使用 `example.com` / `example.test` 和虚构值，不加入个人部署信息、机器凭据或生产日志。

Pull Request 请说明问题、修改后的行为、实际执行的验证，以及尚未覆盖的范围。发布由维护者按 [发布说明](docs/releasing.md) 执行。

项目采用 [MIT License](LICENSE)。

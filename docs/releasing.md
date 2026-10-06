# GitHub 标签发版

[文档索引](README.md) · [贡献指南](../CONTRIBUTING.md)

工作流位于 [release.yml](../.github/workflows/release.yml)。推送版本标签后，GitHub Actions 自动测试、构建并发布 GitHub Release。仅构建 **Linux amd64**，不生成其他平台产物或压缩包。

## 前提

- 仓库已启用 GitHub Actions，允许工作流使用 `contents: write` 创建 Release。
- 标签指向的提交已包含工作流、Go 源码、`go.mod` / `go.sum`、Makefile 和测试脚本。
- 使用内置 `GITHUB_TOKEN`，无需配置个人访问 Token 或任何 OpenBao 生产凭据。

## 发布步骤

先提交并推送准备发布的代码，再创建一个未使用过的标签。以下版本号只是示例，应替换为本次发布版本：

```bash
git tag -a v0.1.0 -m 'Release v0.1.0'
git push origin v0.1.0
```

仅在本地创建标签不会触发工作流。正式版本使用 `vMAJOR.MINOR.PATCH`；预发布可使用 `v0.2.0-rc.1` 等带后缀的标签，工作流会将其标记为 Prerelease。其他以 `v` 开头但格式不符的标签会在校验阶段失败。

## 自动执行内容

1. 检出标签对应的源代码，验证标签格式。
2. 根据 `go.mod` 配置 Go，校验模块，并运行 `go vet` 和竞态测试。
3. 启动一次性本地 OpenBao 2.7.1 容器，运行集成测试和终端交互测试；不连接生产 Server。
4. 使用 `GOOS=linux GOARCH=amd64 CGO_ENABLED=0` 构建静态二进制，去除构建路径和调试符号。
5. 将标签版本注入程序。例如标签 `v0.2.0-rc.1` 的 `baoctl version` 输出 `0.2.0-rc.1`。
6. 生成 SHA-256 校验文件，建立 Release 草稿，上传全部附件后公开发布，自动生成发布说明。

Release 附件：

| 文件 | 内容 |
| --- | --- |
| `baoctl-linux-amd64` | Linux amd64 可执行二进制 |
| `SHA256SUMS` | 该二进制的 SHA-256 校验值 |

GitHub 页面自带的 Source code 下载由 GitHub 提供，不属于工作流构建产物。

## 下载后使用

从同一个 Release 下载两个附件，放入同一目录：

```bash
sha256sum --check SHA256SUMS
sudo install -o root -g root -m 0755 ./baoctl-linux-amd64 /usr/local/bin/baoctl
baoctl version
```

首次接入继续阅读 [完整使用指南](user-guide.md)。升级只替换二进制，不删除现有机器状态或重新创建身份。

## 失败与重跑

测试或构建失败时不会创建 Release。上传失败时可能留下未公开的草稿；修复环境问题后可以在 Actions 页面重跑该次工作流，补全草稿附件再发布。

如果终端集成测试报 `AssertionError: password echoed`，表示测试检测到了密码回显。历史实现先显示提示，再异步关闭回显，快速输入时可能触发竞态；修复后会先关闭回显再显示提示。不要通过删除断言或增加输入延迟绕过检查。测试只使用临时容器中的虚构密码。

代码修复后，仅重跑旧标签的工作流不会使用新代码；应提交修复，再创建指向修复提交的新版本标签。

已公开的 Release 不会被重跑覆盖。代码需要修复时，提交修复并创建新的版本标签，不移动已发布标签。重新发布的产物必须对应新标签。

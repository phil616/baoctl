# baoctl

将 OpenBao 中的应用配置同步为 Docker 可用的 env 文件，或还原经过 SHA256 校验的原始文件。

baoctl 是面向 **Debian / Ubuntu · Linux amd64** 的本地 CLI，适用于通过 SSH 和 CI/CD 发布 Docker 应用的场景。它直接通过 HTTPS API 访问 OpenBao，在部署前按需获取配置，无需常驻 OpenBao Agent。

[快速开始](#快速开始) · [完整文档](docs/README.md) · [发布说明](docs/releasing.md) · [参与贡献](CONTRIBUTING.md) · [MIT License](LICENSE)

## 功能

- **独立机器身份**：交互初始化 AppRole，按应用授予精确只读权限。
- **多环境与文件密钥**：支持 `prod/test/dev`、`apps/files`，文件类自动解码 Base64 并校验 SHA256。
- **多应用管理**：一台机器管理多个应用，每个应用生成独立 env 文件。
- **初始化与应用解耦**：先初始化机器，准备好应用后再添加授权。
- **部署前同步**：自动认证、读取 KV v2 配置、校验格式并原子替换文件。
- **失败阻断发布**：网络、认证、格式或版本检查失败时返回非零退出码。
- **单文件分发**：提供 Linux amd64 静态二进制，目标机器无需 Go、Python、jq 或 `bao` CLI。

```text
OpenBao Web UI 管理应用配置
            ↓
CI/CD 通过 SSH 执行 baoctl sync my-api
            ↓
/run/baoctl/my-api.env
            ↓ 同步成功后
现有发布脚本执行 docker run --env-file ...
```

## 安装

### 使用 Release 二进制

在本项目的 GitHub **Releases** 页面下载同一版本的 `baoctl-linux-amd64` 和 `SHA256SUMS`，放在同一目录，校验通过后安装：

```bash
sha256sum --check SHA256SUMS && \
  sudo install -o root -g root -m 0755 ./baoctl-linux-amd64 /usr/local/bin/baoctl
baoctl version
```

### 从源码构建

开发机需要 Go 1.25+ 和 make。在项目根目录执行：

```bash
make check
make build
sudo scripts/install.sh dist/baoctl
```

构建产物为 `dist/baoctl`，也可以复制到其他 Linux amd64 机器安装。安装脚本只安装 CLI，不安装或修改 OpenBao Server、Agent 和 Docker。

## 快速开始

### 1. 准备服务端

需要一个已初始化、已解封、具有有效 HTTPS 证书的 OpenBao Server，以及：

- `userpass/`：供管理员交互登录。
- `approle/`：供机器自动认证。
- `kv/`：KV v2 引擎，保存应用配置。
- 有权管理机器身份、策略和应用授权的管理员账号。

目标机器需要 root 或 sudo 权限。baoctl 不负责初始化或解封 Server。完整条件见 [前置准备](docs/user-guide.md#1-前置准备)。

### 2. 初始化机器

```bash
sudo baoctl init
```

按向导填写自己的 OpenBao 地址、机器名称和管理员账号密码。**本机应用可以直接回车留空**，先建立机器身份；也可以使用 `sudo baoctl init --apps ""` 跳过应用询问。

文档和向导中的 `https://openbao.example.com` 是占位地址，必须替换。密码仅在终端隐藏输入，不保存为机器的日常登录凭据。

### 3. 创建并登记应用

在 OpenBao Web UI 中进入 `kv/`，创建路径 `prod/apps/my-api/env`。以下为虚构的 JSON 示例，所有值均为字符串：

```json
{
  "APP_MODE": "production",
  "HTTP_PORT": "8080"
}
```

在目标机器登记并同步：

```bash
sudo baoctl app add my-api
sudo baoctl sync my-api
```

生成 `/run/baoctl/my-api.env`，文件权限为 `root:root 0600`。之后每次 `sync` 都使用本机凭据自动登录，无需管理员密码。

增加第二个应用时，创建其独立 KV，再执行 `app add <APP>` 和 `sync <APP>`，无需安装第二份程序。详细步骤见 [创建应用配置](docs/user-guide.md#3-在-openbao-创建应用配置) 和 [多应用接入](docs/user-guide.md#6-同一台机器新增应用)。

### 多环境与文件类

分类参数默认 `--classified prod --typename apps`；登记和同步需指定相同分类：

```bash
sudo baoctl app add my-api --classified test
sudo baoctl sync my-api --classified test
# test/apps/my-api/env → /run/baoctl/my-api.env.test

sudo baoctl app add my-file --classified dev --typename files
sudo baoctl sync my-file --classified dev --typename files
# dev/files/my-file/env → /run/baoctl/my-file/dev/<FILENAME>
```

开发应用输出为 `<APP>.env.development`。文件类 KV 固定包含字符串字段 `B64`、`FILENAME`、`SHA256`，校验通过才写入。完整示例见 [多环境与文件类密钥](docs/classified-files.md)。

### 4. 接入部署脚本

在目标主机的 **root 部署脚本**中，将同步放在停止或删除旧容器之前：

```bash
#!/usr/bin/env bash
set -euo pipefail

# 锁覆盖同一应用的同步和整个部署过程。
exec 9>/run/lock/my-api-deploy.lock
flock -n 9

/usr/local/bin/baoctl sync my-api

# 同步成功后，继续现有的容器替换与健康检查。
# docker run 使用：--env-file /run/baoctl/my-api.env
```

同步失败时停止发布，保留旧容器运行。不要忽略退出码或在失败后使用旧文件继续部署。完整示例见 [CI/CD 接入](docs/user-guide.md#7-接入-cicd)。

## 常用命令

| 命令 | 用途 |
| --- | --- |
| `baoctl init` | 交互初始化或恢复机器身份，应用可留空 |
| `baoctl init --apps my-api,worker` | 初始化时同时登记已存在的应用 |
| `baoctl app add <APP>` | 为本机增加应用读取权限，需要管理员登录 |
| `baoctl sync <APP>` | 获取当前配置并生成 env，无需管理员登录 |
| `baoctl app add <NAME> --classified test --typename files` | 登记测试环境文件类密钥 |
| `baoctl sync <NAME> --classified test --typename files` | 解码文件、校验 SHA256 并写入名称/环境目录 |
| `baoctl sync <APP> --expect-version 7` | 要求当前 KV 版本为 7，否则失败 |
| `baoctl status` | 查看本地登记和文件状态 |
| `baoctl doctor` | 检查连接、机器认证及已登记应用的读取与格式 |
| `baoctl help` / `baoctl version` | 查看帮助 / 版本 |

除帮助和版本外，命令均需 root 权限，可按需加 `sudo`。`--expect-version` 校验当前版本，不下载指定历史版本。

## 安全与使用边界

- 使用 HTTPS 和服务端 Policy 控制访问。每台机器拥有独立凭据，只读取已授权应用。
- `/etc/baoctl/state.json` 持久保存机器凭据，默认 `0600`；不要提交到 Git、复制到其他机器或输出到日志。
- 当前 SecretID 不自动过期；每次同步取得新的短期 Token，用完主动撤销。凭据泄漏仍需撤销身份并处理受影响的业务秘密。
- `/run/baoctl` 下的 env 在主机重启后需要重新同步。配置更新不会自动改变运行中容器的环境变量。
- `apps` 支持 Docker CLI env-file 使用的单行字符串；多行秘密或二进制文件使用 `files`。普通 Compose env-file 兼容性、动态凭据续租不在当前支持范围内。
- 无后台刷新、离线回退或自动容器发布；凭据轮换、撤权和退役尚未提供自动命令。

本项目已通过本地及 OpenBao 2.7.1 集成测试，尚未经过独立安全审计。生产使用前应在自己的环境中验证。实现原理、权限边界与故障处理见 [设计说明](docs/design.md) 和 [运维说明](docs/operations.md)。

## 文档

| 文档 | 内容 |
| --- | --- |
| [文档索引](docs/README.md) | 按使用场景查找文档 |
| [完整使用指南](docs/user-guide.md) | 前置准备、安装、Web UI 创建应用、多应用管理与 CI/CD |
| [多环境与文件类密钥](docs/classified-files.md) | 分类参数、输出路径、Base64 和 SHA256 契约 |
| [运维说明](docs/operations.md) | 凭据生命周期、排错、恢复、升级与备份 |
| [设计与安全边界](docs/design.md) | API 同步流程、身份权限、文件与网络保护 |
| [发布说明](docs/releasing.md) | GitHub 标签发版与二进制下载 |
| [路线图与验证范围](docs/plan.md) | 已实现能力、已验证范围和后续方向 |

## 开发与贡献

```bash
make check
make build
# 额外需要 Docker 和 Python 3，仅连接一次性本地测试实例。
bash scripts/test-integration.sh
```

欢迎通过 Issue 反馈问题或通过 Pull Request 提交改进。请先阅读 [贡献指南](CONTRIBUTING.md)，提交可复现步骤和不含秘密的测试数据。

## 许可证

[MIT License](LICENSE) · Copyright (c) 2026 @altasci

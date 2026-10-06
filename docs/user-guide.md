# baoctl 完整使用指南

[文档索引](README.md) · [项目首页](../README.md)

适用于 baoctl 0.1.0、Debian/Ubuntu、Linux amd64。本文从一台新机器开始，说明如何准备应用配置、登记本机应用、生成 env，并交给已有 Docker 发布脚本使用。

本文中的 `openbao.example.com`、`example-node-02`、`my-api`、`order-worker` 均为通用示例。域名必须替换为自己的 OpenBao 地址，机器名和应用名按实际用途填写；示例 JSON 只含虚构测试数据。

## 1. 前置准备

### 1.1 先理解三个操作

baoctl 中的“应用”就是一个名称与一份 OpenBao 环境变量配置的对应关系，不是安装包，也不是 Docker 容器。

| 操作 | 在哪里做 | 结果 |
| --- | --- | --- |
| 创建应用配置 | OpenBao Web UI | Server 保存该应用的环境变量 |
| 登记本机应用 | 目标机器执行 `init` 或 `app add` | 本机被允许读取该应用 |
| 同步应用配置 | 目标机器执行 `sync` | 生成供 Docker 读取的本地 env 文件 |

机器初始化与应用接入是独立的：可以先运行 `init`，应用留空，完成机器身份初始化。准备部署应用时，再按 **创建配置 → `app add` 登记到机器 → `sync` 同步 → 创建容器** 的顺序操作。`app add` 不会在 Server 上创建业务秘密，`sync` 不会启动应用。当前没有 `app create`、`env edit` 或 `env import` 命令。

### 1.2 Server 与管理员

| 准备项 | 要求 |
| --- | --- |
| OpenBao Server | 已初始化、已解封，通过 HTTPS 可达；本项目集成测试使用 2.7.1 |
| Web UI | 浏览器可访问 `https://openbao.example.com/ui/`，用于创建和修改变量 |
| 人员认证 | 已启用 `userpass/`，有可用管理员用户名和密码 |
| 机器认证 | 已启用 `approle/` |
| 秘密引擎 | `kv/` 已启用且为 **KV v2** |
| 管理员权限 | 可读取待接入的应用，管理新建的机器 Policy、AppRole 和 SecretID，并撤销自己的临时 Token |

使用前请由平台管理员确认以上服务端基础配置已完成。缺少前置条件时，先完成相应配置。baoctl 不会重新初始化 Server，不负责启用认证方法、创建 Secrets Engine 或解封。

管理员账号用于 `init` 和 `app add`。平时 `sync` 使用独立机器凭据，不需要管理员密码。不要将管理员密码配置成 CI 变量供同步使用。

### 1.3 目标生产机器

- 系统为 Debian/Ubuntu 系，架构为 Linux amd64（`uname -m` 应显示 `x86_64`）。
- 能以 root 操作，或使用有 sudo 权限的账号。
- 已有系统 CA 证书，DNS、系统时间正常，能出站访问 OpenBao 的 HTTPS 443。
- 使用现有 Docker 安装和发布流程；baoctl 不安装 Docker。
- 初始化时使用可信的交互终端。若通过 SSH 直接执行管理命令，需要分配终端，例如 `ssh -t`。

在目标机器上检查执行位置和架构：

```bash
hostname
uname -s
uname -m
```

这里不需要开放 8200/8201 端口，也不需要安装 OpenBao Agent、`bao`、Go、Python 或 jq。Go 仅构建程序时需要；本文 CI 示例的 `flock` 来自系统的 util-linux。

### 1.4 准备名称

机器名和应用名都限制为 1–63 个字符：小写字母、数字、下划线、连字符，且必须以字母或数字开头。例如 `example-node-02`、`my-api`。不要直接填包含点号的完整域名或大写 `EXAMPLE-NODE-02`。

应用名决定配置路径。以 `my-api` 为例：

| 用途 | 填写内容 |
| --- | --- |
| CLI 应用参数 | `my-api` |
| Web UI 进入 `kv/` 后的 Secret 路径 | `prod/apps/my-api/env` |
| 工具内部读取的 API 路径，无需手工配置 | `kv/data/prod/apps/my-api/env` |
| 本机默认生成文件 | `/run/baoctl/my-api.env` |

业务路径不放机器名。同一应用部署到多台机器时可以共用这份配置；如果应用实例需要不同配置，应使用不同应用名及独立 KV，例如 `my-api-staging` 和 `my-api-prod`。

## 2. 安装程序

如果本项目 GitHub Releases 已有发行版本，可直接下载 `baoctl-linux-amd64` 和 `SHA256SUMS`，放在同一个目录校验并安装，不必自行构建：

```bash
sha256sum --check SHA256SUMS
sudo install -o root -g root -m 0755 ./baoctl-linux-amd64 /usr/local/bin/baoctl
baoctl version
```

下文是从源码构建并传输到目标机器的流程。两种方式任选其一。

### 2.1 在开发机上构建

在已有 Go 1.25+ 和 make 的开发机上，进入本项目根目录：

```bash
make check
make build
```

产物为 `dist/baoctl`，目标固定为 Linux amd64。通过你现有的可信文件传输方式，把它复制到目标机器；目标机器本身不用安装 Go。

预编译二进制以本项目 GitHub Releases 附件为准；不提供 `curl | bash` 安装入口。

### 2.2 在目标机器上安装

假设传过去的文件位于当前目录，名为 `baoctl`：

```bash
sudo install -o root -g root -m 0755 ./baoctl /usr/local/bin/baoctl
/usr/local/bin/baoctl version
/usr/local/bin/baoctl help
```

Release 二进制应显示标签对应的版本号（不含前缀 `v`）；本地普通构建使用源码中的默认版本。若目标机已有整个项目及构建产物，也可以在项目根目录使用 `sudo scripts/install.sh dist/baoctl`。

安装只放置一个可执行文件。后续所需的状态目录、凭据和输出目录由 CLI 自动创建，无需自己创建 JSON、Policy 或模板文件。

## 3. 在 OpenBao 创建应用配置

本节在**管理员浏览器**中操作，可以在机器初始化前或后完成；仅初始化机器时不必先创建应用。已经存在且路径符合约定的应用，核对内容后直接复用，不要另建一份或覆盖已有数据。

### 3.1 登录并找到 kv 引擎

1. 打开 `https://openbao.example.com/ui/`。
2. 选择用户名密码（`userpass`）登录；如果页面要求认证挂载路径，填写 `userpass`。
3. 使用管理员账号登录，不使用 Root Token。
4. 打开 Secrets，进入现有的 `kv/`，核对引擎版本为 KV v2。

页面文字可能随版本略有差异，按对应功能操作。如果看不到 `kv/`，先确认登录账号和权限，不要随意新建另一个引擎。

### 3.2 创建 my-api

在 `kv/` 根目录选择 **Create secret / 创建秘密**，路径填写：

```text
prod/apps/my-api/env
```

不要写成 `kv/prod/apps/my-api/env` 或 `kv/data/prod/apps/my-api/env`；此时已经位于 `kv/` 引擎内部。如果你已经进入某个子目录，则核对最终完整路径仍是上面这一条。

使用 Key / Value 表单逐项填写应用需要的变量。下面只是非敏感示例：

| Key | Value |
| --- | --- |
| `APP_MODE` | `production` |
| `HTTP_PORT` | `8080` |
| `FEATURE_ENABLED` | `false` |

真实应用需要的数据库密码、API Key 等，也在浏览器表单中填写；不要把它们放入命令行、聊天、截图或仓库。只存该应用应获得的变量，因为同步会输出这个 KV 中的**所有字段**。

若切换到 JSON 编辑模式，上面的例子应写成：

```json
{
  "APP_MODE": "production",
  "HTTP_PORT": "8080",
  "FEATURE_ENABLED": "false"
}
```

注意 `"8080"`、`"false"` 都是字符串，不是 JSON 数字和布尔值。这里只填写字段对象，不添加 API 请求用的外层 `data`。OpenBao 能保存非字符串类型，但 baoctl 的 env 输出要求全部为字符串；相关存储行为见 [KV v2 官方说明](https://openbao.org/docs/secrets/kv/kv-v2/)。

字段格式要求：

- 键名只能使用英文字母、数字、下划线，且首字符不能是数字。
- 值为单行字符串，允许空字符串，拒绝换行、Tab、NUL 等控制字符及多行证书/私钥。
- Key / Value 表单里不要给值额外加 shell 引号。例如值是 `abc`，就填 `abc`；填 `"abc"` 会把引号也交给应用。
- `$`、反斜杠、空格、引号如果本来就是值的一部分，则保留原值。JSON 模式仍遵循 JSON 自身的转义规则。
- 不要创建完全空的字段对象；单行 `KEY=value` 最长 60000 字节，整个输出文件最多 1 MiB。

保存后记录页面显示的 **KV 版本号**。它与某个自定义变量（例如 `CONFIG_REVISION`）不是一回事；baoctl 不要求你额外创建 `CONFIG_REVISION` 字段。

## 4. 首次登记本机应用

本节在**要运行 my-api 的生产机器**上操作，不是在 Server 上操作。

首次接入可以只初始化机器，不填写任何应用。运行：

```bash
sudo /usr/local/bin/baoctl init
```

按提示填写：

| 提示 | 示例填写 | 说明 |
| --- | --- | --- |
| OpenBao 地址 | `https://openbao.example.com` | 必须替换为实际服务地址，不附加 `/ui/` 或 `/v1/` |
| 机器名称 | `example-node-02` | 该机器的运维名称，符合命名规则 |
| 本机应用 | 直接回车留空 | 可选；若同时接入现有应用，填 `my-api` 或逗号分隔的多个名称，不填写 KV 完整路径 |
| 是否继续 | `yes` | 核对当前机器及应用列表后输入 |
| 管理员用户名 | `admin` | 可替换为实际已授权账号 |
| 管理员密码 | 在终端直接输入 | 输入时不显示字符，也不显示星号 |

可以预填非敏感参数，用显式的空 `--apps` 跳过应用询问：

```bash
sudo /usr/local/bin/baoctl init \
  --address https://openbao.example.com \
  --machine example-node-02 \
  --apps ""
```

确认与管理员登录仍然交互进行。没有应用时，工具只创建并验证机器身份，不授予任何业务 KV 读取权限；之后可单独接入应用。首次要同时登记应用，可选择 `--apps my-api` 或 `--apps my-api,order-worker`，此时指定的应用 KV 都必须已存在且格式正确。带应用的列表不允许空条目，例如 `my-api,` 会报错。

空应用初始化成功时会提示“本机身份已初始化并验证，尚未登记应用”。此时可以运行 `sudo baoctl status` 或 `sudo baoctl doctor` 检查机器状态；不会执行应用配置检查。工具自动完成身份与本地凭据配置，不需要手工处理 RoleID、SecretID、Policy 或模板。

**已有本地登记后，不要再次使用 `init --apps ...` 来新增应用。** 新增应用按第 6 节操作；核对或恢复现有登记时直接运行 `sudo baoctl init`，不重新传地址、机器名、应用列表或输出目录。重复运行仍需要管理员登录。

准备接入首个应用时，先按第 3 节在 Web UI 创建它的 KV，再在这台机器运行：

```bash
sudo /usr/local/bin/baoctl app add my-api
```

该步骤需要管理员登录，并为原机器身份增加应用读取权限。若初始化时已通过 `--apps my-api` 登记了该应用，则跳过此步。

`init` 和 `app add` 都不生成 env，也不启动 Docker 容器。

## 5. 同步并验证首个应用

在同一台机器执行：

```bash
sudo /usr/local/bin/baoctl sync my-api
sudo /usr/local/bin/baoctl status
sudo /usr/local/bin/baoctl doctor
```

`sync` 不要求 OpenBao 管理员密码。如果 sudo 本身提示 Linux 账号密码，那是本机提权，与 OpenBao 登录不同。

同步成功输出只包含应用名、KV 版本、字段数量和文件路径，例如下面的**示意输出**：

```text
已同步 my-api：KV 版本 1，3 个字段，/run/baoctl/my-api.env
```

检查文件是否生成及权限，不输出内容：

```bash
sudo test -s /run/baoctl/my-api.env
sudo stat -c '%U:%G %a %n' /run/baoctl/my-api.env
```

文件预期为 `root:root 600`。不要用 `cat` 打印实际 env，不要将普通应用加入有权读取机器凭据的组，也不要改成全员可读。

`status` 展示本地应用列表及文件状态；`doctor` 检查服务连接、机器登录、全部登记应用的读取和格式。它们都不证明 Docker 业务已经健康。`test -s` 也只检查文件非空，发布能否继续必须以本次 `sync` 成功为准。

**仅当你准备首次创建容器时**，可参考以下结构。`APP_IMAGE` 必须先由现有发布流程设为实际镜像，端口、网络、卷等按应用补齐：

```bash
#!/usr/bin/env bash
set -euo pipefail

# 本段在目标机的 root 部署会话中执行。
: "${APP_IMAGE:?请先通过发布流程指定实际应用镜像}"
/usr/local/bin/baoctl sync my-api
docker run -d --name my-api \
  --env-file /run/baoctl/my-api.env \
  "$APP_IMAGE"
```

已有同名容器时不要直接重复上面的 `docker run`，应沿用现有替换容器流程。每个应用只使用自己的 env 文件。

## 6. 同一台机器新增应用

假设本机已登记 `my-api`，现在要增加 `order-worker`。

1. 在 Web UI 的 `kv/` 中创建 `prod/apps/order-worker/env`，填写该应用的字符串环境变量。若已存在，则核对后复用。
2. 在目标机器执行下面的授权命令，交互输入管理员用户名和密码：

```bash
sudo /usr/local/bin/baoctl app add order-worker
```

3. 授权完成后同步，并检查本机登记：

```bash
sudo /usr/local/bin/baoctl sync order-worker
sudo /usr/local/bin/baoctl status
sudo /usr/local/bin/baoctl doctor
```

4. 在 `order-worker` 的 Docker 发布命令中使用 `--env-file /run/baoctl/order-worker.env`，再执行现有部署与健康检查。

此时两份输出分别是：

```text
/run/baoctl/my-api.env
/run/baoctl/order-worker.env
```

原有 `my-api` 授权被保留。本机仍共用一个机器身份，但每个 APP 单独输出一个文件；无需为第二个 APP 安装第二份程序或创建第二个 Agent。

`app add` 每次接收一个应用名。它登记应用和更新授权，不创建远端 KV，不生成 env，也不创建容器。某个应用的管理操作中断后，先运行 `sudo baoctl init` 恢复，再继续发布。

## 7. 接入 CI/CD

CI runner 继续用 SSH 执行你现有的主机部署脚本。管理员只在首次接入或新增应用时交互操作，CI 日常只运行 `sync`。

下面的片段应合入**目标主机的 root 部署脚本**：

```bash
#!/usr/bin/env bash
set -euo pipefail

# 同一应用的同步和整个部署串行执行。
exec 9>/run/lock/my-api-deploy.lock
flock -n 9

# 必须放在停止/删除旧容器之前。
/usr/local/bin/baoctl sync my-api

# 以下继续已有的容器替换流程。
# 现有 docker run 必须使用：
# --env-file /run/baoctl/my-api.env
# 最后继续已有业务健康检查。
```

如果 SSH 使用普通 sudo 账号，在 CI 中调用已经授权的可信 root 部署脚本；非交互提权应使用 `sudo -n`，没有相应授权时直接失败。不要为了让 CI 跑通，把管理员密码传入 SSH 脚本或为普通账号开放任意免密命令。

同步失败必须停止后续部署：保留旧容器运行，不重建、不读旧文件继续。不要写成 `baoctl sync my-api || true`。baoctl 的锁仅覆盖 CLI 命令本身，因此还需要上述部署锁或等价的 CI 并发控制覆盖后续 Docker 操作。

如果已有流水线是直接执行 SSH 命令而非脚本，同样确保同步和后续替换操作在同一个具有 `set -e` 的 Bash 执行范围内，或者显式检查每一步返回值。

## 8. 修改配置与再次发布

1. 在 Web UI 进入 `kv/` 下该应用的 `env`，查看当前版本。
2. 使用编辑或创建新版本功能修改字段。保存前检查其他字段仍在，所有值仍是字符串。遇到版本冲突先重新读取最新内容。
3. 保存并记录新的 KV 版本。
4. 重新运行已有 CI/CD，流水线会先同步再重新创建容器。

如果暂时不发布，也可以在主机单独执行 `sudo baoctl sync my-api`，但它只更新文件，运行中的容器不会因此更新环境变量。没有定时刷新；Web UI 保存后要等下一次成功同步才反映到本机。

增加或删除变量也是同一个流程，无需修改模板或再次 `app add`。删除字段前先确认应用不再需要它；工具只校验格式，不知道业务必需哪些变量。

若本次发布要求精确匹配已核对的 KV 版本，例如版本 7：

```bash
sudo /usr/local/bin/baoctl sync my-api --expect-version 7
```

当前最新版本不是 7 时命令失败。这个参数不是“下载历史版本 7”；如需回滚，先在 Web UI 按平台流程把期望内容保存为新的当前版本，再同步和发布。

## 9. 常见问题

**我只有一份旧的 `.env`，怎么创建应用？**

当前没有自动导入命令。在可信环境中把字段和值录入 Web UI。不要把 `.env` 当 shell 脚本执行；旧文件中的引号、转义和多行格式需要按原消费者语义核对，不能假定整行复制后含义不变。

**我能在新机器输入相同 APP 名称吗？**

可以。在新机器独立安装并 `init`，再用 `app add` 登记同一 APP，即可同步相同 KV；也可在 init 时用可选的 `--apps` 同时登记。不要复制另一台机器的 `/etc/baoctl` 或机器凭据。

**机器重启后 env 不见了怎么办？**

默认 `/run/baoctl` 是运行时目录，重启后清空。下次成功 `sync` 会恢复，不需要重新 init。已有容器保存了创建时的环境变量；重新创建容器前必须同步。

**为什么应用配置已经存在，却提示未在本机登记？**

Server 上有配置不等于本机有权限。空应用 `init` 后也必须用 `app add <APP>` 登记，才能同步；初始化时已用可选的 `--apps` 登记过的应用不必重复添加。

**能否改生成文件的目录？**

新机器首次接入时可以加 `--output-dir`，指定专用绝对路径，例如 `sudo baoctl init --output-dir /run/company-env`。目录应不存在，或已经是 root 所有、0700 的私有目录；不能复用已有 Agent 目录。后续按实际路径修改 Docker 参数。第一版不提供已接入机器的目录迁移命令，建议先使用默认目录。

**需要手动修改 state.json、Policy 或 AppRole 吗？**

正常流程不需要。`state.json` 含长期机器凭据，不要打印或随意编辑。服务端权限被外部修改时，CLI 会拒绝自动覆盖，需要先核对差异。

**能否删除本机应用、轮换凭据或下线机器？**

首版没有这些自动命令。不能用删除本地文件代替服务端撤权，也不要删除其他机器仍使用的共享 KV。按 [运维说明](operations.md) 单独处理。

其他连接、认证、权限和恢复问题见 [运维说明](operations.md)，身份隔离及凭据泄漏影响见 [设计与安全边界](design.md)。

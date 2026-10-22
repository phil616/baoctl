# 设计与安全边界

[文档索引](README.md) · [完整使用指南](user-guide.md)

## 目标与范围

目标环境为 Debian/Ubuntu、Linux amd64，CI runner 经 SSH 调用主机上有 root 权限的部署流程，应用用 `docker run --env-file` 接收环境变量。适合配置低频变化、允许 Server 不可用时阻断发布的场景。因此采用按需 CLI，省去常驻 Agent、systemd、模板和定时刷新。

依赖预先配置好的 OpenBao Server、KV v2、管理员 userpass 认证和机器 AppRole 模型。代码不自动启用引擎，不修改 Server 配置，不初始化或解封，不升级 OpenBao。

## 数据流

```text
管理员：Web UI 修改 kv/<classified>/<typename>/<NAME>/env

首次接入 / 新增应用：
可信主机终端输入管理员密码
  → HTTPS userpass 登录
  → 有应用时才预检应用格式（init 可留空）
  → 保存本地待完成意图
  → 建立/核对机器 Policy 和 AppRole
  → 新身份通过 Response Wrapping 取得独立 SecretID
  → 保存机器凭据
  → 验证机器认证；有应用时验证所有应用读取
  → 撤销临时机器 Token，标记接入完成
  → 撤销管理员 Token

每次发布：
本地 RoleID + SecretID → AppRole 登录 → 短期 Token
  → 读取一个 APP 的最新 KV v2 版本
  → 撤销 Token → 校验 → 原子替换 env
  → CLI 返回成功 → 现有部署脚本创建 Docker 容器
```

## 身份与权限

新身份命名 `baoctl-<machine>-<16位随机十六进制标识>`。一个安装实例一个 AppRole、一个同名 Policy；不同主机各自初始化，禁止复制状态文件或把已接入机器的凭据包含在克隆镜像中。

Policy 只包含登记应用的精确 `kv/data/<classified>/<typename>/<NAME>/env` `read` 和 `auth/token/revoke-self` `update`。没有业务写入、全局通配符、Policy 管理权限或默认 Policy。AppRole 禁用附加 default Policy，使用 service Token：TTL 5 分钟，最大及显式最大 TTL 10 分钟，不续租，每次命令结束主动撤销。

`init` 不要求应用：空机器的 Policy 仅含撤销自身 Token 的权限，不含任何 KV 路径。空机器也可以完成接入并标记为 ready；这个状态表示机器认证已验证，不表示存在应用。后续 `app add` 追加应用权限并验证读取，复用原机器凭据。`--apps` 是初始化时同时登记应用的可选快捷方式。

SecretID 为长期、可多次使用的独立机器凭据，支持无人值守发布。此参数仅用于新建 `baoctl-*` 身份，不更改既有 `agent-*` 身份。短期 Token 无法消除长期机器凭据失窃的风险。

如果机器凭据泄漏，可暴露该机器获准读取的所有 APP，包括精确 data 路径下仍保留的历史 KV 版本；不是只暴露最后一次同步的值。同一 APP 在多台机器共享的数据库等凭据也会同时受影响。root/Docker 管理员可接触本机应用秘密。CLI 无法隔离已失陷的主机管理员。

管理员密码会经过执行接入的机器，因此该机器必须可信。即使之后不保存密码，也不能在已受控主机上安全输入平台管理员密码。

## 可恢复性

schema 2 状态保存 `targets`（期望授权）、`applied_targets`（最后完成授权）、`ready`、安装标识与机器凭据；每个授权目标记录名称、环境和类型。管理操作先保存待完成意图。服务端 Policy 必须等于上次完成版本或当前期望版本，才能继续；其他差异被视为外部变更。角色参数也会完整核对，不直接覆盖已有角色。

一旦取得 SecretID 就立即保存；后续重试复用它。若落盘失败，尝试按 accessor 撤销新 SecretID。极端情况下（服务端签发成功后网络中断或进程被强杀）可能留下未保存的 SecretID，需要管理员通过服务端 accessor 清理。本工具不声称客户端与服务端之间存在原子事务。

状态目录丢失不是普通重试场景：应先确认并处理旧机器身份，不直接生成一个新身份代替。正常 app add 不撤销或替换其他应用权限。

机器命令锁只覆盖 CLI 执行，不覆盖随后 Docker 操作。CI 必须用自身流水线互斥或主机 flock 串行化同一 APP 的同步和整个发布流程。Policy 写入没有 CAS，独立客户端不可并行修改同一个工具管理的 Policy。

## 文件与网络

默认凭据文件 `/etc/baoctl/state.json`，root:root 0600；目录 0700。应用类输出 `/run/baoctl/<APP>.env`，test/dev 分别添加 `.test`/`.development`；文件类输出 `/run/baoctl/<NAME>/<classified>/<FILENAME>`。同样 root:root 0600，目录 0700。程序检查路径祖先的所有者和可写权限，拒绝符号链接及非普通文件；不自动 chmod 既有不安全目录。

正式客户端仅 HTTPS、TLS 1.2+、系统信任链，无跳过 TLS 校验开关，无自动 HTTP 重定向，不读取代理环境变量。客户端默认请求超时 20 秒，清理 Token 使用独立的 10 秒超时。HTTP 错误只显示状态码和固定说明，不打印服务端响应体、请求体或底层可能包含输入值的异常。

临时 env 文件与目标文件在同一个私有目录中，先同步文件再 rename，再同步目录。磁盘异常导致目录 fsync 失败时，命令仍报告失败，但 rename 可能已经完成；调用方必须遵守失败不发布。普通请求和格式错误发生在写入前，不改变已有 env。

env 不承诺原子更新多个 APP；每次只同步一个。无自动离线回退。Server 不可用时保留已有文件但拒绝本次发布。

## 格式选择

应用类针对 Docker CLI 的 env-file，所有值必须为单行字符串。保留字面引号、美元符号、反斜杠、空格，不做 shell 转义。按键名排序生成确定性输出。禁止多行、控制字符和隐式类型转换，避免注入额外变量或改变配置语义。

应用类上游 KV 是该 APP 的变量集合，不另行引入模板。文件类 KV 固定包含 `B64`、`FILENAME`、`SHA256` 三个字符串字段：移除 B64 中的单双引号，按标准 Base64 解码，校验原始字节的 SHA256，再原子替换文件。文件名只允许单个名称，拒绝目录穿越。多行或二进制秘密使用文件类。

授权使用完整目标路径，不以通配符扩大到其他环境或类型。schema 1 读取时映射到 prod/apps，保留旧 Policy 文本顺序及原凭据，下一次管理操作保存为 schema 2。详见 [多环境与文件类密钥](classified-files.md)。

## 官方依据

- [AppRole](https://openbao.org/docs/auth/approle/)
- [Userpass](https://openbao.org/docs/auth/userpass/)
- [Policy](https://openbao.org/docs/concepts/policies/)
- [Token 生命周期](https://openbao.org/docs/concepts/tokens/)
- [Docker run](https://docs.docker.com/reference/cli/docker/container/run/)

API 行为通过临时 OpenBao 2.7.1 实际验证；本地通过不等于生产验收通过。

# 多环境与文件类密钥

[文档索引](README.md) · [完整使用指南](user-guide.md)

## 路径与授权

Web UI 在 `kv/` 引擎下的路径为 `<classified>/<typename>/<NAME>/env`，最后一级 `env` 始终是固定字面量。

| 参数 | 默认值 | 可选值 |
| --- | --- | --- |
| `--classified` | `prod` | `prod`（生产）、`test`（测试）、`dev`（开发） |
| `--typename` | `apps` | `apps`（环境变量）、`files`（原始文件） |

`init`、`app add`、`sync` 都支持这两个参数。`init --apps` 中的所有名称使用同一组分类；机器初始化后，通过 `app add` 追加其他分类。每次调用默认都是 `prod/apps`，不会记住上一次命令的分类。

授权以“环境 + 类型 + 名称”为单位。同一个名称的生产、测试、开发，以及应用类、文件类分别登记；登记和同步需指定相同参数。为 `test/apps/my-api` 授权不会同时授予 `prod/apps/my-api` 或 `test/files/my-api` 的读取权限。

## 应用类

先在 Web UI 创建 `test/apps/altasci-email-platform/env`，再运行：

```bash
sudo baoctl app add altasci-email-platform --classified test
sudo baoctl sync altasci-email-platform --classified test
```

| 环境 | Web UI 路径 | 默认输出 |
| --- | --- | --- |
| `prod` | `prod/apps/altasci-email-platform/env` | `/run/baoctl/altasci-email-platform.env` |
| `test` | `test/apps/altasci-email-platform/env` | `/run/baoctl/altasci-email-platform.env.test` |
| `dev` | `dev/apps/altasci-email-platform/env` | `/run/baoctl/altasci-email-platform.env.development` |

应用类继续按 Docker CLI env-file 格式输出，所有值必须是单行字符串。应用类中的引号按字面量保留。

## 文件类

文件类 KV 必须且只能包含三个字符串字段：

| 字段 | 内容 |
| --- | --- |
| `B64` | 原始文件的标准 Base64 编码；单引号和双引号会被移除后解码 |
| `FILENAME` | 单个文件名，例如 `hello.txt`；不能包含目录、`..` 路径、斜杠或控制字符 |
| `SHA256` | 原始文件的 64 位十六进制 SHA256 摘要，字母大小写均可 |

在 Web UI 的 `kv/` 下创建 `test/files/altasci-email-file/env`，填写以下 JSON。这里的原始内容是 **hello，不包含末尾换行**：

```json
{
  "B64": "'aGVsbG8='",
  "FILENAME": "hello.txt",
  "SHA256": "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
}
```

`B64` 也可以是 `"aGVsbG8="` 或 `"\"aGVsbG8=\""`；这些引号不属于编码内容。程序不执行 shell 展开或转义。二进制文件、多行证书和空文件均可使用文件类；空文件仍需正确的 SHA256。

```bash
sudo baoctl app add altasci-email-file --classified test --typename files
sudo baoctl sync altasci-email-file --classified test --typename files
```

输出为 `/run/baoctl/altasci-email-file/test/hello.txt`。程序按需创建“名称/环境”目录，文件不额外添加 `.env` 后缀。省略分类时读取 `prod/files/altasci-email-file/env`，输出至 `/run/baoctl/altasci-email-file/prod/hello.txt`。

可以在首次初始化时一并登记文件类：

```bash
sudo baoctl init --apps altasci-email-file --classified test --typename files
```

## 校验与覆盖

程序在登记、恢复、`doctor` 和同步时都检查文件字段、Base64、文件名及 SHA256。同步先撤销临时 Token，完成版本和内容校验，再原子替换目标文件。解码失败、摘要不匹配、字段或路径无效，以及 Token 撤销失败均返回非零退出码，保留已有目标文件。

文件权限为 `root:root 0600`，新建私有目录为 `0700`；既有不安全目录或链接会被拒绝。API 响应仍有 2 MiB 上限，包含 Base64 和 JSON 开销。`--expect-version` 同样适用于文件类，校验当前 KV 版本。

`--output-dir`（初始化时配置）替换上述 `/run/baoctl` 根目录。主机重启后需要重新同步。

如果上游修改了 `FILENAME`，会写入新名称的文件；旧名称的文件保留，需由使用者按部署需求清理。`status` 列出文件类目录中已有文件，无法证明这些名称或内容仍对应最新 KV；`doctor` 检查远端配置但不写文件。

## 兼容已有登记

已有 schema 1 的生产应用状态会在读取时映射为 `prod/apps`，保留机器凭据、授权和未完成意图。下一次管理操作保存状态时使用 schema 2；普通 `sync` 和 `status` 不改写状态文件。原有不带分类参数的命令及 `/run/baoctl/<APP>.env` 路径继续可用。旧版二进制不能读取 schema 2；降级前需按实际授权核对状态与服务端 Policy。

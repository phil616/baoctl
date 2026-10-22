# baoctl 文档

[项目首页](../README.md) · [贡献指南](../CONTRIBUTING.md) · [许可证](../LICENSE)

## 从这里开始

首次使用请阅读 [完整使用指南](user-guide.md)。机器初始化与应用接入可以分开进行：先 `init` 建立机器身份，准备好应用 KV 后再 `app add` 和 `sync`。

| 你要做什么 | 文档入口 |
| --- | --- |
| 确认服务端、系统和账号要求 | [前置准备](user-guide.md#1-前置准备) |
| 下载或构建程序 | [安装程序](user-guide.md#2-安装程序) |
| 在 Web UI 创建环境变量 | [创建应用配置](user-guide.md#3-在-openbao-创建应用配置) |
| 初始化机器并接入首个应用 | [首次接入](user-guide.md#4-首次登记本机应用) |
| 验证生成的 env | [同步与验证](user-guide.md#5-同步并验证首个应用) |
| 使用测试/开发环境或还原文件密钥 | [多环境与文件类密钥](classified-files.md) |
| 在同一机器增加应用 | [多应用管理](user-guide.md#6-同一台机器新增应用) |
| 在 SSH 发布脚本中同步配置 | [CI/CD 接入](user-guide.md#7-接入-cicd) |
| 修改变量并再次发布 | [配置更新](user-guide.md#8-修改配置与再次发布) |
| 排查凭据、权限和连接问题 | [运维说明](operations.md) |

## 理解设计

[设计与安全边界](design.md) 解释直接 API 同步、机器身份、最小权限、凭据保存、失败恢复及文件保护。凭据是否过期、主机重启后如何恢复，见 [凭据与文件生命周期](operations.md#凭据与文件生命周期)。

`status` 不保证文件为最新版本，`doctor` 不检查容器业务健康；每次发布仍应以 `sync` 成功和现有业务健康检查为准。

## 参与开发与发布

- [贡献指南](../CONTRIBUTING.md)：开发环境、测试、问题反馈与 Pull Request。
- [发布说明](releasing.md)：推送标签、GitHub Release、Linux amd64 产物与失败重跑。
- [路线图与验证范围](plan.md)：当前能力、测试范围和后续方向。

文档中所有域名、机器名、应用名和变量值均为示例，使用时按自己的部署替换。

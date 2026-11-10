# Codex 0.160.0 登录兼容性检查

2026-10-04，在独立、未登录的 `/private/tmp` CODEX_HOME 中检查本机 ChatGPT 内置 Codex 0.160.0：

- 新打包路径：`/Applications/ChatGPT.app/Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex`。
- 二进制 SHA-256：`6b582e8813ce7e8ed4c52814ee5cf230dba647bf2292df747a4003f2657ef201`。
- `codex login --help` 仍提供 `login`、`status` 与 `--device-auth`，现有调用不传认证、provider 或 endpoint override。
- `app-server generate-ts` 生成的 `GetAccountResponse` 与 ChatGPT Account 字段仍符合现有严格读取校验。相关生成文件保存在本目录。
- 登录仍使用独立 Profile 的 file store、严格环境、全局登录锁及 Profile 操作锁；身份和额度仍通过官方 App Server 校验后提交。

仅允许这个确切版本进入官方登录/重新登录流程。交互运行、退出、purge 与 Desktop 操作仍遵循原版本门槛，未知版本继续拒绝。本证据仅说明命令与协议结构检查，不代表真实账号 OAuth 或多账号兼容性验证。

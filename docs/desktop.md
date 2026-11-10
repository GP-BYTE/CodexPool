# macOS Desktop 激活与恢复（实验性）

日常 `switch` 设置 CodexPool 默认 Profile；要临时把已就绪 Profile 用于 ChatGPT Desktop，使用独立的 `codexpool-desktop-helper`。网页提供入口，并打开终端继续执行确认流程。

**这个功能会临时替换默认 `~/.codex/auth.json`，并把恢复所需的认证数据放在 CodexPool 私有目录。** 它有应用版本与打包路径限制，不保证适配任意新版 ChatGPT。不要把私有数据目录、恢复记录或认证备份分享出去。

## 操作步骤

1. 用 `./codexpool.sh list --json` 在本机查看目标 Profile 的不可变 ID；不要把实际输出贴到公开 Issue。
2. 确认没有未恢复的 Desktop 切换：

   ```bash
   go run ./cmd/codexpool-desktop-helper status
   ```

3. 执行以下命令，将 `<profile-id>` 替换为自己的 Profile ID：

   ```bash
   go run ./cmd/codexpool-desktop-helper activate \
     --profile-id '<profile-id>' \
     --i-understand-this-switches-desktop-auth
   ```

4. 根据终端提示用 `Cmd+Q` 完整退出 ChatGPT。helper 会等待应用退出，不主动杀进程。完成后会请求打开 ChatGPT，请自行核对界面身份。
5. 使用结束后恢复：

   ```bash
   go run ./cmd/codexpool-desktop-helper restore \
     --i-understand-this-switches-desktop-auth
   ```

## 异常恢复

如果状态显示需要恢复，先恢复再开始新的激活。恢复时同样需要完全退出 ChatGPT。只恢复文件、稍后自行启动应用：

```bash
go run ./cmd/codexpool-desktop-helper restore \
  --i-understand-this-switches-desktop-auth --no-launch
```

不要在恢复前删除 CodexPool 数据目录或手动编辑事务文件。恢复目标是第一次切换前的认证状态；helper 的启动成功不等于已验证应用内登录成功。

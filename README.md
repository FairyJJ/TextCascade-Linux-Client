# TextCascade Linux 客户端

跨设备剪贴板同步命令行客户端，连接 TextCascade Server 的 `/api/v1/login` 与 `textcascade.v1` WebSocket。支持 Wayland（`wl-clipboard`）和 X11（`xclip` 或 `xsel`）。

## 构建

需要 Go 1.23 或更高版本，并在构建时联网下载 `gorilla/websocket`：

```sh
go build -trimpath -o textcascade-client .
```

## 安装剪贴板工具

```sh
# Debian / Ubuntu, X11 或 Wayland
sudo apt install wl-clipboard xclip
# Fedora
sudo dnf install wl-clipboard xclip
```

在纯终端/SSH 会话中需要有效桌面会话及 `WAYLAND_DISPLAY` 或 `DISPLAY` 环境变量。

## 运行

```sh
./textcascade-client --server https://your-server.example:8443 --username alice
```

程序会提示输入密码。也可以用 `--password`（会出现在进程参数列表中，不建议日常使用）。自签证书请传入可信 CA：

```sh
./textcascade-client --server https://your-server.example:8443 --username alice --ca /path/to/ca.pem
```

`--insecure` 会关闭服务器证书校验，只建议用于临时测试。客户端每 500ms 检查剪贴板，连接断开后自动登录重连。状态和设备 ID 默认保存在 `~/.config/textcascade/`，可用 `--state` 覆盖状态文件。使用 Ctrl+C 退出。

发送的是未加密文本（协议字段 `encrypted=false`），传输通过 TLS 保护。程序不会同步图片、文件或富文本。

## 图形托盘版（GNOME / Wayland）

客户端包含 PyQt6 托盘控制界面。登录图形桌面后，用户级 systemd 服务会静默启动到托盘；关闭主窗口会隐藏窗口。托盘菜单可打开窗口、暂停/继续同步、重新连接，或退出并停止同步。GNOME 需要启用 AppIndicator 扩展。

### 安装到用户目录

1. 安装 Go 1.23+、Python 3 的 PyQt6、剪贴板工具和 GNOME AppIndicator 扩展；Wayland 推荐 `wl-clipboard`。
2. 构建并安装命令行客户端：

   ```sh
   go build -trimpath -o textcascade-client .
   install -Dm755 textcascade-client ~/.local/bin/textcascade-client
   install -d -m700 ~/.config/textcascade
   ```

3. 创建仅自己可读的密码文件 `~/.config/textcascade/password`，然后执行 `chmod 600 ~/.config/textcascade/password`。
4. 复制 `textcascade-client.service.example` 到 `~/.config/systemd/user/textcascade-client.service`，修改服务地址和账号。
5. 安装托盘程序和服务单元：

   ```sh
   install -Dm755 textcascade_tray.py ~/.local/share/textcascade/textcascade_tray.py
   install -Dm644 deploy/textcascade-tray.service ~/.config/systemd/user/textcascade-tray.service
   systemctl --user daemon-reload
   systemctl --user enable --now textcascade-client.service textcascade-tray.service
   ```

窗口里的自启复选框会同时启用或关闭两个用户服务。托盘 GUI 通过单实例 IPC 工作；从应用菜单再次启动会打开已运行的主窗口。

## 许可与上游

本客户端按 GPL-3.0 发布，完整文本见 [LICENSE](LICENSE)。协议兼容目标为 [long45343/TextCascade-Server](https://github.com/long45343/TextCascade-Server)，来源与许可说明见 [NOTICE](NOTICE)。

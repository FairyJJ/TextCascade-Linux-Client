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

本项目提供 PyQt6 托盘控制程序 `textcascade_tray.py`。它管理 `textcascade-client.service`，由用户级 systemd `graphical-session.target` 启动，以 `--background` 静默进入托盘；关闭主窗口只隐藏窗口，托盘菜单可打开窗口、暂停/继续同步、重新连接或退出并停止同步。GNOME 需要启用 AppIndicator 扩展。

安装托盘程序后，可从应用菜单打开 TextCascade；设置窗口也能切换登录自启。命令行客户端仍负责剪贴板协议和同步。

## 许可与上游

本客户端按 GPL-3.0 发布，完整文本见 [LICENSE](LICENSE)。协议兼容目标为 [long45343/TextCascade-Server](https://github.com/long45343/TextCascade-Server)，来源与许可说明见 [NOTICE](NOTICE)。

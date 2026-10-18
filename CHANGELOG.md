# 更新记录

## v0.1.3

- 实现与 TextCascade 桌面端一致的 PBKDF2-HMAC-SHA256 密钥派生和 AES-256-GCM 加解密。
- 默认加密上传内容，按 `encrypted` 标记解密接收内容；支持配置加密盐与 PBKDF2 迭代次数。
- 验证了用户提供的加密样例可用当前保存的账号凭据成功解密，文本为有效 UTF-8。

## v0.1.2

- 在 GNOME/Wayland 且 XWayland 可用时优先通过 `xclip` 访问剪贴板，避免 `wl-paste` 轮询产生的透明窗口抢焦点。
- 使用 `UTF8_STRING` 读取和写入 X11 剪贴板，修复中文文本编码错误。
- 补充 GNOME/Wayland、Arch Linux 和 EndeavourOS 安装说明。

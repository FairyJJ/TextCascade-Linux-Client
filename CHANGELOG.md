# 更新记录

## v0.1.2

- 在 GNOME/Wayland 且 XWayland 可用时优先通过 `xclip` 访问剪贴板，避免 `wl-paste` 轮询产生的透明窗口抢焦点。
- 使用 `UTF8_STRING` 读取和写入 X11 剪贴板，修复中文文本编码错误。
- 补充 GNOME/Wayland、Arch Linux 和 EndeavourOS 安装说明。

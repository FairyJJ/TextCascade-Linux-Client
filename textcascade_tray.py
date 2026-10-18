#!/usr/bin/env python3
"""TextCascade Linux tray controller for the systemd user service."""
from __future__ import annotations

import subprocess
import sys
import os
from pathlib import Path

from PyQt6.QtCore import Qt, QTimer, QSize, QLockFile
from PyQt6.QtGui import QAction, QColor, QDesktopServices, QIcon, QPainter, QPen, QBrush, QFont
from PyQt6.QtCore import QUrl
from PyQt6.QtNetwork import QLocalServer, QLocalSocket
from PyQt6.QtWidgets import (
    QApplication, QCheckBox, QFrame, QHBoxLayout, QLabel, QMainWindow, QMenu,
    QMessageBox, QPushButton, QSystemTrayIcon, QVBoxLayout, QWidget,
)

APP_NAME = "TextCascade"
SERVICE = "textcascade-client.service"
TRAY_SERVICE = "textcascade-tray.service"
HOME = Path.home()
CONFIG_DIR = HOME / ".config/textcascade"
AUTOSTART_DIR = HOME / ".config/autostart"
AUTOSTART_FILE = AUTOSTART_DIR / "textcascade-tray.desktop"
UNIT_FILE = HOME / ".config/systemd/user" / SERVICE


def command(args: list[str], timeout: float = 5) -> subprocess.CompletedProcess[str]:
    return subprocess.run(args, text=True, capture_output=True, timeout=timeout, check=False)


def service_active() -> bool:
    return command(["systemctl", "--user", "is-active", SERVICE]).stdout.strip() == "active"


def autostart_enabled() -> bool:
	return (command(["systemctl", "--user", "is-enabled", SERVICE]).stdout.strip() == "enabled"
	        and command(["systemctl", "--user", "is-enabled", TRAY_SERVICE]).stdout.strip() == "enabled")


def read_settings() -> tuple[str, str, str]:
    server, username, name = "https://textcascade.cherryjay.top:8443", "jay", "Linux"
    try:
        for line in UNIT_FILE.read_text().splitlines():
            if line.startswith("ExecStart="):
                parts = line.partition("ExecStart=")[2].split()
                for i, part in enumerate(parts[:-1]):
                    if part == "--server": server = parts[i + 1]
                    elif part == "--username": username = parts[i + 1]
                    elif part == "--name": name = parts[i + 1]
    except OSError:
        pass
    return server, username, name


def tray_icon() -> QIcon:
    from PyQt6.QtGui import QPixmap
    pix = QPixmap(64, 64)
    pix.fill(Qt.GlobalColor.transparent)
    p = QPainter(pix)
    p.setRenderHint(QPainter.RenderHint.Antialiasing)
    p.setBrush(QBrush(QColor("#5b7cfa")))
    p.setPen(Qt.PenStyle.NoPen)
    p.drawRoundedRect(5, 5, 54, 54, 17, 17)
    p.setPen(QPen(QColor("white"), 3.4, Qt.PenStyle.SolidLine, Qt.PenCapStyle.RoundCap, Qt.PenJoinStyle.RoundJoin))
    p.setBrush(Qt.BrushStyle.NoBrush)
    p.drawRoundedRect(17, 17, 30, 24, 6, 6)
    p.drawLine(23, 25, 41, 25)
    p.drawLine(23, 32, 37, 32)
    p.drawLine(25, 41, 22, 47)
    p.end()
    return QIcon(pix)


class MainWindow(QMainWindow):
    def __init__(self, tray: QSystemTrayIcon):
        super().__init__()
        self.tray = tray
        self.setWindowTitle("TextCascade 剪贴板同步")
        self.setWindowIcon(tray_icon())
        self.setFixedSize(470, 390)
        self.setStyleSheet("""
            QWidget { background:#f5f7fb; color:#20283a; font-family:'Noto Sans CJK SC','Noto Sans',sans-serif; }
            QFrame#card { background:white; border:1px solid #e7ebf3; border-radius:14px; }
            QLabel#title { font-size:21px; font-weight:700; }
            QLabel#subtle { color:#778198; }
            QPushButton { background:#5b7cfa; color:white; border:0; border-radius:9px; padding:10px 15px; font-weight:600; }
            QPushButton:hover { background:#4768e8; }
            QPushButton#secondary { background:#e9edfa; color:#33415f; }
            QPushButton#danger { background:#fff0f0; color:#b63d45; }
            QCheckBox { spacing:8px; }
        """)

        root = QWidget()
        layout = QVBoxLayout(root)
        layout.setContentsMargins(24, 22, 24, 20)
        layout.setSpacing(15)
        heading = QHBoxLayout()
        brand = QLabel("TextCascade")
        brand.setObjectName("title")
        heading.addWidget(brand)
        heading.addStretch()
        self.status = QLabel("正在检查…")
        heading.addWidget(self.status)
        layout.addLayout(heading)
        subtitle = QLabel("Linux 剪贴板同步客户端")
        subtitle.setObjectName("subtle")
        layout.addWidget(subtitle)

        card = QFrame()
        card.setObjectName("card")
        info = QVBoxLayout(card)
        info.setContentsMargins(18, 16, 18, 16)
        info.setSpacing(11)
        self.server_label = QLabel()
        self.user_label = QLabel()
        self.device_label = QLabel()
        info.addWidget(self.server_label)
        info.addWidget(self.user_label)
        info.addWidget(self.device_label)
        layout.addWidget(card)

        self.autostart = QCheckBox("登录桌面时自动启动，并静默到托盘")
        self.autostart.toggled.connect(self.set_autostart)
        layout.addWidget(self.autostart)
        layout.addStretch()

        actions = QHBoxLayout()
        self.toggle_button = QPushButton("暂停同步")
        self.toggle_button.clicked.connect(self.toggle_service)
        actions.addWidget(self.toggle_button)
        restart = QPushButton("重新连接")
        restart.setObjectName("secondary")
        restart.clicked.connect(self.restart_service)
        actions.addWidget(restart)
        settings = QPushButton("打开配置目录")
        settings.setObjectName("secondary")
        settings.clicked.connect(lambda: QDesktopServices.openUrl(QUrl.fromLocalFile(str(CONFIG_DIR))))
        actions.addWidget(settings)
        layout.addLayout(actions)
        footer = QLabel("关闭此窗口会将程序隐藏到托盘。要退出托盘程序，请使用托盘菜单。")
        footer.setObjectName("subtle")
        footer.setWordWrap(True)
        layout.addWidget(footer)
        self.setCentralWidget(root)

        self.timer = QTimer(self)
        self.timer.timeout.connect(self.refresh)
        self.timer.start(2500)
        self.refresh()

    def refresh(self):
        active = service_active()
        server, username, name = read_settings()
        self.server_label.setText(f"服务器　{server}")
        self.user_label.setText(f"账号　　{username}")
        self.device_label.setText(f"设备　　{name}")
        logs = command(["journalctl", "--user", "-u", SERVICE, "-n", "30", "--no-pager", "-o", "cat"], 4).stdout
        last_connected = logs.rfind("已连接并完成登录")
        last_error = logs.rfind("连接中断:")
        if not active:
            text, color = "● 已暂停", "#8992a5"
        elif last_error > last_connected:
            text, color = "● 正在重连", "#d38b25"
        elif last_connected >= 0:
            text, color = "● 已连接", "#26936a"
        else:
            text, color = "● 正在连接", "#d38b25"
        self.status.setText(text)
        self.status.setStyleSheet(f"color:{color}; font-weight:700")
        self.toggle_button.setText("暂停同步" if active else "开始同步")
        self.autostart.blockSignals(True)
        self.autostart.setChecked(autostart_enabled())
        self.autostart.blockSignals(False)
        self.tray.setToolTip(f"TextCascade · {text[2:]}")

    def toggle_service(self):
        action = "stop" if service_active() else "start"
        result = command(["systemctl", "--user", action, SERVICE], 15)
        if result.returncode:
            QMessageBox.warning(self, "操作失败", result.stderr.strip() or "无法更改同步服务状态。")
        self.refresh()

    def restart_service(self):
        result = command(["systemctl", "--user", "restart", SERVICE], 20)
        if result.returncode:
            QMessageBox.warning(self, "重新连接失败", result.stderr.strip() or "无法重新启动同步服务。")
        self.refresh()

    def set_autostart(self, enabled: bool):
        if enabled:
            command(["systemctl", "--user", "enable", "--now", SERVICE], 20)
            result = command(["systemctl", "--user", "enable", "--now", TRAY_SERVICE], 20)
        else:
            result = command(["systemctl", "--user", "disable", "--now", TRAY_SERVICE], 20)
            command(["systemctl", "--user", "disable", "--now", SERVICE], 20)
            AUTOSTART_FILE.unlink(missing_ok=True)
        if result.returncode:
            QMessageBox.warning(self, "设置失败", result.stderr.strip() or "无法更新开机启动设置。")
        self.refresh()

    def closeEvent(self, event):
        event.ignore()
        self.hide()


class TrayApp:
    def __init__(self, app: QApplication, background: bool):
        self.app = app
        self.tray = QSystemTrayIcon(tray_icon(), app)
        self.tray.setToolTip("TextCascade · 正在检查")
        self.menu = QMenu()
        self.open_action = QAction("打开窗口", self.menu)
        self.open_action.triggered.connect(self.show_window)
        self.menu.addAction(self.open_action)
        self.status_action = QAction("检查状态…", self.menu)
        self.status_action.setEnabled(False)
        self.menu.addAction(self.status_action)
        self.menu.addSeparator()
        self.start_action = QAction("暂停同步", self.menu)
        self.start_action.triggered.connect(self.quick_toggle)
        self.menu.addAction(self.start_action)
        restart_action = QAction("重新连接", self.menu)
        restart_action.triggered.connect(lambda: command(["systemctl", "--user", "restart", SERVICE], 20))
        self.menu.addAction(restart_action)
        self.menu.addSeparator()
        quit_action = QAction("退出 TextCascade 并停止同步", self.menu)
        quit_action.triggered.connect(self.exit_app)
        self.menu.addAction(quit_action)
        self.tray.setContextMenu(self.menu)
        self.window = MainWindow(self.tray)
        self.tray.activated.connect(self.activated)
        self.tray.show()
        self.refresh_timer = QTimer()
        self.refresh_timer.timeout.connect(self.refresh_menu)
        self.refresh_timer.start(4000)
        self.refresh_menu()
        if not background:
            self.window.show()

    def show_window(self):
        self.window.show()
        self.window.raise_()
        self.window.activateWindow()

    def activated(self, reason):
        if reason in (QSystemTrayIcon.ActivationReason.DoubleClick, QSystemTrayIcon.ActivationReason.Trigger):
            self.show_window()

    def quick_toggle(self):
        command(["systemctl", "--user", "stop" if service_active() else "start", SERVICE], 15)
        self.refresh_menu()

    def exit_app(self):
        command(["systemctl", "--user", "stop", SERVICE], 15)
        self.app.quit()

    def refresh_menu(self):
        active = service_active()
        self.status_action.setText("同步运行中" if active else "同步已暂停")
        self.start_action.setText("暂停同步" if active else "开始同步")
        self.tray.setToolTip("TextCascade · " + ("同步运行中" if active else "同步已暂停"))
        self.window.refresh()

    def activate_socket(self, server: QLocalServer):
        socket = server.nextPendingConnection()
        if socket is None:
            return
        def show_if_requested():
            payload = bytes(socket.readAll())
            if b"show" in payload:
                self.show_window()
            socket.disconnectFromServer()
        socket.readyRead.connect(show_if_requested)
        if socket.bytesAvailable():
            show_if_requested()


def main():
    app = QApplication(sys.argv)
    app.setApplicationName(APP_NAME)
    app.setQuitOnLastWindowClosed(False)
    lock_path = CONFIG_DIR / "tray.lock"
    CONFIG_DIR.mkdir(parents=True, exist_ok=True)
    lock = QLockFile(str(lock_path))
    if not lock.tryLock(100):
        socket = QLocalSocket()
        socket.connectToServer(f"textcascade-tray-{os.getuid()}")
        if socket.waitForConnected(500):
            socket.write(b"show")
            socket.waitForBytesWritten(500)
            socket.disconnectFromServer()
        return 0
    tray_app = TrayApp(app, "--background" in sys.argv)
    server = QLocalServer(app)
    server_name = f"textcascade-tray-{os.getuid()}"
    QLocalServer.removeServer(server_name)
    server.listen(server_name)
    server.newConnection.connect(lambda: tray_app.activate_socket(server))
    tray_app.ipc_server = server
    if not QSystemTrayIcon.isSystemTrayAvailable():
        tray_app.show_window()
        tray_app.window.status.setText("● 托盘不可用，窗口保持可见")
        tray_app.window.status.setStyleSheet("color:#d38b25; font-weight:700")
    return app.exec()


if __name__ == "__main__":
    raise SystemExit(main())

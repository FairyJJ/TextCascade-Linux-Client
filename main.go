package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"golang.org/x/term"
)

const subprotocol = "textcascade.v1"

type loginResponse struct {
	Token                    string `json:"token"`
	MaxTextBytes             int    `json:"maxTextBytes"`
	HeartbeatIntervalSeconds int    `json:"heartbeatIntervalSeconds"`
}
type savedState struct {
	Version uint64 `json:"version"`
	Payload string `json:"payload"`
}
type encryptedPayload struct {
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
	Tag        string `json:"tag"`
}
type serverMessage struct {
	Type      string `json:"type"`
	Version   uint64 `json:"version"`
	ID        string `json:"id"`
	Payload   string `json:"payload"`
	Encrypted bool   `json:"encrypted"`
	Hash      string `json:"hash"`
	Latest    *struct {
		Version   uint64 `json:"version"`
		Payload   string `json:"payload"`
		Encrypted bool   `json:"encrypted"`
		Hash      string `json:"hash"`
	} `json:"latest"`
	ServerTimeUTC string `json:"serverTimeUtc"`
	Message       string `json:"message"`
}
type client struct {
	base, username, password, id, name, statePath, salt string
	hashRounds                                          int
	encryption                                          bool
	key                                                 []byte
	insecure                                            bool
	caFile                                              string
	maxText                                             int
	heartbeat                                           int
	state                                               savedState
	mu                                                  sync.Mutex
	conn                                                *websocket.Conn
}

func main() {
	base := flag.String("server", "", "服务端地址，例如 https://sync.example.com:8443")
	user := flag.String("username", "", "账号")
	pass := flag.String("password", "", "密码；省略时交互输入")
	passFile := flag.String("password-file", "", "从权限受限的文件读取密码")
	name := flag.String("name", "Linux Clipboard", "设备名称")
	clientID := flag.String("client-id", "", "设备唯一 ID；省略时自动生成并保存")
	statePath := flag.String("state", defaultStatePath(), "状态文件路径")
	ca := flag.String("ca", "", "自签或私有 CA PEM 文件")
	insecure := flag.Bool("insecure", false, "跳过 TLS 证书校验（仅临时测试）")
	poll := flag.Duration("poll", 500*time.Millisecond, "剪贴板检查间隔")
	check := flag.Bool("check", false, "仅验证登录和 WebSocket 协议，不读取或修改剪贴板")
	encrypt := flag.Bool("encrypt", true, "启用与 TextCascade 桌面端兼容的端到端加密")
	salt := flag.String("encryption-salt", "", "端到端加密盐后缀，必须与其他设备一致")
	rounds := flag.Int("hash-rounds", 664937, "PBKDF2-HMAC-SHA256 迭代次数，必须与其他设备一致")
	flag.Parse()
	if *base == "" || *user == "" {
		flag.Usage()
		os.Exit(2)
	}
	if *passFile != "" {
		b, err := os.ReadFile(*passFile)
		if err != nil {
			fatal(err)
		}
		*pass = strings.TrimRight(string(b), "\r\n")
	}
	if *pass == "" {
		fmt.Fprint(os.Stderr, "密码: ")
		b, err := readPassword()
		if err != nil {
			fatal(err)
		}
		*pass = string(b)
		fmt.Fprintln(os.Stderr)
	}
	c := &client{base: strings.TrimRight(*base, "/"), username: *user, password: *pass, name: *name, statePath: *statePath, salt: *salt, hashRounds: *rounds, encryption: *encrypt, insecure: *insecure, caFile: *ca, maxText: 524288, heartbeat: 30}
	if c.hashRounds <= 0 {
		fatal(errors.New("hash-rounds 必须大于 0"))
	}
	if c.encryption {
		c.key = deriveKey(c.username, c.password, c.salt, c.hashRounds)
	}
	if err := c.loadState(); err != nil {
		fatal(err)
	}
	if *clientID != "" {
		c.id = *clientID
	} else {
		c.id = readOrCreateID(*statePath)
	}
	if c.id == "" {
		fatal(errors.New("无法创建客户端 ID"))
	}
	if *check {
		if err := c.probe(); err != nil {
			fatal(err)
		}
		fmt.Println("检查通过：账号登录成功，WebSocket 握手及 hello/welcome 协议正常。")
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Println("TextCascade Linux 客户端已启动；Ctrl+C 退出。")
	fmt.Printf("设备: %s (%s)\n", c.name, c.id)
	if !clipboardAvailable() {
		fatal(errors.New("找不到可用剪贴板命令。Wayland 安装 wl-clipboard；X11 安装 xclip 或 xsel"))
	}
	for ctx.Err() == nil {
		if err := c.syncSession(ctx, *poll); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "连接中断: %v；5 秒后重试\n", err)
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}
	}
	fmt.Println("已退出。")
}

func (c *client) probe() error {
	token, err := c.login()
	if err != nil {
		return err
	}
	wsURL := strings.Replace(strings.Replace(c.base, "https://", "wss://", 1), "http://", "ws://", 1) + "/api/v1/sync"
	d := websocket.Dialer{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: c.insecure}, Subprotocols: []string{subprotocol}, HandshakeTimeout: 15 * time.Second}
	if c.caFile != "" {
		pem, e := os.ReadFile(c.caFile)
		if e != nil {
			return e
		}
		roots, _ := x509.SystemCertPool()
		if roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return errors.New("CA 文件中没有可用证书")
		}
		d.TLSClientConfig.RootCAs = roots
	}
	conn, _, err := d.Dial(wsURL, http.Header{"Authorization": []string{"Bearer " + token}})
	if err != nil {
		return fmt.Errorf("WebSocket 握手失败: %w", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err = conn.WriteJSON(map[string]any{"type": "hello", "clientId": c.id, "clientName": c.name, "lastServerVersion": c.state.Version, "snapshot": nil}); err != nil {
		return err
	}
	for {
		_, data, e := conn.ReadMessage()
		if e != nil {
			return e
		}
		var msg serverMessage
		if e = json.Unmarshal(data, &msg); e != nil {
			continue
		}
		if msg.Type == "welcome" {
			return nil
		}
		if msg.Type == "error" {
			return fmt.Errorf("服务端拒绝 hello: %s", msg.Message)
		}
	}
}
func (c *client) httpClient() (*http.Client, error) {
	tr := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: c.insecure}}
	if c.caFile != "" {
		pem, err := os.ReadFile(c.caFile)
		if err != nil {
			return nil, err
		}
		roots, _ := x509.SystemCertPool()
		if roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("CA 文件中没有可用证书")
		}
		tr.TLSClientConfig.RootCAs = roots
	}
	return &http.Client{Transport: tr, Timeout: 15 * time.Second}, nil
}
func (c *client) login() (string, error) {
	h, err := c.httpClient()
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]string{"username": c.username, "password": c.password})
	req, err := http.NewRequest("POST", c.base+"/api/v1/login", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := h.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != 200 {
		return "", fmt.Errorf("登录失败 (%s): %s", res.Status, strings.TrimSpace(string(data)))
	}
	var out loginResponse
	if err = json.Unmarshal(data, &out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", errors.New("登录响应没有 token")
	}
	if out.MaxTextBytes > 0 {
		c.maxText = out.MaxTextBytes
	}
	if out.HeartbeatIntervalSeconds > 0 {
		c.heartbeat = out.HeartbeatIntervalSeconds
	}
	return out.Token, nil
}
func (c *client) syncSession(ctx context.Context, poll time.Duration) error {
	token, err := c.login()
	if err != nil {
		return err
	}
	wsURL := strings.Replace(strings.Replace(c.base, "https://", "wss://", 1), "http://", "ws://", 1) + "/api/v1/sync"
	header := http.Header{"Authorization": []string{"Bearer " + token}}
	dialer := websocket.Dialer{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: c.insecure}, Subprotocols: []string{subprotocol}, HandshakeTimeout: 15 * time.Second}
	if c.caFile != "" {
		pem, e := os.ReadFile(c.caFile)
		if e != nil {
			return e
		}
		roots, _ := x509.SystemCertPool()
		if roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return errors.New("CA 文件中没有可用证书")
		}
		dialer.TLSClientConfig.RootCAs = roots
	}
	conn, res, err := dialer.DialContext(ctx, wsURL, header)
	if err != nil {
		if res != nil {
			return fmt.Errorf("WebSocket 握手失败 (%s): %w", res.Status, err)
		}
		return err
	}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	watchDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-watchDone:
		}
	}()
	defer func() { close(watchDone); _ = conn.Close(); c.mu.Lock(); c.conn = nil; c.mu.Unlock() }()
	conn.SetReadLimit(int64(c.maxText + 256*1024))
	clip, clipErr := clipboardRead()
	if clipErr != nil {
		clip = ""
	}
	snap := any(nil)
	if clip != "" {
		payload, encrypted, e := c.encodePayload(clip)
		if e != nil {
			return e
		}
		snap = map[string]any{"payload": payload, "encrypted": encrypted, "hash": hash(clip), "localModifiedAtUtc": time.Now().UTC().Format(time.RFC3339)}
	}
	hello := map[string]any{"type": "hello", "clientId": c.id, "clientName": c.name, "lastServerVersion": c.state.Version, "snapshot": snap}
	if err = conn.WriteJSON(hello); err != nil {
		return err
	}
	fmt.Println("已连接并完成登录")
	stopPoll := make(chan struct{})
	defer close(stopPoll)
	go c.pollClipboard(stopPoll, poll)
	for {
		conn.SetReadDeadline(time.Now().Add(time.Duration(c.heartbeat*2+15) * time.Second))
		_, data, e := conn.ReadMessage()
		if e != nil {
			return e
		}
		var m serverMessage
		if e = json.Unmarshal(data, &m); e != nil {
			continue
		}
		switch m.Type {
		case "welcome":
			if m.Latest != nil {
				if e = c.apply(m.Latest.Payload, m.Latest.Encrypted, m.Latest.Version); e != nil {
					return e
				}
			}
		case "clip":
			if e = c.apply(m.Payload, m.Encrypted, m.Version); e != nil {
				return e
			}
		case "ping":
			e = c.writeJSON(map[string]string{"type": "pong", "clientTimeUtc": time.Now().UTC().Format(time.RFC3339)})
			if e != nil {
				return e
			}
		case "bye":
			return fmt.Errorf("服务器结束连接: %s", m.Message)
		case "error":
			fmt.Fprintf(os.Stderr, "服务端提示: %s\n", m.Message)
		}
		select {
		case <-ctx.Done():
			return nil
		default:
		}
	}
}
func (c *client) writeJSON(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return errors.New("连接已断开")
	}
	return c.conn.WriteJSON(v)
}
func (c *client) pollClipboard(stop <-chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	lastSent, _ := clipboardRead()
	candidate := ""
	var candidateSince time.Time
	const stableFor = 1200 * time.Millisecond
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			text, err := clipboardRead()
			if err != nil || text == "" {
				continue
			}
			if text == lastSent {
				candidate = ""
				continue
			}
			c.mu.Lock()
			same := text == c.state.Payload
			conn := c.conn
			c.mu.Unlock()
			if same {
				lastSent = text
				candidate = ""
				continue
			}
			if candidate != text {
				candidate = text
				candidateSince = time.Now()
				continue
			}
			if time.Since(candidateSince) < stableFor || conn == nil {
				continue
			}
			if len([]byte(text)) > c.maxText {
				fmt.Fprintln(os.Stderr, "剪贴板文本超过服务端大小限制，已跳过")
				lastSent, candidate = text, ""
				continue
			}
			payload, encrypted, encErr := c.encodePayload(text)
			if encErr != nil {
				fmt.Fprintf(os.Stderr, "加密剪贴板失败: %v\n", encErr)
				continue
			}
			err = c.writeJSON(map[string]any{"type": "clip", "id": randomHex(16), "payload": payload, "encrypted": encrypted, "hash": hash(text)})
			if err == nil {
				lastSent, candidate = text, ""
				fmt.Println("已发送本机剪贴板")
			}
		}
	}
}
func (c *client) apply(payload string, encrypted bool, version uint64) error {
	text := payload
	if encrypted {
		if len(c.key) == 0 {
			return errors.New("收到加密剪贴板，但本机未启用加密")
		}
		var p encryptedPayload
		if err := json.Unmarshal([]byte(payload), &p); err != nil {
			return fmt.Errorf("解析加密剪贴板失败: %w", err)
		}
		plain, err := decryptPayload(p, c.key)
		if err != nil {
			return fmt.Errorf("解密剪贴板失败；请确认账号密码、加密盐和迭代次数与其他设备一致: %w", err)
		}
		text = plain
	}
	if text == "" {
		return nil
	}
	if len([]byte(text)) > c.maxText {
		return fmt.Errorf("服务端文本超过客户端限制")
	}
	c.mu.Lock()
	if version < c.state.Version {
		c.mu.Unlock()
		return nil
	}
	if text == c.state.Payload {
		changed := version > c.state.Version
		if changed {
			c.state.Version = version
		}
		c.mu.Unlock()
		if changed {
			return c.saveState()
		}
		return nil
	}
	c.mu.Unlock()
	current, readErr := clipboardRead()
	if readErr != nil || current != text {
		if err := clipboardWrite(text); err != nil {
			return fmt.Errorf("写入系统剪贴板失败: %w", err)
		}
	}
	c.mu.Lock()
	c.state.Payload = text
	if version > c.state.Version {
		c.state.Version = version
	}
	c.mu.Unlock()
	if err := c.saveState(); err != nil {
		return err
	}
	if readErr != nil || current != text {
		fmt.Println("剪贴板已从服务端更新")
	}
	return nil
}

func (c *client) encodePayload(text string) (string, bool, error) {
	if !c.encryption {
		return text, false, nil
	}
	p, err := encryptPayload(text, c.key)
	if err != nil {
		return "", false, err
	}
	b, err := json.Marshal(p)
	return string(b), true, err
}

func deriveKey(username, password, saltSuffix string, rounds int) []byte {
	salt := []byte(username + "$" + password + "$" + saltSuffix)
	return pbkdf2SHA256([]byte(password), salt, rounds, 32)
}

func encryptPayload(text string, key []byte) (encryptedPayload, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return encryptedPayload{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return encryptedPayload{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return encryptedPayload{}, err
	}
	sealed := gcm.Seal(nil, nonce, []byte(text), nil)
	cut := len(sealed) - gcm.Overhead()
	return encryptedPayload{base64.StdEncoding.EncodeToString(nonce), base64.StdEncoding.EncodeToString(sealed[:cut]), base64.StdEncoding.EncodeToString(sealed[cut:])}, nil
}

func decryptPayload(p encryptedPayload, key []byte) (string, error) {
	nonce, err := base64.StdEncoding.DecodeString(p.Nonce)
	if err != nil {
		return "", err
	}
	ciphertext, err := base64.StdEncoding.DecodeString(p.Ciphertext)
	if err != nil {
		return "", err
	}
	tag, err := base64.StdEncoding.DecodeString(p.Tag)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(nonce) != gcm.NonceSize() || len(tag) != gcm.Overhead() {
		return "", errors.New("不支持的 nonce 或 tag 长度")
	}
	return func() (string, error) {
		plain, e := gcm.Open(nil, nonce, append(ciphertext, tag...), nil)
		return string(plain), e
	}()
}

// PBKDF2-HMAC-SHA256 (RFC 2898), with a 32-byte output key.
func pbkdf2SHA256(password, salt []byte, rounds, keyLen int) []byte {
	const hLen = 32
	out := make([]byte, keyLen)
	result := out
	for block := uint32(1); len(out) > 0; block++ {
		mac := hmac.New(sha256.New, password)
		var counter [4]byte
		binary.BigEndian.PutUint32(counter[:], block)
		mac.Write(salt)
		mac.Write(counter[:])
		u := mac.Sum(nil)
		t := append([]byte(nil), u...)
		for i := 1; i < rounds; i++ {
			mac = hmac.New(sha256.New, password)
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		n := len(out)
		if n > hLen {
			n = hLen
		}
		copy(out, t[:n])
		out = out[n:]
	}
	return result
}
func clipboardRead() (string, error) {
	// On GNOME/Wayland, wl-paste may have to create a temporary transparent
	// surface for every read when the compositor lacks the wlroots data-control
	// protocol. Polling that surface steals focus and looks like repeated input.
	// XWayland's clipboard bridge avoids that popup, so prefer xclip whenever
	// both displays are available.
	if os.Getenv("DISPLAY") != "" {
		if _, e := exec.LookPath("xclip"); e == nil {
			return clipboardReadXclip()
		}
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		if _, e := exec.LookPath("wl-paste"); e == nil {
			for _, mime := range []string{"text/plain;charset=utf-8", "text/plain"} {
				b, err := exec.Command("wl-paste", "--no-newline", "--type", mime).Output()
				if err == nil {
					return validClipboardText(b)
				}
			}
			b, err := exec.Command("wl-paste", "--no-newline").Output()
			if err == nil {
				return validClipboardText(b)
			}
			return "", err
		}
	}
	if _, e := exec.LookPath("xclip"); e == nil {
		b, err := exec.Command("xclip", "-selection", "clipboard", "-o").Output()
		if err != nil {
			return "", err
		}
		return validClipboardText(b)
	}
	if _, e := exec.LookPath("xsel"); e == nil {
		b, err := exec.Command("xsel", "--clipboard", "--output").Output()
		if err != nil {
			return "", err
		}
		return validClipboardText(b)
	}
	if _, e := exec.LookPath("wl-paste"); e == nil {
		b, err := exec.Command("wl-paste", "--no-newline").Output()
		if err != nil {
			return "", err
		}
		return validClipboardText(b)
	}
	return "", errors.New("clipboard utility not found")
}
func clipboardReadXclip() (string, error) {
	for _, target := range []string{"UTF8_STRING", "text/plain;charset=utf-8", "text/plain"} {
		b, err := exec.Command("xclip", "-selection", "clipboard", "-o", "-target", target).Output()
		if err == nil {
			return validClipboardText(b)
		}
	}
	return "", errors.New("xclip could not read clipboard text")
}
func validClipboardText(b []byte) (string, error) {
	if !utf8.Valid(b) {
		return "", errors.New("clipboard text is not valid UTF-8")
	}
	return string(b), nil
}
func clipboardAvailable() bool {
	for _, name := range []string{"wl-paste", "xclip", "xsel"} {
		if _, err := exec.LookPath(name); err == nil {
			return true
		}
	}
	return false
}
func clipboardWrite(text string) error {
	var cmd *exec.Cmd
	// On GNOME/Wayland, publish through the native Wayland clipboard even when
	// XWayland is available. CopyQ's clipboard monitor also integrates with
	// Wayland; publishing through xclip makes the owner bounce through the
	// XWayland bridge and can trigger focus flicker while CopyQ is open.
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		if _, e := exec.LookPath("wl-copy"); e == nil {
			cmd = exec.Command("wl-copy", "--type", "text/plain;charset=utf-8")
		}
	}
	if os.Getenv("DISPLAY") != "" {
		if cmd == nil {
			if _, e := exec.LookPath("xclip"); e == nil {
				cmd = exec.Command("xclip", "-selection", "clipboard", "-target", "UTF8_STRING", "-i")
			}
		}
	}
	if cmd == nil {
		if _, e := exec.LookPath("xclip"); e == nil {
			cmd = exec.Command("xclip", "-selection", "clipboard", "-target", "UTF8_STRING", "-i")
		} else if _, e := exec.LookPath("xsel"); e == nil {
			cmd = exec.Command("xsel", "--clipboard", "--input")
		} else if _, e := exec.LookPath("wl-copy"); e == nil {
			cmd = exec.Command("wl-copy")
		}
	}
	if cmd == nil {
		return errors.New("clipboard utility not found")
	}
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}
func hash(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
func randomHex(n int) string {
	b := make([]byte, n)
	if _, e := rand.Read(b); e != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
func defaultStatePath() string {
	d, e := os.UserConfigDir()
	if e != nil {
		return ".config/textcascade/state.json"
	}
	return filepath.Join(d, "textcascade", "state.json")
}
func readOrCreateID(statePath string) string {
	path := filepath.Join(filepath.Dir(statePath), "client-id")
	if b, e := os.ReadFile(path); e == nil && strings.TrimSpace(string(b)) != "" {
		return strings.TrimSpace(string(b))
	}
	id := randomHex(16)
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	_ = os.WriteFile(path, []byte(id+"\n"), 0600)
	return id
}
func (c *client) loadState() error {
	b, e := os.ReadFile(c.statePath)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	return json.Unmarshal(b, &c.state)
}
func (c *client) saveState() error {
	if e := os.MkdirAll(filepath.Dir(c.statePath), 0700); e != nil {
		return e
	}
	b, e := json.Marshal(c.state)
	if e != nil {
		return e
	}
	tmp := c.statePath + ".tmp"
	if e = os.WriteFile(tmp, b, 0600); e != nil {
		return e
	}
	return os.Rename(tmp, c.statePath)
}
func readPassword() ([]byte, error) {
	if !term.IsTerminal(int(syscall.Stdin)) {
		return nil, errors.New("非交互终端请通过 --password 提供密码")
	}
	return term.ReadPassword(int(syscall.Stdin))
}
func fatal(err error) { fmt.Fprintln(os.Stderr, "错误:", err); os.Exit(1) }

package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"xgift/internal/proxy"
	"xgift/internal/vault"
)

const defaultBearer = "Bearer AAAAAAAAAAAAAAAAAAAAANRILgAAAAAAnNwIzUejRCOuH5E6I8xnZz4puTs%3D1Zv7ttfk8LF81IUq16cHjhLTvJu4FA33AGWWjCpTnA"
const defaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"

var stripeKeyPattern = regexp.MustCompile(`^pk_live_[A-Za-z0-9]+$`)

type wizard struct {
	r   *bufio.Reader
	tty bool
}

func (w *wizard) line() (string, error) {
	s, err := w.r.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && s != "") {
		return "", errors.New("读取输入失败")
	}
	return strings.TrimRight(s, "\r\n"), nil
}

// prompt reads one line; empty input falls back to def.
func (w *wizard) prompt(label, def string) (string, error) {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s: ", label)
	}
	s, err := w.line()
	if err != nil {
		return "", err
	}
	if s == "" {
		return def, nil
	}
	return s, nil
}

// secret hides input on a terminal; piped stdin is read as plain lines.
func (w *wizard) secret(label string) (string, error) {
	fmt.Printf("%s: ", label)
	if w.tty {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return "", errors.New("读取输入失败")
		}
		return string(b), nil
	}
	return w.line()
}

func (w *wizard) yesNo(label string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	s, err := w.prompt(fmt.Sprintf("%s [%s]", label, hint), "")
	if err != nil {
		return false, err
	}
	if s == "" {
		return def, nil
	}
	switch strings.ToLower(s) {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	}
	return false, errors.New("请输入 y 或 n")
}

func tail(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return s[len(s)-4:]
}

func luhnValid(number string) bool {
	sum := 0
	for i, n := range number {
		d := int(n - '0')
		if (len(number)-i)%2 == 0 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return sum%10 == 0
}

func writeOwnerOnly(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func ephemeralPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	_, p, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(p)
}

// runSetup is the interactive first-time wizard; it refuses to touch an
// existing vault so updates keep going through put/billing/import-chrome.
func runSetup(ctx context.Context, db, passwordFileFlag string) error {
	w := &wizard{r: bufio.NewReader(os.Stdin), tty: term.IsTerminal(int(os.Stdin.Fd()))}
	fmt.Println("xgift 首次配置向导")
	fmt.Println("将创建加密保管库并写入 X 凭据、支付卡、代理与站点配置。")
	fmt.Println()

	if _, err := os.Stat(db); !os.IsNotExist(err) {
		return fmt.Errorf("保管库已存在或路径不可访问：%s（更新凭据请使用 put / billing / import-chrome）", db)
	}

	defaultPasswordFile := filepath.Join(filepath.Dir(db), "vault-password")
	if passwordFileFlag != "" {
		defaultPasswordFile = passwordFileFlag
	}
	passwordFile, err := w.prompt("密码文件保存路径", defaultPasswordFile)
	if err != nil {
		return err
	}
	if _, err = os.Stat(passwordFile); !os.IsNotExist(err) {
		return fmt.Errorf("密码文件已存在或路径不可访问：%s", passwordFile)
	}

	password := make([]byte, 32)
	if _, err = rand.Read(password); err != nil {
		return err
	}
	encoded := []byte(base64.RawURLEncoding.EncodeToString(password) + "\n")
	clear(password)
	defer clear(encoded)
	if err = writeOwnerOnly(passwordFile, encoded); err != nil {
		return err
	}
	fmt.Printf("密码文件已生成：%s（0600，请备份，丢失后无法恢复保管库）\n", passwordFile)
	if err = writeOwnerOnly(filepath.Join(filepath.Dir(db), "password-path"), []byte(passwordFile+"\n")); err != nil {
		return err
	}
	v, err := vault.Open(db, passwordFile, true)
	if err != nil {
		return err
	}
	defer v.Close()
	fmt.Printf("加密保管库已创建：%s\n\n", db)

	steps := []func() error{
		func() error { return w.setupXCredentials(v) },
		func() error { return w.setupCard(v) },
		func() error { return w.setupProxy(ctx, v) },
		func() error { return w.setupStripeKey(v) },
	}
	for _, step := range steps {
		if err = step(); err != nil {
			fmt.Println()
			fmt.Printf("配置未完成：%v\n", err)
			fmt.Println("已创建的保管库会保留已写入的记录。修复方式：")
			fmt.Printf("  1) 删除 %s、%s 和旁边的 password-path 后重新运行 setup；或\n", db, passwordFile)
			fmt.Println("  2) 用 put --name <proxy|card|cookies|api-auth|stripe-key> / billing / import-chrome 补写缺失记录，")
			fmt.Println("     再以 status 确认全部记录就绪。")
			return err
		}
	}
	siteConfigured, err := w.setupSite(db, passwordFile)
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("配置完成。")
	fmt.Printf("保管库：%s\n密码文件：%s\n", db, passwordFile)
	fmt.Println("已写入记录：cookies、api-auth、card、proxy、stripe-key")
	if siteConfigured {
		fmt.Printf("站点配置：%s\n", filepath.Join(filepath.Dir(db), "site.env"))
	}
	fmt.Println("下一步：xgift status 验证记录；xgift check 验证代理连通；")
	fmt.Println("使用生成的 site.env 启动 xgift-web（详见 README 部署章节）。")
	return nil
}

func (w *wizard) setupXCredentials(v *vault.Vault) error {
	fmt.Println("—— X 凭据 ——")
	fmt.Println("从已登录 x.com 的浏览器获取 auth_token 与 ct0（macOS 之后可用 import-chrome 刷新）。")
	authToken, err := w.secret("auth_token")
	if err != nil {
		return err
	}
	if authToken == "" || strings.ContainsAny(authToken, "\r\n;") {
		return errors.New("auth_token 不能为空且不能包含换行或分号")
	}
	ct0, err := w.secret("ct0")
	if err != nil {
		return err
	}
	if ct0 == "" || strings.ContainsAny(ct0, "\r\n;") {
		return errors.New("ct0 不能为空且不能包含换行或分号")
	}
	cookies, err := json.Marshal(map[string]any{"cookies": []map[string]string{
		{"name": "auth_token", "value": authToken, "domain": ".x.com"},
		{"name": "ct0", "value": ct0, "domain": ".x.com"},
	}})
	if err != nil {
		return err
	}
	defer clear(cookies)
	if err = v.Put("cookies", cookies); err != nil {
		return err
	}

	authorization, err := w.prompt("Authorization 请求头（留空使用 X 网页版默认 Bearer）", "")
	if err != nil {
		return err
	}
	if authorization == "" {
		authorization = defaultBearer
	}
	if !strings.HasPrefix(authorization, "Bearer ") {
		return errors.New("Authorization 必须以 \"Bearer \" 开头")
	}
	userAgent, err := w.prompt("User-Agent（留空使用当前 macOS 版 Chrome）", "")
	if err != nil {
		return err
	}
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	auth, err := json.Marshal(map[string]string{"Authorization": authorization, "UserAgent": userAgent})
	if err != nil {
		return err
	}
	defer clear(auth)
	if err = v.Put("api-auth", auth); err != nil {
		return err
	}
	fmt.Printf("已保存 X 凭据（auth_token 尾号 %s）\n\n", tail(authToken))
	return nil
}

func (w *wizard) setupCard(v *vault.Vault) error {
	fmt.Println("—— 支付卡 ——")
	fmt.Println("只填写发卡行登记的真实信息。")
	card := map[string]string{}

	number, err := w.secret("卡号（仅数字，可含空格或连字符）")
	if err != nil {
		return err
	}
	number = strings.NewReplacer(" ", "", "-", "").Replace(number)
	if !regexp.MustCompile(`^[0-9]{12,19}$`).MatchString(number) || !luhnValid(number) {
		return errors.New("卡号无效（12-19 位数字且须通过校验）")
	}
	card["number"] = number

	month, err := w.prompt("有效期月份（01-12）", "")
	if err != nil {
		return err
	}
	if regexp.MustCompile(`^[1-9]$`).MatchString(month) {
		month = "0" + month
	}
	if !regexp.MustCompile(`^(0[1-9]|1[0-2])$`).MatchString(month) {
		return errors.New("有效期月份必须是 01-12")
	}
	year, err := w.prompt("有效期年份（4 位）", "")
	if err != nil {
		return err
	}
	if !regexp.MustCompile(`^[0-9]{4}$`).MatchString(year) {
		return errors.New("有效期年份必须是 4 位数字")
	}
	y, _ := strconv.Atoi(year)
	m, _ := strconv.Atoi(month)
	if !time.Now().Before(time.Date(y, time.Month(m)+1, 1, 0, 0, 0, 0, time.UTC)) {
		return errors.New("卡片已过期")
	}
	card["exp_month"] = month
	card["exp_year"] = year

	cvc, err := w.secret("CVC（3-4 位）")
	if err != nil {
		return err
	}
	if !regexp.MustCompile(`^[0-9]{3,4}$`).MatchString(cvc) {
		return errors.New("CVC 必须是 3-4 位数字")
	}
	card["cvc"] = cvc

	name, err := w.prompt("持卡人姓名", "")
	if err != nil {
		return err
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("持卡人姓名不能为空")
	}
	card["billing_name"] = name

	email, err := w.prompt("账单邮箱", "")
	if err != nil {
		return err
	}
	at := strings.Index(email, "@")
	if at < 1 || !strings.Contains(email[at+1:], ".") {
		return errors.New("邮箱格式无效")
	}
	card["email"] = email

	country, err := w.prompt("账单国家（两位代码，如 BD）", "")
	if err != nil {
		return err
	}
	country = strings.ToUpper(country)
	if !regexp.MustCompile(`^[A-Z]{2}$`).MatchString(country) {
		return errors.New("账单国家必须是两位字母代码")
	}
	card["billing_country"] = country

	for _, opt := range []struct{ key, label string }{
		{"billing_postal_code", "账单邮编（可选，留空跳过）"},
		{"billing_address_line1", "账单地址行 1（可选，留空跳过）"},
		{"billing_address_line2", "账单地址行 2（可选，留空跳过）"},
		{"billing_city", "账单城市（可选，留空跳过）"},
		{"billing_state", "账单州/省（可选，留空跳过）"},
	} {
		s, e := w.prompt(opt.label, "")
		if e != nil {
			return e
		}
		if s != "" {
			card[opt.key] = s
		}
	}

	raw, err := json.Marshal(card)
	if err != nil {
		return err
	}
	defer clear(raw)
	if err = v.Put("card", raw); err != nil {
		return err
	}
	fmt.Printf("已保存支付卡（尾号 %s）\n\n", tail(number))
	return nil
}

func (w *wizard) setupProxy(ctx context.Context, v *vault.Vault) error {
	fmt.Println("—— 代理配置 ——")
	for {
		fmt.Println("  1) 直连（direct，不使用代理）")
		fmt.Println("  2) AnyTLS 节点（引导填写）")
		fmt.Println("  3) 粘贴 sing-box outbound JSON")
		choice, err := w.prompt("请选择", "1")
		if err != nil {
			return err
		}
		var config []byte
		switch choice {
		case "1":
			config = []byte(`{"outbounds":[{"type":"direct","tag":"direct"}]}`)
		case "2":
			config, err = w.anyTLSConfig()
		case "3":
			config, err = w.pastedProxyConfig()
		default:
			fmt.Println("请输入 1、2 或 3。")
			continue
		}
		if err != nil {
			return err
		}
		defer clear(config)

		port, err := ephemeralPort()
		if err != nil {
			return err
		}
		instance, err := proxy.Start(ctx, config, port)
		if err != nil {
			fmt.Printf("代理配置无效：%v，请重新选择。\n", err)
			continue
		}
		fmt.Println("代理配置有效。")
		test, err := w.yesNo("是否进行连通性测试（通过代理访问 https://x.com）？", false)
		if err != nil {
			instance.Close()
			return err
		}
		if test {
			if err = proxy.Check(ctx, port); err != nil {
				fmt.Printf("连通性测试失败：%v（将保留该配置；若在其他网络环境配置可忽略）\n", err)
			}
		}
		if err = instance.Close(); err != nil {
			return err
		}
		if err = v.Put("proxy", config); err != nil {
			return err
		}
		fmt.Println("已保存代理配置。")
		fmt.Println()
		return nil
	}
}

func (w *wizard) anyTLSConfig() ([]byte, error) {
	host, err := w.prompt("服务器地址", "")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(host) == "" {
		return nil, errors.New("服务器地址不能为空")
	}
	portStr, err := w.prompt("服务器端口（1-65535）", "443")
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("端口必须是 1-65535")
	}
	password, err := w.secret("AnyTLS 密码")
	if err != nil {
		return nil, err
	}
	if password == "" {
		return nil, errors.New("密码不能为空")
	}
	sni, err := w.prompt("TLS SNI", host)
	if err != nil {
		return nil, err
	}
	insecure, err := w.yesNo("跳过 TLS 证书校验？", false)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"outbounds": []map[string]any{{
		"type":        "anytls",
		"tag":         "proxy",
		"server":      host,
		"server_port": port,
		"password":    password,
		"tls":         map[string]any{"enabled": true, "server_name": sni, "insecure": insecure},
	}}})
}

// pastedProxyConfig reads pasted JSON until a blank line, accepting either a
// single outbound object or a full config with an outbounds array.
func (w *wizard) pastedProxyConfig() ([]byte, error) {
	for {
		fmt.Println("请粘贴 JSON（单个 outbound 对象或完整配置；空行结束）：")
		var b strings.Builder
		for {
			s, err := w.line()
			if err != nil {
				return nil, err
			}
			if s == "" {
				break
			}
			b.WriteString(s)
			b.WriteByte('\n')
		}
		raw := []byte(strings.TrimSpace(b.String()))
		var obj map[string]any
		if len(raw) == 0 || json.Unmarshal(raw, &obj) != nil {
			fmt.Println("JSON 无效，请重新粘贴。")
			continue
		}
		if outbounds, ok := obj["outbounds"].([]any); ok && len(outbounds) > 0 {
			return raw, nil
		}
		if obj["type"] != nil && obj["tag"] != nil {
			wrapped, err := json.Marshal(map[string]any{"outbounds": []any{obj}})
			if err != nil {
				return nil, err
			}
			return wrapped, nil
		}
		fmt.Println("需为含 outbounds 数组的完整配置，或含 type 与 tag 的单个 outbound 对象，请重新粘贴。")
	}
}

func (w *wizard) setupStripeKey(v *vault.Vault) error {
	fmt.Println("—— Stripe 公钥 ——")
	fmt.Println("这是 X 结账页面使用的 Stripe 公钥（商户侧，pk_live_ 开头），不是用户的 secret key。")
	key, err := w.secret("Stripe publishable key（pk_live_...）")
	if err != nil {
		return err
	}
	if !stripeKeyPattern.MatchString(key) {
		return errors.New("Stripe 公钥格式无效（须匹配 pk_live_ 加字母数字）")
	}
	if err = v.Put("stripe-key", []byte(key)); err != nil {
		return err
	}
	fmt.Printf("已保存 Stripe 公钥（尾号 %s）\n\n", tail(key))
	return nil
}

func (w *wizard) setupSite(db, passwordFile string) (bool, error) {
	fmt.Println("—— 站点配置（可选）——")
	generate, err := w.yesNo("是否生成站点配置文件？", false)
	if err != nil {
		return false, err
	}
	if !generate {
		return false, nil
	}
	origin, err := w.prompt("站点 Origin（https://<主机名>）", "")
	if err != nil {
		return false, err
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return false, errors.New("Origin 必须是 https://<主机名> 形式（不带路径、查询或用户信息）")
	}
	listen, err := w.prompt("监听地址", "127.0.0.1:8787")
	if err != nil {
		return false, err
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return false, errors.New("监听地址必须使用回环 IP（如 127.0.0.1:8787）")
	}
	payments, err := w.yesNo("是否开启支付？", false)
	if err != nil {
		return false, err
	}

	admin := make([]byte, 24)
	if _, err = rand.Read(admin); err != nil {
		return false, err
	}
	adminPassword := base64.RawURLEncoding.EncodeToString(admin)
	clear(admin)
	dir := filepath.Dir(db)
	adminPath := filepath.Join(dir, "admin-password")
	if err = writeOwnerOnly(adminPath, []byte(adminPassword+"\n")); err != nil {
		return false, err
	}
	enabled := "false"
	if payments {
		enabled = "true"
	}
	siteEnv := fmt.Sprintf("XGIFT_ORIGIN=%s\nXGIFT_LISTEN=%s\nXGIFT_DATA_DIR=%s\nXGIFT_PASSWORD_FILE=%s\nXGIFT_ADMIN_PASSWORD_FILE=%s\nXGIFT_PAYMENTS_ENABLED=%s\n",
		origin, listen, dir, passwordFile, adminPath, enabled)
	envPath := filepath.Join(dir, "site.env")
	if err = writeOwnerOnly(envPath, []byte(siteEnv)); err != nil {
		return false, err
	}
	fmt.Printf("站点配置已生成：%s（0600）\n", envPath)
	fmt.Printf("后台管理员密码（仅此一次显示，亦保存于 %s）：%s\n", adminPath, adminPassword)
	fmt.Println("生产部署请参照 deploy/ 模板，将 site.env 与密钥移至 /etc/xgift/ 并保持仅属主可读。")
	fmt.Println()
	return true, nil
}

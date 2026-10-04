package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"xgift/internal/checkout"
	"xgift/internal/chrome"
	"xgift/internal/proxy"
	"xgift/internal/vault"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "xgift:", err)
		os.Exit(1)
	}
}

func run() error {
	// Permit the requested `xgift username --pay` spelling as well as global flags.
	args := os.Args[1:]
	command := ""
	sub := ""
	rest := []string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			rest = append(rest, a)
			if a == "--db" || a == "--password-file" || a == "--profile" || a == "--port" || a == "--name" || a == "--months" || a == "--last4" {
				i++
				if i >= len(args) {
					return errors.New("missing flag value")
				}
				rest = append(rest, args[i])
			}
		} else if command == "" {
			command = a
		} else if command == "cards" && sub == "" {
			sub = a
		} else {
			return errors.New("unexpected argument")
		}
	}
	f := flag.NewFlagSet("xgift", flag.ContinueOnError)
	defaultDB := "sqlite/vault.db"
	if exe, e := os.Executable(); e == nil {
		if resolved, e := filepath.EvalSymlinks(exe); e == nil {
			candidate := filepath.Join(filepath.Dir(resolved), "..", "sqlite", "vault.db")
			if _, e = os.Stat(candidate); e == nil {
				defaultDB = candidate
			}
		}
	}
	db := f.String("db", defaultDB, "encrypted SQLite record store")
	key := f.String("password-file", os.Getenv("XGIFT_PASSWORD_FILE"), "owner-only password file")
	profile := f.String("profile", "Default", "Chrome directory name")
	port := f.Int("port", 0, "local proxy port; default automatic (proxy command: 18791)")
	months := f.Int("months", 6, "gift duration in months; must match a plan in the catalog record")
	last4 := f.String("last4", "", "card tail for cards remove")
	retire := f.Bool("retire-canceled", false, "archive an inactive, canceled, unpaid order after read-only verification")
	inspect := f.Bool("inspect", false, "read the existing Stripe order status without paying")
	pay := f.Bool("pay", false, "pay only at the exact catalog plan total")
	name := f.String("name", "", "secret name for put")
	f.Usage = func() {
		fmt.Fprintln(f.Output(), "Usage: xgift <setup|init|status|billing|cards|import-chrome|put|proxy|check|check-payment-outbounds|resume-payments|username> [flags]\nsetup is the interactive first-time wizard; init reads a JSON object from stdin; put reads one JSON value from stdin (stripe-key: the raw pk_live_ key; catalog: merchant/plan catalog JSON; payment-outbounds: an outbound array; cards: a card array). cards list|add|remove|unblock|rotate manages the encrypted payment card pool. check-payment-outbounds probes public endpoints without paying.")
		f.PrintDefaults()
	}
	if err := f.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if command == "" {
		f.Usage()
		return nil
	}
	if *port != 0 && (*port < 1024 || *port > 65535) {
		return errors.New("port must be 1024..65535")
	}
	if command == "setup" {
		return runSetup(context.Background(), *db, *key)
	}
	if command == "init" {
		input, err := io.ReadAll(io.LimitReader(os.Stdin, 1024*1024))
		if err != nil {
			return err
		}
		defer clear(input)
		var records map[string]json.RawMessage
		if err = json.Unmarshal(input, &records); err != nil {
			return errors.New("stdin must contain a JSON object of secrets")
		}
		if len(records) == 0 {
			return errors.New("no secrets supplied")
		}
		if _, err = os.Stat(*db); !os.IsNotExist(err) {
			return errors.New("vault already exists or path is inaccessible")
		}
		if *key == "" {
			*key, err = vault.NewPassword()
			if err != nil {
				return err
			}
		}
		v, err := vault.Open(*db, *key, true)
		if err != nil {
			return err
		}
		defer v.Close()
		for n, b := range records {
			if n == "vault-check" {
				return errors.New("reserved secret name")
			}
			if err = v.Put(n, b); err != nil {
				return err
			}
		}
		// Save only the password's path, never the password, next to the vault.
		if err = os.WriteFile(filepath.Join(filepath.Dir(*db), "password-path"), []byte(*key+"\n"), 0600); err != nil {
			return err
		}
		fmt.Printf("Encrypted vault created: %s\nPassword file: %s\n", *db, *key)
		return nil
	}
	if *key == "" {
		p, err := os.ReadFile(filepath.Join(filepath.Dir(*db), "password-path"))
		if err != nil {
			return errors.New("set --password-file or XGIFT_PASSWORD_FILE")
		}
		*key = strings.TrimSpace(string(p))
	}
	v, err := vault.Open(*db, *key, false)
	if err != nil {
		return err
	}
	defer v.Close()
	switch command {
	case "cards":
		return runCards(v, sub, *last4)
	case "check-payment-outbounds":
		failed := false
		enc := json.NewEncoder(os.Stdout)
		if err := checkout.ProbePaymentOutbounds(context.Background(), v, func(result checkout.PaymentNodeProbe) {
			enc.Encode(result)
			if !result.Healthy {
				failed = true
			}
		}); err != nil {
			return err
		}
		if failed {
			return errors.New("one or more payment outbounds could not reach Stripe; no payment submitted")
		}
		return nil
	case "resume-payments":
		lock, e := os.OpenFile(filepath.Join(filepath.Dir(*db), "checkout.lock"), os.O_CREATE|os.O_RDWR, 0600)
		if e != nil {
			return e
		}
		defer lock.Close()
		if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
			return errors.New("another checkout is running")
		}
		defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		if e = checkout.ResetManualPaymentPause(v); e != nil {
			return e
		}
		fmt.Println("Automatic payment pause cleared; payment spacing remains enforced.")
		return nil
	case "billing":
		var fields map[string]string
		if e := json.NewDecoder(io.LimitReader(os.Stdin, 65536)).Decode(&fields); e != nil {
			return errors.New("billing expects a JSON object on stdin")
		}
		n, e := checkout.UpdateCardBilling(v, fields)
		if e != nil {
			return e
		}
		fmt.Printf("Billing information saved in encrypted SQLite for %d card(s)\n", n)
		return nil
	case "status":
		for _, n := range []string{"cookies", "proxy"} {
			b, e := v.Get(n)
			if e != nil {
				return fmt.Errorf("record %s is missing; run setup or put --name %s", n, n)
			}
			if !json.Valid(b) {
				return fmt.Errorf("invalid %s JSON", n)
			}
			clear(b)
			fmt.Printf("%s: encrypted record verified\n", n)
		}
		status, e := checkout.CardsStatus(v)
		if e != nil {
			return e
		}
		usable := 0
		for _, card := range status {
			if card.Usable {
				usable++
			}
		}
		if usable == 0 {
			return checkout.ErrNoUsableCard
		}
		fmt.Printf("cards: %d encrypted record(s), %d usable\n", len(status), usable)
		raw, e := v.Get("api-auth")
		if e != nil {
			return errors.New("record api-auth is missing; fix with put --name api-auth")
		}
		var auth struct{ Authorization string }
		if e = json.Unmarshal(raw, &auth); e != nil || !strings.HasPrefix(auth.Authorization, "Bearer ") {
			clear(raw)
			return errors.New("invalid api-auth record; rewrite with put --name api-auth")
		}
		clear(raw)
		fmt.Println("api-auth: encrypted record verified")
		key, e := v.Get("stripe-key")
		if e != nil {
			return errors.New("record stripe-key is missing; fix with put --name stripe-key")
		}
		if !stripeKeyPattern.Match(key) {
			clear(key)
			return errors.New("invalid stripe-key record; rewrite with put --name stripe-key")
		}
		clear(key)
		fmt.Println("stripe-key: encrypted record verified")
		if _, e = checkout.ReadCatalog(v); e != nil {
			return e
		}
		fmt.Println("catalog: encrypted record verified")
		return nil
	case "import-chrome":
		b, e := chrome.Extract(*profile)
		if e != nil {
			return e
		}
		defer clear(b)
		if e = v.Put("cookies", b); e != nil {
			return e
		}
		fmt.Println("X cookies refreshed in encrypted SQLite vault")
		return nil
	case "put":
		switch *name {
		case "payment-outbounds":
			b, e := io.ReadAll(io.LimitReader(os.Stdin, (1<<20)+1))
			if e != nil {
				return e
			}
			defer clear(b)
			nodes, e := proxy.ParseOutboundPool(b)
			if e != nil {
				return e
			}
			// Updating the pool never edits existing per-order node bindings.
			if e = v.Put(*name, b); e != nil {
				return e
			}
			fmt.Printf("Payment outbound pool saved: %d nodes; existing order bindings retained\n", len(nodes))
			return nil
		case "proxy", "card", "cookies", "api-auth":
			b, e := io.ReadAll(io.LimitReader(os.Stdin, 1024*1024))
			if e != nil {
				return e
			}
			defer clear(b)
			if !json.Valid(b) {
				return errors.New("stdin must be valid JSON")
			}
			return v.Put(*name, b)
		case "cards":
			b, e := io.ReadAll(io.LimitReader(os.Stdin, 1024*1024))
			if e != nil {
				return e
			}
			defer clear(b)
			n, e := checkout.SetCardRecords(v, b)
			if e != nil {
				return e
			}
			fmt.Printf("Card set replaced: %d validated cards\n", n)
			return nil
		case "stripe-key":
			b, e := io.ReadAll(io.LimitReader(os.Stdin, 4096))
			if e != nil {
				return e
			}
			defer clear(b)
			b = bytes.TrimSpace(b)
			if !stripeKeyPattern.Match(b) {
				return errors.New("stdin must be the pk_live_ publishable key")
			}
			return v.Put(*name, b)
		case "catalog":
			b, e := io.ReadAll(io.LimitReader(os.Stdin, 65536))
			if e != nil {
				return e
			}
			defer clear(b)
			if _, e = checkout.ParseCatalog(b); e != nil {
				return e
			}
			return v.Put(*name, b)
		default:
			return errors.New("--name must be proxy, payment-outbounds, card, cards, cookies, api-auth, stripe-key or catalog")
		}
	}
	if command != "proxy" && command != "check" && !regexp.MustCompile(`^@?[A-Za-z0-9_]{1,15}$`).MatchString(command) {
		return errors.New("invalid command or X username")
	}
	if command != "proxy" && command != "check" {
		lock, e := os.OpenFile(filepath.Join(filepath.Dir(*db), "checkout.lock"), os.O_CREATE|os.O_RDWR, 0600)
		if e != nil {
			return e
		}
		defer lock.Close()
		if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
			return errors.New("another checkout command is running")
		}
		defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	}
	if *port == 0 {
		if command == "proxy" {
			*port = 18791
		} else {
			l, e := net.Listen("tcp", "127.0.0.1:0")
			if e != nil {
				return e
			}
			_, p, _ := net.SplitHostPort(l.Addr().String())
			*port, _ = strconv.Atoi(p)
			l.Close()
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	config, err := v.Get("proxy")
	if err != nil {
		return err
	}
	defer clear(config)
	instance, err := proxy.Start(ctx, config, *port)
	if err != nil {
		return err
	}
	defer instance.Close()
	fmt.Fprintf(os.Stderr, "Embedded sing-box proxy listening on 127.0.0.1:%d\n", *port)
	if command == "proxy" {
		<-ctx.Done()
		return nil
	}
	if command == "check" {
		return proxy.Check(ctx, *port)
	}
	if *inspect {
		return checkout.Inspect(ctx, v, command, *port, *months)
	}
	if *retire {
		if *pay {
			return errors.New("--retire-canceled cannot be combined with --pay")
		}
		return checkout.RetireCanceled(ctx, v, command, *port, *months)
	}
	result, e := checkout.Run(ctx, v, command, *pay, *port, *months)
	if result != nil {
		if e == nil || result.Status == "requires_action" || result.Status == "unknown" || result.Status == "submitting" {
			fmt.Println(result.URL)
		}
		fmt.Fprintln(os.Stderr, "Checkout status:", result.Status)
	}
	return e
}

func runCards(v *vault.Vault, sub, last4 string) error {
	switch sub {
	case "", "list":
		status, err := checkout.CardsStatus(v)
		if err != nil {
			return err
		}
		rotation, err := checkout.PaymentRotationStatus(v)
		if err != nil {
			return err
		}
		fmt.Printf("卡池：%d 张（每 %d 个连续订单使用同一卡+节点组合；被拒后立即轮换）\n", rotation.Cards, rotation.BatchSize)
		for i, s := range status {
			line := fmt.Sprintf("  %d) 尾号 %s", i+1, s.Last4)
			if s.Usable {
				line += " 可用"
			} else {
				line += " 不可用：" + s.Problem
			}
			if s.Blocked != "" {
				if s.CoolingSeconds > 0 {
					line += fmt.Sprintf("（整卡冷却：%s，剩 %d 分钟）", s.Blocked, (s.CoolingSeconds+59)/60)
				} else {
					line += "（已封锁：" + s.Blocked + "）"
				}
			}
			if s.PairCooling > 0 {
				line += fmt.Sprintf("（%d 组卡+节点组合冷却中）", s.PairCooling)
			}
			fmt.Println(line)
		}
		if rotation.CardLast4 == "" {
			fmt.Println("当前轮换组合：未开始（下一笔付款时随机选择）")
		} else {
			node := rotation.Node
			if node == "" {
				node = "direct"
			}
			fmt.Printf("当前轮换组合：尾号 %s × %s（已用 %d/%d）\n", rotation.CardLast4, node, rotation.Used, rotation.BatchSize)
		}
		return nil
	case "add":
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		if err != nil {
			return err
		}
		defer clear(b)
		total, err := checkout.AddCardRecords(v, b)
		if err != nil {
			return err
		}
		fmt.Printf("卡池已保存：共 %d 张。缺少的账单字段已从现有卡继承。\n", total)
		return nil
	case "remove":
		remaining, err := checkout.RemoveCardRecord(v, last4)
		if err != nil {
			return err
		}
		fmt.Printf("已移除尾号 %s；卡池剩余 %d 张\n", last4, remaining)
		return nil
	case "unblock":
		cleared, err := checkout.UnblockPaymentCards(v)
		if err != nil {
			return err
		}
		fmt.Printf("已解除 %d 张卡的封锁；若仍有可用卡，付款保护暂停已同步解除\n", cleared)
		return nil
	case "rotate":
		if err := checkout.ResetPaymentRotation(v); err != nil {
			return err
		}
		fmt.Println("已结束当前轮换批次；下一笔付款将随机选择新的卡+节点组合")
		return nil
	default:
		return errors.New("cards expects list, add, remove --last4 XXXX, unblock or rotate")
	}
}

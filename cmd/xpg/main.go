// Command xpg is the standalone X Premium gift link generator service.
// It reuses the xgift checkout engine but never tokenizes a payment card.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"xgift/internal/chrome"
	"xgift/internal/proxy"
	"xgift/internal/vault"
	"xgift/internal/xpg"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "xpg:", err)
		os.Exit(1)
	}
}

func run() error {
	args := os.Args[1:]
	command := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	f := flag.NewFlagSet("xpg", flag.ContinueOnError)
	db := f.String("db", "", "vault database path (non-serve commands)")
	key := f.String("password-file", os.Getenv("XPG_PASSWORD_FILE"), "owner-only password file")
	profile := f.String("profile", "", "Chrome directory name, for example Default or \"Profile 1\"")
	cookiesFile := f.String("cookies-file", "", "cookies JSON file (alternative to --profile)")
	name := f.String("name", "", "vault record name for export")
	anytls := f.String("anytls", "", "anytls:// share link used as the show-account/outbound proxy")
	proxyConfig := f.String("proxy-config", "", "sing-box JSON config file used as the show-account proxy")
	f.Usage = func() {
		fmt.Fprintln(f.Output(), "Usage: xpg <serve|show-account|outbound|export> [flags]\n"+
			"serve (default) runs the standalone link generator configured by XPG_* environment variables.\n"+
			"show-account identifies which X account the given Chrome profile or cookie file belongs to.\n"+
			"outbound <anytls-link> prints a sing-box outbound JSON document and probes it.\n"+
			"export --name <record> prints one encrypted vault record to stdout.")
		f.PrintDefaults()
	}
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	switch command {
	case "serve":
		if len(f.Args()) > 0 {
			f.Usage()
			return errors.New("serve takes no positional arguments")
		}
		return xpg.Run(ctx)

	case "outbound":
		link := *anytls
		if rest := f.Args(); len(rest) == 1 {
			link = rest[0]
		}
		if link == "" {
			return errors.New("outbound needs one anytls:// link")
		}
		config, err := xpg.ParseAnyTLS(link)
		if err != nil {
			return err
		}
		fmt.Println(string(config))
		return probeOutbound(ctx, config)

	case "show-account":
		cookies, err := loadCookies(*profile, *cookiesFile)
		if err != nil {
			return err
		}
		defer clear(cookies)
		client := &http.Client{Timeout: 30 * time.Second}
		if *proxyConfig != "" || *anytls != "" {
			var config []byte
			if *proxyConfig != "" {
				if config, err = os.ReadFile(*proxyConfig); err != nil {
					return err
				}
			} else if config, err = xpg.ParseAnyTLS(*anytls); err != nil {
				return err
			}
			closer, proxied, err := proxyClient(ctx, config)
			if err != nil {
				return err
			}
			defer closer()
			client = proxied
		}
		screen, err := xpg.WhoAmI(ctx, cookies, client)
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "show-account: cookies verified against the X API")
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"screen_name": screen})

	case "export":
		if *name == "" {
			return errors.New("export needs --name <record>")
		}
		if *db == "" || *key == "" {
			return errors.New("export needs --db and --password-file")
		}
		v, err := vault.Open(*db, *key, false)
		if err != nil {
			return err
		}
		defer v.Close()
		raw, err := v.Get(*name)
		if err != nil {
			return fmt.Errorf("record %s is missing", *name)
		}
		if _, err = os.Stdout.Write(bytesTrim(raw)); err != nil {
			return err
		}
		fmt.Println()
		return nil

	default:
		f.Usage()
		return fmt.Errorf("unknown command %q", command)
	}
}

func bytesTrim(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

func loadCookies(profile, cookiesFile string) ([]byte, error) {
	if profile != "" && cookiesFile != "" {
		return nil, errors.New("choose either --profile or --cookies-file")
	}
	if cookiesFile != "" {
		return os.ReadFile(cookiesFile)
	}
	if profile == "" {
		return nil, errors.New("show-account needs --profile or --cookies-file")
	}
	return chrome.Extract(profile)
}

// proxyClient starts an embedded sing-box instance and returns an HTTP client
// routed through its local mixed listener.
func proxyClient(ctx context.Context, config []byte) (func(), *http.Client, error) {
	port, err := freeLocalPort()
	if err != nil {
		return nil, nil, err
	}
	instance, err := proxy.Start(ctx, config, port)
	if err != nil {
		return nil, nil, err
	}
	p, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	return func() { instance.Close() }, &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(p), TLSHandshakeTimeout: 15 * time.Second},
	}, nil
}

// probeOutbound starts the printed outbound and checks X reachability plus the
// exit country, so a broken node is caught before it reaches the vault.
func probeOutbound(ctx context.Context, config []byte) error {
	port, err := freeLocalPort()
	if err != nil {
		return err
	}
	instance, err := proxy.Start(ctx, config, port)
	if err != nil {
		return fmt.Errorf("outbound is not usable: %w", err)
	}
	defer instance.Close()
	if err = proxy.Check(ctx, port); err != nil {
		return err
	}
	info, err := exitInfo(ctx, port)
	if err == nil {
		fmt.Printf("Exit: %s (%s, %s)\n", info.IP, info.Country, info.City)
	}
	return nil
}

func freeLocalPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	l.Close()
	return strconv.Atoi(port)
}

func exitInfo(ctx context.Context, port int) (exitInfoResult, error) {
	p, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(p)}}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://ipinfo.io/json", nil)
	if err != nil {
		return exitInfoResult{}, err
	}
	res, err := client.Do(req)
	if err != nil {
		return exitInfoResult{}, err
	}
	defer res.Body.Close()
	var out exitInfoResult
	err = json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&out)
	return out, err
}

type exitInfoResult struct {
	IP      string `json:"ip"`
	Country string `json:"country"`
	City    string `json:"city"`
}

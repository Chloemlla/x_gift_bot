package xpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// The same public web bearer and client metadata the X web app uses; they are
// not account secrets.
const defaultBearer = "Bearer AAAAAAAAAAAAAAAAAAAAANRILgAAAAAAnNwIzUejRCOuH5E6I8xnZz4puTs%3D1Zv7ttfk8LF81IUq16cHjhLTvJu4FA33AGWWjCpTnA"
const defaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"

// ParseCookieFile reads the vault "cookies" record shape:
// {"cookies":[{"name":"auth_token","value":"...","domain":".x.com"}, ...]}.
func ParseCookieFile(raw []byte) (http.Header, error) {
	var state struct {
		Cookies []struct {
			Name   string `json:"name"`
			Value  string `json:"value"`
			Domain string `json:"domain"`
		} `json:"cookies"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, errors.New("invalid cookie file")
	}
	h := http.Header{}
	found := map[string]bool{}
	for _, c := range state.Cookies {
		if c.Domain != ".x.com" && c.Domain != "x.com" {
			return nil, errors.New("unexpected cookie domain")
		}
		if c.Name != "auth_token" && c.Name != "ct0" {
			return nil, errors.New("unexpected cookie name")
		}
		if c.Value == "" || strings.ContainsAny(c.Value, "\r\n;") {
			return nil, errors.New("invalid X cookie value")
		}
		if found[c.Name] {
			return nil, errors.New("duplicate X cookie")
		}
		found[c.Name] = true
		h.Add("Cookie", c.Name+"="+c.Value)
		if c.Name == "ct0" {
			h.Set("X-Csrf-Token", c.Value)
		}
	}
	if !found["auth_token"] || !found["ct0"] {
		return nil, errors.New("required X cookies missing (auth_token, ct0)")
	}
	return h, nil
}

// WhoAmI identifies which X account a cookie pair belongs to. No request body
// or cookie value is ever printed; only the verified screen name is returned.
func WhoAmI(ctx context.Context, cookies []byte, client *http.Client) (string, error) {
	headers, err := ParseCookieFile(cookies)
	if err != nil {
		return "", err
	}
	for _, target := range []string{
		"https://x.com/i/api/1.1/account/verify_credentials.json?skip_status=true&include_entities=false",
		"https://api.x.com/1.1/account/verify_credentials.json?skip_status=true&include_entities=false",
	} {
		req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
		if err != nil {
			return "", err
		}
		req.Header = headers.Clone()
		req.Header.Set("Authorization", defaultBearer)
		req.Header.Set("User-Agent", defaultUserAgent)
		res, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("X request failed: %w", err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if readErr != nil {
			continue
		}
		if res.StatusCode != 200 {
			if res.StatusCode == 401 || res.StatusCode == 403 {
				return "", errors.New("X rejected these cookies (HTTP " + fmt.Sprint(res.StatusCode) + ")")
			}
			continue
		}
		var profile struct {
			ScreenName string                     `json:"screen_name"`
			Errors     []struct{ Message string } `json:"errors"`
		}
		if err := json.Unmarshal(raw, &profile); err != nil || profile.ScreenName == "" {
			continue
		}
		return profile.ScreenName, nil
	}
	return "", errors.New("could not determine the X account for these cookies")
}

// ParseAnyTLS converts an anytls:// share link into a sing-box outbound JSON
// document with a single proxy outbound. peer= becomes the TLS SNI; udp=1 is
// informational (the anytls protocol negotiates UDP itself).
func ParseAnyTLS(link string) ([]byte, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || u.Scheme != "anytls" {
		return nil, errors.New("link must be an anytls:// share link")
	}
	host := u.Hostname()
	portStr := u.Port()
	if host == "" || portStr == "" {
		return nil, errors.New("anytls link must include host and port")
	}
	password := ""
	if u.User != nil {
		password = u.User.Username()
	}
	if password == "" {
		return nil, errors.New("anytls link must include the password")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("anytls link must include a valid port")
	}
	sni := host
	if peer := u.Query().Get("peer"); peer != "" {
		sni = peer
	}
	insecure := false
	switch u.Query().Get("insecure") {
	case "1", "true":
		insecure = true
	}
	outbound := map[string]any{
		"type":        "anytls",
		"tag":         "proxy",
		"server":      host,
		"server_port": port,
		"password":    password,
		"tls":         map[string]any{"enabled": true, "server_name": sni, "insecure": insecure},
	}
	return json.Marshal(map[string]any{"outbounds": []any{outbound}})
}

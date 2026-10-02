package checkout

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"xgift/internal/vault"
)

type Plan struct {
	Months, Minor int
	ProductID     string
}

var ErrNotEligible = errors.New("recipient cannot receive Premium gifts")
var ErrUserNotFound = errors.New("recipient was not found")

// Eligibility is read-only: it neither creates a checkout nor submits a payment.
func Eligibility(ctx context.Context, v *vault.Vault, user string, port int) (string, error) {
	c, err := newXClient(v, port)
	if err != nil {
		return "", err
	}
	defer c.close()
	return c.recipient(ctx, user)
}

func planFor(months int) (Plan, error) {
	switch months {
	case 3:
		return Plan{3, 30000, "prod_EXAMPLE3MO"}, nil
	case 6:
		return Plan{6, 60000, "prod_EXAMPLE6MO"}, nil
	}
	return Plan{}, errors.New("only 3 months / 300 BDT or 6 months / 600 BDT are allowed")
}
func (p Plan) Name() string { return fmt.Sprintf("Premium Gift - %d months", p.Months) }

type xClient struct {
	vault   *vault.Vault
	http    *http.Client
	headers http.Header
}

func newXClient(v *vault.Vault, port int) (*xClient, error) {
	raw, e := v.Get("api-auth")
	if e != nil {
		return nil, errors.New("X API authentication metadata is missing or unreadable")
	}
	defer clear(raw)
	var auth struct{ Authorization, UserAgent string }
	if json.Unmarshal(raw, &auth) != nil || !strings.HasPrefix(auth.Authorization, "Bearer ") {
		return nil, errors.New("invalid X API authentication metadata")
	}
	raw, e = v.Get("cookies")
	if e != nil {
		return nil, e
	}
	defer clear(raw)
	var state struct {
		Cookies []struct{ Name, Value, Domain string }
	}
	if e = json.Unmarshal(raw, &state); e != nil {
		return nil, e
	}
	h := http.Header{"Authorization": {auth.Authorization}, "User-Agent": {auth.UserAgent}, "Content-Type": {"application/json"}, "Origin": {"https://x.com"}, "X-Twitter-Auth-Type": {"OAuth2Session"}, "X-Twitter-Active-User": {"yes"}, "X-Twitter-Client-Language": {"en"}}
	found := map[string]bool{}
	for _, c := range state.Cookies {
		if c.Domain != ".x.com" && c.Domain != "x.com" {
			return nil, errors.New("unexpected cookie domain")
		}
		if c.Name != "auth_token" && c.Name != "ct0" {
			return nil, errors.New("unexpected cookie name")
		}
		if found[c.Name] || c.Value == "" || strings.ContainsAny(c.Value, "\r\n;") {
			return nil, errors.New("invalid X cookie")
		}
		found[c.Name] = true
		h.Add("Cookie", c.Name+"="+c.Value)
		if c.Name == "ct0" {
			h.Set("X-Csrf-Token", c.Value)
		}
	}
	if !found["ct0"] || !found["auth_token"] {
		return nil, errors.New("required X cookies missing")
	}
	p, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(p), TLSHandshakeTimeout: 15 * time.Second}, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("unexpected X API redirect") }}
	return &xClient{http: client, headers: h, vault: v}, nil
}
func (c *xClient) close() { c.http.CloseIdleConnections() }
func (c *xClient) call(ctx context.Context, user, name, id string, variables any, mutation bool, out any) error {
	if mutation {
		return c.callOnce(ctx, user, name, id, variables, true, out)
	}
	return retrySafe(ctx, func() error { return c.callOnce(ctx, user, name, id, variables, false, out) })
}
func (c *xClient) callOnce(ctx context.Context, user, name, id string, variables any, mutation bool, out any) (callErr error) {
	// A fixed-operation audit is encrypted before sending, then completed even on
	// transport/body failures. Never store request headers or cookies here.
	var audit struct {
		Operation  string `json:"operation"`
		Variables  any    `json:"variables"`
		StartedAt  int64  `json:"started_at"`
		FinishedAt int64  `json:"finished_at,omitempty"`
		Phase      string `json:"phase"`
		HTTP       int    `json:"http_status,omitempty"`
		Body       string `json:"body,omitempty"`
		Cause      string `json:"cause,omitempty"`
		Failure    string `json:"failure,omitempty"`
	}
	if mutation {
		audit.Operation, audit.Variables, audit.StartedAt, audit.Phase = name, variables, time.Now().Unix(), "request_pending"
		key := fmt.Sprintf("x-create-attempt:%s:%d", user, time.Now().UnixNano())
		persist := func() error {
			b, e := json.Marshal(audit)
			if e != nil {
				return e
			}
			defer clear(b)
			return c.vault.Put(key, b)
		}
		if e := persist(); e != nil {
			return errors.New("could not preserve X creation attempt before sending")
		}
		defer func() {
			audit.FinishedAt = time.Now().Unix()
			if callErr != nil {
				audit.Failure = callErr.Error()
			}
			if e := persist(); e != nil {
				callErr = errors.New("could not preserve X creation outcome; automatic recovery blocked")
			}
		}()
	}
	target := "https://x.com/i/api/graphql/" + id + "/" + name
	method := "GET"
	var body []byte
	if mutation {
		method = "POST"
		body, _ = json.Marshal(map[string]any{"variables": variables, "queryId": id})
	} else {
		b, _ := json.Marshal(variables)
		q := url.Values{"variables": {string(b)}}
		if name == "useSubscriptionProductDetailsByRestIdQuery" {
			q.Set("features", `{"subscriptions_marketing_page_fetch_promotions":true}`)
		}
		target += "?" + q.Encode()
	}
	req, e := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if e != nil {
		return e
	}
	req.Header = c.headers.Clone()
	req.Header.Set("Referer", "https://x.com/"+user+"/gift-premium")
	res, e := c.http.Do(req)
	if e != nil {
		audit.Phase, audit.Cause = "transport_failed", e.Error()
		return temporary(fmt.Errorf("X %s request failed", name))
	}
	audit.HTTP, audit.Phase = res.StatusCode, "response_received"
	defer res.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	defer clear(raw)
	if len(raw) > 2<<20 {
		audit.Body = string(raw[:2<<20])
		audit.Phase = "response_too_large"
		return errors.New("X response exceeded size limit")
	}
	audit.Body = string(raw)
	if e != nil {
		audit.Phase, audit.Cause = "response_read_failed", e.Error()
		if res.StatusCode >= 400 && res.StatusCode < 500 {
			return httpFailure(errors.New("X error response could not be read"), res.StatusCode, res.Header.Get("Retry-After"))
		}
		return temporary(errors.New("X response could not be read"))
	}
	audit.Phase = "response_validation"
	if res.StatusCode != 200 {
		return httpFailure(fmt.Errorf("X %s returned HTTP %d; no payment attempted", name, res.StatusCode), res.StatusCode, res.Header.Get("Retry-After"))
	}
	var envelope struct {
		Errors []json.RawMessage `json:"errors"`
	}
	if e = json.Unmarshal(raw, &envelope); e != nil {
		return errors.New("X returned non-JSON data")
	}
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("X %s rejected the operation", name)
	}
	if e = json.Unmarshal(raw, out); e != nil {
		return e
	}
	audit.Phase = "response_decoded"
	return nil
}
func (c *xClient) recipient(ctx context.Context, user string) (string, error) {
	return c.identity(ctx, user, true)
}
func (c *xClient) identity(ctx context.Context, user string, requireEligible bool) (string, error) {
	var r struct {
		Data struct {
			User struct {
				Result struct {
					ID       string `json:"rest_id"`
					Eligible bool   `json:"premium_gifting_eligible"`
					Core     struct {
						Screen string `json:"screen_name"`
					} `json:"core"`
				} `json:"result"`
			} `json:"user"`
		} `json:"data"`
	}
	if e := c.call(ctx, user, "PremiumGiftingQuery", "kn8hCE6bHstQV2MtfYDTKg", map[string]string{"screenName": user}, false, &r); e != nil {
		return "", e
	}
	u := r.Data.User.Result
	if u.ID == "" {
		return "", ErrUserNotFound
	}
	if !strings.EqualFold(u.Core.Screen, user) {
		return "", errors.New("recipient identity or gift eligibility could not be verified")
	}
	if requireEligible && !u.Eligible {
		return "", ErrNotEligible
	}
	return u.ID, nil
}
func (c *xClient) quote(ctx context.Context, user string, p Plan) error {
	var r struct {
		Data struct {
			Product struct {
				ID     string `json:"rest_id"`
				Prices []struct {
					Amount   int64  `json:"amount_local_micro"`
					Currency string `json:"currency_code"`
					Type     string `json:"price_type"`
				} `json:"prices"`
			} `json:"web_subscription_product_details_by_rest_id"`
		} `json:"data"`
	}
	if e := c.call(ctx, user, "useSubscriptionProductDetailsByRestIdQuery", "Se1Bp6zcNnuXYXRecV2qLA", map[string]string{"stripeId": p.ProductID}, false, &r); e != nil {
		return e
	}
	product := r.Data.Product
	if product.ID != p.ProductID || len(product.Prices) != 1 {
		return errors.New("unexpected X product or price list")
	}
	price := product.Prices[0]
	if price.Type != "OneTime" || !strings.EqualFold(price.Currency, "bdt") || price.Amount != int64(p.Minor)*10000 {
		return errors.New("X price is not exactly the allowed one-time BDT amount")
	}
	return nil
}
func (c *xClient) create(ctx context.Context, user, recipient string, p Plan) (string, string, error) {
	var r struct {
		Data struct {
			Gift struct {
				ID     string `json:"session_id"`
				URL    string `json:"session_url"`
				Status string `json:"session_status"`
			} `json:"onetimepurchase_gift"`
		} `json:"data"`
	}
	variables := map[string]string{"cancel_url": "https://x.com/" + user + "/gift-premium", "success_url": "https://x.com/" + user + "/gift-premium/success", "external_product_id": p.ProductID, "gift_recipient": recipient}
	if e := c.call(ctx, user, "useOneTimePurchaseGiftMutation", "GqTVJ4S1526tLkxj69xIZw", variables, true, &r); e != nil {
		return "", "", e
	}
	s := r.Data.Gift
	if s.Status != "Unpaid" || !sessionURL(s.URL, s.ID) {
		return "", "", errors.New("X did not return an unpaid matching Stripe checkout")
	}
	return s.ID, s.URL, nil
}

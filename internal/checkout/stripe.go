package checkout

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"xgift/internal/vault"
)

const xMerchant = "acct_EXAMPLE"

type stripeClient struct {
	http  *http.Client
	key   string
	vault *vault.Vault
}
type stripeError struct {
	Code, Type                string
	Message, Param, RequestID string
	HTTP                      int
	Replayed                  bool
}

func (e *stripeError) Error() string {
	return fmt.Sprintf("Stripe rejected the request (HTTP %d, type=%s, code=%s, param=%s, request=%s): %s", e.HTTP, e.Type, e.Code, e.Param, e.RequestID, e.Message)
}
func newStripe(v *vault.Vault, port int) (*stripeClient, error) {
	key, e := v.Get("stripe-key")
	if e != nil {
		return nil, e
	}
	if !regexp.MustCompile(`^pk_live_[A-Za-z0-9]+$`).Match(key) {
		return nil, errors.New("invalid Stripe merchant publishable key")
	}
	p, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	return &stripeClient{http: &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(p), TLSHandshakeTimeout: 15 * time.Second}, Timeout: 35 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("unexpected Stripe API redirect") }}, key: string(key), vault: v}, nil
}
func (s *stripeClient) close() { s.http.CloseIdleConnections() }
func (s *stripeClient) call(ctx context.Context, method, path string, form url.Values, idempotency string, out any) error {
	form.Set("key", s.key)
	target := "https://api.stripe.com/v1/" + path
	var input io.Reader
	if method == "GET" {
		target += "?" + form.Encode()
	} else {
		input = strings.NewReader(form.Encode())
	}
	req, e := http.NewRequestWithContext(ctx, method, target, input)
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if idempotency != "" {
		req.Header.Set("Idempotency-Key", idempotency)
	}
	res, e := s.http.Do(req)
	if e != nil {
		return errors.New("Stripe transport failed; request outcome may be unknown")
	}
	defer res.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if e != nil {
		return errors.New("Stripe response could not be read")
	}
	defer clear(raw)
	var envelope struct {
		Error *struct{ Type, Code, Message, Param string } `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return errors.New("Stripe returned non-JSON data")
	}
	if envelope.Error != nil {
		diagnostic, _ := json.Marshal(map[string]any{"http_status": res.StatusCode, "request_id": res.Header.Get("Request-Id"), "idempotent_replayed": res.Header.Get("Idempotent-Replayed"), "path": path, "error": envelope.Error})
		if err := s.vault.Put("stripe-error:last", diagnostic); err != nil {
			return errors.New("could not persist Stripe error; order requires inspection")
		}
		msg := envelope.Error.Message
		for name, values := range form {
			if strings.Contains(name, "card[") || strings.Contains(name, "billing_details") || name == "key" {
				for _, value := range values {
					if value != "" {
						msg = strings.ReplaceAll(msg, value, "[redacted]")
					}
				}
			}
		}
		msg = regexp.MustCompile(`[0-9]{12,19}|(?:cs_live_|pm_|pi_|pk_live_|sk_live_)[A-Za-z0-9_]+`).ReplaceAllString(msg, "[redacted]")
		msg = strings.Map(func(r rune) rune {
			if r < 32 || r == 127 {
				return ' '
			}
			return r
		}, msg)
		if len(msg) > 600 {
			msg = msg[:600]
		}
		return &stripeError{Code: safeErrorField(envelope.Error.Code), Type: safeErrorField(envelope.Error.Type), HTTP: res.StatusCode, Message: msg, Param: safeErrorField(envelope.Error.Param), RequestID: safeErrorField(res.Header.Get("Request-Id")), Replayed: res.Header.Get("Idempotent-Replayed") == "true"}
	}
	if res.StatusCode != 200 {
		return &stripeError{HTTP: res.StatusCode}
	}
	return json.Unmarshal(raw, out)
}

type paymentPage struct {
	IntentPresent bool   `json:"-"`
	IntentNull    bool   `json:"-"`
	SessionID     string `json:"session_id"`
	Currency      string `json:"currency"`
	Mode          string `json:"mode"`
	Live          bool   `json:"livemode"`
	Status        string `json:"status"`
	PaymentStatus string `json:"payment_status"`
	Checksum      string `json:"init_checksum"`
	SuccessURL    string `json:"success_url"`
	CancelURL     string `json:"cancel_url"`
	Account       struct {
		ID string `json:"account_id"`
	} `json:"account_settings"`
	SetupFuture  json.RawMessage                    `json:"setup_future_usage"`
	Subscription json.RawMessage                    `json:"subscription_data"`
	SetupIntent  json.RawMessage                    `json:"setup_intent"`
	Total        struct{ Due, Subtotal, Total int } `json:"total_summary"`
	Group        struct {
		Currency             string `json:"currency"`
		Due, Subtotal, Total int
		Items                []struct {
			Name                      string `json:"name"`
			Quantity, Subtotal, Total int
			Price                     struct {
				Currency, Type string
				UnitAmount     int             `json:"unit_amount"`
				Recurring      json.RawMessage `json:"recurring"`
				Product        struct {
					ID, Name string
					Live     bool `json:"livemode"`
				} `json:"product"`
			} `json:"price"`
		} `json:"line_items"`
	} `json:"line_item_group"`
	Intent *struct {
		ID, Status, Currency string
		Amount               int
		AmountReceived       int `json:"amount_received"`
	} `json:"payment_intent"`
}

func (p *paymentPage) UnmarshalJSON(b []byte) error {
	type plain paymentPage
	var value plain
	if err := json.Unmarshal(b, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return err
	}
	*p = paymentPage(value)
	raw, ok := fields["payment_intent"]
	p.IntentPresent = ok
	p.IntentNull = ok && bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
	return nil
}
func nullJSON(v json.RawMessage) bool { return len(v) == 0 || string(v) == "null" }
func (p *paymentPage) guard(r *Record, plan Plan, before bool) error {
	if p.SessionID != r.SessionID || p.Account.ID != xMerchant || !p.Live || p.Mode != "payment" || p.Currency != "bdt" || p.Group.Currency != "bdt" || p.SuccessURL != "https://x.com/"+r.Username+"/gift-premium/success" || p.CancelURL != "https://x.com/"+r.Username+"/gift-premium" {
		return errors.New("Stripe merchant, session, recipient return URLs, currency or payment mode mismatch")
	}
	if !nullJSON(p.SetupFuture) || !nullJSON(p.Subscription) || !nullJSON(p.SetupIntent) {
		return errors.New("recurring payments or saved-card setup are not allowed")
	}
	if p.Total.Total != plan.Minor || p.Total.Subtotal != plan.Minor || p.Group.Total != plan.Minor || p.Group.Subtotal != plan.Minor || len(p.Group.Items) != 1 {
		return errors.New("Stripe final total or line item count does not match the exact allowed price")
	}
	item := p.Group.Items[0]
	if item.Name != plan.Name() || item.Quantity != 1 || item.Subtotal != plan.Minor || item.Total != plan.Minor || item.Price.Currency != "bdt" || item.Price.Type != "one_time" || item.Price.UnitAmount != plan.Minor || !nullJSON(item.Price.Recurring) || item.Price.Product.ID != plan.ProductID || item.Price.Product.Name != plan.Name() || !item.Price.Product.Live {
		return errors.New("Stripe product, duration, quantity or unit amount mismatch")
	}
	if p.Intent != nil {
		if p.Intent.Currency != "bdt" || p.Intent.Amount != plan.Minor {
			return errors.New("Stripe payment intent amount or currency mismatch")
		}
		if before {
			return errors.New("an existing payment intent requires inspection before another submission")
		}
		if p.PaymentStatus == "paid" && (p.Intent.Status != "succeeded" || p.Intent.AmountReceived != plan.Minor) {
			return errors.New("Stripe payment intent does not confirm the exact received amount")
		}
	}
	if before && (!p.IntentPresent || !p.IntentNull) {
		return errors.New("Stripe must explicitly return a null payment intent before submitting")
	}
	if before && (p.Status != "open" || p.PaymentStatus != "unpaid" || p.Total.Due != plan.Minor || p.Group.Due != plan.Minor || p.Checksum == "") {
		return errors.New("Stripe checkout is not open and unpaid at the exact authorized amount")
	}
	return nil
}
func (s *stripeClient) page(ctx context.Context, r *Record, init bool) (*paymentPage, error) {
	method, path := "GET", "payment_pages/"+r.SessionID
	form := url.Values{}
	if init {
		method = "POST"
		path += "/init"
		form.Set("browser_locale", "en")
		form.Set("redirect_type", "url")
	}
	var page paymentPage
	e := s.call(ctx, method, path, form, "", &page)
	return &page, e
}

type card struct {
	Number  string `json:"number"`
	Month   string `json:"exp_month"`
	Year    string `json:"exp_year"`
	CVC     string `json:"cvc"`
	Name    string `json:"billing_name"`
	Email   string `json:"email"`
	Country string `json:"billing_country"`
	Postal  string `json:"billing_postal_code"`
	Line1   string `json:"billing_address_line1"`
	Line2   string `json:"billing_address_line2"`
	City    string `json:"billing_city"`
	State   string `json:"billing_state"`
}

func readCard(v *vault.Vault) (card, error) {
	raw, e := v.Get("card")
	if e != nil {
		return card{}, e
	}
	defer clear(raw)
	var c card
	if json.Unmarshal(raw, &c) != nil {
		return c, errors.New("invalid card record")
	}
	if !regexp.MustCompile(`^[0-9]{12,19}$`).MatchString(c.Number) || !regexp.MustCompile(`^(0[1-9]|1[0-2])$`).MatchString(c.Month) || !regexp.MustCompile(`^[0-9]{4}$`).MatchString(c.Year) || !regexp.MustCompile(`^[0-9]{3,4}$`).MatchString(c.CVC) || strings.TrimSpace(c.Name) == "" || !strings.Contains(c.Email, "@") {
		return c, errors.New("card, cardholder name or email is incomplete")
	}
	sum := 0
	for i, n := range c.Number {
		d := int(n - '0')
		if (len(c.Number)-i)%2 == 0 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	if sum%10 != 0 {
		return c, errors.New("card number checksum is invalid")
	}
	year, _ := strconv.Atoi(c.Year)
	month, _ := strconv.Atoi(c.Month)
	if !time.Now().Before(time.Date(year, time.Month(month)+1, 1, 0, 0, 0, 0, time.UTC)) {
		return c, errors.New("card has expired")
	}
	if c.Country == "" {
		return c, errors.New("Stripe requires a billing address; supply the card billing country and applicable address fields")
	}
	if !regexp.MustCompile(`^[A-Z]{2}$`).MatchString(c.Country) {
		return c, errors.New("invalid supplied billing country")
	}
	return c, nil
}

func CheckPaymentConfiguration(v *vault.Vault) error {
	_, err := readCard(v)
	return err
}
func (s *stripeClient) tokenize(ctx context.Context, r *Record, c card) (string, error) {
	form := url.Values{"type": {"card"}, "card[number]": {c.Number}, "card[exp_month]": {c.Month}, "card[exp_year]": {c.Year}, "card[cvc]": {c.CVC}, "billing_details[name]": {c.Name}, "billing_details[email]": {c.Email}}
	if c.Country != "" {
		form.Set("billing_details[address][country]", c.Country)
	}
	if c.Postal != "" {
		form.Set("billing_details[address][postal_code]", c.Postal)
	}
	for name, value := range map[string]string{"line1": c.Line1, "line2": c.Line2, "city": c.City, "state": c.State} {
		if value != "" {
			form.Set("billing_details[address]["+name+"]", value)
		}
	}
	var pm struct {
		ID, Type string
		Live     bool `json:"livemode"`
	}
	e := s.call(ctx, "POST", "payment_methods", form, idempotency(r, "method"), &pm)
	if e != nil {
		return "", e
	}
	if !pm.Live || pm.Type != "card" || !regexp.MustCompile(`^pm_[A-Za-z0-9]+$`).MatchString(pm.ID) {
		return "", errors.New("invalid Stripe card token")
	}
	return pm.ID, nil
}
func idempotency(r *Record, operation string) string {
	sum := sha256.Sum256([]byte("xgift-v1:" + operation + ":" + r.SessionID + ":" + r.RecipientID + ":" + strconv.Itoa(r.Months)))
	return "xgift-" + hex.EncodeToString(sum[:])
}
func confirmationForm(r *Record, p *paymentPage, method string, plan Plan) url.Values {
	return url.Values{"payment_method": {method}, "expected_amount": {strconv.Itoa(plan.Minor)}, "expected_payment_method_type": {"card"}, "init_checksum": {p.Checksum}, "return_url": {"https://x.com/" + r.Username + "/gift-premium/success"}}
}
func (s *stripeClient) confirm(ctx context.Context, r *Record) (*paymentPage, error) {
	form, e := url.ParseQuery(r.ConfirmParameters)
	if e != nil {
		return nil, e
	}
	var result paymentPage
	e = s.call(ctx, "POST", "payment_pages/"+r.SessionID+"/confirm", form, r.ConfirmKey, &result)
	return &result, e
}

func safeErrorField(s string) string {
	if len(s) > 100 {
		return "[redacted]"
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_.\[\]-]*$`).MatchString(s) {
		return "[redacted]"
	}
	return s
}

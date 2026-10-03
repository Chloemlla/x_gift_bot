package checkout

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"xgift/internal/vault"
)

func TestResumeOrderBoundaries(t *testing.T) {
	dir := t.TempDir()
	password := filepath.Join(dir, "password")
	if err := os.WriteFile(password, []byte(strings.Repeat("p", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	v, err := vault.Open(filepath.Join(dir, "vault.db"), password, true)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	base := Record{Username: "recipient", RecipientID: "1234", Months: 6, Amount: 60000, Currency: "BDT", ProductID: "prod_EXAMPLE6MO", SessionID: "cs_live_Original123", URL: "https://checkout.stripe.com/z/pay/cs_live_Original123", Status: "created", Created: 123}
	tests := []struct {
		name        string
		mutate      func(*Record)
		wantError   string
		wantSuccess bool
	}{
		{"unpaid reruns X checks", func(r *Record) {}, "X API authentication metadata", false},
		{"already successful", func(r *Record) { r.Status = "succeeded" }, "", true},
		{"wrong account", func(r *Record) { r.Username = "other" }, "identity or plan mismatch", false},
		{"wrong recipient", func(r *Record) { r.RecipientID = "9999" }, "identity or plan mismatch", false},
		{"wrong price", func(r *Record) { r.Amount = 30000 }, "identity or plan mismatch", false},
		{"wrong plan", func(r *Record) { r.Months = 3 }, "identity or plan mismatch", false},
		{"unknown reconciles without X eligibility", func(r *Record) { r.Status = "unknown"; r.SubmittedAt = 124 }, "original confirmation evidence", false},
		{"submitting reconciles without X eligibility", func(r *Record) { r.Status = "submitting"; r.SubmittedAt = 124 }, "original confirmation evidence", false},
		{"requires action reconciles", func(r *Record) { r.Status = "requires_action"; r.SubmittedAt = 124 }, "original confirmation evidence", false},
		{"created with submitted time", func(r *Record) { r.SubmittedAt = 124 }, "payment evidence", false},
		{"created with confirm parameters", func(r *Record) { r.ConfirmParameters = "payment_method=pm_old" }, "payment evidence", false},
		{"created with confirm key", func(r *Record) { r.ConfirmKey = "old" }, "payment evidence", false},
		{"created with payment method", func(r *Record) { r.PaymentMethod = "pm_old" }, "payment evidence", false},
		{"created with preflight evidence", func(r *Record) { r.PreflightSaved = true }, "payment evidence", false},
		{"unsupported state", func(r *Record) { r.Status = "canceled" }, "cannot be resumed", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := base
			tt.mutate(&r)
			raw, _ := json.Marshal(r)
			if err := v.Put("checkout:1234", raw); err != nil {
				t.Fatal(err)
			}
			result, err := ResumeForRecipient(context.Background(), v, "recipient", "1234", 0, 6)
			if tt.wantSuccess {
				if err != nil || result == nil || result.Status != "succeeded" {
					t.Fatalf("unexpected result: %v %v", result, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("error=%v, want %q", err, tt.wantError)
			}
			after, e := v.Get("checkout:1234")
			if e != nil || string(after) != string(raw) {
				t.Fatal("read-only/rejected resume changed order")
			}
		})
	}
}

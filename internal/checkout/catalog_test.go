package checkout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"xgift/internal/vault"
)

func TestParseCatalog(t *testing.T) {
	valid := `{"merchant":"acct_TESTMERCHANT","currency":"usd","plans":[{"months":3,"amount":30000,"product":"prod_TEST3MO"},{"months":6,"amount":60000,"product":"prod_TEST6MO"}]}`
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"valid", valid, ""},
		{"single plan", `{"merchant":"acct_TESTMERCHANT","currency":"usd","plans":[{"months":12,"amount":120000,"product":"prod_TEST12MO"}]}`, ""},
		{"not json", `{"merchant":`, "invalid catalog JSON"},
		{"bad merchant", `{"merchant":"acct_","currency":"usd","plans":[{"months":3,"amount":30000,"product":"prod_TEST3MO"}]}`, "invalid catalog merchant"},
		{"uppercase currency", `{"merchant":"acct_TESTMERCHANT","currency":"USD","plans":[{"months":3,"amount":30000,"product":"prod_TEST3MO"}]}`, "invalid catalog currency"},
		{"no plans", `{"merchant":"acct_TESTMERCHANT","currency":"usd","plans":[]}`, "one or two plans"},
		{"three plans", `{"merchant":"acct_TESTMERCHANT","currency":"usd","plans":[{"months":1,"amount":100,"product":"prod_A"},{"months":2,"amount":200,"product":"prod_B"},{"months":3,"amount":300,"product":"prod_C"}]}`, "one or two plans"},
		{"duplicate months", `{"merchant":"acct_TESTMERCHANT","currency":"usd","plans":[{"months":3,"amount":100,"product":"prod_A"},{"months":3,"amount":200,"product":"prod_B"}]}`, "unique values from 1 to 24"},
		{"months out of range", `{"merchant":"acct_TESTMERCHANT","currency":"usd","plans":[{"months":36,"amount":100,"product":"prod_A"}]}`, "unique values from 1 to 24"},
		{"zero amount", `{"merchant":"acct_TESTMERCHANT","currency":"usd","plans":[{"months":3,"amount":0,"product":"prod_A"}]}`, "amount must be positive"},
		{"bad product", `{"merchant":"acct_TESTMERCHANT","currency":"usd","plans":[{"months":3,"amount":100,"product":"sku_A"}]}`, "invalid catalog plan product"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := ParseCatalog([]byte(tt.raw))
			if tt.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error=%v, want %q (catalog=%+v)", err, tt.want, c)
			}
		})
	}
}

func TestCatalogPlanFor(t *testing.T) {
	c, err := ParseCatalog([]byte(`{"merchant":"acct_TESTMERCHANT","currency":"usd","plans":[{"months":3,"amount":30000,"product":"prod_TEST3MO"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := c.PlanFor(3)
	if err != nil || plan.Minor != 30000 || plan.ProductID != "prod_TEST3MO" || plan.Merchant != "acct_TESTMERCHANT" || plan.Currency != "usd" {
		t.Fatalf("unexpected plan: %+v %v", plan, err)
	}
	if _, err = c.PlanFor(6); err == nil {
		t.Fatal("unknown duration must be rejected")
	}
}

func TestReadCatalogMissing(t *testing.T) {
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
	if _, err = ReadCatalog(v); err == nil || !strings.Contains(err.Error(), "catalog record is missing") {
		t.Fatalf("error=%v, want missing-record guidance", err)
	}
}

package checkout

import (
	"encoding/json"
	"errors"
	"regexp"
	"xgift/internal/vault"
)

// Catalog is the operator-configured Stripe merchant and gift plan list,
// stored as the encrypted vault record "catalog". Amounts are minor units.
type Catalog struct {
	Merchant string        `json:"merchant"`
	Currency string        `json:"currency"`
	Plans    []CatalogPlan `json:"plans"`
}

type CatalogPlan struct {
	Months  int    `json:"months"`
	Amount  int    `json:"amount"`
	Product string `json:"product"`
}

var (
	catalogMerchantPattern = regexp.MustCompile(`^acct_[A-Za-z0-9]+$`)
	catalogCurrencyPattern = regexp.MustCompile(`^[a-z]{3}$`)
	catalogProductPattern  = regexp.MustCompile(`^prod_[A-Za-z0-9]+$`)
)

// ParseCatalog validates catalog JSON. Values never appear in error text.
func ParseCatalog(raw []byte) (Catalog, error) {
	var c Catalog
	if json.Unmarshal(raw, &c) != nil {
		return c, errors.New("invalid catalog JSON")
	}
	if !catalogMerchantPattern.MatchString(c.Merchant) {
		return c, errors.New("invalid catalog merchant")
	}
	if !catalogCurrencyPattern.MatchString(c.Currency) {
		return c, errors.New("invalid catalog currency")
	}
	if len(c.Plans) < 1 || len(c.Plans) > 2 {
		return c, errors.New("catalog must define one or two plans")
	}
	seen := map[int]bool{}
	for _, p := range c.Plans {
		if p.Months < 1 || p.Months > 24 || seen[p.Months] {
			return c, errors.New("catalog plan months must be unique values from 1 to 24")
		}
		seen[p.Months] = true
		if p.Amount <= 0 {
			return c, errors.New("catalog plan amount must be positive")
		}
		if !catalogProductPattern.MatchString(p.Product) {
			return c, errors.New("invalid catalog plan product")
		}
	}
	return c, nil
}

// ReadCatalog loads the catalog per checkout flow, like the Stripe key, so the
// site boots before the record exists and checkouts fail with a clear error.
func ReadCatalog(v *vault.Vault) (Catalog, error) {
	raw, err := v.Get("catalog")
	if err != nil {
		return Catalog{}, errors.New("catalog record is missing or unreadable; write it with setup or put --name catalog")
	}
	defer clear(raw)
	return ParseCatalog(raw)
}

// PlanFor resolves a configured plan; unknown durations are rejected.
func (c Catalog) PlanFor(months int) (Plan, error) {
	for _, p := range c.Plans {
		if p.Months == months {
			return Plan{Months: p.Months, Minor: p.Amount, ProductID: p.Product, Merchant: c.Merchant, Currency: c.Currency}, nil
		}
	}
	return Plan{}, errors.New("no catalog plan allows this duration")
}

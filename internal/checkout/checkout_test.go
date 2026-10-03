package checkout

import "testing"

func TestSessionURL(t *testing.T) {
	const id = "cs_live_Regression123"
	tests := []struct {
		name string
		link string
		id   string
		want bool
	}{
		{"g checkout", "https://checkout.stripe.com/g/pay/" + id, id, true},
		{"c checkout", "https://checkout.stripe.com/c/pay/" + id, id, true},
		{"f checkout regression", "https://checkout.stripe.com/f/pay/" + id, id, true},
		{"checkout fragment", "https://checkout.stripe.com/f/pay/" + id + "#client-data", id, true},
		{"http", "http://checkout.stripe.com/f/pay/" + id, id, false},
		{"other host", "https://checkout.stripe.com.example.com/f/pay/" + id, id, false},
		{"credentials", "https://user@checkout.stripe.com/f/pay/" + id, id, false},
		{"port", "https://checkout.stripe.com:443/f/pay/" + id, id, false},
		{"query", "https://checkout.stripe.com/f/pay/" + id + "?session=other", id, false},
		{"multiple letters", "https://checkout.stripe.com/ab/pay/" + id, id, false},
		{"digit route", "https://checkout.stripe.com/1/pay/" + id, id, false},
		{"symbol route", "https://checkout.stripe.com/-/pay/" + id, id, false},
		{"missing route", "https://checkout.stripe.com//pay/" + id, id, false},
		{"non-ASCII route", "https://checkout.stripe.com/é/pay/" + id, id, false},
		{"wrong operation", "https://checkout.stripe.com/a/other/" + id, id, false},
		{"session mismatch", "https://checkout.stripe.com/f/pay/cs_live_Other", id, false},
		{"test session", "https://checkout.stripe.com/f/pay/cs_test_Regression123", "cs_test_Regression123", false},
		{"missing session", "https://checkout.stripe.com/f/pay/", "", false},
		{"suffix", "https://checkout.stripe.com/f/pay/" + id + "/extra", id, false},
		{"malformed", "https://%/f/pay/" + id, id, false},
	}
	for _, letter := range "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ" {
		tests = append(tests, struct {
			name string
			link string
			id   string
			want bool
		}{"letter " + string(letter), "https://checkout.stripe.com/" + string(letter) + "/pay/" + id, id, true})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sessionURL(tt.link, tt.id); got != tt.want {
				t.Errorf("sessionURL() = %v, want %v", got, tt.want)
			}
		})
	}
}

package checkout

import (
	"strings"
	"testing"
)

func TestCanceledAuthenticationRequiresTerminalBoundIntent(t *testing.T) {
	raw := `{"object":"payment_intent","id":"pi_Test","livemode":true,"status":"canceled","currency":"bdt","amount":60000,"capture_method":"automatic","canceled_at":123}`
	plan := Plan{Minor: 60000, Currency: "bdt"}
	if err := guardCanceledAuthentication([]byte(raw), "pi_Test", plan); err != nil {
		t.Fatal(err)
	}
	for _, change := range [][2]string{{`"canceled"`, `"requires_action"`}, {`"canceled"`, `"succeeded"`}, {`"pi_Test"`, `"pi_Other"`}, {`60000`, `30000`}, {`"bdt"`, `"usd"`}, {`"automatic"`, `"manual"`}, {`"canceled_at":123`, `"canceled_at":0`}, {`"livemode":true`, `"livemode":false`}, {`"canceled_at":123`, `"canceled_at":123,"amount_received":60000`}, {`"canceled_at":123`, `"canceled_at":123,"amount_capturable":60000`}, {`"canceled_at":123`, `"canceled_at":123,"latest_charge":"ch_Test"`}} {
		if err := guardCanceledAuthentication([]byte(strings.Replace(raw, change[0], change[1], 1)), "pi_Test", plan); err == nil {
			t.Fatalf("accepted invalid evidence %v", change)
		}
	}
}

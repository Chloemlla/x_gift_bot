package proxy

import "testing"

func TestOutboundPoolOnlyAcceptsIndependentArray(t *testing.T) {
	for _, raw := range []string{
		`{"outbounds":[]}`, `null`, `[null]`, `[{}]`,
		`[{"type":"selector","tag":"a","server":"example.invalid","server_port":443}]`,
		`[{"type":"http","tag":"a","server":"example.invalid","server_port":443,"detour":"other"}]`,
		`[{"type":"http","tag":"a","server":"example.invalid","server_port":443},{"type":"http","tag":"a","server":"other.invalid","server_port":443}]`,
	} {
		if _, err := ParseOutboundPool([]byte(raw)); err == nil {
			t.Fatalf("accepted invalid shape: %s", raw)
		}
	}
	for _, raw := range []string{`[]`, `[{"type":"http","tag":"a","server":"example.invalid","server_port":443}]`} {
		if _, err := ParseOutboundPool([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
}

package checkout

import (
	"errors"
	"testing"
	"time"
)

func TestCheckoutCreationIntervalPersistsAndFailsClosed(t *testing.T) {
	v := controlFixture(t)
	now := time.UnixMilli(1700000000000)
	if err := reserveCheckoutCreation(v, now); err != nil {
		t.Fatal(err)
	}
	for _, offset := range []time.Duration{-time.Second, 0, 14999 * time.Millisecond} {
		if err := reserveCheckoutCreation(v, now.Add(offset)); !errors.Is(err, ErrCheckoutRateLimited) {
			t.Fatalf("offset %s: %v", offset, err)
		}
	}
	if err := reserveCheckoutCreation(v, now.Add(15*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := reserveCheckoutCreation(v, now.Add(16*time.Second)); !errors.Is(err, ErrCheckoutRateLimited) {
		t.Fatal("accepted a second reservation", err)
	}
	if err := v.Put("checkout-creation:last", []byte("broken")); err != nil {
		t.Fatal(err)
	}
	if err := reserveCheckoutCreation(v, now.Add(time.Hour)); err == nil {
		t.Fatal("corrupt clock bypassed limiter")
	}
}

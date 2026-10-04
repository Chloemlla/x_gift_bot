package checkout

import (
	"strings"
	"testing"
)

const (
	testCardOne   = `{"number":"4242424242424242","exp_month":"12","exp_year":"2099","cvc":"123","billing_name":"Test Holder","email":"holder@example.invalid","billing_country":"US"}`
	testCardTwo   = `{"number":"4000056655665556","exp_month":"11","exp_year":"2099","cvc":"456","billing_name":"Test Holder","email":"holder@example.invalid","billing_country":"US"}`
	testCardThree = `{"number":"5555555555554444","exp_month":"10","exp_year":"2099","cvc":"789","billing_name":"Test Holder","email":"holder@example.invalid","billing_country":"US"}`
)

func TestReadCardsPrefersSetAndFallsBackToLegacy(t *testing.T) {
	v := controlFixture(t)
	if err := v.Put("card", []byte(testCardOne)); err != nil {
		t.Fatal(err)
	}
	cards, err := readCards(v)
	if err != nil || len(cards) != 1 || cards[0].Number != "4242424242424242" {
		t.Fatalf("legacy fallback failed: %+v %v", cards, err)
	}
	if n, err := AddCardRecords(v, []byte(testCardTwo)); err != nil || n != 2 {
		t.Fatalf("add failed: %d %v", n, err)
	}
	if n, err := AddCardRecords(v, []byte("["+testCardThree+"]")); err != nil || n != 3 {
		t.Fatalf("append failed: %d %v", n, err)
	}
	cards, err = readCards(v)
	if err != nil || len(cards) != 3 {
		t.Fatalf("set not preferred: %+v %v", cards, err)
	}
	if cards[1].Name != "Test Holder" || cards[1].Country != "US" {
		t.Fatal("billing was not inherited")
	}
	// Re-adding an existing number replaces it instead of duplicating.
	if n, err := AddCardRecords(v, []byte(strings.Replace(testCardTwo, `"456"`, `"999"`, 1))); err != nil || n != 3 {
		t.Fatalf("duplicate replacement failed: %d %v", n, err)
	}
	cards, _ = readCards(v)
	if cards[1].CVC != "999" {
		t.Fatal("duplicate card was not updated")
	}
}

func TestCardValidationAndExpiryAreEnforced(t *testing.T) {
	v := controlFixture(t)
	if _, err := AddCardRecords(v, []byte(`{"number":"4242424242424243","exp_month":"12","exp_year":"2099","cvc":"123","billing_name":"N","email":"a@b.invalid","billing_country":"US"}`)); err == nil {
		t.Fatal("bad checksum accepted")
	}
	if _, err := AddCardRecords(v, []byte(`{"number":"4242424242424242","exp_month":"01","exp_year":"2000","cvc":"123","billing_name":"N","email":"a@b.invalid","billing_country":"US"}`)); err == nil {
		t.Fatal("expired card accepted")
	}
	if _, err := AddCardRecords(v, []byte(testCardOne)); err != nil {
		t.Fatal(err)
	}
	// A second card without billing inherits the first profile.
	if _, err := AddCardRecords(v, []byte(`{"number":"4000056655665556","exp_month":"11","exp_year":"2099","cvc":"456"}`)); err != nil {
		t.Fatal(err)
	}
	status, err := CardsStatus(v)
	if err != nil || len(status) != 2 || !status[0].Usable || !status[1].Usable {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	if _, err = RemoveCardRecord(v, "5556"); err != nil {
		t.Fatal(err)
	}
	if _, err = RemoveCardRecord(v, "4242"); err == nil {
		t.Fatal("removing the last card was allowed")
	}
}

func TestSetCardRecordsReplacesAndRejectsEmpty(t *testing.T) {
	v := controlFixture(t)
	if _, err := v.Get(cardsRecord); err == nil {
		t.Fatal("unexpected card set fixture")
	}
	if _, err := SetCardRecords(v, []byte(`[]`)); err == nil {
		t.Fatal("empty set accepted")
	}
	if n, err := SetCardRecords(v, []byte("["+testCardOne+","+testCardTwo+"]")); err != nil || n != 2 {
		t.Fatalf("set failed: %d %v", n, err)
	}
	rotation, err := PaymentRotationStatus(v)
	if err != nil || rotation.Cards != 2 || rotation.Usable != 2 || rotation.BatchSize != PaymentBatchSize {
		t.Fatalf("status=%+v err=%v", rotation, err)
	}
}

func TestCardSetFingerprintTracksMembershipNotOrder(t *testing.T) {
	v := controlFixture(t)
	if _, err := SetCardRecords(v, []byte("["+testCardOne+","+testCardTwo+"]")); err != nil {
		t.Fatal(err)
	}
	before, err := paymentCardFingerprint(v)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = SetCardRecords(v, []byte("["+testCardTwo+","+testCardOne+"]")); err != nil {
		t.Fatal(err)
	}
	after, _ := paymentCardFingerprint(v)
	if before != after {
		t.Fatal("set fingerprint changed when only order changed")
	}
	if _, err = AddCardRecords(v, []byte(testCardThree)); err != nil {
		t.Fatal(err)
	}
	changed, _ := paymentCardFingerprint(v)
	if changed == before {
		t.Fatal("set fingerprint did not change after adding a card")
	}
}

func TestBlockedCardsAreSkippedUntilExplicitlyUnblocked(t *testing.T) {
	v := controlFixture(t)
	if _, err := SetCardRecords(v, []byte("["+testCardOne+","+testCardTwo+","+testCardThree+"]")); err != nil {
		t.Fatal(err)
	}
	cards, _ := readCards(v)
	first := cardFingerprint(cards[0])
	second := cardFingerprint(cards[1])
	if err := blockPaymentCard(v, first, "do_not_try_again"); err != nil {
		t.Fatal(err)
	}
	if err := blockPaymentCard(v, second, "do_not_try_again"); err != nil {
		t.Fatal(err)
	}
	if usable, err := hasUsableCard(v); err != nil || !usable {
		t.Fatal("third card should remain usable")
	}
	pair, _, err := choosePaymentPair(v, cards, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cardFingerprint(pair) != cardFingerprint(cards[2]) {
		t.Fatal("blocked card was selected")
	}
	if err := blockPaymentCard(v, cardFingerprint(cards[2]), "do_not_try_again"); err != nil {
		t.Fatal(err)
	}
	if usable, _ := hasUsableCard(v); usable {
		t.Fatal("all cards blocked but still reported usable")
	}
	if _, _, err := choosePaymentPair(v, cards, nil, "", ""); err != ErrNoUsableCard {
		t.Fatalf("err=%v", err)
	}
	if cleared, err := UnblockPaymentCards(v); err != nil || cleared != 3 {
		t.Fatalf("cleared=%d err=%v", cleared, err)
	}
	if usable, _ := hasUsableCard(v); !usable {
		t.Fatal("unblock did not restore rotation")
	}
}

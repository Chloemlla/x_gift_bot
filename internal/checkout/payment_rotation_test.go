package checkout

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"xgift/internal/proxy"
	"xgift/internal/vault"
)

// rotationSetup builds a vault with the given card records and the standard
// two-server node pool used by the route tests.
func rotationSetup(t *testing.T, cards ...string) *vault.Vault {
	t.Helper()
	v := controlFixture(t)
	if err := v.Put("payment-outbounds", []byte(twoPaymentNodes)); err != nil {
		t.Fatal(err)
	}
	if _, err := SetCardRecords(v, []byte("["+strings.Join(cards, ",")+"]")); err != nil {
		t.Fatal(err)
	}
	return v
}

func assignPair(t *testing.T, v *vault.Vault, recipient string, rotate bool) (string, string) {
	t.Helper()
	route, card, err := assignPaymentRoute(v, recipient, rotate)
	if err != nil {
		t.Fatal(err)
	}
	return route.NodeID, cardFingerprint(card)
}

func TestThreeConsecutiveOrdersShareOnePair(t *testing.T) {
	v := rotationSetup(t, testCardOne, testCardTwo, testCardThree)
	firstNode, firstCard := assignPair(t, v, "101", false)
	secondNode, secondCard := assignPair(t, v, "102", false)
	thirdNode, thirdCard := assignPair(t, v, "103", false)
	if firstNode != secondNode || secondNode != thirdNode || firstCard != secondCard || secondCard != thirdCard {
		t.Fatalf("first three orders did not share one pair: %s/%s %s/%s %s/%s", firstNode, firstCard, secondNode, secondCard, thirdNode, thirdCard)
	}
	rotation, err := readPaymentRotation(v)
	if err != nil || rotation.Used != PaymentBatchSize {
		t.Fatalf("rotation=%+v err=%v", rotation, err)
	}
	fourthNode, _ := assignPair(t, v, "104", false)
	if fourthNode == thirdNode {
		t.Fatal("fourth order did not rotate to another pair")
	}
	rotation, _ = readPaymentRotation(v)
	if rotation.Used != 1 {
		t.Fatalf("new batch did not restart the counter: %+v", rotation)
	}
}

func TestDeclineEndsBatchAndNextRotationAvoidsCard(t *testing.T) {
	v := rotationSetup(t, testCardOne, testCardTwo, testCardThree)
	node, card := assignPair(t, v, "201", false)
	if err := markPaymentDecline(v, "201"); err != nil {
		t.Fatal(err)
	}
	rotation, err := readPaymentRotation(v)
	if err != nil || rotation.Used != PaymentBatchSize {
		t.Fatalf("decline was not recorded as a batch end: %+v %v", rotation, err)
	}
	if blocked, _ := paymentPairBlocked(v, card, node); !blocked {
		t.Fatal("declined card+node pair was not cooled down")
	}
	if route, e := readPaymentRoute(v, "201"); e != nil {
		t.Fatal(e)
	} else if until, e := nodeCoolingUntil(v, route.Outbound); e != nil || until <= time.Now().Unix() {
		t.Fatal("declined exit node and its shared IP were not cooled")
	}
	if blocked, _ := paymentCardBlocked(v, card); blocked {
		t.Fatal("first decline cooled the whole card instead of the node")
	}
	nextNode, _ := assignPair(t, v, "202", false)
	if nextNode == node {
		t.Fatal("next order reused the declined node")
	}
	if _, err := readPaymentRotation(v); err != nil {
		t.Fatal(err)
	}
}

func TestDeclinedPairLeavesOtherNodesForTheSameCard(t *testing.T) {
	v := rotationSetup(t, testCardOne, testCardTwo, testCardThree)
	cards, _ := readCards(v)
	nodes, err := proxy.ParseOutboundPool([]byte(twoPaymentNodes))
	if err != nil {
		t.Fatal(err)
	}
	first := cardFingerprint(cards[0])
	if err := coolPaymentPairLocked(v, first, outboundID(nodes[0]), "declined"); err != nil {
		t.Fatal(err)
	}
	if blocked, _ := paymentCardBlocked(v, first); blocked {
		t.Fatal("one pair decline cooled the whole card")
	}
	picked, node, err := choosePaymentPair(v, []card{cards[0]}, nodes, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cardFingerprint(picked) != first || outboundID(node) == outboundID(nodes[0]) {
		t.Fatal("same card was not retried through another node")
	}
	if err := coolPaymentPairLocked(v, first, outboundID(nodes[1]), "declined"); err != nil {
		t.Fatal(err)
	}
	if blocked, _ := paymentCardBlocked(v, first); !blocked {
		t.Fatal("second decline on another node did not cool the card")
	}
}

func TestRetryAfterDeclineRotatesPair(t *testing.T) {
	v := rotationSetup(t, testCardOne, testCardTwo, testCardThree)
	node, card := assignPair(t, v, "301", false)
	next, nextCard, err := assignPaymentRoute(v, "301", true)
	if err != nil {
		t.Fatal(err)
	}
	if next.NodeID == node {
		t.Fatal("retry kept the rejected node")
	}
	if cardFingerprint(nextCard) == card {
		t.Fatal("retry reused the rejected card")
	}
	if next.Card != cardFingerprint(nextCard) {
		t.Fatal("retry route did not persist its card binding")
	}
}

func TestRepeatAttemptKeepsBoundPairWithoutConsumingTwice(t *testing.T) {
	v := rotationSetup(t, testCardOne, testCardTwo, testCardThree)
	node, card := assignPair(t, v, "401", false)
	again, againCard, err := assignPaymentRoute(v, "401", false)
	if err != nil {
		t.Fatal(err)
	}
	if again.NodeID != node || cardFingerprint(againCard) != card {
		t.Fatal("repeat attempt did not keep its bound pair")
	}
	rotation, _ := readPaymentRotation(v)
	if rotation.Used != 1 {
		t.Fatalf("repeat attempt consumed another batch slot: %+v", rotation)
	}
}

func TestLegacyRouteAdoptsRunningBatchCard(t *testing.T) {
	v := rotationSetup(t, testCardOne, testCardTwo, testCardThree)
	if _, err := selectPaymentRoute(v, "501"); err != nil {
		t.Fatal(err)
	}
	_, batchCard := assignPair(t, v, "502", false)
	next, card, err := assignPaymentRoute(v, "501", false)
	if err != nil {
		t.Fatal(err)
	}
	if cardFingerprint(card) != batchCard {
		t.Fatal("legacy route did not adopt the running batch card")
	}
	if next.Card != batchCard {
		t.Fatal("card binding was not persisted on the legacy route")
	}
	rotation, _ := readPaymentRotation(v)
	if rotation.Used != 2 {
		t.Fatalf("legacy binding did not consume a batch slot: %+v", rotation)
	}
}

func TestCoolingBatchNodeMovesWithoutChangingCard(t *testing.T) {
	v := rotationSetup(t, testCardOne, testCardTwo, testCardThree)
	nodeID, card := assignPair(t, v, "601", false)
	rotation, err := readPaymentRotation(v)
	if err != nil {
		t.Fatal(err)
	}
	if err = coolPaymentNode(v, rotation.Outbound); err != nil {
		t.Fatal(err)
	}
	nextNode, nextCard := assignPair(t, v, "602", false)
	if nextNode == nodeID {
		t.Fatal("cooling node was kept for the running batch")
	}
	if nextCard != card {
		t.Fatal("card changed during transport-failure node failover")
	}
	if rotation, _ = readPaymentRotation(v); rotation.Used != 2 {
		t.Fatalf("node failover did not consume exactly one slot: %+v", rotation)
	}
}

func TestEmptyPoolKeepsCardRotationOnDirect(t *testing.T) {
	v := controlFixture(t)
	if _, err := SetCardRecords(v, []byte("["+testCardOne+","+testCardTwo+"]")); err != nil {
		t.Fatal(err)
	}
	route, card, err := assignPaymentRoute(v, "701", false)
	if err != nil {
		t.Fatal(err)
	}
	if routeGroup(route.Outbound) != "direct" || route.Card != cardFingerprint(card) {
		t.Fatalf("route=%+v", route)
	}
}

func TestDoNotTryAgainBlocksOneCardWithoutGlobalPause(t *testing.T) {
	v := rotationSetup(t, testCardOne, testCardTwo, testCardThree)
	route, _, err := assignPaymentRoute(v, "801", false)
	if err != nil {
		t.Fatal(err)
	}
	declined := route.Card
	r := &Record{RecipientID: route.Recipient, SessionID: "cs_live_RotationOne", Status: "declined", LastError: &stripeError{HTTP: 402, Type: "card_error", AdviceCode: "do_not_try_again"}}
	if err = savePaymentControl(v, paymentControl{LastSession: r.SessionID}); err != nil {
		t.Fatal(err)
	}
	if err = paymentOutcome(v, r); err != nil {
		t.Fatal(err)
	}
	if paused, _ := PaymentPaused(v); paused {
		t.Fatal("one blocked card paused rotation while two usable cards remain")
	}
	if blocked, _ := paymentCardBlocked(v, declined); !blocked {
		t.Fatal("declined card was not blocked")
	}
	cards, _ := readCards(v)
	for _, c := range cards {
		if cardFingerprint(c) != declined {
			if err = blockPaymentCard(v, cardFingerprint(c), "do_not_try_again"); err != nil {
				t.Fatal(err)
			}
		}
	}
	second := &Record{RecipientID: route.Recipient, SessionID: "cs_live_RotationTwo", Status: "declined", LastError: &stripeError{HTTP: 402, Type: "card_error", AdviceCode: "do_not_try_again"}}
	if err = savePaymentControl(v, paymentControl{LastSession: second.SessionID}); err != nil {
		t.Fatal(err)
	}
	if err = paymentOutcome(v, second); err != nil {
		t.Fatal(err)
	}
	if paused, _ := PaymentPaused(v); !paused {
		t.Fatal("all cards blocked but the site stayed open")
	}
	if cleared, err := UnblockPaymentCards(v); err != nil || cleared < 3 {
		t.Fatalf("cleared=%d err=%v", cleared, err)
	}
	if paused, _ := PaymentPaused(v); paused {
		t.Fatal("explicit unblock did not lift the provider pause")
	}
	if usable, _ := hasUsableCard(v); !usable {
		t.Fatal("unblock did not restore rotation")
	}
}

func TestCoolingCardReturnsAfterCooldown(t *testing.T) {
	v := rotationSetup(t, testCardOne, testCardTwo, testCardThree)
	cards, _ := readCards(v)
	first := cardFingerprint(cards[0])
	if err := coolPaymentCard(v, first, "declined"); err != nil {
		t.Fatal(err)
	}
	if blocked, _ := paymentCardBlocked(v, first); !blocked {
		t.Fatal("cooled card still selectable")
	}
	blocks, _ := readCardBlocks(v)
	entry := blocks[first]
	entry.Until = time.Now().Add(-time.Second).Unix()
	blocks[first] = entry
	if err := saveCardBlocks(v, blocks); err != nil {
		t.Fatal(err)
	}
	if blocked, _ := paymentCardBlocked(v, first); blocked {
		t.Fatal("expired cooldown kept the card out of rotation")
	}
	if usable, _ := hasUsableCard(v); !usable {
		t.Fatal("expired cooldown broke the usable card set")
	}
}

func TestTwoCardRotationAlternatesBatches(t *testing.T) {
	v := rotationSetup(t, testCardOne, testCardTwo)
	var batches []string
	for order := 0; order < 6; order++ {
		_, card := assignPair(t, v, fmt.Sprintf("81%d", order), false)
		batches = append(batches, card)
	}
	if batches[0] != batches[1] || batches[1] != batches[2] {
		t.Fatalf("first batch was not consistent: %v", batches)
	}
	if batches[3] != batches[4] || batches[4] != batches[5] {
		t.Fatalf("second batch was not consistent: %v", batches)
	}
	if batches[0] == batches[3] {
		t.Fatalf("two-card batch selection did not alternate: %v", batches)
	}
	// A forced rotation (declined retry) must also alternate the card.
	_, first, err := assignPaymentRoute(v, "82", true)
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := assignPaymentRoute(v, "83", true)
	if err != nil {
		t.Fatal(err)
	}
	if cardFingerprint(first) == cardFingerprint(second) {
		t.Fatal("consecutive forced rotations reused the same card")
	}
}

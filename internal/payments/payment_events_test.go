package payments

import "testing"

func TestPaymentEventBrokerPublishesOnlyToMatchingToken(t *testing.T) {
	broker := NewPaymentEventBroker(nil, nil)
	matching, unsubscribeMatching := broker.Subscribe("payment-token")
	defer unsubscribeMatching()
	other, unsubscribeOther := broker.Subscribe("other-token")
	defer unsubscribeOther()

	broker.publish("payment-token")
	select {
	case <-matching:
	default:
		t.Fatal("matching subscriber did not receive payment event")
	}
	select {
	case <-other:
		t.Fatal("non-matching subscriber received payment event")
	default:
	}
}

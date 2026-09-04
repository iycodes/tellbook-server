package appdata

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestInboxEventBrokerRoutesAndUnsubscribes(t *testing.T) {
	broker := NewInboxEventBroker(nil, nil)
	providerID, otherProviderID := uuid.New(), uuid.New()
	providerSignals, unsubscribe, err := broker.Subscribe("provider", providerID, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	otherSignals, otherUnsubscribe, err := broker.Subscribe("provider", otherProviderID, "127.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	defer otherUnsubscribe()

	broker.publish(inboxEventSubscriptionKey{ActorType: "provider", ActorID: providerID})
	select {
	case signal := <-providerSignals:
		if signal != InboxBrokerWake {
			t.Fatalf("provider signal = %v, want wake", signal)
		}
	default:
		t.Fatal("provider did not receive its routed wake-up")
	}
	select {
	case <-otherSignals:
		t.Fatal("another provider received a foreign wake-up")
	default:
	}

	unsubscribe()
	broker.publish(inboxEventSubscriptionKey{ActorType: "provider", ActorID: providerID})
	select {
	case <-providerSignals:
		t.Fatal("unsubscribed provider received another wake-up")
	default:
	}
}

func TestInboxEventBrokerEnforcesActorAndRemoteLimits(t *testing.T) {
	broker := NewInboxEventBroker(nil, nil)
	providerID := uuid.New()
	unsubscribes := make([]func(), 0, maxInboxStreamsPerActor)
	for range maxInboxStreamsPerActor {
		_, unsubscribe, err := broker.Subscribe("provider", providerID, "192.0.2.1")
		if err != nil {
			t.Fatal(err)
		}
		unsubscribes = append(unsubscribes, unsubscribe)
	}
	if _, _, err := broker.Subscribe("provider", providerID, "192.0.2.2"); !errors.Is(err, ErrInboxStreamLimit) {
		t.Fatalf("fourth actor stream error = %v, want stream limit", err)
	}
	for _, unsubscribe := range unsubscribes {
		unsubscribe()
	}

	remoteUnsubscribes := make([]func(), 0, maxInboxStreamsPerRemoteIP)
	for range maxInboxStreamsPerRemoteIP {
		_, unsubscribe, err := broker.Subscribe("provider", uuid.New(), "198.51.100.1")
		if err != nil {
			t.Fatal(err)
		}
		remoteUnsubscribes = append(remoteUnsubscribes, unsubscribe)
	}
	if _, _, err := broker.Subscribe("provider", uuid.New(), "198.51.100.1"); !errors.Is(err, ErrInboxStreamLimit) {
		t.Fatalf("remote overflow error = %v, want stream limit", err)
	}
	for _, unsubscribe := range remoteUnsubscribes {
		unsubscribe()
	}
}

func TestInboxRemoteIPUsesSanitizedSocketAddress(t *testing.T) {
	request := httptest.NewRequest("GET", "/v1/app/inbox/events", nil)
	request.RemoteAddr = "198.51.100.20:4000"
	if got := inboxRemoteIP(request); got != "198.51.100.20" {
		t.Fatalf("inboxRemoteIP() = %q", got)
	}
}

func TestInboxEventBrokerOverflowBecomesReset(t *testing.T) {
	broker := NewInboxEventBroker(nil, nil)
	providerID := uuid.New()
	signals, unsubscribe, err := broker.Subscribe("provider", providerID, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	key := inboxEventSubscriptionKey{ActorType: "provider", ActorID: providerID}
	for range inboxBrokerSignalBuffer + 1 {
		broker.publish(key)
	}

	if signal := <-signals; signal != InboxBrokerReset {
		t.Fatalf("overflow signal = %v, want reset", signal)
	}
	select {
	case signal := <-signals:
		t.Fatalf("overflow queue retained extra signal %v", signal)
	default:
	}
}

func TestInboxNotificationPayloadRoundTrip(t *testing.T) {
	clientID, customerID := uuid.New(), uuid.New()
	gotClientID, gotCustomerID, ok := parseInboxNotificationPayload(
		inboxNotificationPayload(42, clientID, customerID),
	)
	if !ok || gotClientID != clientID || gotCustomerID != customerID {
		t.Fatalf("notification payload = %s/%s/%v", gotClientID, gotCustomerID, ok)
	}
	if _, _, ok := parseInboxNotificationPayload("bad|" + clientID.String() + "|" + customerID.String()); ok {
		t.Fatal("malformed sequence was accepted")
	}
}

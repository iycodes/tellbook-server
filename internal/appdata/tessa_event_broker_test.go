package appdata

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestTessaEventBrokerRoutesAndUnsubscribes(t *testing.T) {
	broker := NewTessaEventBroker(nil, nil)
	providerID, otherProviderID := uuid.New(), uuid.New()
	providerSignals, unsubscribe, err := broker.Subscribe(providerID)
	if err != nil {
		t.Fatal(err)
	}
	otherSignals, otherUnsubscribe, err := broker.Subscribe(otherProviderID)
	if err != nil {
		t.Fatal(err)
	}
	defer otherUnsubscribe()

	broker.publish(providerID)
	select {
	case signal := <-providerSignals:
		if signal != TessaBrokerWake {
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
	unsubscribe()
	broker.publish(providerID)
	select {
	case <-providerSignals:
		t.Fatal("unsubscribed provider received another wake-up")
	default:
	}
}

func TestTessaEventBrokerEnforcesPerProviderLimit(t *testing.T) {
	broker := NewTessaEventBroker(nil, nil)
	providerID := uuid.New()
	unsubscribes := make([]func(), 0, maxTessaStreamsPerClient)
	for range maxTessaStreamsPerClient {
		_, unsubscribe, err := broker.Subscribe(providerID)
		if err != nil {
			t.Fatal(err)
		}
		unsubscribes = append(unsubscribes, unsubscribe)
	}
	if _, _, err := broker.Subscribe(providerID); !errors.Is(err, ErrTessaStreamLimit) {
		t.Fatalf("overflow stream error = %v, want stream limit", err)
	}
	for _, unsubscribe := range unsubscribes {
		unsubscribe()
	}
	if _, unsubscribe, err := broker.Subscribe(providerID); err != nil {
		t.Fatalf("stream slot was not released: %v", err)
	} else {
		unsubscribe()
	}
}

func TestTessaEventBrokerRejectsInvalidSubscription(t *testing.T) {
	if _, _, err := (*TessaEventBroker)(nil).Subscribe(uuid.New()); !errors.Is(err, ErrTessaStreamLimit) {
		t.Fatalf("nil broker error = %v, want stream limit", err)
	}
	broker := NewTessaEventBroker(nil, nil)
	if _, _, err := broker.Subscribe(uuid.Nil); !errors.Is(err, ErrTessaStreamLimit) {
		t.Fatalf("nil provider error = %v, want stream limit", err)
	}
}

func TestTessaEventBrokerOverflowBecomesSingleReset(t *testing.T) {
	broker := NewTessaEventBroker(nil, nil)
	providerID := uuid.New()
	signals, unsubscribe, err := broker.Subscribe(providerID)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	for range tessaBrokerSignalBuffer + 1 {
		broker.publish(providerID)
	}

	if signal := <-signals; signal != TessaBrokerReset {
		t.Fatalf("overflow signal = %v, want reset", signal)
	}
	select {
	case signal := <-signals:
		t.Fatalf("overflow queue retained extra signal %v", signal)
	default:
	}
}

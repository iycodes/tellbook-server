package appdata

import (
	"net/http/httptest"
	"testing"
)

func TestWriteInboxSSEFramesCursorEventAndJSON(t *testing.T) {
	recorder := httptest.NewRecorder()
	if ok := writeInboxSSE(
		recorder,
		recorder,
		"inbox",
		"a1",
		[]byte(`{"cursor":"a1","type":"message.created"}`),
	); !ok {
		t.Fatal("writeInboxSSE() failed")
	}
	want := "id: a1\nevent: inbox\ndata: {\"cursor\":\"a1\",\"type\":\"message.created\"}\n\n"
	if got := recorder.Body.String(); got != want {
		t.Fatalf("SSE frame = %q, want %q", got, want)
	}
}

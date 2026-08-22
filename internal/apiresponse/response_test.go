package apiresponse

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteErrorUsesStableEnvelope(t *testing.T) {
	w := httptest.NewRecorder()
	WriteError(w, http.StatusBadGateway, errors.New("agent unavailable"))
	var got ErrorEnvelope
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusBadGateway || got.OK || got.Code != "upstream_unavailable" || got.Message != "agent unavailable" || got.Error != got.Message || !got.Retryable {
		t.Fatalf("unexpected response: status=%d body=%+v", w.Code, got)
	}
}

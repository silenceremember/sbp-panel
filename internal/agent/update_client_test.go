package agent

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunUpdateClientWaitsForCompletion(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		progress := UpdateProgress{Status: "running", Progress: 2, Stage: "Preparing update."}
		if r.Method == http.MethodGet && polls.Add(1) >= 2 {
			progress = UpdateProgress{Status: "done", Progress: 100, Stage: "Update complete.", Target: "1.0.1"}
		}
		_ = json.NewEncoder(w).Encode(progress)
	}))
	defer server.Close()
	var output bytes.Buffer
	if err := runUpdateClient(server.Client(), server.URL, &output, time.Millisecond, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if text := output.String(); !strings.Contains(text, "[  2%]") || !strings.Contains(text, "[100%]") {
		t.Fatalf("unexpected progress output: %q", text)
	}
	if strings.Contains(output.String(), "\x1b[") {
		t.Fatalf("non-terminal output contains ANSI escapes: %q", output.String())
	}
}

func TestRunUpdateClientReportsAgentError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"message":"another operation is running"}`))
	}))
	defer server.Close()
	err := runUpdateClient(server.Client(), server.URL, &bytes.Buffer{}, time.Millisecond, time.Now().Add(time.Second))
	if err == nil || !strings.Contains(err.Error(), "another operation is running") {
		t.Fatalf("unexpected error: %v", err)
	}
}

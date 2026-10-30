package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthz(t *testing.T) {
	// 1. ARRANGE – build everything the code needs
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	// 2. ACT – call the thing you're testing, once
	newMux().ServeHTTP(rec, req)

	// 3. ASSERT – check what came out
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d,want%d", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); body != "ok" {
		t.Fatalf("body = %q, want %q", body, "ok")
	}
}

// 1. What does the function take? Build each input. That's ARRANGE.
// 2. What does it give back? Call it, then use the result. That's ACT.
// 3. What's its job? Check that the job got done. That's ASSERT.
// 4. Make the test fail on purpose once, to prove it can.
func TestLogRequests(t *testing.T) {
	//ARRANGE
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil)) //create JSONHandler that writes to &buf using given options (nil)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot) //StatusTeapot = status code 418
	})

	handler := logRequests(logger, next)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	//ACT
	handler.ServeHTTP(rec, req)

	//ASSERT
	if !strings.Contains(buf.String(), `"status":418`) {
		t.Fatalf("log does not contain status 418, got: %s", buf.String())
	}
}

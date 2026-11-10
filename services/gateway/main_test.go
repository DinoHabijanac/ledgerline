package main

import (
	"net/http"
	"net/http/httptest"
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

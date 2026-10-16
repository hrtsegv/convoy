package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hrtsegv/convoy/internal/config"
)

func TestJobRequestValidation(t *testing.T) {
	handler := Handler(context.Background(), nil, config.Config{JobQueryTimeout: time.Second}, testLogger())
	for _, test := range []struct {
		name        string
		body        string
		contentType string
		status      int
	}{
		{name: "missing content type", body: `{}`, status: 415},
		{name: "wrong content type", body: `{}`, contentType: "text/plain", status: 415},
		{name: "invalid JSON", body: `{`, contentType: "application/json", status: 400},
		{name: "array body", body: `[]`, contentType: "application/json", status: 400},
		{name: "unknown field", body: `{"kind":"echo","payload":{},"queue":"other"}`, contentType: "application/json", status: 400},
		{name: "multiple objects", body: `{"kind":"echo","payload":{}} {}`, contentType: "application/json", status: 400},
		{name: "missing kind", body: `{"payload":{}}`, contentType: "application/json", status: 400},
		{name: "invalid kind", body: `{"kind":" bad","payload":{}}`, contentType: "application/json", status: 400},
		{name: "missing payload", body: `{"kind":"echo"}`, contentType: "application/json", status: 400},
		{name: "oversize body", body: `{"kind":"echo","payload":"` + strings.Repeat("x", maxJobRequestBytes) + `"}`, contentType: "application/json", status: 413},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/v1/jobs", strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.status || !json.Valid(recorder.Body.Bytes()) {
				t.Fatalf("status=%d; want %d with a JSON error", recorder.Code, test.status)
			}
		})
	}
}

func TestJobRoutesRejectInvalidIDsAndShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	handler := Handler(ctx, nil, config.Config{JobQueryTimeout: time.Second}, testLogger())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("GET", "/v1/jobs/not-a-uuid", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid ID: status=%d; want 400", recorder.Code)
	}
	cancel()
	for _, request := range []*http.Request{
		httptest.NewRequest("POST", "/v1/jobs", strings.NewReader(`{"kind":"echo","payload":{}}`)),
		httptest.NewRequest("GET", "/v1/jobs/00000000-0000-0000-0000-000000000000", nil),
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("shutdown: status=%d; want 503", recorder.Code)
		}
	}
}

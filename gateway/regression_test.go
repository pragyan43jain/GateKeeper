package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestBackendURLRejectsUnsupportedTargets(t *testing.T) {
	for _, raw := range []string{"ftp://host", "http://user:password@host", "https://host/#fragment"} {
		if _, err := parseBackendURLs(raw, ""); err == nil {
			t.Errorf("accepted invalid target %q", raw)
		}
	}
}

func TestBackendProxyHostAndCredentialIsolation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "" {
			t.Error("gateway credential leaked upstream")
		}
		if r.Host == "public.example" {
			t.Error("public host forwarded upstream")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	node := NewBackendNode(target, nil, nil)
	req := httptest.NewRequest("GET", "http://public.example/api/students", nil)
	req.Header.Set("X-API-Key", "private-key")
	res := httptest.NewRecorder()
	node.proxy.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("got %d", res.Code)
	}
}

func TestFlushCommitsStatus(t *testing.T) {
	res := httptest.NewRecorder()
	writer := newStatusResponseWriter(res)
	writer.Flush()
	writer.WriteHeader(http.StatusBadGateway)
	if writer.statusCode != http.StatusOK || res.Code != http.StatusOK {
		t.Fatal("flush did not commit 200")
	}
}

func TestEmptyConfiguredKeyFailsClosed(t *testing.T) {
	handler := newGatewayHandler("", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("forwarded unauthenticated request") }), nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest("GET", "/api/students", nil))
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", res.Code)
	}
}

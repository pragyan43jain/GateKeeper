package main

import (
	"net/url"
	"testing"
)

func TestRouteRegistryUsesLongestPrefixAndMethodAllowlist(t *testing.T) {
	firstURL, _ := url.Parse("http://first.example:8080")
	secondURL, _ := url.Parse("http://second.example:8080")
	first := NewBackendNode(firstURL, nil, nil)
	first.configID = "backend-first"
	second := NewBackendNode(secondURL, nil, nil)
	second.configID = "backend-second"
	pool, err := NewBackendPool([]*BackendNode{first, second})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRouteRegistry(pool)
	routes := []ManagedRoute{
		{ID: "r1", Name: "items", PathPrefix: "/api/items", Methods: []string{"GET", "POST"}, BackendID: "backend-first", AuthRequired: true, Enabled: true},
		{ID: "r2", Name: "private items", PathPrefix: "/api/items/private", Methods: []string{"GET"}, BackendID: "backend-second", AuthRequired: false, Enabled: true},
	}
	if err := registry.Replace(routes); err != nil {
		t.Fatalf("replace route table: %v", err)
	}
	route, matched, allowed := registry.Match("/api/items/private/1", "GET")
	if !matched || !allowed || route.ID != "r2" || route.AuthRequired {
		t.Fatalf("longest route match = %+v matched=%v allowed=%v", route, matched, allowed)
	}
	if _, matched, _ := registry.Match("/api/items2", "GET"); matched {
		t.Fatal("prefix match crossed a path segment boundary")
	}
	if route, matched, allowed := registry.Match("/api/items/1", "DELETE"); !matched || allowed || route.ID != "r1" {
		t.Fatalf("method mismatch = %+v matched=%v allowed=%v", route, matched, allowed)
	}
}

func TestRouteRegistryRejectsReservedAndInactiveBackends(t *testing.T) {
	target, _ := url.Parse("http://backend.example:8080")
	node := NewBackendNode(target, nil, nil)
	node.configID = "backend-1"
	pool, err := NewBackendPool([]*BackendNode{node})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRouteRegistry(pool)
	base := ManagedRoute{ID: "r1", Name: "route", Methods: []string{"GET"}, BackendID: "backend-1", Enabled: true}
	reserved := base
	reserved.PathPrefix = "/api/keys"
	if err := registry.Replace([]ManagedRoute{reserved}); err == nil {
		t.Fatal("reserved management route was accepted")
	}
	inactive := base
	inactive.PathPrefix = "/api/health"
	inactive.BackendID = "missing"
	if err := registry.Replace([]ManagedRoute{inactive}); err == nil {
		t.Fatal("route with an inactive backend was accepted")
	}
}

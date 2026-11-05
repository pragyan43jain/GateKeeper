package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
)

type routeSnapshot struct {
	routes []ManagedRoute
}

// RouteRegistry atomically publishes validated route sets and resolves the
// longest matching path prefix without locking the request path.
type RouteRegistry struct {
	pool     *BackendPool
	snapshot atomic.Pointer[routeSnapshot]
}

func NewRouteRegistry(pool *BackendPool) *RouteRegistry {
	registry := &RouteRegistry{pool: pool}
	registry.snapshot.Store(&routeSnapshot{routes: []ManagedRoute{}})
	return registry
}

func (r *RouteRegistry) Replace(routes []ManagedRoute) error {
	if r == nil || r.pool == nil {
		return errors.New("route registry has no backend pool")
	}
	if len(routes) > 500 {
		return errors.New("route table cannot contain more than 500 routes")
	}
	nodes := make(map[string]*BackendNode)
	for _, node := range r.pool.Nodes() {
		if node.configID != "" {
			nodes[node.configID] = node
		}
	}
	seenPath := make(map[string]struct{}, len(routes))
	validated := make([]ManagedRoute, 0, len(routes))
	for _, route := range routes {
		if !route.Enabled {
			continue
		}
		if err := validateRoutePath(route.PathPrefix); err != nil {
			return fmt.Errorf("route %q: %w", route.Name, err)
		}
		if _, exists := seenPath[route.PathPrefix]; exists {
			return fmt.Errorf("duplicate route path prefix %q", route.PathPrefix)
		}
		seenPath[route.PathPrefix] = struct{}{}
		if len(route.Methods) == 0 {
			return fmt.Errorf("route %q must allow at least one HTTP method", route.Name)
		}
		if (route.RequestsPerWindow == 0) != (route.WindowSeconds == 0) || route.RequestsPerWindow < 0 || route.WindowSeconds < 0 {
			return fmt.Errorf("route %q has an invalid rate-limit policy", route.Name)
		}
		methods := make([]string, 0, len(route.Methods))
		methodSeen := make(map[string]struct{}, len(route.Methods))
		for _, method := range route.Methods {
			method = strings.ToUpper(strings.TrimSpace(method))
			if !validRouteMethod(method) {
				return fmt.Errorf("route %q has unsupported method %q", route.Name, method)
			}
			if _, exists := methodSeen[method]; !exists {
				methodSeen[method] = struct{}{}
				methods = append(methods, method)
			}
		}
		route.Methods = methods
		if nodes[route.BackendID] == nil {
			return fmt.Errorf("route %q references a backend that is not enabled", route.Name)
		}
		validated = append(validated, route)
	}
	sort.Slice(validated, func(i, j int) bool {
		if len(validated[i].PathPrefix) == len(validated[j].PathPrefix) {
			return validated[i].PathPrefix < validated[j].PathPrefix
		}
		return len(validated[i].PathPrefix) > len(validated[j].PathPrefix)
	})
	r.snapshot.Store(&routeSnapshot{routes: validated})
	return nil
}

func (r *RouteRegistry) Routes() []ManagedRoute {
	if r == nil {
		return nil
	}
	snapshot := r.snapshot.Load()
	if snapshot == nil {
		return nil
	}
	routes := make([]ManagedRoute, len(snapshot.routes))
	copy(routes, snapshot.routes)
	for i := range routes {
		routes[i].Methods = append([]string(nil), snapshot.routes[i].Methods...)
	}
	return routes
}

func (r *RouteRegistry) Match(requestPath, method string) (ManagedRoute, bool, bool) {
	if r == nil {
		return ManagedRoute{}, false, false
	}
	snapshot := r.snapshot.Load()
	if snapshot == nil {
		return ManagedRoute{}, false, false
	}
	for _, route := range snapshot.routes {
		if !routePathMatches(route.PathPrefix, requestPath) {
			continue
		}
		for _, allowed := range route.Methods {
			if allowed == method || allowed == "*" {
				return route, true, true
			}
		}
		return route, true, false
	}
	return ManagedRoute{}, false, false
}

func (r *RouteRegistry) Backend(route ManagedRoute) *BackendNode {
	if r == nil || r.pool == nil {
		return nil
	}
	return r.pool.NodeByConfigID(route.BackendID)
}

func validateRoutePath(routePath string) error {
	if routePath == "" || len(routePath) > 200 || !strings.HasPrefix(routePath, "/") || strings.HasPrefix(routePath, "//") ||
		strings.ContainsAny(routePath, "?#") || strings.Contains(routePath, "..") || routePath != "/" && strings.HasSuffix(routePath, "/") {
		return errors.New("path prefix must be a normalized absolute path")
	}
	decoded, err := url.PathUnescape(routePath)
	if err != nil || decoded != routePath {
		return errors.New("path prefix contains invalid escaping")
	}
	if reservedManagementPath(routePath) {
		return errors.New("path prefix overlaps a reserved management endpoint")
	}
	return nil
}

func reservedManagementPath(routePath string) bool {
	for _, reserved := range []string{"/api/auth", "/api/metrics", "/api/users", "/api/keys", "/api/applications", "/api/backends", "/api/routes", "/api/settings", "/api/audit-logs", "/api/requests"} {
		if routePath == reserved || strings.HasPrefix(routePath, reserved+"/") || strings.HasPrefix(reserved, routePath+"/") {
			return true
		}
	}
	return false
}

func validRouteMethod(method string) bool {
	if method == "*" {
		return true
	}
	switch method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		return true
	default:
		return false
	}
}

func routePathMatches(prefix, requestPath string) bool {
	if prefix == "/" || requestPath == prefix {
		return true
	}
	return strings.HasPrefix(requestPath, prefix) && strings.HasSuffix(prefix, "/") ||
		strings.HasPrefix(requestPath, prefix) && len(requestPath) > len(prefix) && requestPath[len(prefix)] == '/'
}

func remoteClientIdentity(remoteAddr, forwarded string) string {
	if ip := net.ParseIP(strings.TrimSpace(forwarded)); ip != nil {
		return ip.String()
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err == nil {
		if ip := net.ParseIP(host); ip != nil {
			return ip.String()
		}
	}
	return remoteAddr
}

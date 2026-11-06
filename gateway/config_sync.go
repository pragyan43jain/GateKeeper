package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"log"
	"time"
)

// Replicas converge on persistent configuration without restarting. Database
// failures retain the last active configuration; per-key checks still fail closed.
func startConfigurationSync(store *ManagementStore, pool *BackendPool, routes *RouteRegistry) func() {
	if store == nil {
		return func() {}
	}
	stop, done := make(chan struct{}), make(chan struct{})
	var previous [32]byte
	syncConfiguration := func() error {
		pool.managementMu.Lock()
		defer pool.managementMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		backends, err := store.ListBackends(ctx)
		if err != nil {
			return err
		}
		rules, err := store.ListRoutes(ctx)
		if err != nil {
			return err
		}
		settings, found, err := store.LoadLoadBalancingSettings(ctx)
		if err != nil {
			return err
		}
		data, err := json.Marshal(struct {
			Backends []ManagedBackend
			Routes   []ManagedRoute
			Settings LoadBalancingSettings
		}{backends, rules, settings})
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		if digest == previous {
			return nil
		}
		if err := applyManagedBackends(pool, backends, store, ctx, false); err != nil {
			return err
		}
		if found {
			if err := pool.Configure(settings.Strategy, settings.Weights); err != nil {
				return err
			}
		}
		if err := routes.Replace(rules); err != nil {
			return err
		}
		previous = digest
		return nil
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := syncConfiguration(); err != nil {
					log.Printf("configuration_sync_failed: %v", err)
				}
			case <-stop:
				return
			}
		}
	}()
	return func() { close(stop); <-done }
}

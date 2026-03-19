package adapter

import (
	"fmt"
	"sync"

	"github.com/1919chichi/rc_1919chichi/internal/model"
)

// VendorStore is the minimal store interface the registry needs.
// Implemented by *store.Store.
type VendorStore interface {
	GetVendor(id string) (*model.VendorConfig, error)
}

// Registry resolves a vendor_id to a VendorAdapter.
// Resolution order: code adapters first, then config-based adapters from DB.
type Registry struct {
	mu           sync.RWMutex
	codeAdapters map[string]VendorAdapter
	store        VendorStore
}

func NewRegistry(store VendorStore) *Registry {
	return &Registry{
		codeAdapters: make(map[string]VendorAdapter),
		store:        store,
	}
}

// Register adds a code-level adapter that takes priority over DB config.
func (r *Registry) Register(adapter VendorAdapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.codeAdapters[adapter.VendorID()] = adapter
}

// Resolve finds the adapter for the given vendor.
// Code adapters are checked first; DB config is the fallback.
func (r *Registry) Resolve(vendorID string) (VendorAdapter, error) {
	r.mu.RLock()
	if adapter, ok := r.codeAdapters[vendorID]; ok {
		r.mu.RUnlock()
		return adapter, nil
	}
	r.mu.RUnlock()

	config, err := r.store.GetVendor(vendorID)
	if err != nil {
		return nil, fmt.Errorf("vendor %q not found: %w", vendorID, err)
	}
	if !config.IsActive {
		return nil, fmt.Errorf("vendor %q is inactive", vendorID)
	}
	return NewConfigAdapter(*config), nil
}

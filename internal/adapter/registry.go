// package adapter 的 Registry：将 vendor_id 解析为具体的 VendorAdapter
package adapter

import (
	"fmt"
	"sync"

	"github.com/1919chichi/rc_1919chichi/internal/model"
)

// VendorStore 是 Registry 所需的最小存储接口
// 由 *store.Store 实现，用于从数据库获取厂商配置
type VendorStore interface {
	GetVendor(id string) (*model.VendorConfig, error)
}

// Registry 根据 vendor_id 解析出对应的 VendorAdapter
// 解析顺序：优先使用代码注册的适配器（codeAdapters），其次从数据库配置动态创建 ConfigAdapter
type Registry struct {
	mu           sync.RWMutex              // 读写锁，保证并发安全
	codeAdapters map[string]VendorAdapter // 代码级注册的适配器（优先级高）
	store        VendorStore              // 数据库存储，用于回退到配置驱动
}

// NewRegistry 创建 Registry 实例
func NewRegistry(store VendorStore) *Registry {
	return &Registry{
		codeAdapters: make(map[string]VendorAdapter),
		store:        store,
	}
}

// Register 注册一个代码级适配器，其优先级高于数据库中的配置
// 用于需要自定义逻辑的厂商（如复杂认证、特殊请求构造）
func (r *Registry) Register(adapter VendorAdapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.codeAdapters[adapter.VendorID()] = adapter
}

// Resolve 根据 vendorID 查找并返回对应的 VendorAdapter
// 先查 codeAdapters，没有再从 store 拉取配置并创建 ConfigAdapter
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

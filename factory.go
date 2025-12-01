package migrate

import "sync"

// ImporterFactory is a function that creates an Importer
type ImporterFactoryFunc func(config ImporterConfig) (Importer, error)

// ExporterFactory is a function that creates an Exporter
type ExporterFactoryFunc func(config ExporterConfig) (Exporter, error)

var (
	importerFactories = make(map[VMType]ImporterFactoryFunc)
	exporterFactories = make(map[VMType]ExporterFactoryFunc)
	factoryMu         sync.RWMutex
)

// RegisterImporterFactory registers an importer factory for a VM type
func RegisterImporterFactory(vmType VMType, factory ImporterFactoryFunc) {
	factoryMu.Lock()
	defer factoryMu.Unlock()
	importerFactories[vmType] = factory
}

// RegisterExporterFactory registers an exporter factory for a VM type
func RegisterExporterFactory(vmType VMType, factory ExporterFactoryFunc) {
	factoryMu.Lock()
	defer factoryMu.Unlock()
	exporterFactories[vmType] = factory
}

// Factory functions for creating VM-specific exporters and importers
// These use registered factories or fall back to placeholders

// SubnetEVM exporters/importers
func NewSubnetEVMExporter(config ExporterConfig) (Exporter, error) {
	factoryMu.RLock()
	factory, ok := exporterFactories[VMTypeSubnetEVM]
	factoryMu.RUnlock()
	if ok {
		return factory(config)
	}
	return nil, ErrUnsupportedVMType
}

func NewSubnetEVMImporter(config ImporterConfig) (Importer, error) {
	factoryMu.RLock()
	factory, ok := importerFactories[VMTypeSubnetEVM]
	factoryMu.RUnlock()
	if ok {
		return factory(config)
	}
	return nil, ErrUnsupportedVMType
}

// C-Chain exporters/importers
func NewCChainExporter(config ExporterConfig) (Exporter, error) {
	factoryMu.RLock()
	factory, ok := exporterFactories[VMTypeCChain]
	factoryMu.RUnlock()
	if ok {
		return factory(config)
	}
	return nil, ErrUnsupportedVMType
}

func NewCChainImporter(config ImporterConfig) (Importer, error) {
	// For RPC-based import, use the RPCImporter
	if config.RPCURL != "" {
		return NewRPCImporter(config)
	}
	// TODO: Implement direct database import
	return nil, ErrUnsupportedVMType
}

// Coreth exporters/importers (legacy C-Chain VM)
func NewCorethExporter(config ExporterConfig) (Exporter, error) {
	factoryMu.RLock()
	factory, ok := exporterFactories[VMTypeCoreth]
	factoryMu.RUnlock()
	if ok {
		return factory(config)
	}
	return nil, ErrUnsupportedVMType
}

func NewCorethImporter(config ImporterConfig) (Importer, error) {
	// TODO: Implement using coreth package
	return nil, ErrUnsupportedVMType
}

// Zoo L2 exporters/importers
func NewZooL2Exporter(config ExporterConfig) (Exporter, error) {
	factoryMu.RLock()
	factory, ok := exporterFactories[VMTypeZooL2]
	factoryMu.RUnlock()
	if ok {
		return factory(config)
	}
	return nil, ErrUnsupportedVMType
}

func NewZooL2Importer(config ImporterConfig) (Importer, error) {
	factoryMu.RLock()
	factory, ok := importerFactories[VMTypeZooL2]
	factoryMu.RUnlock()
	if ok {
		return factory(config)
	}
	return nil, ErrUnsupportedVMType
}

// P-Chain exporters/importers
func NewPChainExporter(config ExporterConfig) (Exporter, error) {
	factoryMu.RLock()
	factory, ok := exporterFactories[VMTypePChain]
	factoryMu.RUnlock()
	if ok {
		return factory(config)
	}
	return nil, ErrUnsupportedVMType
}

func NewPChainImporter(config ImporterConfig) (Importer, error) {
	factoryMu.RLock()
	factory, ok := importerFactories[VMTypePChain]
	factoryMu.RUnlock()
	if ok {
		return factory(config)
	}
	return nil, ErrUnsupportedVMType
}

// X-Chain exporters/importers
func NewXChainExporter(config ExporterConfig) (Exporter, error) {
	factoryMu.RLock()
	factory, ok := exporterFactories[VMTypeXChain]
	factoryMu.RUnlock()
	if ok {
		return factory(config)
	}
	return nil, ErrUnsupportedVMType
}

func NewXChainImporter(config ImporterConfig) (Importer, error) {
	factoryMu.RLock()
	factory, ok := importerFactories[VMTypeXChain]
	factoryMu.RUnlock()
	if ok {
		return factory(config)
	}
	return nil, ErrUnsupportedVMType
}

// Q-Chain exporters/importers (EVM-compatible, RPC-based)
func NewQChainExporter(config ExporterConfig) (Exporter, error) {
	factoryMu.RLock()
	factory, ok := exporterFactories[VMTypeQChain]
	factoryMu.RUnlock()
	if ok {
		return factory(config)
	}
	return nil, ErrUnsupportedVMType
}

func NewQChainImporter(config ImporterConfig) (Importer, error) {
	factoryMu.RLock()
	factory, ok := importerFactories[VMTypeQChain]
	factoryMu.RUnlock()
	if ok {
		return factory(config)
	}
	return nil, ErrUnsupportedVMType
}

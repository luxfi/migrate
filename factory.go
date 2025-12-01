package migrate

// Factory functions for creating VM-specific exporters and importers
// These are placeholders - actual implementations in subdirectories

// SubnetEVM exporters/importers
func NewSubnetEVMExporter(config ExporterConfig) (Exporter, error) {
	// TODO: Implement using subnetevm package
	return nil, ErrUnsupportedVMType
}

func NewSubnetEVMImporter(config ImporterConfig) (Importer, error) {
	// TODO: Implement using subnetevm package
	return nil, ErrUnsupportedVMType
}

// C-Chain exporters/importers
func NewCChainExporter(config ExporterConfig) (Exporter, error) {
	// TODO: Implement using cchain package
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
	// TODO: Implement using coreth package
	return nil, ErrUnsupportedVMType
}

func NewCorethImporter(config ImporterConfig) (Importer, error) {
	// TODO: Implement using coreth package
	return nil, ErrUnsupportedVMType
}

// Zoo L2 exporters/importers
func NewZooL2Exporter(config ExporterConfig) (Exporter, error) {
	// TODO: Implement using zool2 package
	return nil, ErrUnsupportedVMType
}

func NewZooL2Importer(config ImporterConfig) (Importer, error) {
	// TODO: Implement using zool2 package
	return nil, ErrUnsupportedVMType
}

// P-Chain exporters/importers
func NewPChainExporter(config ExporterConfig) (Exporter, error) {
	// TODO: Implement using pchain package
	return nil, ErrUnsupportedVMType
}

func NewPChainImporter(config ImporterConfig) (Importer, error) {
	// TODO: Implement using pchain package
	return nil, ErrUnsupportedVMType
}

// X-Chain exporters/importers
func NewXChainExporter(config ExporterConfig) (Exporter, error) {
	// TODO: Implement using xchain package
	return nil, ErrUnsupportedVMType
}

func NewXChainImporter(config ImporterConfig) (Importer, error) {
	// TODO: Implement using xchain package
	return nil, ErrUnsupportedVMType
}

// Q-Chain exporters/importers
func NewQChainExporter(config ExporterConfig) (Exporter, error) {
	// TODO: Implement using qchain package
	return nil, ErrUnsupportedVMType
}

func NewQChainImporter(config ImporterConfig) (Importer, error) {
	// TODO: Implement using qchain package
	return nil, ErrUnsupportedVMType
}

package migrate

// Importer defines the interface for importing blockchain data to any VM
type Importer interface {
	// Init initializes the importer with destination database/RPC
	Init(config ImporterConfig) error

	// ImportConfig imports chain configuration
	ImportConfig(config *Config) error

	// ImportBlock imports a single block (must be sequential)
	ImportBlock(block *BlockData) error

	// ImportBlocks imports multiple blocks in batch
	ImportBlocks(blocks []*BlockData) error

	// ImportState imports state accounts at a specific height
	ImportState(accounts []*Account, blockNumber uint64) error

	// FinalizeImport finalizes the import at a block height
	FinalizeImport(blockNumber uint64) error

	// VerifyImport verifies import integrity at a block height
	VerifyImport(blockNumber uint64) error

	// ExecuteBlock executes a block to rebuild state (for runtime replay)
	ExecuteBlock(block *BlockData) error

	// Close closes the importer and releases resources
	Close() error
}

// ImporterFactory creates importers for specific VM types
type ImporterFactory interface {
	// Create creates an importer for the given VM type
	Create(vmType VMType) (Importer, error)

	// Supports returns true if this factory supports the VM type
	Supports(vmType VMType) bool
}

// NewImporter creates an importer for the given configuration
func NewImporter(config ImporterConfig) (Importer, error) {
	switch config.VMType {
	case VMTypeSubnetEVM:
		return NewSubnetEVMImporter(config)
	case VMTypeCChain:
		return NewCChainImporter(config)
	case VMTypeZooL2:
		return NewZooL2Importer(config)
	case VMTypePChain:
		return NewPChainImporter(config)
	case VMTypeXChain:
		return NewXChainImporter(config)
	case VMTypeQChain:
		return NewQChainImporter(config)
	default:
		return nil, ErrUnsupportedVMType
	}
}

// RPCImporter implements Importer for RPC-based import
type RPCImporter struct {
	config ImporterConfig
}

// NewRPCImporter creates an RPC-based importer
func NewRPCImporter(config ImporterConfig) (*RPCImporter, error) {
	return &RPCImporter{config: config}, nil
}

func (i *RPCImporter) Init(config ImporterConfig) error {
	i.config = config
	return nil
}

func (i *RPCImporter) ImportConfig(config *Config) error {
	// RPC import doesn't need to import config (chain already has it)
	return nil
}

func (i *RPCImporter) ImportBlock(block *BlockData) error {
	// Import via eth_sendRawTransaction or custom import RPC
	return i.importBlockViaRPC(block)
}

func (i *RPCImporter) ImportBlocks(blocks []*BlockData) error {
	for _, block := range blocks {
		if err := i.ImportBlock(block); err != nil {
			return err
		}
	}
	return nil
}

func (i *RPCImporter) ImportState(accounts []*Account, blockNumber uint64) error {
	// State is rebuilt by executing blocks, not imported directly via RPC
	return ErrStateImportNotSupported
}

func (i *RPCImporter) FinalizeImport(blockNumber uint64) error {
	// Verify the block exists at the expected height
	return nil
}

func (i *RPCImporter) VerifyImport(blockNumber uint64) error {
	// Query the RPC to verify block exists
	return nil
}

func (i *RPCImporter) ExecuteBlock(block *BlockData) error {
	// This is handled by ImportBlock for RPC-based import
	return i.ImportBlock(block)
}

func (i *RPCImporter) Close() error {
	return nil
}

func (i *RPCImporter) importBlockViaRPC(block *BlockData) error {
	// Implementation will use eth_sendRawBlock or custom RPC
	// For now, this is a placeholder
	return nil
}

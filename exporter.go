package migrate

import (
	"context"

	"github.com/luxfi/geth/common"
)

// Exporter defines the interface for exporting blockchain data from any VM
type Exporter interface {
	// Init initializes the exporter with source database/RPC
	Init(config ExporterConfig) error

	// GetInfo returns metadata about the source chain
	GetInfo() (*Info, error)

	// ExportBlocks exports blocks in a range (inclusive)
	// Returns channels for streaming blocks and any errors
	ExportBlocks(ctx context.Context, start, end uint64) (<-chan *BlockData, <-chan error)

	// ExportBlocksWithState exports blocks with full state attached to genesis block
	// This is required for importing state that allows balance queries
	ExportBlocksWithState(ctx context.Context, start, end uint64) (<-chan *BlockData, <-chan error)

	// ExportState exports state at a specific block height
	// Returns channels for streaming accounts and any errors
	ExportState(ctx context.Context, blockNumber uint64) (<-chan *Account, <-chan error)

	// ExportAccount exports a specific account's state
	ExportAccount(ctx context.Context, address common.Address, blockNumber uint64) (*Account, error)

	// ExportStateTrie exports raw state trie KV pairs for direct database import.
	// This is the ONLY correct way to migrate state - RPC block import cannot recreate state.
	// Exports the following prefixes:
	//   - A* (account trie nodes): namespace + 'A' + hexPath → trie node data
	//   - O* (storage trie nodes): namespace + 'O' + accountHash + hexPath → trie node data
	//   - c* (code blobs): namespace + 'c' + codeHash → bytecode
	//   - L* (state ID mappings, optional): namespace + 'L' + stateRoot → stateID
	// The key/value pairs are raw bytes - the importer must write them exactly as received.
	ExportStateTrie(ctx context.Context) (<-chan *TrieNode, <-chan error)

	// ExportConfig exports chain configuration (genesis, network ID, etc.)
	ExportConfig() (*Config, error)

	// VerifyExport verifies export integrity at a block height
	VerifyExport(blockNumber uint64) error

	// Close closes the exporter and releases resources
	Close() error
}

// ExporterFactory creates exporters for specific VM types
type ExporterFactory interface {
	// Create creates an exporter for the given VM type
	Create(vmType VMType) (Exporter, error)

	// Supports returns true if this factory supports the VM type
	Supports(vmType VMType) bool
}

// NewExporter creates an exporter for the given configuration
func NewExporter(config ExporterConfig) (Exporter, error) {
	switch config.VMType {
	case VMTypeSubnetEVM:
		return NewSubnetEVMExporter(config)
	case VMTypeCChain:
		return NewCChainExporter(config)
	case VMTypeZooL2:
		return NewZooL2Exporter(config)
	case VMTypePChain:
		return NewPChainExporter(config)
	case VMTypeXChain:
		return NewXChainExporter(config)
	case VMTypeQChain:
		return NewQChainExporter(config)
	default:
		return nil, ErrUnsupportedVMType
	}
}

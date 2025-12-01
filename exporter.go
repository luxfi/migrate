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

	// ExportState exports state at a specific block height
	// Returns channels for streaming accounts and any errors
	ExportState(ctx context.Context, blockNumber uint64) (<-chan *Account, <-chan error)

	// ExportAccount exports a specific account's state
	ExportAccount(ctx context.Context, address common.Address, blockNumber uint64) (*Account, error)

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
	case VMTypeCoreth:
		return NewCorethExporter(config)
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

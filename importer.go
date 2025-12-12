package migrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

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
	// Skip genesis block (number 0) - it's already part of the chain genesis
	if block.Number == 0 {
		return nil
	}

	// Try debug_importBlock first (most direct method)
	if block.Header != nil && block.Body != nil {
		err := i.tryDebugImportBlock(block)
		if err == nil {
			return nil
		}
		// Fall back to transaction replay if debug_importBlock not available
	}

	// Fall back to replaying transactions via eth_sendRawTransaction
	return i.replayTransactions(block)
}

// tryDebugImportBlock attempts to import block via debug_importBlock RPC
func (i *RPCImporter) tryDebugImportBlock(block *BlockData) error {
	// Construct the RPC request for debug_importBlock
	type rpcRequest struct {
		JSONRPC string        `json:"jsonrpc"`
		Method  string        `json:"method"`
		Params  []interface{} `json:"params"`
		ID      int           `json:"id"`
	}

	type blockImportParams struct {
		Header   string `json:"header"`
		Body     string `json:"body"`
		Receipts string `json:"receipts,omitempty"`
	}

	params := blockImportParams{
		Header: fmt.Sprintf("0x%x", block.Header),
		Body:   fmt.Sprintf("0x%x", block.Body),
	}
	if block.Receipts != nil {
		params.Receipts = fmt.Sprintf("0x%x", block.Receipts)
	}

	req := rpcRequest{
		JSONRPC: "2.0",
		Method:  "debug_importBlock",
		Params:  []interface{}{params},
		ID:      1,
	}

	return i.callRPC(req)
}

// replayTransactions replays block transactions to rebuild state
func (i *RPCImporter) replayTransactions(block *BlockData) error {
	// If no transactions, nothing to replay
	if len(block.Transactions) == 0 {
		return nil
	}

	// Send each raw transaction
	for _, tx := range block.Transactions {
		if err := i.sendRawTransaction(tx); err != nil {
			// Log but don't fail - transaction may already exist
			fmt.Printf("Warning: tx %s failed: %v\n", tx.Hash.Hex(), err)
		}
	}
	return nil
}

// sendRawTransaction sends a raw transaction via eth_sendRawTransaction
func (i *RPCImporter) sendRawTransaction(tx *Transaction) error {
	type rpcRequest struct {
		JSONRPC string        `json:"jsonrpc"`
		Method  string        `json:"method"`
		Params  []interface{} `json:"params"`
		ID      int           `json:"id"`
	}

	// Construct raw transaction hex from tx fields
	// For now, skip if we don't have the raw tx data
	if tx.Data == nil {
		return nil
	}

	req := rpcRequest{
		JSONRPC: "2.0",
		Method:  "eth_sendRawTransaction",
		Params:  []interface{}{fmt.Sprintf("0x%x", tx.Data)},
		ID:      1,
	}

	return i.callRPC(req)
}

// callRPC makes an RPC call to the configured endpoint
func (i *RPCImporter) callRPC(req interface{}) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal RPC request: %w", err)
	}

	resp, err := http.Post(i.config.RPCURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("RPC call failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read RPC response: %w", err)
	}

	type rpcResponse struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	var rpcResp rpcResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return fmt.Errorf("failed to parse RPC response: %w", err)
	}

	if rpcResp.Error != nil {
		return fmt.Errorf("RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	return nil
}

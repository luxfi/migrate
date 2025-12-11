// Package cchain provides C-Chain-specific import functionality
package cchain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/luxfi/migrate"
)

// ImportBlockEntry represents a single block for the migrate_importBlocks RPC
type ImportBlockEntry struct {
	Height   uint64 `json:"height"`
	Hash     string `json:"hash"`
	Header   string `json:"header"`
	Body     string `json:"body"`
	Receipts string `json:"receipts"`
}

// ImportBlocksRequest represents the request for importing blocks
type ImportBlocksRequest struct {
	Blocks []ImportBlockEntry `json:"blocks"`
}

// RPCImporter imports blocks to C-Chain via RPC
type RPCImporter struct {
	config migrate.ImporterConfig
	client *http.Client
}

// NewRPCImporter creates a new C-Chain RPC importer
func NewRPCImporter(config migrate.ImporterConfig) (*RPCImporter, error) {
	if config.RPCURL == "" {
		return nil, fmt.Errorf("RPC URL required for C-Chain import")
	}
	return &RPCImporter{
		config: config,
		client: &http.Client{},
	}, nil
}

func (i *RPCImporter) Init(config migrate.ImporterConfig) error {
	i.config = config
	return nil
}

func (i *RPCImporter) ImportConfig(config *migrate.Config) error {
	// C-Chain already has config, no need to import
	return nil
}

func (i *RPCImporter) ImportBlock(block *migrate.BlockData) error {
	return i.ImportBlocks([]*migrate.BlockData{block})
}

func (i *RPCImporter) ImportBlocks(blocks []*migrate.BlockData) error {
	// Convert BlockData to ImportBlockEntry format for migrate_importBlocks RPC
	entries := make([]ImportBlockEntry, len(blocks))
	for idx, block := range blocks {
		entries[idx] = ImportBlockEntry{
			Height:   block.Number,
			Hash:     block.Hash.Hex(),
			Header:   addHexPrefix(block.Header),
			Body:     addHexPrefix(block.Body),
			Receipts: addHexPrefix(block.Receipts),
		}
	}

	req := ImportBlocksRequest{Blocks: entries}
	return i.sendRPCRequest("migrate_importBlocks", []interface{}{req})
}

// addHexPrefix converts byte slice to hex string with 0x prefix
func addHexPrefix(data migrate.HexBytes) string {
	if len(data) == 0 {
		return "0x"
	}
	return "0x" + fmt.Sprintf("%x", []byte(data))
}

func (i *RPCImporter) ImportState(accounts []*migrate.Account, blockNumber uint64) error {
	// State is rebuilt by executing blocks
	return migrate.ErrStateImportNotSupported
}

func (i *RPCImporter) FinalizeImport(blockNumber uint64) error {
	// Verify the block was imported correctly
	return i.VerifyImport(blockNumber)
}

func (i *RPCImporter) VerifyImport(blockNumber uint64) error {
	// Query eth_getBlockByNumber to verify
	result, err := i.callRPC("eth_getBlockByNumber", []interface{}{
		fmt.Sprintf("0x%x", blockNumber),
		false,
	})
	if err != nil {
		return err
	}
	if result == nil {
		return migrate.ErrBlockNotFound
	}
	return nil
}

func (i *RPCImporter) ExecuteBlock(block *migrate.BlockData) error {
	return i.ImportBlock(block)
}

func (i *RPCImporter) Close() error {
	return nil
}

func (i *RPCImporter) sendRPCRequest(method string, params []interface{}) error {
	_, err := i.callRPC(method, params)
	return err
}

func (i *RPCImporter) callRPC(method string, params []interface{}) (interface{}, error) {
	request := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	}

	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}

	resp, err := i.client.Post(i.config.RPCURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, migrate.ErrRPCConnectionFailed
	}
	defer resp.Body.Close()

	var result struct {
		Result interface{}       `json:"result"`
		Error  *json.RawMessage  `json:"error"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	if result.Error != nil {
		return nil, fmt.Errorf("RPC error: %s", string(*result.Error))
	}

	return result.Result, nil
}

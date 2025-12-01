// Package qchain provides Q-Chain (Quantum Chain) export/import functionality
package qchain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/luxfi/migrate"
)

// Importer imports Q-Chain data via RPC
type Importer struct {
	config migrate.ImporterConfig
	client *http.Client
	mu     sync.Mutex
	inited bool
}

// NewImporter creates a new Q-Chain importer
func NewImporter(config migrate.ImporterConfig) (*Importer, error) {
	return &Importer{
		config: config,
		client: &http.Client{},
	}, nil
}

func (i *Importer) Init(config migrate.ImporterConfig) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.inited {
		return fmt.Errorf("importer already initialized")
	}

	i.config = config
	if i.config.RPCURL == "" {
		return fmt.Errorf("RPC URL required for Q-Chain import")
	}

	i.inited = true
	return nil
}

func (i *Importer) ImportConfig(config *migrate.Config) error {
	// Q-Chain already has config, no need to import
	return nil
}

func (i *Importer) ImportBlock(block *migrate.BlockData) error {
	i.mu.Lock()
	inited := i.inited
	i.mu.Unlock()

	if !inited {
		return migrate.ErrNotInitialized
	}

	// Use lux_importBlock RPC to import the block
	_, err := i.callRPC("lux_importBlock", []interface{}{block})
	return err
}

func (i *Importer) ImportBlocks(blocks []*migrate.BlockData) error {
	for _, block := range blocks {
		if err := i.ImportBlock(block); err != nil {
			return err
		}
	}
	return nil
}

func (i *Importer) ImportState(accounts []*migrate.Account, blockNumber uint64) error {
	// State is rebuilt by executing blocks, not imported directly via RPC
	return migrate.ErrStateImportNotSupported
}

func (i *Importer) FinalizeImport(blockNumber uint64) error {
	return i.VerifyImport(blockNumber)
}

func (i *Importer) VerifyImport(blockNumber uint64) error {
	i.mu.Lock()
	inited := i.inited
	i.mu.Unlock()

	if !inited {
		return migrate.ErrNotInitialized
	}

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

func (i *Importer) ExecuteBlock(block *migrate.BlockData) error {
	return i.ImportBlock(block)
}

func (i *Importer) Close() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.inited = false
	return nil
}

func (i *Importer) callRPC(method string, params []interface{}) (interface{}, error) {
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
		Result interface{}      `json:"result"`
		Error  *json.RawMessage `json:"error"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	if result.Error != nil {
		return nil, fmt.Errorf("RPC error: %s", string(*result.Error))
	}

	return result.Result, nil
}

func init() {
	migrate.RegisterImporterFactory(migrate.VMTypeQChain, func(config migrate.ImporterConfig) (migrate.Importer, error) {
		return NewImporter(config)
	})
}

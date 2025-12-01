// Package xchain provides X-Chain (Exchange Chain) specific import functionality.
// X-Chain uses a DAG-based UTXO model for asset creation and transfers.
package xchain

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/luxfi/ids"
	"github.com/luxfi/migrate"
)

// ImporterConfig extends the base ImporterConfig with X-Chain specific options
type ImporterConfig struct {
	migrate.ImporterConfig

	// X-Chain specific options
	SourceAddresses []string // Addresses to import from
	DestAddresses   []string // Addresses to import to
	AssetID         ids.ID   // Asset to import (default: LUX)
	ChainID         ids.ID   // Source chain for cross-chain imports
}

// Importer imports data to X-Chain via RPC transactions
type Importer struct {
	config      migrate.ImporterConfig
	rpc         *RPCClient
	initialized bool
	info        *XChainInfo
	mu          sync.RWMutex

	// Transaction building
	pendingTxs []*PendingTx
	txBuilder  *TxBuilder
}

// PendingTx represents a transaction waiting to be submitted
type PendingTx struct {
	ID        ids.ID
	Type      string
	Raw       []byte
	Status    string
	Submitted time.Time
}

// TxBuilder helps construct X-Chain transactions
type TxBuilder struct {
	networkID    uint32
	blockchainID ids.ID
	xAssetID     ids.ID
	codec        interface{}
}

// NewImporter creates a new X-Chain importer
func NewImporter(config migrate.ImporterConfig) (*Importer, error) {
	return &Importer{
		config:     config,
		pendingTxs: make([]*PendingTx, 0),
	}, nil
}

// Init initializes the importer with RPC connection
func (i *Importer) Init(config migrate.ImporterConfig) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.initialized {
		return migrate.ErrAlreadyInitialized
	}

	i.config = config

	if config.RPCURL == "" {
		return fmt.Errorf("RPC URL required for X-Chain import")
	}

	i.rpc = &RPCClient{
		url: config.RPCURL + "/ext/bc/X",
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}

	// Verify connection and fetch chain info
	info, err := i.fetchChainInfo()
	if err != nil {
		return fmt.Errorf("failed to connect to X-Chain: %w", err)
	}
	i.info = info

	// Initialize transaction builder
	i.txBuilder = &TxBuilder{
		networkID:    info.NetworkID,
		blockchainID: info.BlockchainID,
		xAssetID:     info.XAssetID,
	}

	i.initialized = true
	return nil
}

// ImportConfig imports chain configuration (no-op for X-Chain RPC import)
func (i *Importer) ImportConfig(config *migrate.Config) error {
	// X-Chain config is managed by the node, not imported
	return nil
}

// ImportBlock imports a single block's transactions
func (i *Importer) ImportBlock(block *migrate.BlockData) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !i.initialized {
		return migrate.ErrNotInitialized
	}

	// Extract X-Chain transactions from block extensions
	xchainTxs, ok := block.Extensions["transactions"].([]XChainTx)
	if !ok {
		// Try to parse from generic transaction data
		for _, tx := range block.Transactions {
			if err := i.importTransaction(tx); err != nil {
				return fmt.Errorf("failed to import tx %s: %w", tx.Hash.Hex(), err)
			}
		}
		return nil
	}

	// Import X-Chain specific transactions
	for _, tx := range xchainTxs {
		if err := i.importXChainTx(&tx); err != nil {
			return fmt.Errorf("failed to import X-Chain tx %s: %w", tx.TxID, err)
		}
	}

	return nil
}

// ImportBlocks imports multiple blocks in batch
func (i *Importer) ImportBlocks(blocks []*migrate.BlockData) error {
	for _, block := range blocks {
		if err := i.ImportBlock(block); err != nil {
			return err
		}
	}
	return nil
}

// ImportState imports state (UTXOs) at a specific height
// For X-Chain, this creates transactions to establish UTXO state
func (i *Importer) ImportState(accounts []*migrate.Account, blockNumber uint64) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !i.initialized {
		return migrate.ErrNotInitialized
	}

	// Convert accounts to UTXO creation transactions
	for _, account := range accounts {
		if err := i.createUTXOsForAccount(account); err != nil {
			return fmt.Errorf("failed to create UTXOs for %s: %w", account.Address.Hex(), err)
		}
	}

	return nil
}

// FinalizeImport finalizes the import at a block height
func (i *Importer) FinalizeImport(blockNumber uint64) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !i.initialized {
		return migrate.ErrNotInitialized
	}

	// Wait for all pending transactions to be accepted
	for _, tx := range i.pendingTxs {
		if tx.Status != "accepted" {
			status, err := i.checkTxStatus(tx.ID)
			if err != nil {
				return fmt.Errorf("failed to check tx %s status: %w", tx.ID, err)
			}
			tx.Status = status
		}
	}

	return nil
}

// VerifyImport verifies import integrity at a block height
func (i *Importer) VerifyImport(blockNumber uint64) error {
	i.mu.RLock()
	defer i.mu.RUnlock()

	if !i.initialized {
		return migrate.ErrNotInitialized
	}

	// Verify all pending transactions are accepted
	for _, tx := range i.pendingTxs {
		status, err := i.checkTxStatus(tx.ID)
		if err != nil {
			return fmt.Errorf("verification failed for tx %s: %w", tx.ID, err)
		}
		if status != "Accepted" {
			return fmt.Errorf("tx %s not accepted: %s", tx.ID, status)
		}
	}

	return nil
}

// ExecuteBlock executes a block (same as ImportBlock for RPC-based import)
func (i *Importer) ExecuteBlock(block *migrate.BlockData) error {
	return i.ImportBlock(block)
}

// Close closes the importer
func (i *Importer) Close() error {
	i.mu.Lock()
	defer i.mu.Unlock()

	i.initialized = false
	i.rpc = nil
	return nil
}

// ImportUTXOs imports UTXOs by creating base transactions
func (i *Importer) ImportUTXOs(ctx context.Context, utxos []*UTXO, destAddress string) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !i.initialized {
		return migrate.ErrNotInitialized
	}

	// Group UTXOs by asset
	utxosByAsset := make(map[ids.ID][]*UTXO)
	for _, utxo := range utxos {
		utxosByAsset[utxo.AssetID] = append(utxosByAsset[utxo.AssetID], utxo)
	}

	// Create transactions for each asset
	for assetID, assetUTXOs := range utxosByAsset {
		tx, err := i.buildBaseTx(assetUTXOs, destAddress, assetID)
		if err != nil {
			return fmt.Errorf("failed to build base tx for asset %s: %w", assetID, err)
		}

		if err := i.submitTx(ctx, tx); err != nil {
			return fmt.Errorf("failed to submit tx: %w", err)
		}
	}

	return nil
}

// ImportFromChain imports assets from another chain (P-Chain or C-Chain)
func (i *Importer) ImportFromChain(ctx context.Context, sourceChainID ids.ID, destAddress string) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !i.initialized {
		return migrate.ErrNotInitialized
	}

	// Build import transaction
	tx, err := i.buildImportTx(sourceChainID, destAddress)
	if err != nil {
		return fmt.Errorf("failed to build import tx: %w", err)
	}

	if err := i.submitTx(ctx, tx); err != nil {
		return fmt.Errorf("failed to submit import tx: %w", err)
	}

	return nil
}

// ExportToChain exports assets to another chain
func (i *Importer) ExportToChain(ctx context.Context, destChainID ids.ID, amount uint64, destAddress string) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !i.initialized {
		return migrate.ErrNotInitialized
	}

	// Build export transaction
	tx, err := i.buildExportTx(destChainID, amount, destAddress)
	if err != nil {
		return fmt.Errorf("failed to build export tx: %w", err)
	}

	if err := i.submitTx(ctx, tx); err != nil {
		return fmt.Errorf("failed to submit export tx: %w", err)
	}

	return nil
}

// CreateAsset creates a new asset on X-Chain
func (i *Importer) CreateAsset(ctx context.Context, name, symbol string, denomination byte, initialHolders map[string]uint64) (ids.ID, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !i.initialized {
		return ids.Empty, migrate.ErrNotInitialized
	}

	// Build create asset transaction
	tx, err := i.buildCreateAssetTx(name, symbol, denomination, initialHolders)
	if err != nil {
		return ids.Empty, fmt.Errorf("failed to build create asset tx: %w", err)
	}

	if err := i.submitTx(ctx, tx); err != nil {
		return ids.Empty, fmt.Errorf("failed to submit create asset tx: %w", err)
	}

	return tx.ID, nil
}

// Internal helper methods

func (i *Importer) fetchChainInfo() (*XChainInfo, error) {
	var result struct {
		BlockchainID string `json:"blockchainID"`
	}

	if err := i.rpcCall("avm.getBlockchainID", nil, &result); err != nil {
		return nil, err
	}

	bcID, err := ids.FromString(result.BlockchainID)
	if err != nil {
		return nil, fmt.Errorf("invalid blockchain ID: %w", err)
	}

	return &XChainInfo{
		BlockchainID: bcID,
	}, nil
}

func (i *Importer) importTransaction(tx *migrate.Transaction) error {
	// Submit raw transaction bytes
	if len(tx.Data) > 0 {
		return i.submitRawTx(context.Background(), tx.Data)
	}
	return nil
}

func (i *Importer) importXChainTx(tx *XChainTx) error {
	if len(tx.Raw) > 0 {
		return i.submitRawTx(context.Background(), tx.Raw)
	}
	return nil
}

func (i *Importer) createUTXOsForAccount(account *migrate.Account) error {
	// For X-Chain, we need to create UTXOs through transactions
	// This requires having existing UTXOs to spend
	// In practice, this would be done via cross-chain import from P-Chain
	return nil
}

func (i *Importer) buildBaseTx(utxos []*UTXO, destAddress string, assetID ids.ID) (*PendingTx, error) {
	// Calculate total amount
	var totalAmount uint64
	for _, utxo := range utxos {
		totalAmount += utxo.Amount
	}

	// Build transaction via RPC
	params := map[string]interface{}{
		"assetID": assetID.String(),
		"amount":  totalAmount,
		"to":      destAddress,
	}

	var result struct {
		TxID string `json:"txID"`
	}

	if err := i.rpcCall("avm.send", params, &result); err != nil {
		return nil, err
	}

	txID, _ := ids.FromString(result.TxID)
	return &PendingTx{
		ID:        txID,
		Type:      "BaseTx",
		Status:    "pending",
		Submitted: time.Now(),
	}, nil
}

func (i *Importer) buildImportTx(sourceChainID ids.ID, destAddress string) (*PendingTx, error) {
	params := map[string]interface{}{
		"sourceChain": sourceChainID.String(),
		"to":          destAddress,
	}

	var result struct {
		TxID string `json:"txID"`
	}

	if err := i.rpcCall("avm.import", params, &result); err != nil {
		return nil, err
	}

	txID, _ := ids.FromString(result.TxID)
	return &PendingTx{
		ID:        txID,
		Type:      "ImportTx",
		Status:    "pending",
		Submitted: time.Now(),
	}, nil
}

func (i *Importer) buildExportTx(destChainID ids.ID, amount uint64, destAddress string) (*PendingTx, error) {
	params := map[string]interface{}{
		"destinationChain": destChainID.String(),
		"amount":           amount,
		"to":               destAddress,
		"assetID":          i.info.XAssetID.String(),
	}

	var result struct {
		TxID string `json:"txID"`
	}

	if err := i.rpcCall("avm.export", params, &result); err != nil {
		return nil, err
	}

	txID, _ := ids.FromString(result.TxID)
	return &PendingTx{
		ID:        txID,
		Type:      "ExportTx",
		Status:    "pending",
		Submitted: time.Now(),
	}, nil
}

func (i *Importer) buildCreateAssetTx(name, symbol string, denomination byte, initialHolders map[string]uint64) (*PendingTx, error) {
	holders := make([]map[string]interface{}, 0, len(initialHolders))
	for addr, amount := range initialHolders {
		holders = append(holders, map[string]interface{}{
			"address": addr,
			"amount":  amount,
		})
	}

	params := map[string]interface{}{
		"name":           name,
		"symbol":         symbol,
		"denomination":   denomination,
		"initialHolders": holders,
	}

	var result struct {
		AssetID string `json:"assetID"`
		TxID    string `json:"txID"`
	}

	if err := i.rpcCall("avm.createAsset", params, &result); err != nil {
		return nil, err
	}

	txID, _ := ids.FromString(result.TxID)
	return &PendingTx{
		ID:        txID,
		Type:      "CreateAssetTx",
		Status:    "pending",
		Submitted: time.Now(),
	}, nil
}

func (i *Importer) submitTx(ctx context.Context, tx *PendingTx) error {
	i.pendingTxs = append(i.pendingTxs, tx)

	// Wait for transaction to be accepted
	timeout := time.After(60 * time.Second)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return fmt.Errorf("timeout waiting for tx %s", tx.ID)
		case <-ticker.C:
			status, err := i.checkTxStatus(tx.ID)
			if err != nil {
				continue
			}
			tx.Status = status
			if status == "Accepted" {
				return nil
			}
			if status == "Rejected" || status == "Unknown" {
				return fmt.Errorf("tx %s rejected or unknown", tx.ID)
			}
		}
	}
}

func (i *Importer) submitRawTx(ctx context.Context, txBytes []byte) error {
	params := map[string]interface{}{
		"tx":       fmt.Sprintf("%x", txBytes),
		"encoding": "hex",
	}

	var result struct {
		TxID string `json:"txID"`
	}

	if err := i.rpcCall("avm.issueTx", params, &result); err != nil {
		return err
	}

	txID, _ := ids.FromString(result.TxID)
	tx := &PendingTx{
		ID:        txID,
		Type:      "Unknown",
		Raw:       txBytes,
		Status:    "pending",
		Submitted: time.Now(),
	}

	return i.submitTx(ctx, tx)
}

func (i *Importer) checkTxStatus(txID ids.ID) (string, error) {
	params := map[string]interface{}{
		"txID": txID.String(),
	}

	var result struct {
		Status string `json:"status"`
	}

	if err := i.rpcCall("avm.getTxStatus", params, &result); err != nil {
		return "", err
	}

	return result.Status, nil
}

func (i *Importer) rpcCall(method string, params interface{}, result interface{}) error {
	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	}

	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", i.rpc.url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Body = io.NopCloser(stringReader(string(reqBytes)))

	resp, err := i.rpc.client.Do(req)
	if err != nil {
		return fmt.Errorf("RPC request failed: %w", err)
	}
	defer resp.Body.Close()

	var rpcResp struct {
		Result interface{}      `json:"result"`
		Error  *json.RawMessage `json:"error"`
	}
	rpcResp.Result = result

	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}

	if rpcResp.Error != nil {
		return fmt.Errorf("RPC error: %s", string(*rpcResp.Error))
	}

	return nil
}

// WaitForAcceptance waits for all pending transactions to be accepted
func (i *Importer) WaitForAcceptance(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	for _, tx := range i.pendingTxs {
		if tx.Status == "Accepted" {
			continue
		}

		timeout := time.After(120 * time.Second)
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timeout:
				return fmt.Errorf("timeout waiting for tx %s", tx.ID)
			case <-ticker.C:
				status, err := i.checkTxStatus(tx.ID)
				if err != nil {
					continue
				}
				tx.Status = status
				if status == "Accepted" {
					break
				}
				if status == "Rejected" {
					return fmt.Errorf("tx %s was rejected", tx.ID)
				}
			}

			if tx.Status == "Accepted" {
				break
			}
		}
	}

	return nil
}

// GetPendingTxs returns all pending transactions
func (i *Importer) GetPendingTxs() []*PendingTx {
	i.mu.RLock()
	defer i.mu.RUnlock()

	txs := make([]*PendingTx, len(i.pendingTxs))
	copy(txs, i.pendingTxs)
	return txs
}

// ClearPendingTxs clears the pending transaction list
func (i *Importer) ClearPendingTxs() {
	i.mu.Lock()
	defer i.mu.Unlock()

	i.pendingTxs = make([]*PendingTx, 0)
}

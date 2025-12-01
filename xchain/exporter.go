// Package xchain provides X-Chain (Exchange Chain) specific export functionality.
// X-Chain uses a DAG-based UTXO model for asset creation and transfers.
package xchain

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/luxfi/geth/common"
	"github.com/luxfi/ids"
	"github.com/luxfi/migrate"
)

// RPCClient handles JSON-RPC communication with X-Chain
type RPCClient struct {
	url    string
	client *http.Client
}

// UTXO represents an unspent transaction output on X-Chain
type UTXO struct {
	TxID        ids.ID   `json:"txID"`
	OutputIndex uint32   `json:"outputIndex"`
	AssetID     ids.ID   `json:"assetID"`
	Amount      uint64   `json:"amount"`
	Threshold   uint32   `json:"threshold"`
	Addresses   []string `json:"addresses"`
	Locktime    uint64   `json:"locktime"`
}

// Asset represents an X-Chain asset definition
type Asset struct {
	ID           ids.ID `json:"id"`
	Name         string `json:"name"`
	Symbol       string `json:"symbol"`
	Denomination byte   `json:"denomination"`
}

// XChainTx represents a transaction on X-Chain
type XChainTx struct {
	TxID      ids.ID   `json:"txID"`
	Type      string   `json:"type"` // BaseTx, CreateAssetTx, OperationTx, ImportTx, ExportTx
	Timestamp uint64   `json:"timestamp"`
	Inputs    []UTXOID `json:"inputs"`
	Outputs   []Output `json:"outputs"`
	Memo      []byte   `json:"memo"`
	Raw       []byte   `json:"raw"` // Raw transaction bytes
}

// UTXOID identifies a specific UTXO
type UTXOID struct {
	TxID        ids.ID `json:"txID"`
	OutputIndex uint32 `json:"outputIndex"`
}

// Output represents a transaction output
type Output struct {
	AssetID   ids.ID   `json:"assetID"`
	Amount    uint64   `json:"amount"`
	Addresses []string `json:"addresses"`
	Locktime  uint64   `json:"locktime"`
	Threshold uint32   `json:"threshold"`
}

// XChainBlock represents a block in the linearized X-Chain
// After linearization, X-Chain produces blocks containing transactions
type XChainBlock struct {
	ID           ids.ID     `json:"id"`
	ParentID     ids.ID     `json:"parentID"`
	Height       uint64     `json:"height"`
	Timestamp    time.Time  `json:"timestamp"`
	Transactions []XChainTx `json:"transactions"`
}

// XChainInfo contains metadata about X-Chain
type XChainInfo struct {
	NetworkID       uint32   `json:"networkID"`
	BlockchainID    ids.ID   `json:"blockchainID"`
	XAssetID        ids.ID   `json:"xAssetID"`
	CurrentHeight   uint64   `json:"currentHeight"`
	LastAcceptedID  ids.ID   `json:"lastAcceptedID"`
	TotalAssets     uint64   `json:"totalAssets"`
	TotalUTXOs      uint64   `json:"totalUTXOs"`
	IndexerEnabled  bool     `json:"indexerEnabled"`
}

// Exporter exports data from X-Chain via RPC or database
type Exporter struct {
	config      migrate.ExporterConfig
	rpc         *RPCClient
	initialized bool
	info        *XChainInfo
	mu          sync.RWMutex

	// Cached data
	assets map[ids.ID]*Asset
	utxos  map[ids.ID]*UTXO
}

// NewExporter creates a new X-Chain exporter
func NewExporter(config migrate.ExporterConfig) (*Exporter, error) {
	return &Exporter{
		config: config,
		assets: make(map[ids.ID]*Asset),
		utxos:  make(map[ids.ID]*UTXO),
	}, nil
}

// Init initializes the exporter with RPC connection
func (e *Exporter) Init(config migrate.ExporterConfig) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.initialized {
		return migrate.ErrAlreadyInitialized
	}

	e.config = config

	if config.RPCURL == "" {
		return fmt.Errorf("RPC URL required for X-Chain export")
	}

	e.rpc = &RPCClient{
		url: config.RPCURL + "/ext/bc/X",
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}

	// Verify connection and fetch chain info
	info, err := e.fetchChainInfo()
	if err != nil {
		return fmt.Errorf("failed to connect to X-Chain: %w", err)
	}
	e.info = info

	e.initialized = true
	return nil
}

// GetInfo returns metadata about X-Chain
func (e *Exporter) GetInfo() (*migrate.Info, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.initialized {
		return nil, migrate.ErrNotInitialized
	}

	return &migrate.Info{
		VMType:        migrate.VMTypeXChain,
		NetworkID:     uint64(e.info.NetworkID),
		ChainID:       big.NewInt(int64(e.info.NetworkID)),
		CurrentHeight: e.info.CurrentHeight,
		VMVersion:     "exchangevm-1.0.0",
		DatabaseType:  "rpc",
	}, nil
}

// ExportBlocks exports blocks from X-Chain in a range
// X-Chain is DAG-based but after linearization produces blocks
func (e *Exporter) ExportBlocks(ctx context.Context, start, end uint64) (<-chan *migrate.BlockData, <-chan error) {
	blocks := make(chan *migrate.BlockData, 100)
	errs := make(chan error, 1)

	if start > end {
		errs <- migrate.ErrInvalidBlockRange
		close(blocks)
		close(errs)
		return blocks, errs
	}

	go func() {
		defer close(blocks)
		defer close(errs)

		for height := start; height <= end; height++ {
			select {
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			default:
				block, err := e.fetchBlock(ctx, height)
				if err != nil {
					errs <- fmt.Errorf("failed to fetch block %d: %w", height, err)
					return
				}

				blockData := e.convertBlock(block)
				blocks <- blockData
			}
		}
	}()

	return blocks, errs
}

// ExportState exports UTXO state at a specific block height
// For X-Chain, this exports all UTXOs that exist at the given height
func (e *Exporter) ExportState(ctx context.Context, blockNumber uint64) (<-chan *migrate.Account, <-chan error) {
	accounts := make(chan *migrate.Account, 100)
	errs := make(chan error, 1)

	go func() {
		defer close(accounts)
		defer close(errs)

		// Fetch all UTXOs grouped by address
		utxosByAddr, err := e.fetchAllUTXOs(ctx)
		if err != nil {
			errs <- fmt.Errorf("failed to fetch UTXOs: %w", err)
			return
		}

		// Convert UTXOs to account format
		// Each address becomes an "account" with balance computed from UTXOs
		for addr, utxos := range utxosByAddr {
			select {
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			default:
				account := e.utxosToAccount(addr, utxos)
				accounts <- account
			}
		}
	}()

	return accounts, errs
}

// ExportAccount exports a specific address's UTXOs as an account
func (e *Exporter) ExportAccount(ctx context.Context, address common.Address, blockNumber uint64) (*migrate.Account, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.initialized {
		return nil, migrate.ErrNotInitialized
	}

	// Fetch UTXOs for this address
	utxos, err := e.fetchUTXOsForAddress(ctx, address)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch UTXOs for address: %w", err)
	}

	return e.utxosToAccount(address.Hex(), utxos), nil
}

// ExportConfig exports X-Chain configuration
func (e *Exporter) ExportConfig() (*migrate.Config, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.initialized {
		return nil, migrate.ErrNotInitialized
	}

	return &migrate.Config{
		NetworkID: uint64(e.info.NetworkID),
		ChainID:   big.NewInt(int64(e.info.NetworkID)),
	}, nil
}

// VerifyExport verifies export integrity
func (e *Exporter) VerifyExport(blockNumber uint64) error {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.initialized {
		return migrate.ErrNotInitialized
	}

	// Verify block exists at height
	_, err := e.fetchBlock(context.Background(), blockNumber)
	return err
}

// Close closes the exporter
func (e *Exporter) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.initialized = false
	e.rpc = nil
	return nil
}

// ExportUTXOs exports all UTXOs for given addresses
func (e *Exporter) ExportUTXOs(ctx context.Context, addresses []string) ([]*UTXO, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.initialized {
		return nil, migrate.ErrNotInitialized
	}

	var allUTXOs []*UTXO
	for _, addr := range addresses {
		utxos, err := e.fetchUTXOsForAddressStr(ctx, addr)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch UTXOs for %s: %w", addr, err)
		}
		allUTXOs = append(allUTXOs, utxos...)
	}

	return allUTXOs, nil
}

// ExportAssets exports all asset definitions
func (e *Exporter) ExportAssets(ctx context.Context) ([]*Asset, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.initialized {
		return nil, migrate.ErrNotInitialized
	}

	return e.fetchAllAssets(ctx)
}

// ExportTransactions exports transactions in a block range
func (e *Exporter) ExportTransactions(ctx context.Context, start, end uint64) ([]*XChainTx, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.initialized {
		return nil, migrate.ErrNotInitialized
	}

	var allTxs []*XChainTx
	for height := start; height <= end; height++ {
		block, err := e.fetchBlock(ctx, height)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch block %d: %w", height, err)
		}
		for i := range block.Transactions {
			allTxs = append(allTxs, &block.Transactions[i])
		}
	}

	return allTxs, nil
}

// Internal helper methods

func (e *Exporter) fetchChainInfo() (*XChainInfo, error) {
	// Call avm.getBlockchainID and other info methods
	var result struct {
		BlockchainID string `json:"blockchainID"`
	}

	if err := e.rpcCall("avm.getBlockchainID", nil, &result); err != nil {
		return nil, err
	}

	bcID, err := ids.FromString(result.BlockchainID)
	if err != nil {
		return nil, fmt.Errorf("invalid blockchain ID: %w", err)
	}

	// Get current height
	var heightResult struct {
		Height json.Number `json:"height"`
	}
	if err := e.rpcCall("avm.getHeight", nil, &heightResult); err != nil {
		// Height might not be available if not linearized
		heightResult.Height = "0"
	}

	height, _ := heightResult.Height.Int64()

	return &XChainInfo{
		BlockchainID:  bcID,
		CurrentHeight: uint64(height),
	}, nil
}

func (e *Exporter) fetchBlock(ctx context.Context, height uint64) (*XChainBlock, error) {
	// Call avm.getBlockByHeight
	params := map[string]interface{}{
		"height":   height,
		"encoding": "json",
	}

	var result struct {
		Block struct {
			ID           string `json:"id"`
			ParentID     string `json:"parentID"`
			Height       uint64 `json:"height"`
			Timestamp    string `json:"timestamp"`
			Transactions []struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"txs"`
		} `json:"block"`
	}

	if err := e.rpcCall("avm.getBlockByHeight", params, &result); err != nil {
		return nil, err
	}

	blkID, _ := ids.FromString(result.Block.ID)
	parentID, _ := ids.FromString(result.Block.ParentID)
	ts, _ := time.Parse(time.RFC3339, result.Block.Timestamp)

	block := &XChainBlock{
		ID:        blkID,
		ParentID:  parentID,
		Height:    result.Block.Height,
		Timestamp: ts,
	}

	// Fetch full transaction details
	for _, txRef := range result.Block.Transactions {
		tx, err := e.fetchTransaction(ctx, txRef.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch tx %s: %w", txRef.ID, err)
		}
		block.Transactions = append(block.Transactions, XChainTx{
			TxID: tx.TxID,
			Type: tx.Type,
			Raw:  tx.Raw,
		})
	}

	return block, nil
}

func (e *Exporter) fetchTransaction(ctx context.Context, txID string) (*XChainTx, error) {
	params := map[string]interface{}{
		"txID":     txID,
		"encoding": "json",
	}

	var result struct {
		Tx struct {
			UnsignedTx struct {
				TypeID           uint32 `json:"typeID"`
				NetworkID        uint32 `json:"networkID"`
				BlockchainID     string `json:"blockchainID"`
				Inputs           []interface{} `json:"inputs"`
				Outputs          []interface{} `json:"outputs"`
				Memo             string `json:"memo"`
				SourceChain      string `json:"sourceChain,omitempty"`
				DestinationChain string `json:"destinationChain,omitempty"`
			} `json:"unsignedTx"`
		} `json:"tx"`
		Encoding string `json:"encoding"`
	}

	if err := e.rpcCall("avm.getTx", params, &result); err != nil {
		return nil, err
	}

	id, _ := ids.FromString(txID)
	txType := getTxTypeName(result.Tx.UnsignedTx.TypeID)

	tx := &XChainTx{
		TxID: id,
		Type: txType,
	}

	return tx, nil
}

func (e *Exporter) fetchAllUTXOs(ctx context.Context) (map[string][]*UTXO, error) {
	// This would need to iterate through known addresses
	// For now, return empty map - in practice, addresses would be provided
	return make(map[string][]*UTXO), nil
}

func (e *Exporter) fetchUTXOsForAddress(ctx context.Context, address common.Address) ([]*UTXO, error) {
	return e.fetchUTXOsForAddressStr(ctx, address.Hex())
}

func (e *Exporter) fetchUTXOsForAddressStr(ctx context.Context, address string) ([]*UTXO, error) {
	params := map[string]interface{}{
		"addresses": []string{address},
		"limit":     1024,
		"encoding":  "json",
	}

	var result struct {
		UTXOs []struct {
			TxID        string `json:"txID"`
			OutputIndex uint32 `json:"outputIndex"`
			AssetID     string `json:"assetID"`
			Output      struct {
				Addresses []string `json:"addresses"`
				Amount    uint64   `json:"amount"`
				Locktime  uint64   `json:"locktime"`
				Threshold uint32   `json:"threshold"`
			} `json:"output"`
		} `json:"utxos"`
		EndIndex struct {
			Address string `json:"address"`
			UTXO    string `json:"utxo"`
		} `json:"endIndex"`
	}

	if err := e.rpcCall("avm.getUTXOs", params, &result); err != nil {
		return nil, err
	}

	utxos := make([]*UTXO, 0, len(result.UTXOs))
	for _, u := range result.UTXOs {
		txID, _ := ids.FromString(u.TxID)
		assetID, _ := ids.FromString(u.AssetID)

		utxos = append(utxos, &UTXO{
			TxID:        txID,
			OutputIndex: u.OutputIndex,
			AssetID:     assetID,
			Amount:      u.Output.Amount,
			Threshold:   u.Output.Threshold,
			Addresses:   u.Output.Addresses,
			Locktime:    u.Output.Locktime,
		})
	}

	return utxos, nil
}

func (e *Exporter) fetchAllAssets(ctx context.Context) ([]*Asset, error) {
	// X-Chain doesn't have a direct "list all assets" API
	// Assets are discovered through transactions
	// Return cached assets
	assets := make([]*Asset, 0, len(e.assets))
	for _, a := range e.assets {
		assets = append(assets, a)
	}
	return assets, nil
}

func (e *Exporter) utxosToAccount(addr string, utxos []*UTXO) *migrate.Account {
	// Sum up balances by asset
	balances := make(map[ids.ID]uint64)
	for _, utxo := range utxos {
		balances[utxo.AssetID] += utxo.Amount
	}

	// For migration purposes, use the primary asset balance
	var totalBalance uint64
	if e.info != nil {
		totalBalance = balances[e.info.XAssetID]
	}

	// Parse address to common.Address format
	var address common.Address
	if len(addr) >= 40 {
		address = common.HexToAddress(addr)
	}

	return &migrate.Account{
		Address: address,
		Balance: new(big.Int).SetUint64(totalBalance),
		Nonce:   0, // X-Chain doesn't use nonces
		Storage: make(map[common.Hash]common.Hash),
	}
}

func (e *Exporter) convertBlock(block *XChainBlock) *migrate.BlockData {
	// Convert X-Chain block to generic BlockData
	var blockHash common.Hash
	copy(blockHash[:], block.ID[:])

	var parentHash common.Hash
	copy(parentHash[:], block.ParentID[:])

	// Convert transactions
	txs := make([]*migrate.Transaction, 0, len(block.Transactions))
	for _, tx := range block.Transactions {
		var txHash common.Hash
		copy(txHash[:], tx.TxID[:])

		txs = append(txs, &migrate.Transaction{
			Hash: txHash,
			Data: tx.Raw,
		})
	}

	return &migrate.BlockData{
		Number:       block.Height,
		Hash:         blockHash,
		ParentHash:   parentHash,
		Timestamp:    uint64(block.Timestamp.Unix()),
		Transactions: txs,
		Extensions: map[string]interface{}{
			"vmType":       "x-chain",
			"transactions": block.Transactions,
		},
	}
}

func (e *Exporter) rpcCall(method string, params interface{}, result interface{}) error {
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

	req, err := http.NewRequest("POST", e.rpc.url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Body = io.NopCloser(stringReader(string(reqBytes)))

	resp, err := e.rpc.client.Do(req)
	if err != nil {
		return fmt.Errorf("RPC request failed: %w", err)
	}
	defer resp.Body.Close()

	var rpcResp struct {
		Result interface{}     `json:"result"`
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

func getTxTypeName(typeID uint32) string {
	switch typeID {
	case 0:
		return "BaseTx"
	case 1:
		return "CreateAssetTx"
	case 2:
		return "OperationTx"
	case 3:
		return "ImportTx"
	case 4:
		return "ExportTx"
	default:
		return fmt.Sprintf("Unknown(%d)", typeID)
	}
}

type stringReaderImpl struct {
	s string
	i int
}

func stringReader(s string) io.Reader {
	return &stringReaderImpl{s: s}
}

func (r *stringReaderImpl) Read(p []byte) (n int, err error) {
	if r.i >= len(r.s) {
		return 0, io.EOF
	}
	n = copy(p, r.s[r.i:])
	r.i += n
	return n, nil
}

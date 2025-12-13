// Package cchain provides C-Chain-specific export/import functionality.
// C-Chain is the main EVM chain, exports via RPC (eth_getBlockByNumber, debug_accountRange).
package cchain

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"

	"github.com/luxfi/geth/common"
	"github.com/luxfi/geth/core/types"
	"github.com/luxfi/geth/crypto"
	"github.com/luxfi/migrate"
)

func init() {
	// Register the C-Chain exporter factory
	migrate.RegisterExporterFactory(migrate.VMTypeCChain, func(config migrate.ExporterConfig) (migrate.Exporter, error) {
		return NewExporter(config)
	})
}

const (
	// Buffer sizes for streaming channels
	blockBufferSize   = 100
	accountBufferSize = 1000
	errorBufferSize   = 1

	// RPC batch size for account iteration
	accountBatchSize = 256
)

// rpcRequest represents a JSON-RPC request
type rpcRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      int           `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

// rpcResponse represents a JSON-RPC response
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

// rpcError represents a JSON-RPC error
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// rpcBlock represents a block from eth_getBlockByNumber
type rpcBlock struct {
	Number           string        `json:"number"`
	Hash             string        `json:"hash"`
	ParentHash       string        `json:"parentHash"`
	Nonce            string        `json:"nonce"`
	MixHash          string        `json:"mixHash"`
	Sha3Uncles       string        `json:"sha3Uncles"`
	LogsBloom        string        `json:"logsBloom"`
	TransactionsRoot string        `json:"transactionsRoot"`
	StateRoot        string        `json:"stateRoot"`
	ReceiptsRoot     string        `json:"receiptsRoot"`
	Miner            string        `json:"miner"`
	Difficulty       string        `json:"difficulty"`
	TotalDifficulty  string        `json:"totalDifficulty"`
	ExtraData        string        `json:"extraData"`
	Size             string        `json:"size"`
	GasLimit         string        `json:"gasLimit"`
	GasUsed          string        `json:"gasUsed"`
	Timestamp        string        `json:"timestamp"`
	BaseFeePerGas    string        `json:"baseFeePerGas"`
	Transactions     []interface{} `json:"transactions"`
	Uncles           []string      `json:"uncles"`
}

// rpcTransaction represents a transaction from eth_getBlockByNumber (full tx mode)
type rpcTransaction struct {
	Hash             string  `json:"hash"`
	Nonce            string  `json:"nonce"`
	BlockHash        string  `json:"blockHash"`
	BlockNumber      string  `json:"blockNumber"`
	TransactionIndex string  `json:"transactionIndex"`
	From             string  `json:"from"`
	To               *string `json:"to"`
	Value            string  `json:"value"`
	Gas              string  `json:"gas"`
	GasPrice         string  `json:"gasPrice"`
	Input            string  `json:"input"`
	V                string  `json:"v"`
	R                string  `json:"r"`
	S                string  `json:"s"`
	// EIP-1559 fields
	MaxFeePerGas         string              `json:"maxFeePerGas"`
	MaxPriorityFeePerGas string              `json:"maxPriorityFeePerGas"`
	Type                 string              `json:"type"`
	AccessList           []rpcAccessListItem `json:"accessList"`
}

// rpcAccessListItem represents an access list item
type rpcAccessListItem struct {
	Address     string   `json:"address"`
	StorageKeys []string `json:"storageKeys"`
}

// rpcAccountRange represents the result from debug_accountRange
type rpcAccountRange struct {
	Accounts map[string]*rpcDumpAccount `json:"accounts"`
	Next     string                     `json:"next"`
}

// rpcDumpAccount represents an account from debug_accountRange
type rpcDumpAccount struct {
	Balance  string            `json:"balance"`
	Nonce    uint64            `json:"nonce"`
	Root     string            `json:"root"`
	CodeHash string            `json:"codeHash"`
	Code     string            `json:"code"`
	Storage  map[string]string `json:"storage"`
	Address  *string           `json:"address"`
	Key      string            `json:"key"`
}

// rpcChainConfig represents chain configuration from eth_chainId
type rpcChainConfig struct {
	ChainID string `json:"chainId"`
}

// Exporter exports blocks and state from C-Chain via RPC.
type Exporter struct {
	config migrate.ExporterConfig

	// HTTP client for RPC
	client *http.Client

	// Cached chain info
	chainID     *big.Int
	networkID   uint64
	genesisHash common.Hash
	headBlock   uint64

	// State
	initialized bool
	mu          sync.RWMutex
	requestID   int
}

// NewExporter creates a new C-Chain RPC exporter.
func NewExporter(config migrate.ExporterConfig) (*Exporter, error) {
	if config.RPCURL == "" {
		return nil, fmt.Errorf("RPC URL required for C-Chain export")
	}
	return &Exporter{
		config: config,
		client: &http.Client{},
	}, nil
}

// Init initializes the exporter with source RPC endpoint.
func (e *Exporter) Init(config migrate.ExporterConfig) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.initialized {
		return migrate.ErrAlreadyInitialized
	}

	e.config = config

	if config.RPCURL == "" {
		return fmt.Errorf("RPC URL required for C-Chain export")
	}

	// Test RPC connection and load chain info
	if err := e.loadChainInfo(); err != nil {
		return fmt.Errorf("failed to load chain info: %w", err)
	}

	e.initialized = true
	return nil
}

// loadChainInfo loads genesis hash, chain ID, and head block info from RPC.
func (e *Exporter) loadChainInfo() error {
	// Get chain ID
	chainIDHex, err := e.callRPCString("eth_chainId", nil)
	if err != nil {
		return fmt.Errorf("eth_chainId failed: %w", err)
	}
	e.chainID = hexToBigInt(chainIDHex)

	// Get network ID (net_version returns decimal string, not hex)
	netIDStr, err := e.callRPCString("net_version", nil)
	if err != nil {
		// Fallback to chain ID if net_version not available
		e.networkID = e.chainID.Uint64()
	} else {
		// net_version can return decimal or hex
		var netID *big.Int
		if strings.HasPrefix(netIDStr, "0x") {
			netID = hexToBigInt(netIDStr)
		} else {
			netID = new(big.Int)
			netID.SetString(netIDStr, 10)
		}
		if netID != nil {
			e.networkID = netID.Uint64()
		}
	}

	// Get genesis block hash
	genesisBlock, err := e.getBlockByNumber(0, false)
	if err != nil {
		return fmt.Errorf("failed to get genesis block: %w", err)
	}
	e.genesisHash = common.HexToHash(genesisBlock.Hash)

	// Get head block number
	blockNumHex, err := e.callRPCString("eth_blockNumber", nil)
	if err != nil {
		return fmt.Errorf("eth_blockNumber failed: %w", err)
	}
	e.headBlock = hexToUint64(blockNumHex)

	return nil
}

// GetInfo returns metadata about the source chain.
func (e *Exporter) GetInfo() (*migrate.Info, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.initialized {
		return nil, migrate.ErrNotInitialized
	}

	// Get head block for state root
	headBlock, err := e.getBlockByNumber(e.headBlock, false)
	if err != nil {
		return nil, fmt.Errorf("failed to get head block: %w", err)
	}

	return &migrate.Info{
		VMType:          migrate.VMTypeCChain,
		NetworkID:       e.networkID,
		ChainID:         e.chainID,
		GenesisHash:     e.genesisHash,
		CurrentHeight:   e.headBlock,
		TotalDifficulty: hexToBigInt(headBlock.TotalDifficulty),
		StateRoot:       common.HexToHash(headBlock.StateRoot),
		VMVersion:       "c-chain",
		DatabaseType:    "rpc",
		IsPruned:        false, // Unknown via RPC, assume full node
		ArchiveMode:     true,  // Assume archive for export
		HasWarpMessages: false,
		HasProposerVM:   true, // C-Chain uses ProposerVM
	}, nil
}

// ExportBlocks exports blocks in a range (inclusive).
// Returns channels for streaming blocks and any errors.
func (e *Exporter) ExportBlocks(ctx context.Context, start, end uint64) (<-chan *migrate.BlockData, <-chan error) {
	blocks := make(chan *migrate.BlockData, blockBufferSize)
	errs := make(chan error, errorBufferSize)

	// Validate inputs
	if start > end {
		errs <- migrate.ErrInvalidBlockRange
		close(blocks)
		close(errs)
		return blocks, errs
	}

	e.mu.RLock()
	if !e.initialized {
		e.mu.RUnlock()
		errs <- migrate.ErrNotInitialized
		close(blocks)
		close(errs)
		return blocks, errs
	}
	e.mu.RUnlock()

	go func() {
		defer close(blocks)
		defer close(errs)

		for height := start; height <= end; height++ {
			select {
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			default:
			}

			blockData, err := e.exportBlock(height)
			if err != nil {
				errs <- fmt.Errorf("block %d: %w", height, err)
				return
			}

			select {
			case blocks <- blockData:
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			}
		}
	}()

	return blocks, errs
}

// ExportBlocksWithState exports blocks with full state attached to genesis block.
// For C-Chain RPC exporter, this is the same as ExportBlocks since state is
// reconstructed via transaction execution during import.
func (e *Exporter) ExportBlocksWithState(ctx context.Context, start, end uint64) (<-chan *migrate.BlockData, <-chan error) {
	return e.ExportBlocks(ctx, start, end)
}

// exportBlock exports a single block at the given height via RPC.
func (e *Exporter) exportBlock(height uint64) (*migrate.BlockData, error) {
	// Get block with full transactions
	rpcBlk, err := e.getBlockByNumber(height, true)
	if err != nil {
		return nil, err
	}

	if rpcBlk == nil {
		return nil, migrate.ErrBlockNotFound
	}

	// Parse block nonce
	var blockNonce types.BlockNonce
	nonceBytes := hexToBytes(rpcBlk.Nonce)
	if len(nonceBytes) == 8 {
		copy(blockNonce[:], nonceBytes)
	}

	blockData := &migrate.BlockData{
		Number:           hexToUint64(rpcBlk.Number),
		Hash:             common.HexToHash(rpcBlk.Hash),
		ParentHash:       common.HexToHash(rpcBlk.ParentHash),
		Timestamp:        hexToUint64(rpcBlk.Timestamp),
		StateRoot:        common.HexToHash(rpcBlk.StateRoot),
		ReceiptsRoot:     common.HexToHash(rpcBlk.ReceiptsRoot),
		TransactionsRoot: common.HexToHash(rpcBlk.TransactionsRoot),
		GasLimit:         hexToUint64(rpcBlk.GasLimit),
		GasUsed:          hexToUint64(rpcBlk.GasUsed),
		Difficulty:       hexToBigInt(rpcBlk.Difficulty),
		TotalDifficulty:  hexToBigInt(rpcBlk.TotalDifficulty),
		Coinbase:         common.HexToAddress(rpcBlk.Miner),
		Nonce:            blockNonce,
		MixHash:          common.HexToHash(rpcBlk.MixHash),
		ExtraData:        hexToBytes(rpcBlk.ExtraData),
		BaseFee:          hexToBigInt(rpcBlk.BaseFeePerGas),
		Extensions:       make(map[string]interface{}),
	}

	// Convert transactions
	txs := make([]*migrate.Transaction, 0, len(rpcBlk.Transactions))
	for _, txRaw := range rpcBlk.Transactions {
		// Full tx mode returns objects
		txMap, ok := txRaw.(map[string]interface{})
		if !ok {
			continue
		}

		// Marshal back to JSON and unmarshal to rpcTransaction
		txJSON, err := json.Marshal(txMap)
		if err != nil {
			continue
		}

		var rpcTx rpcTransaction
		if err := json.Unmarshal(txJSON, &rpcTx); err != nil {
			continue
		}

		tx := convertRPCTransaction(&rpcTx)
		txs = append(txs, tx)
	}
	blockData.Transactions = txs

	// Export receipts if configured
	if e.config.ExportReceipts {
		receipts, err := e.getBlockReceipts(height)
		if err == nil {
			// Attach receipts to transactions
			for i, receipt := range receipts {
				if i < len(blockData.Transactions) {
					blockData.Transactions[i].Receipt = receipt
				}
			}
		}
	}

	// Add C-Chain specific extensions
	if rpcBlk.BaseFeePerGas != "" {
		blockData.Extensions["hasEIP1559"] = true
	}

	// Export state changes if configured
	if e.config.ExportState {
		stateChanges, err := e.exportBlockStateChanges(height, txs)
		if err == nil {
			blockData.StateChanges = stateChanges
		}
	}

	return blockData, nil
}

// exportBlockStateChanges exports state changes affected by transactions in a block.
// This includes all accounts touched by transactions (from, to, contract creations).
func (e *Exporter) exportBlockStateChanges(blockNumber uint64, txs []*migrate.Transaction) (map[common.Address]*migrate.Account, error) {
	stateChanges := make(map[common.Address]*migrate.Account)

	// Collect all addresses affected by transactions
	affectedAddresses := make(map[common.Address]bool)

	for _, tx := range txs {
		// Add sender
		affectedAddresses[tx.From] = true

		// Add recipient
		if tx.To != nil {
			affectedAddresses[*tx.To] = true
		}

		// Add contract creation address
		if tx.Receipt != nil && tx.Receipt.ContractAddress != nil {
			affectedAddresses[*tx.Receipt.ContractAddress] = true
		}

		// Add addresses from logs (contracts that emitted events)
		if tx.Receipt != nil {
			for _, log := range tx.Receipt.Logs {
				affectedAddresses[log.Address] = true
			}
		}
	}

	// Export state for each affected address
	for addr := range affectedAddresses {
		account, err := e.ExportAccount(nil, addr, blockNumber)
		if err != nil {
			// Skip accounts that can't be fetched (shouldn't happen)
			continue
		}
		stateChanges[addr] = account
	}

	return stateChanges, nil
}

// convertRPCTransaction converts an RPC transaction to migrate.Transaction.
func convertRPCTransaction(rpcTx *rpcTransaction) *migrate.Transaction {
	tx := &migrate.Transaction{
		Hash:     common.HexToHash(rpcTx.Hash),
		Nonce:    hexToUint64(rpcTx.Nonce),
		From:     common.HexToAddress(rpcTx.From),
		Value:    hexToBigInt(rpcTx.Value),
		Gas:      hexToUint64(rpcTx.Gas),
		GasPrice: hexToBigInt(rpcTx.GasPrice),
		Data:     hexToBytes(rpcTx.Input),
		V:        hexToBigInt(rpcTx.V),
		R:        hexToBigInt(rpcTx.R),
		S:        hexToBigInt(rpcTx.S),
	}

	// Set To address (nil for contract creation)
	if rpcTx.To != nil && *rpcTx.To != "" {
		to := common.HexToAddress(*rpcTx.To)
		tx.To = &to
	}

	// EIP-1559 fields
	if rpcTx.MaxFeePerGas != "" {
		tx.GasFeeCap = hexToBigInt(rpcTx.MaxFeePerGas)
	}
	if rpcTx.MaxPriorityFeePerGas != "" {
		tx.GasTipCap = hexToBigInt(rpcTx.MaxPriorityFeePerGas)
	}

	// Access list
	if len(rpcTx.AccessList) > 0 {
		accessList := make(types.AccessList, len(rpcTx.AccessList))
		for i, item := range rpcTx.AccessList {
			keys := make([]common.Hash, len(item.StorageKeys))
			for j, key := range item.StorageKeys {
				keys[j] = common.HexToHash(key)
			}
			accessList[i] = types.AccessTuple{
				Address:     common.HexToAddress(item.Address),
				StorageKeys: keys,
			}
		}
		tx.AccessList = accessList
	}

	return tx
}

// getBlockReceipts fetches receipts for a block.
func (e *Exporter) getBlockReceipts(blockNumber uint64) ([]*migrate.Receipt, error) {
	blockNumHex := fmt.Sprintf("0x%x", blockNumber)

	// Try eth_getBlockReceipts first (newer RPC method)
	result, err := e.callRPC("eth_getBlockReceipts", []interface{}{blockNumHex})
	if err != nil {
		// Fallback: get receipts individually via block transactions
		return nil, err
	}

	var rpcReceipts []map[string]interface{}
	if err := json.Unmarshal(result, &rpcReceipts); err != nil {
		return nil, err
	}

	receipts := make([]*migrate.Receipt, len(rpcReceipts))
	for i, rpcRcpt := range rpcReceipts {
		receipts[i] = convertRPCReceipt(rpcRcpt)
	}

	return receipts, nil
}

// convertRPCReceipt converts an RPC receipt to migrate.Receipt.
func convertRPCReceipt(rpcRcpt map[string]interface{}) *migrate.Receipt {
	receipt := &migrate.Receipt{}

	if status, ok := rpcRcpt["status"].(string); ok {
		receipt.Status = hexToUint64(status)
	}
	if cumulativeGas, ok := rpcRcpt["cumulativeGasUsed"].(string); ok {
		receipt.CumulativeGasUsed = hexToUint64(cumulativeGas)
	}
	if gasUsed, ok := rpcRcpt["gasUsed"].(string); ok {
		receipt.GasUsed = hexToUint64(gasUsed)
	}
	if txHash, ok := rpcRcpt["transactionHash"].(string); ok {
		receipt.TransactionHash = common.HexToHash(txHash)
	}
	if contractAddr, ok := rpcRcpt["contractAddress"].(string); ok && contractAddr != "" {
		addr := common.HexToAddress(contractAddr)
		receipt.ContractAddress = &addr
	}

	// Parse bloom filter
	if bloomHex, ok := rpcRcpt["logsBloom"].(string); ok {
		bloomBytes := hexToBytes(bloomHex)
		if len(bloomBytes) == types.BloomByteLength {
			copy(receipt.Bloom[:], bloomBytes)
		}
	}

	// Parse logs
	if logsRaw, ok := rpcRcpt["logs"].([]interface{}); ok {
		logs := make([]*types.Log, len(logsRaw))
		for i, logRaw := range logsRaw {
			if logMap, ok := logRaw.(map[string]interface{}); ok {
				log := &types.Log{}
				if addr, ok := logMap["address"].(string); ok {
					log.Address = common.HexToAddress(addr)
				}
				if data, ok := logMap["data"].(string); ok {
					log.Data = hexToBytes(data)
				}
				if topicsRaw, ok := logMap["topics"].([]interface{}); ok {
					topics := make([]common.Hash, len(topicsRaw))
					for j, topicRaw := range topicsRaw {
						if topic, ok := topicRaw.(string); ok {
							topics[j] = common.HexToHash(topic)
						}
					}
					log.Topics = topics
				}
				if blockNum, ok := logMap["blockNumber"].(string); ok {
					log.BlockNumber = hexToUint64(blockNum)
				}
				if txHash, ok := logMap["transactionHash"].(string); ok {
					log.TxHash = common.HexToHash(txHash)
				}
				if txIndex, ok := logMap["transactionIndex"].(string); ok {
					log.TxIndex = uint(hexToUint64(txIndex))
				}
				if blockHash, ok := logMap["blockHash"].(string); ok {
					log.BlockHash = common.HexToHash(blockHash)
				}
				if logIndex, ok := logMap["logIndex"].(string); ok {
					log.Index = uint(hexToUint64(logIndex))
				}
				logs[i] = log
			}
		}
		receipt.Logs = logs
	}

	return receipt
}

// ExportState exports state at a specific block height using debug_accountRange.
// Returns channels for streaming accounts and any errors.
func (e *Exporter) ExportState(ctx context.Context, blockNumber uint64) (<-chan *migrate.Account, <-chan error) {
	accounts := make(chan *migrate.Account, accountBufferSize)
	errs := make(chan error, errorBufferSize)

	e.mu.RLock()
	if !e.initialized {
		e.mu.RUnlock()
		errs <- migrate.ErrNotInitialized
		close(accounts)
		close(errs)
		return accounts, errs
	}
	e.mu.RUnlock()

	go func() {
		defer close(accounts)
		defer close(errs)

		// Use debug_accountRange to iterate all accounts
		blockNumHex := fmt.Sprintf("0x%x", blockNumber)
		nextKey := common.Hash{} // Start from zero

		for {
			select {
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			default:
			}

			// Call debug_accountRange
			// Parameters: blockNumber, start, maxResults, nocode, nostorage, incompletes
			result, err := e.callRPC("debug_accountRange", []interface{}{
				blockNumHex,
				nextKey.Hex(),
				accountBatchSize,
				false, // nocode: false (include code)
				false, // nostorage: false (include storage)
				false, // incompletes: false
			})
			if err != nil {
				errs <- fmt.Errorf("debug_accountRange failed: %w", err)
				return
			}

			var accountRange rpcAccountRange
			if err := json.Unmarshal(result, &accountRange); err != nil {
				errs <- fmt.Errorf("parse debug_accountRange: %w", err)
				return
			}

			// Process accounts
			for addrHex, dumpAcc := range accountRange.Accounts {
				select {
				case <-ctx.Done():
					errs <- ctx.Err()
					return
				default:
				}

				account := convertDumpAccount(addrHex, dumpAcc)

				select {
				case accounts <- account:
				case <-ctx.Done():
					errs <- ctx.Err()
					return
				}
			}

			// Check for more accounts
			if accountRange.Next == "" || accountRange.Next == "0x0000000000000000000000000000000000000000000000000000000000000000" {
				break
			}
			nextKey = common.HexToHash(accountRange.Next)
		}
	}()

	return accounts, errs
}

// convertDumpAccount converts a debug_accountRange account to migrate.Account.
func convertDumpAccount(addrHex string, dumpAcc *rpcDumpAccount) *migrate.Account {
	// Determine address
	var addr common.Address
	if dumpAcc.Address != nil && *dumpAcc.Address != "" {
		addr = common.HexToAddress(*dumpAcc.Address)
	} else {
		addr = common.HexToAddress(addrHex)
	}

	// Parse balance
	balance := new(big.Int)
	if dumpAcc.Balance != "" {
		// Balance can be decimal or hex
		if strings.HasPrefix(dumpAcc.Balance, "0x") {
			balance = hexToBigInt(dumpAcc.Balance)
		} else {
			balance.SetString(dumpAcc.Balance, 10)
		}
	}

	// Parse storage
	storage := make(map[common.Hash]common.Hash)
	for key, value := range dumpAcc.Storage {
		storage[common.HexToHash(key)] = common.HexToHash(value)
	}

	// Parse code hash
	var codeHash common.Hash
	if dumpAcc.CodeHash != "" {
		codeHash = common.HexToHash(dumpAcc.CodeHash)
	} else {
		codeHash = types.EmptyCodeHash
	}

	// Parse storage root
	var storageRoot common.Hash
	if dumpAcc.Root != "" {
		storageRoot = common.HexToHash(dumpAcc.Root)
	} else {
		storageRoot = types.EmptyRootHash
	}

	return &migrate.Account{
		Address:     addr,
		Nonce:       dumpAcc.Nonce,
		Balance:     balance,
		CodeHash:    codeHash,
		StorageRoot: storageRoot,
		Code:        hexToBytes(dumpAcc.Code),
		Storage:     storage,
	}
}

// ExportAccount exports a specific account's state.
func (e *Exporter) ExportAccount(ctx context.Context, address common.Address, blockNumber uint64) (*migrate.Account, error) {
	e.mu.RLock()
	if !e.initialized {
		e.mu.RUnlock()
		return nil, migrate.ErrNotInitialized
	}
	e.mu.RUnlock()

	blockNumHex := fmt.Sprintf("0x%x", blockNumber)
	addrHex := address.Hex()

	// Get balance
	balanceHex, err := e.callRPCString("eth_getBalance", []interface{}{addrHex, blockNumHex})
	if err != nil {
		return nil, fmt.Errorf("eth_getBalance: %w", err)
	}

	// Get nonce
	nonceHex, err := e.callRPCString("eth_getTransactionCount", []interface{}{addrHex, blockNumHex})
	if err != nil {
		return nil, fmt.Errorf("eth_getTransactionCount: %w", err)
	}

	// Get code
	codeHex, err := e.callRPCString("eth_getCode", []interface{}{addrHex, blockNumHex})
	if err != nil {
		return nil, fmt.Errorf("eth_getCode: %w", err)
	}
	code := hexToBytes(codeHex)

	// Compute code hash
	var codeHash common.Hash
	if len(code) > 0 {
		codeHash = common.BytesToHash(crypto.Keccak256(code))
	} else {
		codeHash = types.EmptyCodeHash
	}

	// Get storage (limited - use debug_storageRangeAt for full storage)
	storage := make(map[common.Hash]common.Hash)

	// Try debug_storageRangeAt for full storage export
	if err := e.exportAccountStorage(address, blockNumber, storage); err != nil {
		// Fallback: storage will be incomplete without debug access
	}

	return &migrate.Account{
		Address:     address,
		Nonce:       hexToUint64(nonceHex),
		Balance:     hexToBigInt(balanceHex),
		CodeHash:    codeHash,
		StorageRoot: types.EmptyRootHash, // Not available via standard RPC
		Code:        code,
		Storage:     storage,
	}, nil
}

// exportAccountStorage exports storage for an account using debug_storageRangeAt.
func (e *Exporter) exportAccountStorage(address common.Address, blockNumber uint64, storage map[common.Hash]common.Hash) error {
	blockNumHex := fmt.Sprintf("0x%x", blockNumber)
	addrHex := address.Hex()
	nextKey := common.Hash{}

	for {
		// debug_storageRangeAt: blockHash/Number, txIndex, address, startKey, maxResult
		result, err := e.callRPC("debug_storageRangeAt", []interface{}{
			blockNumHex,
			0, // transaction index
			addrHex,
			nextKey.Hex(),
			256, // max results
		})
		if err != nil {
			return err
		}

		var storageRange struct {
			Storage map[string]struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			} `json:"storage"`
			NextKey *string `json:"nextKey"`
		}
		if err := json.Unmarshal(result, &storageRange); err != nil {
			return err
		}

		// Process storage slots
		for _, slot := range storageRange.Storage {
			key := common.HexToHash(slot.Key)
			value := common.HexToHash(slot.Value)
			storage[key] = value
		}

		// Check for more storage
		if storageRange.NextKey == nil || *storageRange.NextKey == "" {
			break
		}
		nextKey = common.HexToHash(*storageRange.NextKey)
	}

	return nil
}

// ExportConfig exports chain configuration (genesis, network ID, etc.).
func (e *Exporter) ExportConfig() (*migrate.Config, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.initialized {
		return nil, migrate.ErrNotInitialized
	}

	config := &migrate.Config{
		NetworkID: e.networkID,
		ChainID:   e.chainID,
		IsCoreth:  false,
	}

	// Export genesis block
	genesisBlock, err := e.exportBlock(0)
	if err == nil {
		config.GenesisBlock = genesisBlock
	}

	// Try to get chain config via debug_chaindbProperty or assume defaults
	// C-Chain typically enables all forks at block 0
	zero := big.NewInt(0)
	config.HomesteadBlock = zero
	config.EIP150Block = zero
	config.EIP155Block = zero
	config.EIP158Block = zero
	config.ByzantiumBlock = zero
	config.ConstantinopleBlock = zero
	config.PetersburgBlock = zero
	config.IstanbulBlock = zero
	config.BerlinBlock = zero
	config.LondonBlock = zero

	return config, nil
}

// VerifyExport verifies export integrity at a block height.
func (e *Exporter) VerifyExport(blockNumber uint64) error {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.initialized {
		return migrate.ErrNotInitialized
	}

	// Verify block exists via RPC
	block, err := e.getBlockByNumber(blockNumber, false)
	if err != nil {
		return fmt.Errorf("failed to get block %d: %w", blockNumber, err)
	}

	if block == nil {
		return migrate.ErrBlockNotFound
	}

	// Verify state availability by querying an account
	blockNumHex := fmt.Sprintf("0x%x", blockNumber)
	_, err = e.callRPCString("eth_getBalance", []interface{}{
		"0x0000000000000000000000000000000000000000",
		blockNumHex,
	})
	if err != nil {
		return migrate.ErrStateNotAvailable
	}

	return nil
}

// Close closes the exporter and releases resources.
func (e *Exporter) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.initialized = false
	e.client = nil
	return nil
}

// getBlockByNumber fetches a block by number via RPC.
func (e *Exporter) getBlockByNumber(number uint64, fullTx bool) (*rpcBlock, error) {
	blockNumHex := fmt.Sprintf("0x%x", number)
	result, err := e.callRPC("eth_getBlockByNumber", []interface{}{blockNumHex, fullTx})
	if err != nil {
		return nil, err
	}

	if string(result) == "null" {
		return nil, migrate.ErrBlockNotFound
	}

	var block rpcBlock
	if err := json.Unmarshal(result, &block); err != nil {
		return nil, fmt.Errorf("parse block: %w", err)
	}

	return &block, nil
}

// callRPC executes a JSON-RPC call and returns the result.
func (e *Exporter) callRPC(method string, params []interface{}) (json.RawMessage, error) {
	e.requestID++
	if params == nil {
		params = []interface{}{}
	}

	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      e.requestID,
		Method:  method,
		Params:  params,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	resp, err := e.client.Post(e.config.RPCURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, migrate.ErrRPCConnectionFailed
	}
	defer resp.Body.Close()

	var rpcResp rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	return rpcResp.Result, nil
}

// callRPCString executes a JSON-RPC call and returns the result as a string.
func (e *Exporter) callRPCString(method string, params []interface{}) (string, error) {
	result, err := e.callRPC(method, params)
	if err != nil {
		return "", err
	}

	var str string
	if err := json.Unmarshal(result, &str); err != nil {
		return "", fmt.Errorf("parse string result: %w", err)
	}

	return str, nil
}

// Hex conversion helpers

func hexToUint64(s string) uint64 {
	if s == "" {
		return 0
	}
	s = strings.TrimPrefix(s, "0x")
	val, _ := new(big.Int).SetString(s, 16)
	if val == nil {
		return 0
	}
	return val.Uint64()
}

func hexToBigInt(s string) *big.Int {
	if s == "" {
		return nil
	}
	s = strings.TrimPrefix(s, "0x")
	val, _ := new(big.Int).SetString(s, 16)
	return val
}

func hexToBytes(s string) []byte {
	if s == "" || s == "0x" {
		return nil
	}
	s = strings.TrimPrefix(s, "0x")
	if len(s)%2 != 0 {
		s = "0" + s
	}
	b, _ := hex.DecodeString(s)
	return b
}

// Ensure Exporter implements migrate.Exporter
var _ migrate.Exporter = (*Exporter)(nil)

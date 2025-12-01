// Package zool2 provides Zoo L2 chain export/import functionality.
// Zoo L2 chains are Layer 2 EVM-compatible chains in the Zoo ecosystem.
package zool2

import (
	"context"
	"fmt"
	"math/big"
	"sync"

	"github.com/luxfi/geth/common"
	"github.com/luxfi/geth/core/rawdb"
	"github.com/luxfi/geth/core/state"
	"github.com/luxfi/geth/core/types"
	"github.com/luxfi/geth/ethdb"
	"github.com/luxfi/geth/ethdb/pebble"
	"github.com/luxfi/geth/rlp"
	"github.com/luxfi/geth/trie"
	"github.com/luxfi/geth/triedb"
	"github.com/luxfi/migrate"
)

const (
	// Default channel buffer sizes
	blockChannelBuffer   = 100
	accountChannelBuffer = 100

	// PebbleDB settings for read-only export
	defaultCache   = 512  // MB
	defaultHandles = 1024 // file handles
)

// Exporter exports blocks and state from Zoo L2 PebbleDB
type Exporter struct {
	config      migrate.ExporterConfig
	db          ethdb.Database
	trieDB      *triedb.Database
	initialized bool
	mu          sync.RWMutex
}

// NewExporter creates a new Zoo L2 exporter
func NewExporter(config migrate.ExporterConfig) (*Exporter, error) {
	return &Exporter{
		config: config,
	}, nil
}

// Init initializes the exporter with the source database
func (e *Exporter) Init(config migrate.ExporterConfig) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.initialized {
		return migrate.ErrAlreadyInitialized
	}

	e.config = config

	// Open PebbleDB in read-only mode
	kvdb, err := pebble.New(config.DatabasePath, defaultCache, defaultHandles, "zool2/", true)
	if err != nil {
		return fmt.Errorf("failed to open pebble database: %w", err)
	}

	// Wrap in nofreezedb for simple access (no ancient freezer)
	e.db = rawdb.NewDatabase(kvdb)

	// Initialize trie database for state access
	e.trieDB = triedb.NewDatabase(e.db, nil)

	e.initialized = true
	return nil
}

// GetInfo returns metadata about the source Zoo L2 chain
func (e *Exporter) GetInfo() (*migrate.Info, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.initialized {
		return nil, migrate.ErrNotInitialized
	}

	// Read head block hash
	headHash := rawdb.ReadHeadBlockHash(e.db)
	if headHash == (common.Hash{}) {
		return nil, fmt.Errorf("no head block found")
	}

	// Read head block number
	headNumber, found := rawdb.ReadHeaderNumber(e.db, headHash)
	if !found {
		return nil, fmt.Errorf("head block number not found")
	}

	// Read head header
	headHeader := rawdb.ReadHeader(e.db, headHash, headNumber)
	if headHeader == nil {
		return nil, fmt.Errorf("head header not found")
	}

	// Read genesis hash
	genesisHash := rawdb.ReadCanonicalHash(e.db, 0)

	// Read chain config for chain ID
	chainConfig := rawdb.ReadChainConfig(e.db, genesisHash)
	var chainID *big.Int
	if chainConfig != nil {
		chainID = chainConfig.ChainID
	}

	return &migrate.Info{
		VMType:        migrate.VMTypeZooL2,
		ChainID:       chainID,
		GenesisHash:   genesisHash,
		CurrentHeight: headNumber,
		StateRoot:     headHeader.Root,
		VMVersion:     "zoo-l2-v1",
		DatabaseType:  "pebble",
		IsPruned:      false, // Assume archive mode
		ArchiveMode:   true,
	}, nil
}

// ExportBlocks exports blocks in a range (inclusive) via streaming channels
func (e *Exporter) ExportBlocks(ctx context.Context, start, end uint64) (<-chan *migrate.BlockData, <-chan error) {
	blocks := make(chan *migrate.BlockData, blockChannelBuffer)
	errs := make(chan error, 1)

	// Validate inputs
	if start > end {
		errs <- migrate.ErrInvalidBlockRange
		close(blocks)
		close(errs)
		return blocks, errs
	}

	e.mu.RLock()
	initialized := e.initialized
	e.mu.RUnlock()

	if !initialized {
		errs <- migrate.ErrNotInitialized
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
			}

			block, err := e.exportBlock(height)
			if err != nil {
				errs <- fmt.Errorf("failed to export block %d: %w", height, err)
				return
			}

			select {
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			case blocks <- block:
			}
		}
	}()

	return blocks, errs
}

// exportBlock exports a single block at the given height
func (e *Exporter) exportBlock(number uint64) (*migrate.BlockData, error) {
	// Get canonical hash for this block number
	hash := rawdb.ReadCanonicalHash(e.db, number)
	if hash == (common.Hash{}) {
		return nil, fmt.Errorf("canonical hash not found for block %d", number)
	}

	// Read header
	header := rawdb.ReadHeader(e.db, hash, number)
	if header == nil {
		return nil, fmt.Errorf("header not found for block %d", number)
	}

	// Read body (transactions and uncles)
	body := rawdb.ReadBody(e.db, hash, number)
	if body == nil {
		// Genesis or empty block may have nil body
		body = &types.Body{}
	}

	// Read receipts if configured
	var receiptsRLP []byte
	if e.config.ExportReceipts {
		receipts := rawdb.ReadReceipts(e.db, hash, number, header.Time, nil)
		if receipts != nil {
			var err error
			receiptsRLP, err = rlp.EncodeToBytes(receipts)
			if err != nil {
				return nil, fmt.Errorf("failed to encode receipts: %w", err)
			}
		}
	}

	// Encode header
	headerRLP, err := rlp.EncodeToBytes(header)
	if err != nil {
		return nil, fmt.Errorf("failed to encode header: %w", err)
	}

	// Encode body
	bodyRLP, err := rlp.EncodeToBytes(body)
	if err != nil {
		return nil, fmt.Errorf("failed to encode body: %w", err)
	}

	// Convert transactions to migrate format
	txs := make([]*migrate.Transaction, len(body.Transactions))
	for i, tx := range body.Transactions {
		txs[i] = convertTransaction(tx)
	}

	// Encode uncle headers
	uncleRLPs := make([][]byte, len(body.Uncles))
	for i, uncle := range body.Uncles {
		uncleRLPs[i], err = rlp.EncodeToBytes(uncle)
		if err != nil {
			return nil, fmt.Errorf("failed to encode uncle: %w", err)
		}
	}

	return &migrate.BlockData{
		Number:           number,
		Hash:             hash,
		ParentHash:       header.ParentHash,
		Timestamp:        header.Time,
		StateRoot:        header.Root,
		ReceiptsRoot:     header.ReceiptHash,
		TransactionsRoot: header.TxHash,
		GasLimit:         header.GasLimit,
		GasUsed:          header.GasUsed,
		Difficulty:       header.Difficulty,
		Coinbase:         header.Coinbase,
		Nonce:            header.Nonce,
		MixHash:          header.MixDigest,
		ExtraData:        header.Extra,
		BaseFee:          header.BaseFee,
		Header:           headerRLP,
		Body:             bodyRLP,
		Receipts:         receiptsRLP,
		Transactions:     txs,
		UncleHeaders:     uncleRLPs,
		Extensions: map[string]interface{}{
			"vm_type": "zoo-l2",
		},
	}, nil
}

// convertTransaction converts a geth transaction to migrate format
func convertTransaction(tx *types.Transaction) *migrate.Transaction {
	v, r, s := tx.RawSignatureValues()

	mtx := &migrate.Transaction{
		Hash:     tx.Hash(),
		Nonce:    tx.Nonce(),
		To:       tx.To(),
		Value:    tx.Value(),
		Gas:      tx.Gas(),
		GasPrice: tx.GasPrice(),
		Data:     tx.Data(),
		V:        v,
		R:        r,
		S:        s,
	}

	// EIP-1559 fields
	if tx.Type() == types.DynamicFeeTxType {
		mtx.GasTipCap = tx.GasTipCap()
		mtx.GasFeeCap = tx.GasFeeCap()
	}

	// Access list
	if tx.Type() == types.AccessListTxType || tx.Type() == types.DynamicFeeTxType {
		mtx.AccessList = tx.AccessList()
	}

	// From address (derived from signature)
	signer := types.LatestSignerForChainID(tx.ChainId())
	if from, err := types.Sender(signer, tx); err == nil {
		mtx.From = from
	}

	return mtx
}

// ExportState exports state at a specific block height via streaming
func (e *Exporter) ExportState(ctx context.Context, blockNumber uint64) (<-chan *migrate.Account, <-chan error) {
	accounts := make(chan *migrate.Account, accountChannelBuffer)
	errs := make(chan error, 1)

	e.mu.RLock()
	initialized := e.initialized
	e.mu.RUnlock()

	if !initialized {
		errs <- migrate.ErrNotInitialized
		close(accounts)
		close(errs)
		return accounts, errs
	}

	go func() {
		defer close(accounts)
		defer close(errs)

		// Get block header for state root
		hash := rawdb.ReadCanonicalHash(e.db, blockNumber)
		if hash == (common.Hash{}) {
			errs <- fmt.Errorf("canonical hash not found for block %d", blockNumber)
			return
		}

		header := rawdb.ReadHeader(e.db, hash, blockNumber)
		if header == nil {
			errs <- fmt.Errorf("header not found for block %d", blockNumber)
			return
		}

		// Open state trie at root
		stateTrie, err := trie.NewStateTrie(trie.StateTrieID(header.Root), e.trieDB)
		if err != nil {
			errs <- fmt.Errorf("failed to open state trie: %w", err)
			return
		}

		// Create iterator for all accounts
		nodeIter, err := stateTrie.NodeIterator(nil)
		if err != nil {
			errs <- fmt.Errorf("failed to create trie iterator: %w", err)
			return
		}
		it := trie.NewIterator(nodeIter)

		for it.Next() {
			select {
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			default:
			}

			// Decode account from iterator value
			var acc types.StateAccount
			if err := rlp.DecodeBytes(it.Value, &acc); err != nil {
				errs <- fmt.Errorf("failed to decode account: %w", err)
				return
			}

			// Get address from key (hash of address)
			addrHash := common.BytesToHash(it.Key)

			// Read preimage to get actual address (if available)
			address := rawdb.ReadPreimage(e.db, addrHash)
			if address == nil {
				// Address preimage not available, skip or use hash
				continue
			}

			acct := &migrate.Account{
				Address:     common.BytesToAddress(address),
				Nonce:       acc.Nonce,
				Balance:     acc.Balance.ToBig(),
				CodeHash:    common.BytesToHash(acc.CodeHash),
				StorageRoot: acc.Root,
			}

			// Read code if account has code
			if !isEmptyCodeHash(acc.CodeHash) {
				acct.Code = rawdb.ReadCode(e.db, common.BytesToHash(acc.CodeHash))
			}

			// Export storage if account has storage
			if acc.Root != types.EmptyRootHash {
				storage, err := e.exportAccountStorage(acc.Root, addrHash)
				if err != nil {
					errs <- fmt.Errorf("failed to export storage for %s: %w", acct.Address.Hex(), err)
					return
				}
				acct.Storage = storage
			}

			select {
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			case accounts <- acct:
			}
		}

		if it.Err != nil {
			errs <- fmt.Errorf("trie iteration error: %w", it.Err)
		}
	}()

	return accounts, errs
}

// isEmptyCodeHash checks if the code hash represents empty code
func isEmptyCodeHash(codeHash []byte) bool {
	return common.BytesToHash(codeHash) == types.EmptyCodeHash
}

// exportAccountStorage exports all storage slots for an account
func (e *Exporter) exportAccountStorage(storageRoot common.Hash, addrHash common.Hash) (map[common.Hash]common.Hash, error) {
	storage := make(map[common.Hash]common.Hash)

	// Open storage trie
	storageTrie, err := trie.NewStateTrie(trie.StorageTrieID(common.Hash{}, addrHash, storageRoot), e.trieDB)
	if err != nil {
		return nil, fmt.Errorf("failed to open storage trie: %w", err)
	}

	nodeIter, err := storageTrie.NodeIterator(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create storage iterator: %w", err)
	}
	it := trie.NewIterator(nodeIter)

	for it.Next() {
		// Key is hash of storage slot
		keyHash := common.BytesToHash(it.Key)

		// Value is RLP encoded
		var value common.Hash
		if _, content, _, err := rlp.Split(it.Value); err == nil {
			value.SetBytes(content)
		}

		storage[keyHash] = value
	}

	if it.Err != nil {
		return nil, fmt.Errorf("storage iteration error: %w", it.Err)
	}

	return storage, nil
}

// ExportAccount exports a specific account's state
func (e *Exporter) ExportAccount(ctx context.Context, address common.Address, blockNumber uint64) (*migrate.Account, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.initialized {
		return nil, migrate.ErrNotInitialized
	}

	// Get block header for state root
	hash := rawdb.ReadCanonicalHash(e.db, blockNumber)
	if hash == (common.Hash{}) {
		return nil, fmt.Errorf("canonical hash not found for block %d", blockNumber)
	}

	header := rawdb.ReadHeader(e.db, hash, blockNumber)
	if header == nil {
		return nil, fmt.Errorf("header not found for block %d", blockNumber)
	}

	// Create state database
	stateDB, err := state.New(header.Root, state.NewDatabase(e.trieDB, nil))
	if err != nil {
		return nil, fmt.Errorf("failed to create state database: %w", err)
	}

	acct := &migrate.Account{
		Address:  address,
		Nonce:    stateDB.GetNonce(address),
		Balance:  stateDB.GetBalance(address).ToBig(),
		CodeHash: stateDB.GetCodeHash(address),
		Code:     stateDB.GetCode(address),
		Storage:  make(map[common.Hash]common.Hash),
	}

	// Get storage root
	acct.StorageRoot = stateDB.GetStorageRoot(address)

	return acct, nil
}

// ExportConfig exports chain configuration
func (e *Exporter) ExportConfig() (*migrate.Config, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.initialized {
		return nil, migrate.ErrNotInitialized
	}

	// Read genesis hash
	genesisHash := rawdb.ReadCanonicalHash(e.db, 0)
	if genesisHash == (common.Hash{}) {
		return nil, fmt.Errorf("genesis hash not found")
	}

	// Read chain config
	chainConfig := rawdb.ReadChainConfig(e.db, genesisHash)
	if chainConfig == nil {
		return nil, fmt.Errorf("chain config not found")
	}

	// Read genesis header
	genesisHeader := rawdb.ReadHeader(e.db, genesisHash, 0)
	if genesisHeader == nil {
		return nil, fmt.Errorf("genesis header not found")
	}

	// Export genesis block
	genesisBlock, err := e.exportBlock(0)
	if err != nil {
		return nil, fmt.Errorf("failed to export genesis block: %w", err)
	}

	config := &migrate.Config{
		ChainID:             chainConfig.ChainID,
		GenesisBlock:        genesisBlock,
		HomesteadBlock:      chainConfig.HomesteadBlock,
		EIP150Block:         chainConfig.EIP150Block,
		EIP155Block:         chainConfig.EIP155Block,
		EIP158Block:         chainConfig.EIP158Block,
		ByzantiumBlock:      chainConfig.ByzantiumBlock,
		ConstantinopleBlock: chainConfig.ConstantinopleBlock,
		PetersburgBlock:     chainConfig.PetersburgBlock,
		IstanbulBlock:       chainConfig.IstanbulBlock,
		BerlinBlock:         chainConfig.BerlinBlock,
		LondonBlock:         chainConfig.LondonBlock,
	}

	return config, nil
}

// VerifyExport verifies export integrity at a block height
func (e *Exporter) VerifyExport(blockNumber uint64) error {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.initialized {
		return migrate.ErrNotInitialized
	}

	// Verify block exists
	hash := rawdb.ReadCanonicalHash(e.db, blockNumber)
	if hash == (common.Hash{}) {
		return migrate.ErrBlockNotFound
	}

	// Verify header
	header := rawdb.ReadHeader(e.db, hash, blockNumber)
	if header == nil {
		return migrate.ErrBlockNotFound
	}

	// Verify state root is accessible (if exporting state)
	if e.config.ExportState {
		_, err := trie.NewStateTrie(trie.StateTrieID(header.Root), e.trieDB)
		if err != nil {
			return migrate.ErrStateNotAvailable
		}
	}

	return nil
}

// Close closes the exporter and releases resources
func (e *Exporter) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.initialized {
		return nil
	}

	var errs []error

	if e.trieDB != nil {
		if err := e.trieDB.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if e.db != nil {
		if err := e.db.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	e.initialized = false

	if len(errs) > 0 {
		return fmt.Errorf("close errors: %v", errs)
	}
	return nil
}

// Ensure Exporter implements migrate.Exporter
var _ migrate.Exporter = (*Exporter)(nil)

// Package zool2 provides Zoo L2 chain export/import functionality.
package zool2

import (
	"fmt"
	"math/big"
	"sync"

	"github.com/holiman/uint256"
	"github.com/luxfi/geth/common"
	"github.com/luxfi/geth/core/rawdb"
	"github.com/luxfi/geth/core/state"
	"github.com/luxfi/geth/core/tracing"
	"github.com/luxfi/geth/core/types"
	"github.com/luxfi/geth/ethdb"
	"github.com/luxfi/geth/ethdb/pebble"
	"github.com/luxfi/geth/params"
	"github.com/luxfi/geth/rlp"
	"github.com/luxfi/geth/trie"
	"github.com/luxfi/geth/triedb"
	"github.com/luxfi/migrate"
)

const (
	// Default batch size for imports
	defaultBatchSize = 1000

	// PebbleDB settings for write operations
	importCache   = 1024 // MB
	importHandles = 2048 // file handles
)

// Importer imports blocks and state to Zoo L2 PebbleDB
type Importer struct {
	config      migrate.ImporterConfig
	db          ethdb.Database
	trieDB      *triedb.Database
	chainConfig *params.ChainConfig
	initialized bool
	lastBlock   uint64
	mu          sync.Mutex
}

// NewImporter creates a new Zoo L2 importer
func NewImporter(config migrate.ImporterConfig) (*Importer, error) {
	return &Importer{
		config: config,
	}, nil
}

// Init initializes the importer with the destination database
func (i *Importer) Init(config migrate.ImporterConfig) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.initialized {
		return migrate.ErrAlreadyInitialized
	}

	i.config = config

	// Open PebbleDB in write mode
	kvdb, err := pebble.New(config.DatabasePath, importCache, importHandles, "zool2/", false)
	if err != nil {
		return fmt.Errorf("failed to open pebble database: %w", err)
	}

	i.db = rawdb.NewDatabase(kvdb)

	// Initialize trie database
	i.trieDB = triedb.NewDatabase(i.db, nil)

	// Check existing chain state
	headHash := rawdb.ReadHeadBlockHash(i.db)
	if headHash != (common.Hash{}) {
		headNumber, found := rawdb.ReadHeaderNumber(i.db, headHash)
		if found {
			i.lastBlock = headNumber
		}
	}

	i.initialized = true
	return nil
}

// ImportConfig imports chain configuration
func (i *Importer) ImportConfig(config *migrate.Config) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !i.initialized {
		return migrate.ErrNotInitialized
	}

	// Build params.ChainConfig from migrate.Config
	i.chainConfig = &params.ChainConfig{
		ChainID:             config.ChainID,
		HomesteadBlock:      config.HomesteadBlock,
		EIP150Block:         config.EIP150Block,
		EIP155Block:         config.EIP155Block,
		EIP158Block:         config.EIP158Block,
		ByzantiumBlock:      config.ByzantiumBlock,
		ConstantinopleBlock: config.ConstantinopleBlock,
		PetersburgBlock:     config.PetersburgBlock,
		IstanbulBlock:       config.IstanbulBlock,
		BerlinBlock:         config.BerlinBlock,
		LondonBlock:         config.LondonBlock,
	}

	// Import genesis block if provided
	if config.GenesisBlock != nil {
		if err := i.importBlockData(config.GenesisBlock, true); err != nil {
			return fmt.Errorf("failed to import genesis block: %w", err)
		}

		// Write chain config
		rawdb.WriteChainConfig(i.db, config.GenesisBlock.Hash, i.chainConfig)
	}

	return nil
}

// ImportBlock imports a single block (must be sequential)
func (i *Importer) ImportBlock(block *migrate.BlockData) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !i.initialized {
		return migrate.ErrNotInitialized
	}

	// Verify sequential import
	if block.Number > 0 && block.Number != i.lastBlock+1 {
		return fmt.Errorf("block %d out of sequence, expected %d", block.Number, i.lastBlock+1)
	}

	return i.importBlockData(block, false)
}

// importBlockData performs the actual block import
func (i *Importer) importBlockData(block *migrate.BlockData, isGenesis bool) error {
	// Decode header from RLP
	var header types.Header
	if err := rlp.DecodeBytes(block.Header, &header); err != nil {
		return fmt.Errorf("failed to decode header: %w", err)
	}

	// Decode body from RLP if present
	var body types.Body
	if len(block.Body) > 0 {
		if err := rlp.DecodeBytes(block.Body, &body); err != nil {
			return fmt.Errorf("failed to decode body: %w", err)
		}
	}

	// Write header
	rawdb.WriteHeader(i.db, &header)

	// Write body
	rawdb.WriteBody(i.db, block.Hash, block.Number, &body)

	// Write canonical hash
	rawdb.WriteCanonicalHash(i.db, block.Hash, block.Number)

	// Write receipts if present
	if len(block.Receipts) > 0 {
		var receipts types.Receipts
		if err := rlp.DecodeBytes(block.Receipts, &receipts); err != nil {
			return fmt.Errorf("failed to decode receipts: %w", err)
		}
		rawdb.WriteReceipts(i.db, block.Hash, block.Number, receipts)
	}

	// Write transaction lookups
	for _, tx := range body.Transactions {
		rawdb.WriteTxLookupEntries(i.db, block.Number, []common.Hash{tx.Hash()})
	}

	// Update head pointers
	rawdb.WriteHeadBlockHash(i.db, block.Hash)
	rawdb.WriteHeadHeaderHash(i.db, block.Hash)

	i.lastBlock = block.Number
	return nil
}

// ImportBlocks imports multiple blocks in batch
func (i *Importer) ImportBlocks(blocks []*migrate.BlockData) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !i.initialized {
		return migrate.ErrNotInitialized
	}

	batch := i.db.NewBatch()
	defer batch.Reset()

	for _, block := range blocks {
		// Verify sequential import
		if block.Number > 0 && block.Number != i.lastBlock+1 {
			return fmt.Errorf("block %d out of sequence, expected %d", block.Number, i.lastBlock+1)
		}

		if err := i.importBlockToBatch(batch, block); err != nil {
			return err
		}

		i.lastBlock = block.Number

		// Write batch periodically
		if batch.ValueSize() > defaultBatchSize*1024 {
			if err := batch.Write(); err != nil {
				return fmt.Errorf("failed to write batch: %w", err)
			}
			batch.Reset()
		}
	}

	// Write remaining batch
	if batch.ValueSize() > 0 {
		if err := batch.Write(); err != nil {
			return fmt.Errorf("failed to write final batch: %w", err)
		}
	}

	// Update head pointers
	if len(blocks) > 0 {
		lastBlock := blocks[len(blocks)-1]
		rawdb.WriteHeadBlockHash(i.db, lastBlock.Hash)
		rawdb.WriteHeadHeaderHash(i.db, lastBlock.Hash)
	}

	return nil
}

// importBlockToBatch adds a block to the batch
func (i *Importer) importBlockToBatch(batch ethdb.Batch, block *migrate.BlockData) error {
	// Decode header from RLP
	var header types.Header
	if err := rlp.DecodeBytes(block.Header, &header); err != nil {
		return fmt.Errorf("failed to decode header: %w", err)
	}

	// Decode body from RLP if present
	var body types.Body
	if len(block.Body) > 0 {
		if err := rlp.DecodeBytes(block.Body, &body); err != nil {
			return fmt.Errorf("failed to decode body: %w", err)
		}
	}

	// Write header
	headerKey := headerKeyFor(block.Number, block.Hash)
	headerData, _ := rlp.EncodeToBytes(&header)
	batch.Put(headerKey, headerData)

	// Write hash -> number mapping
	numberKey := headerNumberKeyFor(block.Hash)
	batch.Put(numberKey, encodeBlockNumber(block.Number))

	// Write body
	bodyKey := blockBodyKeyFor(block.Number, block.Hash)
	bodyData, _ := rlp.EncodeToBytes(&body)
	batch.Put(bodyKey, bodyData)

	// Write canonical hash
	hashKey := headerHashKeyFor(block.Number)
	batch.Put(hashKey, block.Hash.Bytes())

	// Write receipts if present
	if len(block.Receipts) > 0 {
		receiptsKey := blockReceiptsKeyFor(block.Number, block.Hash)
		batch.Put(receiptsKey, block.Receipts)
	}

	return nil
}

// ImportState imports state accounts at a specific height
func (i *Importer) ImportState(accounts []*migrate.Account, blockNumber uint64) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !i.initialized {
		return migrate.ErrNotInitialized
	}

	// Get the header at blockNumber to verify state root
	hash := rawdb.ReadCanonicalHash(i.db, blockNumber)
	if hash == (common.Hash{}) {
		return fmt.Errorf("block %d not found", blockNumber)
	}

	header := rawdb.ReadHeader(i.db, hash, blockNumber)
	if header == nil {
		return fmt.Errorf("header not found for block %d", blockNumber)
	}

	// Create a new state database
	stateDB, err := state.New(types.EmptyRootHash, state.NewDatabase(i.trieDB, nil))
	if err != nil {
		return fmt.Errorf("failed to create state database: %w", err)
	}

	// Import each account
	for _, acct := range accounts {
		// Set basic account properties
		stateDB.SetNonce(acct.Address, acct.Nonce, tracing.NonceChangeUnspecified)
		if acct.Balance != nil {
			stateDB.SetBalance(acct.Address, uint256FromBig(acct.Balance), tracing.BalanceChangeUnspecified)
		}

		// Set code if present
		if len(acct.Code) > 0 {
			stateDB.SetCode(acct.Address, acct.Code, tracing.CodeChangeUnspecified)
		}

		// Set storage slots
		for key, value := range acct.Storage {
			stateDB.SetState(acct.Address, key, value)
		}
	}

	// Commit state changes (third param is noStorageWiping)
	root, err := stateDB.Commit(blockNumber, false, false)
	if err != nil {
		return fmt.Errorf("failed to commit state: %w", err)
	}

	// Verify state root matches if not preserving state
	if !i.config.PreserveState && root != header.Root {
		return fmt.Errorf("state root mismatch: expected %s, got %s", header.Root.Hex(), root.Hex())
	}

	// Commit trie database
	if err := i.trieDB.Commit(root, false); err != nil {
		return fmt.Errorf("failed to commit trie database: %w", err)
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

	// Verify block exists
	hash := rawdb.ReadCanonicalHash(i.db, blockNumber)
	if hash == (common.Hash{}) {
		return migrate.ErrBlockNotFound
	}

	// Update finalized block marker
	rawdb.WriteFinalizedBlockHash(i.db, hash)

	// Sync database
	if err := i.db.SyncKeyValue(); err != nil {
		return fmt.Errorf("failed to sync database: %w", err)
	}

	return nil
}

// VerifyImport verifies import integrity at a block height
func (i *Importer) VerifyImport(blockNumber uint64) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !i.initialized {
		return migrate.ErrNotInitialized
	}

	// Verify block exists
	hash := rawdb.ReadCanonicalHash(i.db, blockNumber)
	if hash == (common.Hash{}) {
		return migrate.ErrBlockNotFound
	}

	// Verify header
	header := rawdb.ReadHeader(i.db, hash, blockNumber)
	if header == nil {
		return migrate.ErrBlockNotFound
	}

	// Verify body
	body := rawdb.ReadBody(i.db, hash, blockNumber)
	if body == nil && blockNumber > 0 {
		// Non-genesis blocks should have bodies (even if empty)
		return fmt.Errorf("body not found for block %d", blockNumber)
	}

	// Verify state is accessible if enabled
	if i.config.VerifyState {
		_, err := trie.NewStateTrie(trie.StateTrieID(header.Root), i.trieDB)
		if err != nil {
			return migrate.ErrStateNotAvailable
		}
	}

	return nil
}

// ExecuteBlock imports a block with its state.
// Note: Full EVM execution requires a complete ChainContext with consensus engine.
// For migration purposes, this imports the block and trusts the provided state root.
// Use ImportState for state migration, and ImportBlock for block data.
func (i *Importer) ExecuteBlock(block *migrate.BlockData) error {
	// For migration, we trust the source chain's execution.
	// Just import the block data directly.
	return i.ImportBlock(block)
}

// Close closes the importer and releases resources
func (i *Importer) Close() error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !i.initialized {
		return nil
	}

	var errs []error

	if i.trieDB != nil {
		if err := i.trieDB.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if i.db != nil {
		if err := i.db.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	i.initialized = false

	if len(errs) > 0 {
		return fmt.Errorf("close errors: %v", errs)
	}
	return nil
}

// uint256FromBig converts big.Int to uint256 for state balance
func uint256FromBig(b *big.Int) *uint256.Int {
	if b == nil {
		return new(uint256.Int)
	}
	u := new(uint256.Int)
	u.SetFromBig(b)
	return u
}

// Ensure Importer implements migrate.Importer
var _ migrate.Importer = (*Importer)(nil)

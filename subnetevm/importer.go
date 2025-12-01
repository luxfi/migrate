// Package subnetevm provides SubnetEVM-specific import functionality
package subnetevm

import (
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/luxfi/geth/common"
	"github.com/luxfi/geth/core/rawdb"
	"github.com/luxfi/geth/core/types"
	"github.com/luxfi/geth/crypto"
	"github.com/luxfi/geth/ethdb"
	"github.com/luxfi/geth/ethdb/pebble"
	"github.com/luxfi/geth/rlp"
	"github.com/luxfi/migrate"
)

func init() {
	// Register SubnetEVM importer factory
	migrate.RegisterImporterFactory(migrate.VMTypeSubnetEVM, func(config migrate.ImporterConfig) (migrate.Importer, error) {
		imp, err := NewImporter(config)
		if err != nil {
			return nil, err
		}
		// Auto-initialize if database path is provided
		if config.DatabasePath != "" {
			if err := imp.Init(config); err != nil {
				return nil, err
			}
		}
		return imp, nil
	})
}

const (
	// defaultCacheSize is the default cache size for PebbleDB in MB
	defaultCacheSize = 512

	// defaultHandles is the default number of file handles
	defaultHandles = 256

	// defaultBatchSize is the default batch size for imports
	defaultBatchSize = 1000
)

// Importer imports blocks to SubnetEVM PebbleDB
type Importer struct {
	config      migrate.ImporterConfig
	kvstore     ethdb.KeyValueStore
	db          ethdb.Database
	initialized bool
	closed      bool

	// Track import state
	lastImportedBlock uint64
	lastImportedHash  common.Hash

	// Batch for efficient writes
	batch     ethdb.Batch
	batchSize int
	batchMu   sync.Mutex
}

// NewImporter creates a new SubnetEVM importer
func NewImporter(config migrate.ImporterConfig) (*Importer, error) {
	imp := &Importer{
		config:    config,
		batchSize: defaultBatchSize,
	}
	if config.BatchSize > 0 {
		imp.batchSize = config.BatchSize
	}
	return imp, nil
}

// Init initializes the importer with the destination database
func (i *Importer) Init(config migrate.ImporterConfig) error {
	if i.initialized {
		return migrate.ErrAlreadyInitialized
	}
	i.config = config

	if config.DatabasePath == "" {
		return errors.New("database path required for SubnetEVM import")
	}

	// Open PebbleDB
	kvstore, err := pebble.New(config.DatabasePath, defaultCacheSize, defaultHandles, "subnetevm/import/", false)
	if err != nil {
		return fmt.Errorf("failed to open pebble database: %w", err)
	}
	i.kvstore = kvstore
	i.db = rawdb.NewDatabase(kvstore) // Wrap as ethdb.Database
	i.batch = kvstore.NewBatch()
	i.initialized = true

	// Try to read last imported block from database
	i.readLastImportedBlock()

	return nil
}

// readLastImportedBlock reads the last imported block from the database
func (i *Importer) readLastImportedBlock() {
	hash := rawdb.ReadHeadBlockHash(i.db)
	if hash == (common.Hash{}) {
		return
	}
	number, ok := rawdb.ReadHeaderNumber(i.db, hash)
	if !ok {
		return
	}
	i.lastImportedBlock = number
	i.lastImportedHash = hash
}

// ImportConfig imports chain configuration to the database
func (i *Importer) ImportConfig(config *migrate.Config) error {
	if !i.initialized {
		return migrate.ErrNotInitialized
	}
	if config == nil {
		return nil
	}

	// If genesis block is provided, store it
	if config.GenesisBlock != nil {
		if err := i.ImportBlock(config.GenesisBlock); err != nil {
			return fmt.Errorf("failed to import genesis block: %w", err)
		}
	}

	// Import genesis allocations if provided
	if len(config.GenesisAlloc) > 0 {
		if err := i.importGenesisAlloc(config.GenesisAlloc); err != nil {
			return fmt.Errorf("failed to import genesis alloc: %w", err)
		}
	}

	return nil
}

// importGenesisAlloc writes genesis account allocations
func (i *Importer) importGenesisAlloc(alloc map[common.Address]*migrate.Account) error {
	for addr, account := range alloc {
		if account == nil {
			continue
		}

		// Write account code if present
		if len(account.Code) > 0 {
			codeHash := crypto.Keccak256Hash(account.Code)
			rawdb.WriteCode(i.batch, codeHash, account.Code)
		}

		// Write account snapshot
		if err := i.writeAccountSnapshot(addr, account); err != nil {
			return err
		}

		// Write storage if present
		for key, value := range account.Storage {
			if err := i.writeStorageSnapshot(addr, key, value); err != nil {
				return err
			}
		}
	}

	return i.flushBatch()
}

// ImportBlock imports a single block (must be sequential)
func (i *Importer) ImportBlock(block *migrate.BlockData) error {
	if !i.initialized {
		return migrate.ErrNotInitialized
	}
	if block == nil {
		return errors.New("block is nil")
	}

	// Verify sequential import (except for genesis)
	if block.Number > 0 {
		if i.lastImportedBlock > 0 && block.Number != i.lastImportedBlock+1 {
			return fmt.Errorf("non-sequential block import: expected %d, got %d",
				i.lastImportedBlock+1, block.Number)
		}
		if i.lastImportedHash != (common.Hash{}) && block.ParentHash != i.lastImportedHash {
			return fmt.Errorf("parent hash mismatch: expected %s, got %s",
				i.lastImportedHash.Hex(), block.ParentHash.Hex())
		}
	}

	// Convert and write the block
	header, body, err := i.convertBlockData(block)
	if err != nil {
		return fmt.Errorf("failed to convert block data: %w", err)
	}

	// Write header
	i.writeHeader(header)

	// Write body
	i.writeBody(block.Hash, block.Number, body)

	// Write canonical hash mapping
	i.writeCanonicalHash(block.Number, block.Hash)

	// Write hash to number mapping
	i.writeHeaderNumber(block.Hash, block.Number)

	// Write receipts if provided
	if len(block.Receipts) > 0 {
		i.writeReceipts(block.Hash, block.Number, block.Receipts)
	}

	// Write transaction lookups
	for idx, tx := range block.Transactions {
		if tx != nil {
			i.writeTxLookup(tx.Hash, block.Hash, block.Number, uint64(idx))
		}
	}

	// Update tracking
	i.lastImportedBlock = block.Number
	i.lastImportedHash = block.Hash

	// Flush batch if needed
	i.batchMu.Lock()
	defer i.batchMu.Unlock()
	if i.batch.ValueSize() >= i.batchSize*1024 {
		if err := i.flushBatchLocked(); err != nil {
			return err
		}
	}

	return nil
}

// convertBlockData converts migrate.BlockData to geth types
func (i *Importer) convertBlockData(block *migrate.BlockData) (*types.Header, *types.Body, error) {
	// Build header from BlockData
	header := &types.Header{
		ParentHash:  block.ParentHash,
		Root:        block.StateRoot,
		TxHash:      block.TransactionsRoot,
		ReceiptHash: block.ReceiptsRoot,
		Coinbase:    block.Coinbase,
		Difficulty:  block.Difficulty,
		Number:      new(big.Int).SetUint64(block.Number),
		GasLimit:    block.GasLimit,
		GasUsed:     block.GasUsed,
		Time:        block.Timestamp,
		Extra:       block.ExtraData,
		MixDigest:   block.MixHash,
		Nonce:       block.Nonce,
		BaseFee:     block.BaseFee,
	}

	// If RLP encoded header is provided, decode it to get all fields
	if len(block.Header) > 0 {
		var decodedHeader types.Header
		if err := rlp.DecodeBytes(block.Header, &decodedHeader); err == nil {
			header = &decodedHeader
		}
	}

	// Build body
	var txs types.Transactions
	for _, mtx := range block.Transactions {
		if mtx == nil {
			continue
		}
		tx := i.convertTransaction(mtx)
		if tx != nil {
			txs = append(txs, tx)
		}
	}

	// Decode uncles if provided
	var uncles []*types.Header
	for _, uncleRLP := range block.UncleHeaders {
		var uncle types.Header
		if err := rlp.DecodeBytes(uncleRLP, &uncle); err == nil {
			uncles = append(uncles, &uncle)
		}
	}

	body := &types.Body{
		Transactions: txs,
		Uncles:       uncles,
	}

	// If RLP encoded body is provided, prefer it
	if len(block.Body) > 0 {
		var decodedBody types.Body
		if err := rlp.DecodeBytes(block.Body, &decodedBody); err == nil {
			body = &decodedBody
		}
	}

	return header, body, nil
}

// convertTransaction converts migrate.Transaction to geth types.Transaction
func (i *Importer) convertTransaction(mtx *migrate.Transaction) *types.Transaction {
	if mtx == nil {
		return nil
	}

	var tx *types.Transaction

	// Determine transaction type based on fields present
	if mtx.GasFeeCap != nil && mtx.GasTipCap != nil {
		// EIP-1559 dynamic fee transaction
		inner := &types.DynamicFeeTx{
			Nonce:     mtx.Nonce,
			GasTipCap: mtx.GasTipCap,
			GasFeeCap: mtx.GasFeeCap,
			Gas:       mtx.Gas,
			To:        mtx.To,
			Value:     mtx.Value,
			Data:      mtx.Data,
		}
		if mtx.AccessList != nil {
			inner.AccessList = mtx.AccessList
		}
		if mtx.V != nil && mtx.R != nil && mtx.S != nil {
			inner.V = mtx.V
			inner.R = mtx.R
			inner.S = mtx.S
		}
		tx = types.NewTx(inner)
	} else if mtx.AccessList != nil {
		// EIP-2930 access list transaction
		inner := &types.AccessListTx{
			Nonce:      mtx.Nonce,
			GasPrice:   mtx.GasPrice,
			Gas:        mtx.Gas,
			To:         mtx.To,
			Value:      mtx.Value,
			Data:       mtx.Data,
			AccessList: mtx.AccessList,
		}
		if mtx.V != nil && mtx.R != nil && mtx.S != nil {
			inner.V = mtx.V
			inner.R = mtx.R
			inner.S = mtx.S
		}
		tx = types.NewTx(inner)
	} else {
		// Legacy transaction
		inner := &types.LegacyTx{
			Nonce:    mtx.Nonce,
			GasPrice: mtx.GasPrice,
			Gas:      mtx.Gas,
			To:       mtx.To,
			Value:    mtx.Value,
			Data:     mtx.Data,
		}
		if mtx.V != nil && mtx.R != nil && mtx.S != nil {
			inner.V = mtx.V
			inner.R = mtx.R
			inner.S = mtx.S
		}
		tx = types.NewTx(inner)
	}

	return tx
}

// writeHeader writes a header to the batch
func (i *Importer) writeHeader(header *types.Header) {
	hash := header.Hash()
	number := header.Number.Uint64()

	// Encode header
	data, err := rlp.EncodeToBytes(header)
	if err != nil {
		return
	}

	// headerKey = headerPrefix + num (uint64 big endian) + hash
	key := headerKey(number, hash)
	i.batch.Put(key, data)
}

// writeBody writes a block body to the batch
func (i *Importer) writeBody(hash common.Hash, number uint64, body *types.Body) {
	data, err := rlp.EncodeToBytes(body)
	if err != nil {
		return
	}
	key := blockBodyKey(number, hash)
	i.batch.Put(key, data)
}

// writeCanonicalHash writes the canonical hash for a block number
func (i *Importer) writeCanonicalHash(number uint64, hash common.Hash) {
	key := headerHashKey(number)
	i.batch.Put(key, hash.Bytes())
}

// writeHeaderNumber writes the hash to number mapping
func (i *Importer) writeHeaderNumber(hash common.Hash, number uint64) {
	key := headerNumberKey(hash)
	enc := encodeBlockNumber(number)
	i.batch.Put(key, enc)
}

// writeReceipts writes receipts to the batch
func (i *Importer) writeReceipts(hash common.Hash, number uint64, receiptsRLP []byte) {
	key := blockReceiptsKey(number, hash)
	i.batch.Put(key, receiptsRLP)
}

// writeTxLookup writes a transaction lookup entry
func (i *Importer) writeTxLookup(txHash, blockHash common.Hash, blockNumber, txIndex uint64) {
	// txLookupKey = txLookupPrefix + txHash
	key := txLookupKey(txHash)
	// Store block number for the lookup (simplified format)
	enc := encodeBlockNumber(blockNumber)
	i.batch.Put(key, enc)
}

// writeAccountSnapshot writes an account snapshot
func (i *Importer) writeAccountSnapshot(addr common.Address, account *migrate.Account) error {
	if account == nil {
		return nil
	}

	// Serialize account for snapshot
	// Format: [nonce, balance, storageRoot, codeHash]
	var codeHash common.Hash
	if len(account.Code) > 0 {
		codeHash = crypto.Keccak256Hash(account.Code)
	} else {
		codeHash = account.CodeHash
	}

	// Use slim account encoding for snapshots
	data := encodeSlimAccount(account.Nonce, account.Balance, account.StorageRoot, codeHash)

	key := accountSnapshotKey(crypto.Keccak256Hash(addr.Bytes()))
	return i.batch.Put(key, data)
}

// writeStorageSnapshot writes a storage slot snapshot
func (i *Importer) writeStorageSnapshot(addr common.Address, slot, value common.Hash) error {
	addrHash := crypto.Keccak256Hash(addr.Bytes())
	slotHash := crypto.Keccak256Hash(slot.Bytes())
	key := storageSnapshotKey(addrHash, slotHash)
	return i.batch.Put(key, value.Bytes())
}

// ImportBlocks imports multiple blocks in batch
func (i *Importer) ImportBlocks(blocks []*migrate.BlockData) error {
	if !i.initialized {
		return migrate.ErrNotInitialized
	}

	for _, block := range blocks {
		if err := i.ImportBlock(block); err != nil {
			return err
		}
	}

	return i.flushBatch()
}

// ImportState imports state accounts at a specific height
func (i *Importer) ImportState(accounts []*migrate.Account, blockNumber uint64) error {
	if !i.initialized {
		return migrate.ErrNotInitialized
	}

	for _, account := range accounts {
		if account == nil {
			continue
		}

		// Write account code if present
		if len(account.Code) > 0 {
			codeHash := crypto.Keccak256Hash(account.Code)
			rawdb.WriteCode(i.batch, codeHash, account.Code)
		}

		// Write account snapshot
		if err := i.writeAccountSnapshot(account.Address, account); err != nil {
			return err
		}

		// Write storage slots
		for slot, value := range account.Storage {
			if err := i.writeStorageSnapshot(account.Address, slot, value); err != nil {
				return err
			}
		}
	}

	return i.flushBatch()
}

// FinalizeImport finalizes the import at a block height
func (i *Importer) FinalizeImport(blockNumber uint64) error {
	if !i.initialized {
		return migrate.ErrNotInitialized
	}

	// Flush any remaining batched writes
	if err := i.flushBatch(); err != nil {
		return err
	}

	// Read the hash for this block number
	hash := rawdb.ReadCanonicalHash(i.db, blockNumber)
	if hash == (common.Hash{}) {
		return fmt.Errorf("block %d not found", blockNumber)
	}

	// Update head block hash
	rawdb.WriteHeadBlockHash(i.db, hash)
	rawdb.WriteHeadHeaderHash(i.db, hash)
	rawdb.WriteHeadFastBlockHash(i.db, hash)

	return nil
}

// VerifyImport verifies import integrity at a block height
func (i *Importer) VerifyImport(blockNumber uint64) error {
	if !i.initialized {
		return migrate.ErrNotInitialized
	}

	// Verify canonical hash exists
	hash := rawdb.ReadCanonicalHash(i.db, blockNumber)
	if hash == (common.Hash{}) {
		return fmt.Errorf("canonical hash not found for block %d", blockNumber)
	}

	// Verify header exists
	header := rawdb.ReadHeader(i.db, hash, blockNumber)
	if header == nil {
		return fmt.Errorf("header not found for block %d", blockNumber)
	}

	// Verify body exists
	body := rawdb.ReadBody(i.db, hash, blockNumber)
	if body == nil {
		return fmt.Errorf("body not found for block %d", blockNumber)
	}

	// Verify hash matches header hash
	if header.Hash() != hash {
		return fmt.Errorf("header hash mismatch at block %d: expected %s, got %s",
			blockNumber, hash.Hex(), header.Hash().Hex())
	}

	return nil
}

// ExecuteBlock executes a block to rebuild state (for runtime replay)
func (i *Importer) ExecuteBlock(block *migrate.BlockData) error {
	// For direct database import, we just import the block
	// Full execution requires EVM which is out of scope for direct DB import
	return i.ImportBlock(block)
}

// Close closes the importer and releases resources
func (i *Importer) Close() error {
	if i.closed {
		return nil
	}

	// Flush any pending writes
	if i.batch != nil {
		i.flushBatch()
	}

	// Close database
	if i.db != nil {
		if err := i.db.Close(); err != nil {
			return err
		}
	}

	i.closed = true
	i.initialized = false
	return nil
}

// flushBatch writes all batched data to the database
func (i *Importer) flushBatch() error {
	i.batchMu.Lock()
	defer i.batchMu.Unlock()
	return i.flushBatchLocked()
}

func (i *Importer) flushBatchLocked() error {
	if i.batch == nil || i.batch.ValueSize() == 0 {
		return nil
	}

	if err := i.batch.Write(); err != nil {
		return fmt.Errorf("failed to write batch: %w", err)
	}
	i.batch.Reset()
	return nil
}

// NOTE: Key encoding functions are defined in schema.go

// encodeSlimAccount encodes account data for snapshot storage
func encodeSlimAccount(nonce uint64, balance *big.Int, storageRoot, codeHash common.Hash) []byte {
	// Slim account encoding: [nonce, balance, storageRoot, codeHash]
	// Using RLP for compatibility
	type slimAccount struct {
		Nonce       uint64
		Balance     *big.Int
		StorageRoot common.Hash
		CodeHash    common.Hash
	}

	acc := slimAccount{
		Nonce:       nonce,
		Balance:     balance,
		StorageRoot: storageRoot,
		CodeHash:    codeHash,
	}

	data, _ := rlp.EncodeToBytes(acc)
	return data
}

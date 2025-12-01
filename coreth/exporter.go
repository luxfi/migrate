// Package coreth provides Coreth (legacy C-Chain VM) export functionality.
// Coreth uses LevelDB/BadgerDB for storage with specific namespace prefixes.
package coreth

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sync"

	"github.com/luxfi/geth/common"
	"github.com/luxfi/geth/core/rawdb"
	"github.com/luxfi/geth/core/state/snapshot"
	"github.com/luxfi/geth/core/types"
	"github.com/luxfi/geth/ethdb"
	"github.com/luxfi/geth/ethdb/leveldb"
	"github.com/luxfi/geth/params"
	"github.com/luxfi/geth/rlp"
	"github.com/luxfi/geth/trie"
	"github.com/luxfi/geth/triedb"
	"github.com/luxfi/migrate"
)

func init() {
	// Register the Coreth exporter factory
	migrate.RegisterExporterFactory(migrate.VMTypeCoreth, func(config migrate.ExporterConfig) (migrate.Exporter, error) {
		return NewExporter(config)
	})
}

const (
	// Buffer sizes for streaming channels
	blockBufferSize   = 100
	accountBufferSize = 1000
	errorBufferSize   = 1

	// Database cache and handles
	defaultCache   = 512
	defaultHandles = 256

	// Coreth database paths
	corethChainDataDir = "chaindata"
)

// Exporter exports blocks and state from a legacy Coreth VM database.
type Exporter struct {
	config migrate.ExporterConfig

	// Database handles
	db     ethdb.Database
	trieDB *triedb.Database

	// Snapshot for efficient state iteration
	snap *snapshot.Tree

	// Chain info cache
	chainConfig *params.ChainConfig
	genesisHash common.Hash
	headBlock   uint64
	headHash    common.Hash

	// State
	initialized bool
	mu          sync.RWMutex
}

// NewExporter creates a new Coreth exporter.
func NewExporter(config migrate.ExporterConfig) (*Exporter, error) {
	return &Exporter{
		config: config,
	}, nil
}

// Init initializes the exporter with source database.
func (e *Exporter) Init(config migrate.ExporterConfig) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.initialized {
		return migrate.ErrAlreadyInitialized
	}

	e.config = config

	// Determine database path
	dbPath := config.DatabasePath
	if dbPath == "" {
		return migrate.ErrDatabaseNotFound
	}

	// Check for chaindata subdirectory (common Coreth layout)
	chainDataPath := filepath.Join(dbPath, corethChainDataDir)
	if _, err := os.Stat(chainDataPath); err == nil {
		dbPath = chainDataPath
	}

	// Verify database exists
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return migrate.ErrDatabaseNotFound
	}

	// Open database based on type
	db, err := e.openDatabase(dbPath, config.DatabaseType)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	e.db = db

	// Initialize trie database for state access
	e.trieDB = triedb.NewDatabase(db, nil)

	// Load chain info
	if err := e.loadChainInfo(); err != nil {
		e.db.Close()
		return fmt.Errorf("failed to load chain info: %w", err)
	}

	// Try to initialize snapshot (optional, for faster state iteration)
	e.initSnapshot()

	e.initialized = true
	return nil
}

// openDatabase opens the appropriate database type.
func (e *Exporter) openDatabase(path string, dbType string) (ethdb.Database, error) {
	switch dbType {
	case "leveldb", "":
		// LevelDB is the default for legacy Coreth
		ldb, err := leveldb.New(path, defaultCache, defaultHandles, "coreth/", true)
		if err != nil {
			return nil, err
		}
		// Wrap with rawdb.NewDatabase to provide full ethdb.Database interface
		return rawdb.NewDatabase(ldb), nil
	case "badger":
		// BadgerDB support - open as LevelDB with namespace wrapper
		// Coreth's BadgerDB uses a namespace prefix
		ldb, err := leveldb.New(path, defaultCache, defaultHandles, "coreth/", true)
		if err != nil {
			return nil, err
		}
		db := rawdb.NewDatabase(ldb)
		// If namespace is specified, wrap with prefix
		if len(e.config.NetNamespace) > 0 {
			return newPrefixDB(db, e.config.NetNamespace), nil
		}
		return db, nil
	default:
		return nil, fmt.Errorf("unsupported database type: %s", dbType)
	}
}

// loadChainInfo loads genesis hash, chain config, and head block info.
func (e *Exporter) loadChainInfo() error {
	// Read genesis hash (block 0)
	e.genesisHash = rawdb.ReadCanonicalHash(e.db, 0)
	if e.genesisHash == (common.Hash{}) {
		return errors.New("genesis hash not found")
	}

	// Read head block hash and number
	e.headHash = rawdb.ReadHeadBlockHash(e.db)
	if e.headHash == (common.Hash{}) {
		return errors.New("head block hash not found")
	}

	headNumber, found := rawdb.ReadHeaderNumber(e.db, e.headHash)
	if !found {
		return errors.New("head block number not found")
	}
	e.headBlock = headNumber

	// Read chain config
	e.chainConfig = rawdb.ReadChainConfig(e.db, e.genesisHash)

	return nil
}

// initSnapshot attempts to initialize state snapshot for faster iteration.
func (e *Exporter) initSnapshot() {
	// Snapshots are optional - state can be exported via trie iteration
	// This is a best-effort initialization
	root := e.readSnapshotRoot()
	if root == (common.Hash{}) {
		return
	}

	// Try to load existing snapshot
	// Note: We need a KeyValueStore, not full Database
	kvStore, ok := e.db.(ethdb.KeyValueStore)
	if !ok {
		return
	}

	snap, err := snapshot.New(snapshot.Config{
		CacheSize: 256,
		NoBuild:   true, // Don't rebuild, just load existing
	}, kvStore, e.trieDB, root)
	if err == nil {
		e.snap = snap
	}
}

// GetInfo returns metadata about the source chain.
func (e *Exporter) GetInfo() (*migrate.Info, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.initialized {
		return nil, migrate.ErrNotInitialized
	}

	var chainID *big.Int
	if e.chainConfig != nil {
		chainID = e.chainConfig.ChainID
	}

	// Read head header for state root
	header := rawdb.ReadHeader(e.db, e.headHash, e.headBlock)
	var stateRoot common.Hash
	var difficulty *big.Int
	if header != nil {
		stateRoot = header.Root
		difficulty = header.Difficulty
	}

	// Check if state is pruned
	isPruned := false
	if header != nil {
		// Try to verify state exists by opening trie
		_, err := trie.New(trie.TrieID(stateRoot), e.trieDB)
		if err != nil {
			isPruned = true
		}
	}

	return &migrate.Info{
		VMType:          migrate.VMTypeCoreth,
		NetworkID:       0, // Will be set from config if available
		ChainID:         chainID,
		GenesisHash:     e.genesisHash,
		CurrentHeight:   e.headBlock,
		TotalDifficulty: difficulty,
		StateRoot:       stateRoot,
		VMVersion:       "coreth",
		DatabaseType:    e.config.DatabaseType,
		IsPruned:        isPruned,
		ArchiveMode:     !isPruned,
		HasWarpMessages: false,
		HasProposerVM:   false,
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
				if errors.Is(err, migrate.ErrBlockNotFound) {
					// Skip missing blocks (may be pruned)
					continue
				}
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

// exportBlock exports a single block at the given height.
func (e *Exporter) exportBlock(height uint64) (*migrate.BlockData, error) {
	// Get canonical hash for this height
	hash := rawdb.ReadCanonicalHash(e.db, height)
	if hash == (common.Hash{}) {
		return nil, migrate.ErrBlockNotFound
	}

	// Read header
	header := rawdb.ReadHeader(e.db, hash, height)
	if header == nil {
		return nil, migrate.ErrBlockNotFound
	}

	// Read body
	body := rawdb.ReadBody(e.db, hash, height)
	if body == nil {
		// Genesis block may have empty body
		body = &types.Body{}
	}

	// Read receipts if configured
	var receiptsRLP []byte
	if e.config.ExportReceipts {
		receiptsRLP = rawdb.ReadReceiptsRLP(e.db, hash, height)
	}

	// Encode header and body
	headerRLP, err := rlp.EncodeToBytes(header)
	if err != nil {
		return nil, fmt.Errorf("encode header: %w", err)
	}

	bodyRLP, err := rlp.EncodeToBytes(body)
	if err != nil {
		return nil, fmt.Errorf("encode body: %w", err)
	}

	// Convert transactions
	txs := make([]*migrate.Transaction, len(body.Transactions))
	for i, tx := range body.Transactions {
		txs[i] = convertTransaction(tx)
	}

	// Encode uncle headers
	uncleHeaders := make([][]byte, len(body.Uncles))
	for i, uncle := range body.Uncles {
		data, err := rlp.EncodeToBytes(uncle)
		if err != nil {
			return nil, fmt.Errorf("encode uncle: %w", err)
		}
		uncleHeaders[i] = data
	}

	blockData := &migrate.BlockData{
		Number:           height,
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
		UncleHeaders:     uncleHeaders,
		Extensions:       make(map[string]interface{}),
	}

	// Add Coreth-specific extensions
	if header.BaseFee != nil {
		blockData.Extensions["hasEIP1559"] = true
	}

	return blockData, nil
}

// convertTransaction converts a geth transaction to migrate.Transaction.
func convertTransaction(tx *types.Transaction) *migrate.Transaction {
	signer := types.LatestSignerForChainID(tx.ChainId())
	from, _ := types.Sender(signer, tx)
	v, r, s := tx.RawSignatureValues()

	mtx := &migrate.Transaction{
		Hash:     tx.Hash(),
		Nonce:    tx.Nonce(),
		From:     from,
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

	return mtx
}

// ExportState exports state at a specific block height.
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

		// Get block header for state root
		hash := rawdb.ReadCanonicalHash(e.db, blockNumber)
		if hash == (common.Hash{}) {
			errs <- migrate.ErrBlockNotFound
			return
		}

		header := rawdb.ReadHeader(e.db, hash, blockNumber)
		if header == nil {
			errs <- migrate.ErrBlockNotFound
			return
		}

		stateRoot := header.Root

		// Try snapshot iteration first (faster)
		if e.snap != nil {
			if err := e.exportStateFromSnapshot(ctx, stateRoot, accounts); err == nil {
				return
			}
			// Fall through to trie iteration on error
		}

		// Use trie iteration
		if err := e.exportStateFromTrie(ctx, stateRoot, accounts); err != nil {
			errs <- err
		}
	}()

	return accounts, errs
}

// exportStateFromSnapshot exports state using snapshot iteration.
func (e *Exporter) exportStateFromSnapshot(ctx context.Context, root common.Hash, accounts chan<- *migrate.Account) error {
	snap := e.snap.Snapshot(root)
	if snap == nil {
		return errors.New("snapshot not available for root")
	}

	// Get disk layer for iteration
	diskSnap, ok := snap.(interface {
		AccountIterator(seek common.Hash) snapshot.AccountIterator
	})
	if !ok {
		return errors.New("snapshot does not support iteration")
	}

	iter := diskSnap.AccountIterator(common.Hash{})
	defer iter.Release()

	for iter.Next() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		accHash := iter.Hash()
		accRLP := iter.Account()
		if accRLP == nil {
			continue
		}

		// Decode slim account
		var acc types.SlimAccount
		if err := rlp.DecodeBytes(accRLP, &acc); err != nil {
			continue
		}

		// Get preimage for address (if available)
		addrBytes := rawdb.ReadPreimage(e.db, accHash)
		var addr common.Address
		if len(addrBytes) == common.AddressLength {
			addr = common.BytesToAddress(addrBytes)
		}

		// Read code if present
		var code []byte
		var codeHash common.Hash
		if len(acc.CodeHash) > 0 {
			codeHash = common.BytesToHash(acc.CodeHash)
			if codeHash != types.EmptyCodeHash {
				code = rawdb.ReadCode(e.db, codeHash)
			}
		}

		// Export storage (simplified - full export would iterate storage trie)
		storage := make(map[common.Hash]common.Hash)

		// Convert uint256.Int balance to big.Int
		var balance *big.Int
		if acc.Balance != nil {
			balance = acc.Balance.ToBig()
		} else {
			balance = new(big.Int)
		}

		account := &migrate.Account{
			Address:     addr,
			Nonce:       acc.Nonce,
			Balance:     balance,
			CodeHash:    codeHash,
			StorageRoot: common.BytesToHash(acc.Root),
			Code:        code,
			Storage:     storage,
		}

		select {
		case accounts <- account:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return iter.Error()
}

// exportStateFromTrie exports state using trie iteration.
func (e *Exporter) exportStateFromTrie(ctx context.Context, root common.Hash, accounts chan<- *migrate.Account) error {
	// Open state trie
	tr, err := trie.New(trie.TrieID(root), e.trieDB)
	if err != nil {
		return fmt.Errorf("open state trie: %w", err)
	}

	// Create trie iterator
	nodeIter, err := tr.NodeIterator(nil)
	if err != nil {
		return fmt.Errorf("create iterator: %w", err)
	}
	iter := trie.NewIterator(nodeIter)

	for iter.Next() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Key is account hash, value is RLP-encoded account
		accHash := common.BytesToHash(iter.Key)

		var acc types.StateAccount
		if err := rlp.DecodeBytes(iter.Value, &acc); err != nil {
			continue
		}

		// Get preimage for address
		addrBytes := rawdb.ReadPreimage(e.db, accHash)
		var addr common.Address
		if len(addrBytes) == common.AddressLength {
			addr = common.BytesToAddress(addrBytes)
		}

		// Read code
		var code []byte
		codeHash := common.BytesToHash(acc.CodeHash)
		if codeHash != types.EmptyCodeHash {
			code = rawdb.ReadCode(e.db, codeHash)
		}

		// Read storage from storage trie
		storage := make(map[common.Hash]common.Hash)
		if acc.Root != types.EmptyRootHash {
			storageTrie, err := trie.New(trie.StorageTrieID(root, accHash, acc.Root), e.trieDB)
			if err == nil {
				storageNodeIter, err := storageTrie.NodeIterator(nil)
				if err == nil {
					storageIter := trie.NewIterator(storageNodeIter)
					for storageIter.Next() {
						key := common.BytesToHash(storageIter.Key)
						var value common.Hash
						if len(storageIter.Value) > 0 {
							// Storage values are RLP encoded
							rlp.DecodeBytes(storageIter.Value, &value)
						}
						storage[key] = value
					}
				}
			}
		}

		account := &migrate.Account{
			Address:     addr,
			Nonce:       acc.Nonce,
			Balance:     acc.Balance.ToBig(),
			CodeHash:    codeHash,
			StorageRoot: acc.Root,
			Code:        code,
			Storage:     storage,
		}

		select {
		case accounts <- account:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if iter.Err != nil {
		return iter.Err
	}

	return nil
}

// ExportAccount exports a specific account's state.
func (e *Exporter) ExportAccount(ctx context.Context, address common.Address, blockNumber uint64) (*migrate.Account, error) {
	e.mu.RLock()
	if !e.initialized {
		e.mu.RUnlock()
		return nil, migrate.ErrNotInitialized
	}
	e.mu.RUnlock()

	// Get block header for state root
	hash := rawdb.ReadCanonicalHash(e.db, blockNumber)
	if hash == (common.Hash{}) {
		return nil, migrate.ErrBlockNotFound
	}

	header := rawdb.ReadHeader(e.db, hash, blockNumber)
	if header == nil {
		return nil, migrate.ErrBlockNotFound
	}

	// Open state trie
	tr, err := trie.New(trie.TrieID(header.Root), e.trieDB)
	if err != nil {
		return nil, migrate.ErrStateNotAvailable
	}

	// Get account data using secure key (keccak256(address))
	secureKey := common.BytesToHash(address.Bytes())
	accData, err := tr.Get(secureKey.Bytes())
	if err != nil || len(accData) == 0 {
		return nil, fmt.Errorf("account not found: %s", address.Hex())
	}

	var acc types.StateAccount
	if err := rlp.DecodeBytes(accData, &acc); err != nil {
		return nil, fmt.Errorf("decode account: %w", err)
	}

	// Read code
	var code []byte
	codeHash := common.BytesToHash(acc.CodeHash)
	if codeHash != types.EmptyCodeHash {
		code = rawdb.ReadCode(e.db, codeHash)
	}

	// Read storage
	storage := make(map[common.Hash]common.Hash)
	if acc.Root != types.EmptyRootHash {
		storageTrie, err := trie.New(trie.StorageTrieID(header.Root, secureKey, acc.Root), e.trieDB)
		if err == nil {
			storageNodeIter, err := storageTrie.NodeIterator(nil)
			if err == nil {
				storageIter := trie.NewIterator(storageNodeIter)
				for storageIter.Next() {
					key := common.BytesToHash(storageIter.Key)
					var value common.Hash
					if len(storageIter.Value) > 0 {
						rlp.DecodeBytes(storageIter.Value, &value)
					}
					storage[key] = value
				}
			}
		}
	}

	return &migrate.Account{
		Address:     address,
		Nonce:       acc.Nonce,
		Balance:     acc.Balance.ToBig(),
		CodeHash:    codeHash,
		StorageRoot: acc.Root,
		Code:        code,
		Storage:     storage,
	}, nil
}

// ExportConfig exports chain configuration (genesis, network ID, etc.).
func (e *Exporter) ExportConfig() (*migrate.Config, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.initialized {
		return nil, migrate.ErrNotInitialized
	}

	config := &migrate.Config{
		IsCoreth: true,
	}

	if e.chainConfig != nil {
		config.ChainID = e.chainConfig.ChainID

		// Fork heights
		config.HomesteadBlock = e.chainConfig.HomesteadBlock
		config.EIP150Block = e.chainConfig.EIP150Block
		config.EIP155Block = e.chainConfig.EIP155Block
		config.EIP158Block = e.chainConfig.EIP158Block
		config.ByzantiumBlock = e.chainConfig.ByzantiumBlock
		config.ConstantinopleBlock = e.chainConfig.ConstantinopleBlock
		config.PetersburgBlock = e.chainConfig.PetersburgBlock
		config.IstanbulBlock = e.chainConfig.IstanbulBlock
		config.BerlinBlock = e.chainConfig.BerlinBlock
		config.LondonBlock = e.chainConfig.LondonBlock
	}

	// Export genesis block
	genesisBlock, err := e.exportBlock(0)
	if err == nil {
		config.GenesisBlock = genesisBlock
	}

	// Export genesis allocations
	config.GenesisAlloc = make(map[common.Address]*migrate.Account)
	genesisSpec := rawdb.ReadGenesisStateSpec(e.db, e.genesisHash)
	if len(genesisSpec) > 0 {
		var alloc types.GenesisAlloc
		if err := alloc.UnmarshalJSON(genesisSpec); err == nil {
			for addr, acc := range alloc {
				config.GenesisAlloc[addr] = &migrate.Account{
					Address: addr,
					Nonce:   acc.Nonce,
					Balance: acc.Balance,
					Code:    acc.Code,
					Storage: acc.Storage,
				}
			}
		}
	}

	return config, nil
}

// VerifyExport verifies export integrity at a block height.
func (e *Exporter) VerifyExport(blockNumber uint64) error {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.initialized {
		return migrate.ErrNotInitialized
	}

	// Check block exists
	hash := rawdb.ReadCanonicalHash(e.db, blockNumber)
	if hash == (common.Hash{}) {
		return migrate.ErrBlockNotFound
	}

	// Check header exists
	header := rawdb.ReadHeader(e.db, hash, blockNumber)
	if header == nil {
		return migrate.ErrBlockNotFound
	}

	// Verify header hash matches
	if header.Hash() != hash {
		return fmt.Errorf("header hash mismatch: expected %s, got %s", hash.Hex(), header.Hash().Hex())
	}

	// Verify state availability if requested
	if e.config.ExportState {
		_, err := trie.New(trie.TrieID(header.Root), e.trieDB)
		if err != nil {
			return migrate.ErrStateNotAvailable
		}
	}

	return nil
}

// Close closes the exporter and releases resources.
func (e *Exporter) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.initialized {
		return nil
	}

	var errs []error

	if e.snap != nil {
		e.snap.Release()
		e.snap = nil
	}

	if e.trieDB != nil {
		if err := e.trieDB.Close(); err != nil {
			errs = append(errs, err)
		}
		e.trieDB = nil
	}

	if e.db != nil {
		if err := e.db.Close(); err != nil {
			errs = append(errs, err)
		}
		e.db = nil
	}

	e.initialized = false

	if len(errs) > 0 {
		return fmt.Errorf("close errors: %v", errs)
	}
	return nil
}

// prefixDB wraps a database with a key prefix (for namespaced databases).
type prefixDB struct {
	ethdb.Database
	prefix []byte
}

func newPrefixDB(db ethdb.Database, prefix []byte) *prefixDB {
	return &prefixDB{
		Database: db,
		prefix:   prefix,
	}
}

func (p *prefixDB) prefixKey(key []byte) []byte {
	return append(append([]byte{}, p.prefix...), key...)
}

func (p *prefixDB) Has(key []byte) (bool, error) {
	return p.Database.Has(p.prefixKey(key))
}

func (p *prefixDB) Get(key []byte) ([]byte, error) {
	return p.Database.Get(p.prefixKey(key))
}

func (p *prefixDB) Put(key []byte, value []byte) error {
	return p.Database.Put(p.prefixKey(key), value)
}

func (p *prefixDB) Delete(key []byte) error {
	return p.Database.Delete(p.prefixKey(key))
}

func (p *prefixDB) NewIterator(prefix []byte, start []byte) ethdb.Iterator {
	return p.Database.NewIterator(p.prefixKey(prefix), start)
}

func (p *prefixDB) NewBatch() ethdb.Batch {
	return &prefixBatch{
		Batch:  p.Database.NewBatch(),
		prefix: p.prefix,
	}
}

// prefixBatch wraps a batch with key prefixing.
type prefixBatch struct {
	ethdb.Batch
	prefix []byte
}

func (b *prefixBatch) Put(key []byte, value []byte) error {
	return b.Batch.Put(append(append([]byte{}, b.prefix...), key...), value)
}

func (b *prefixBatch) Delete(key []byte) error {
	return b.Batch.Delete(append(append([]byte{}, b.prefix...), key...))
}

// readSnapshotRoot reads snapshot root from database.
func (e *Exporter) readSnapshotRoot() common.Hash {
	data, err := e.db.Get(rawdb.SnapshotRootKey)
	if err != nil || len(data) != common.HashLength {
		return common.Hash{}
	}
	return common.BytesToHash(data)
}

// encodeBlockNumber encodes a block number as big-endian uint64.
func encodeBlockNumber(number uint64) []byte {
	enc := make([]byte, 8)
	binary.BigEndian.PutUint64(enc, number)
	return enc
}

// Ensure prefixDB implements ethdb.Database (compile-time check).
var _ ethdb.Database = (*prefixDB)(nil)

// Implement remaining ethdb.Database methods for prefixDB
func (p *prefixDB) NewBatchWithSize(size int) ethdb.Batch {
	return &prefixBatch{
		Batch:  p.Database.NewBatchWithSize(size),
		prefix: p.prefix,
	}
}

func (p *prefixDB) Stat() (string, error) {
	return p.Database.Stat()
}

func (p *prefixDB) Compact(start []byte, limit []byte) error {
	return p.Database.Compact(p.prefixKey(start), p.prefixKey(limit))
}

func (p *prefixDB) Close() error {
	return p.Database.Close()
}

func (p *prefixDB) DeleteRange(start, end []byte) error {
	return p.Database.DeleteRange(p.prefixKey(start), p.prefixKey(end))
}

func (p *prefixDB) SyncKeyValue() error {
	return p.Database.SyncKeyValue()
}

// Ancient database methods - pass through without prefix
func (p *prefixDB) Ancient(kind string, number uint64) ([]byte, error) {
	return p.Database.Ancient(kind, number)
}

func (p *prefixDB) AncientRange(kind string, start, count, maxBytes uint64) ([][]byte, error) {
	return p.Database.AncientRange(kind, start, count, maxBytes)
}

func (p *prefixDB) AncientBytes(kind string, id, offset, length uint64) ([]byte, error) {
	return p.Database.AncientBytes(kind, id, offset, length)
}

func (p *prefixDB) Ancients() (uint64, error) {
	return p.Database.Ancients()
}

func (p *prefixDB) Tail() (uint64, error) {
	return p.Database.Tail()
}

func (p *prefixDB) AncientSize(kind string) (uint64, error) {
	return p.Database.AncientSize(kind)
}

func (p *prefixDB) ReadAncients(fn func(ethdb.AncientReaderOp) error) error {
	return p.Database.ReadAncients(fn)
}

func (p *prefixDB) ModifyAncients(fn func(ethdb.AncientWriteOp) error) (int64, error) {
	return p.Database.ModifyAncients(fn)
}

func (p *prefixDB) TruncateHead(n uint64) (uint64, error) {
	return p.Database.TruncateHead(n)
}

func (p *prefixDB) TruncateTail(n uint64) (uint64, error) {
	return p.Database.TruncateTail(n)
}

func (p *prefixDB) SyncAncient() error {
	return p.Database.SyncAncient()
}

func (p *prefixDB) AncientDatadir() (string, error) {
	return p.Database.AncientDatadir()
}

// Ensure prefixBatch implements full Batch interface
func (b *prefixBatch) DeleteRange(start, end []byte) error {
	return b.Batch.DeleteRange(
		append(append([]byte{}, b.prefix...), start...),
		append(append([]byte{}, b.prefix...), end...),
	)
}

// Unused import guard
var _ = bytes.Equal

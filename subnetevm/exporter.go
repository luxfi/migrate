// Package subnetevm provides SubnetEVM-specific export functionality
// for migrating blockchain data from SubnetEVM PebbleDB databases.
package subnetevm

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/cockroachdb/pebble"
	"github.com/luxfi/geth/common"
	"github.com/luxfi/geth/core/types"
	"github.com/luxfi/geth/crypto"
	"github.com/luxfi/geth/rlp"
	"github.com/luxfi/migrate"
)

// NOTE: Database key prefixes and encoding functions are defined in schema.go

// Exporter exports blocks from SubnetEVM PebbleDB
type Exporter struct {
	config      migrate.ExporterConfig
	db          *pebble.DB
	initialized bool
	mu          sync.RWMutex

	// Cached chain info
	chainID     *big.Int
	genesisHash common.Hash
	headBlock   uint64

	// Chain ID prefix for databases with prefixed keys (32 bytes)
	// Some SubnetEVM databases (e.g., ZOO) have all keys prefixed with the chain ID
	chainIDPrefix []byte
}

// NewExporter creates a new SubnetEVM exporter
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
	opts := &pebble.Options{
		ReadOnly: true,
	}
	db, err := pebble.Open(config.DatabasePath, opts)
	if err != nil {
		return fmt.Errorf("failed to open pebble database: %w", err)
	}
	e.db = db

	// Read chain info
	if err := e.loadChainInfo(); err != nil {
		db.Close()
		return fmt.Errorf("failed to load chain info: %w", err)
	}

	e.initialized = true
	return nil
}

// loadChainInfo reads basic chain information from the database
func (e *Exporter) loadChainInfo() error {
	// First, try to detect if this database has prefixed keys
	// by scanning for header prefix patterns
	if err := e.detectChainIDPrefix(); err != nil {
		// Not a fatal error - continue without prefix
		e.chainIDPrefix = nil
	}

	// Read head block hash
	headHash, err := e.get(e.prefixKey(headBlockKey))
	if err != nil {
		return fmt.Errorf("failed to read head block: %w", err)
	}
	if len(headHash) == 0 {
		return errors.New("head block not found")
	}

	// Read head block number
	hash := common.BytesToHash(headHash)
	number, err := e.readHeaderNumber(hash)
	if err != nil {
		return fmt.Errorf("failed to read head block number: %w", err)
	}
	e.headBlock = number

	// Read genesis hash (block 0)
	genesisHash := e.readCanonicalHash(0)
	if genesisHash == (common.Hash{}) {
		return errors.New("genesis block not found")
	}
	e.genesisHash = genesisHash

	// Read chain config
	configData, err := e.get(e.prefixKey(configKey(genesisHash)))
	if err == nil && len(configData) > 0 {
		var chainConfig struct {
			ChainID *big.Int `json:"chainId"`
		}
		if err := json.Unmarshal(configData, &chainConfig); err == nil {
			e.chainID = chainConfig.ChainID
		}
	}

	return nil
}

// detectChainIDPrefix scans the database to detect if keys have a 32-byte chain ID prefix
func (e *Exporter) detectChainIDPrefix() error {
	// Scan for header prefix "h" - if we find keys starting with 32-byte prefix + "h", we have prefixed keys
	iter, err := e.db.NewIter(&pebble.IterOptions{})
	if err != nil {
		return err
	}
	defer iter.Close()

	// Look for keys that match the pattern: [32-byte prefix]h[8-byte block number]n
	// The "n" suffix indicates a header hash key for a block number
	for iter.First(); iter.Valid(); iter.Next() {
		key := iter.Key()

		// Check for prefixed header hash key pattern: [32 bytes][h][8 bytes][n]
		// Total length: 32 + 1 + 8 + 1 = 42 bytes
		if len(key) == ChainIDPrefixLen+1+8+1 {
			if key[ChainIDPrefixLen] == 'h' && key[ChainIDPrefixLen+1+8] == 'n' {
				// Found a prefixed header hash key - extract the prefix
				e.chainIDPrefix = make([]byte, ChainIDPrefixLen)
				copy(e.chainIDPrefix, key[:ChainIDPrefixLen])
				return nil
			}
		}

		// Also check for non-prefixed standard header hash key: h[8 bytes]n (10 bytes)
		if len(key) == 1+8+1 && key[0] == 'h' && key[9] == 'n' {
			// Standard non-prefixed format
			e.chainIDPrefix = nil
			return nil
		}

		// Check a reasonable number of keys
		// Skip ahead if we haven't found it yet
	}

	return errors.New("could not detect key format")
}

// prefixKey adds the chain ID prefix to a key if the database uses prefixed keys
func (e *Exporter) prefixKey(key []byte) []byte {
	if len(e.chainIDPrefix) == 0 {
		return key
	}
	prefixedKey := make([]byte, len(e.chainIDPrefix)+len(key))
	copy(prefixedKey, e.chainIDPrefix)
	copy(prefixedKey[len(e.chainIDPrefix):], key)
	return prefixedKey
}

// GetInfo returns metadata about the source chain
func (e *Exporter) GetInfo() (*migrate.Info, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.initialized {
		return nil, migrate.ErrNotInitialized
	}

	// Get state root from head block
	headHash := e.readCanonicalHash(e.headBlock)
	header := e.readHeader(headHash, e.headBlock)
	var stateRoot common.Hash
	if header != nil {
		stateRoot = header.Root
	}

	return &migrate.Info{
		VMType:        migrate.VMTypeSubnetEVM,
		ChainID:       e.chainID,
		GenesisHash:   e.genesisHash,
		CurrentHeight: e.headBlock,
		StateRoot:     stateRoot,
		DatabaseType:  "pebble",
		VMVersion:     "subnet-evm",
	}, nil
}

// ExportBlocks exports blocks in a range (inclusive)
func (e *Exporter) ExportBlocks(ctx context.Context, start, end uint64) (<-chan *migrate.BlockData, <-chan error) {
	blocks := make(chan *migrate.BlockData, 100)
	errs := make(chan error, 1)

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
				block, err := e.exportBlock(height)
				if err != nil {
					errs <- fmt.Errorf("failed to export block %d: %w", height, err)
					return
				}
				if block == nil {
					errs <- fmt.Errorf("block %d not found", height)
					return
				}
				blocks <- block
			}
		}
	}()

	return blocks, errs
}

// exportBlock reads and converts a single block from the database
func (e *Exporter) exportBlock(number uint64) (*migrate.BlockData, error) {
	// Get canonical hash for this block number
	hash := e.readCanonicalHash(number)
	if hash == (common.Hash{}) {
		return nil, migrate.ErrBlockNotFound
	}

	// Read header
	header := e.readHeader(hash, number)
	if header == nil {
		return nil, fmt.Errorf("header not found for block %d", number)
	}

	// Read body
	body := e.readBody(hash, number)

	// Read receipts (optional, may not exist for all blocks)
	receiptsRLP := e.readReceiptsRLP(hash, number)

	// Encode header to RLP
	headerRLP, err := rlp.EncodeToBytes(header)
	if err != nil {
		return nil, fmt.Errorf("failed to encode header: %w", err)
	}

	// Encode body to RLP
	var bodyRLP []byte
	if body != nil {
		bodyRLP, err = rlp.EncodeToBytes(body)
		if err != nil {
			return nil, fmt.Errorf("failed to encode body: %w", err)
		}
	}

	// Build block data
	blockData := &migrate.BlockData{
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
		Extensions:       make(map[string]interface{}),
	}

	// Process transactions if body exists
	if body != nil {
		blockData.Transactions = make([]*migrate.Transaction, len(body.Transactions))
		for i, tx := range body.Transactions {
			blockData.Transactions[i] = e.convertTransaction(tx)
		}

		// Encode uncle headers
		if len(body.Uncles) > 0 {
			blockData.UncleHeaders = make([][]byte, len(body.Uncles))
			for i, uncle := range body.Uncles {
				uncleRLP, err := rlp.EncodeToBytes(uncle)
				if err != nil {
					return nil, fmt.Errorf("failed to encode uncle: %w", err)
				}
				blockData.UncleHeaders[i] = uncleRLP
			}
		}
	}

	return blockData, nil
}

// convertTransaction converts a geth transaction to migrate.Transaction
func (e *Exporter) convertTransaction(tx *types.Transaction) *migrate.Transaction {
	var to *common.Address
	if tx.To() != nil {
		addr := *tx.To()
		to = &addr
	}

	// Get signer
	signer := types.LatestSignerForChainID(tx.ChainId())
	from, _ := types.Sender(signer, tx)

	v, r, s := tx.RawSignatureValues()

	migrateTx := &migrate.Transaction{
		Hash:     tx.Hash(),
		Nonce:    tx.Nonce(),
		From:     from,
		To:       to,
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
		migrateTx.GasTipCap = tx.GasTipCap()
		migrateTx.GasFeeCap = tx.GasFeeCap()
	}

	// Access list
	if tx.Type() == types.AccessListTxType || tx.Type() == types.DynamicFeeTxType {
		migrateTx.AccessList = tx.AccessList()
	}

	return migrateTx
}

// ExportState exports state at a specific block height
func (e *Exporter) ExportState(ctx context.Context, blockNumber uint64) (<-chan *migrate.Account, <-chan error) {
	accounts := make(chan *migrate.Account, 100)
	errs := make(chan error, 1)

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

		// Check if snapshot is available
		snapshotRoot, err := e.get(e.prefixKey(snapshotRootKey))
		if err != nil || len(snapshotRoot) == 0 {
			errs <- migrate.ErrStateNotAvailable
			return
		}

		// Build prefixed bounds for snapshot iteration
		lowerBound := e.prefixKey(snapshotAccountPrefix)
		upperBound := e.prefixKey(append(snapshotAccountPrefix, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff))

		// Iterate through snapshot accounts
		iter, err := e.db.NewIter(&pebble.IterOptions{
			LowerBound: lowerBound,
			UpperBound: upperBound,
		})
		if err != nil {
			errs <- fmt.Errorf("failed to create iterator: %w", err)
			return
		}
		defer iter.Close()

		// Expected key length includes prefix
		prefixLen := len(e.chainIDPrefix)
		expectedKeyLen := prefixLen + len(snapshotAccountPrefix) + common.HashLength

		for iter.First(); iter.Valid(); iter.Next() {
			select {
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			default:
			}

			key := iter.Key()
			if len(key) != expectedKeyLen {
				continue
			}

			// Strip prefix and snapshot prefix to get account hash
			accountHash := common.BytesToHash(key[prefixLen+len(snapshotAccountPrefix):])
			value := iter.Value()

			// Decode slim account format
			account, err := e.decodeSnapshotAccount(value)
			if err != nil {
				continue // Skip malformed accounts
			}
			account.Address = common.BytesToAddress(accountHash.Bytes()[12:]) // Approximation, actual address needs preimage

			// Read code if it exists
			if account.CodeHash != (common.Hash{}) && account.CodeHash != crypto.Keccak256Hash(nil) {
				code := e.readCode(account.CodeHash)
				account.Code = code
			}

			// Read storage if available
			account.Storage = e.readAccountStorage(accountHash)

			accounts <- account
		}

		if err := iter.Error(); err != nil {
			errs <- fmt.Errorf("iterator error: %w", err)
		}
	}()

	return accounts, errs
}

// decodeSnapshotAccount decodes a slim account from snapshot format
func (e *Exporter) decodeSnapshotAccount(data []byte) (*migrate.Account, error) {
	// Slim account format: nonce (varint) + balance (big.Int bytes) + storageRoot + codeHash
	if len(data) == 0 {
		return nil, errors.New("empty account data")
	}

	// Use RLP decoding for slim account
	var slimAccount struct {
		Nonce       uint64
		Balance     *big.Int
		Root        []byte
		CodeHash    []byte
	}

	if err := rlp.DecodeBytes(data, &slimAccount); err != nil {
		return nil, err
	}

	account := &migrate.Account{
		Nonce:   slimAccount.Nonce,
		Balance: slimAccount.Balance,
	}

	if len(slimAccount.Root) == common.HashLength {
		account.StorageRoot = common.BytesToHash(slimAccount.Root)
	}
	if len(slimAccount.CodeHash) == common.HashLength {
		account.CodeHash = common.BytesToHash(slimAccount.CodeHash)
	}

	return account, nil
}

// readAccountStorage reads all storage slots for an account
func (e *Exporter) readAccountStorage(accountHash common.Hash) map[common.Hash]common.Hash {
	storage := make(map[common.Hash]common.Hash)

	// Build prefixed storage key bounds
	storagePrefix := append(snapshotStoragePrefix, accountHash.Bytes()...)
	lowerBound := e.prefixKey(storagePrefix)
	upperBound := e.prefixKey(append(storagePrefix, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff))

	iter, err := e.db.NewIter(&pebble.IterOptions{
		LowerBound: lowerBound,
		UpperBound: upperBound,
	})
	if err != nil {
		return storage
	}
	defer iter.Close()

	// Expected key length includes chain ID prefix
	prefixLen := len(e.chainIDPrefix)
	expectedKeyLen := prefixLen + len(snapshotStoragePrefix) + 2*common.HashLength

	for iter.First(); iter.Valid(); iter.Next() {
		key := iter.Key()
		if len(key) != expectedKeyLen {
			continue
		}

		// Strip chain ID prefix and storage prefix + account hash to get storage hash
		storageHash := common.BytesToHash(key[prefixLen+len(snapshotStoragePrefix)+common.HashLength:])
		value := iter.Value()

		if len(value) <= common.HashLength {
			storage[storageHash] = common.BytesToHash(common.LeftPadBytes(value, common.HashLength))
		}
	}

	return storage
}

// ExportAccount exports a specific account's state
func (e *Exporter) ExportAccount(ctx context.Context, address common.Address, blockNumber uint64) (*migrate.Account, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.initialized {
		return nil, migrate.ErrNotInitialized
	}

	// Hash the address for snapshot lookup
	accountHash := crypto.Keccak256Hash(address.Bytes())

	// Read from snapshot (with chain ID prefix)
	key := e.prefixKey(append(snapshotAccountPrefix, accountHash.Bytes()...))
	data, err := e.get(key)
	if err != nil || len(data) == 0 {
		return nil, migrate.ErrStateNotAvailable
	}

	account, err := e.decodeSnapshotAccount(data)
	if err != nil {
		return nil, err
	}
	account.Address = address

	// Read code
	if account.CodeHash != (common.Hash{}) && account.CodeHash != crypto.Keccak256Hash(nil) {
		account.Code = e.readCode(account.CodeHash)
	}

	// Read storage
	account.Storage = e.readAccountStorage(accountHash)

	return account, nil
}

// ExportConfig exports chain configuration
func (e *Exporter) ExportConfig() (*migrate.Config, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.initialized {
		return nil, migrate.ErrNotInitialized
	}

	config := &migrate.Config{
		ChainID: e.chainID,
	}

	// Read chain config JSON
	configData, err := e.get(configKey(e.genesisHash))
	if err == nil && len(configData) > 0 {
		var chainConfig struct {
			ChainID             *big.Int `json:"chainId"`
			HomesteadBlock      *big.Int `json:"homesteadBlock"`
			EIP150Block         *big.Int `json:"eip150Block"`
			EIP155Block         *big.Int `json:"eip155Block"`
			EIP158Block         *big.Int `json:"eip158Block"`
			ByzantiumBlock      *big.Int `json:"byzantiumBlock"`
			ConstantinopleBlock *big.Int `json:"constantinopleBlock"`
			PetersburgBlock     *big.Int `json:"petersburgBlock"`
			IstanbulBlock       *big.Int `json:"istanbulBlock"`
			BerlinBlock         *big.Int `json:"berlinBlock"`
			LondonBlock         *big.Int `json:"londonBlock"`
		}
		if err := json.Unmarshal(configData, &chainConfig); err == nil {
			config.ChainID = chainConfig.ChainID
			config.HomesteadBlock = chainConfig.HomesteadBlock
			config.EIP150Block = chainConfig.EIP150Block
			config.EIP155Block = chainConfig.EIP155Block
			config.EIP158Block = chainConfig.EIP158Block
			config.ByzantiumBlock = chainConfig.ByzantiumBlock
			config.ConstantinopleBlock = chainConfig.ConstantinopleBlock
			config.PetersburgBlock = chainConfig.PetersburgBlock
			config.IstanbulBlock = chainConfig.IstanbulBlock
			config.BerlinBlock = chainConfig.BerlinBlock
			config.LondonBlock = chainConfig.LondonBlock
		}
	}

	// Export genesis block data
	genesisBlock, err := e.exportBlock(0)
	if err == nil {
		config.GenesisBlock = genesisBlock
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
	hash := e.readCanonicalHash(blockNumber)
	if hash == (common.Hash{}) {
		return migrate.ErrBlockNotFound
	}

	// Verify header exists and is valid
	header := e.readHeader(hash, blockNumber)
	if header == nil {
		return fmt.Errorf("header not found for block %d", blockNumber)
	}

	// Verify header hash matches
	if header.Hash() != hash {
		return fmt.Errorf("header hash mismatch at block %d", blockNumber)
	}

	// Verify body exists (for non-empty blocks)
	body := e.readBody(hash, blockNumber)
	if body != nil {
		// Verify transaction root
		txHash := types.DeriveSha(types.Transactions(body.Transactions), nil)
		if txHash != header.TxHash {
			return fmt.Errorf("transaction root mismatch at block %d", blockNumber)
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

	var err error
	if e.db != nil {
		err = e.db.Close()
		e.db = nil
	}
	e.initialized = false
	return err
}

// Database helper methods

func (e *Exporter) get(key []byte) ([]byte, error) {
	val, closer, err := e.db.Get(key)
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	defer closer.Close()

	// Copy the value since it's only valid until closer.Close()
	result := make([]byte, len(val))
	copy(result, val)
	return result, nil
}

// NOTE: Key encoding functions (encodeBlockNumber, headerKey, etc.) are defined in schema.go

// readCanonicalHash retrieves the hash assigned to a canonical block number
func (e *Exporter) readCanonicalHash(number uint64) common.Hash {
	data, err := e.get(e.prefixKey(headerHashKey(number)))
	if err != nil || len(data) == 0 {
		return common.Hash{}
	}
	return common.BytesToHash(data)
}

// readHeaderNumber returns the header number assigned to a hash
func (e *Exporter) readHeaderNumber(hash common.Hash) (uint64, error) {
	data, err := e.get(e.prefixKey(headerNumberKey(hash)))
	if err != nil || len(data) == 0 {
		return 0, errors.New("header number not found")
	}

	// Handle both 4-byte (migrated) and 8-byte (standard) formats
	if len(data) == 4 {
		return uint64(binary.BigEndian.Uint32(data)), nil
	} else if len(data) == 8 {
		return binary.BigEndian.Uint64(data), nil
	}
	return 0, errors.New("invalid header number format")
}

// readHeader retrieves the block header corresponding to the hash
func (e *Exporter) readHeader(hash common.Hash, number uint64) *types.Header {
	data, err := e.get(e.prefixKey(headerKey(number, hash)))
	if err != nil || len(data) == 0 {
		return nil
	}

	header := new(types.Header)
	if err := rlp.DecodeBytes(data, header); err != nil {
		return nil
	}
	return header
}

// readBody retrieves the block body corresponding to the hash
func (e *Exporter) readBody(hash common.Hash, number uint64) *types.Body {
	data, err := e.get(e.prefixKey(blockBodyKey(number, hash)))
	if err != nil || len(data) == 0 {
		return nil
	}

	body := new(types.Body)
	if err := rlp.DecodeBytes(data, body); err != nil {
		return nil
	}
	return body
}

// readReceiptsRLP retrieves the block receipts in RLP encoding
func (e *Exporter) readReceiptsRLP(hash common.Hash, number uint64) []byte {
	data, err := e.get(e.prefixKey(blockReceiptsKey(number, hash)))
	if err != nil {
		return nil
	}
	return data
}

// readCode retrieves the contract code of the provided code hash
func (e *Exporter) readCode(hash common.Hash) []byte {
	// Try prefixed code scheme first
	data, err := e.get(e.prefixKey(codeKey(hash)))
	if err == nil && len(data) > 0 {
		return data
	}

	// Fall back to legacy scheme (hash as key)
	data, _ = e.get(e.prefixKey(hash.Bytes()))
	return data
}

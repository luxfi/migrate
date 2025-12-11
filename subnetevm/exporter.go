// Package subnetevm provides SubnetEVM-specific export functionality
// for migrating blockchain data from SubnetEVM PebbleDB databases.
package subnetevm

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// decodeLegacyHeader decodes a header from RLP bytes, handling legacy formats
// where optional fields like WithdrawalsHash may be encoded as empty bytes.
func decodeLegacyHeader(data []byte) (*types.Header, error) {
	// First try standard decoding
	header := new(types.Header)
	if err := rlp.DecodeBytes(data, header); err == nil {
		return header, nil
	}

	// Try legacy format - decode as a list and handle manually
	var fields []rlp.RawValue
	if err := rlp.DecodeBytes(data, &fields); err != nil {
		return nil, fmt.Errorf("failed to decode header fields: %w", err)
	}

	if len(fields) < 15 {
		return nil, fmt.Errorf("header has too few fields: %d", len(fields))
	}

	h := &types.Header{}

	// Decode required fields (0-14)
	if err := rlp.DecodeBytes(fields[0], &h.ParentHash); err != nil {
		return nil, fmt.Errorf("failed to decode ParentHash: %w", err)
	}
	if err := rlp.DecodeBytes(fields[1], &h.UncleHash); err != nil {
		return nil, fmt.Errorf("failed to decode UncleHash: %w", err)
	}
	if err := rlp.DecodeBytes(fields[2], &h.Coinbase); err != nil {
		return nil, fmt.Errorf("failed to decode Coinbase: %w", err)
	}
	if err := rlp.DecodeBytes(fields[3], &h.Root); err != nil {
		return nil, fmt.Errorf("failed to decode Root: %w", err)
	}
	if err := rlp.DecodeBytes(fields[4], &h.TxHash); err != nil {
		return nil, fmt.Errorf("failed to decode TxHash: %w", err)
	}
	if err := rlp.DecodeBytes(fields[5], &h.ReceiptHash); err != nil {
		return nil, fmt.Errorf("failed to decode ReceiptHash: %w", err)
	}
	if err := rlp.DecodeBytes(fields[6], &h.Bloom); err != nil {
		return nil, fmt.Errorf("failed to decode Bloom: %w", err)
	}
	if err := rlp.DecodeBytes(fields[7], &h.Difficulty); err != nil {
		return nil, fmt.Errorf("failed to decode Difficulty: %w", err)
	}
	if err := rlp.DecodeBytes(fields[8], &h.Number); err != nil {
		return nil, fmt.Errorf("failed to decode Number: %w", err)
	}
	if err := rlp.DecodeBytes(fields[9], &h.GasLimit); err != nil {
		return nil, fmt.Errorf("failed to decode GasLimit: %w", err)
	}
	if err := rlp.DecodeBytes(fields[10], &h.GasUsed); err != nil {
		return nil, fmt.Errorf("failed to decode GasUsed: %w", err)
	}
	if err := rlp.DecodeBytes(fields[11], &h.Time); err != nil {
		return nil, fmt.Errorf("failed to decode Time: %w", err)
	}
	if err := rlp.DecodeBytes(fields[12], &h.Extra); err != nil {
		return nil, fmt.Errorf("failed to decode Extra: %w", err)
	}
	if err := rlp.DecodeBytes(fields[13], &h.MixDigest); err != nil {
		return nil, fmt.Errorf("failed to decode MixDigest: %w", err)
	}
	if err := rlp.DecodeBytes(fields[14], &h.Nonce); err != nil {
		return nil, fmt.Errorf("failed to decode Nonce: %w", err)
	}

	// Decode optional fields (15+), ignoring empty values
	if len(fields) > 15 {
		if err := decodeOptionalBigInt(fields[15], &h.BaseFee); err != nil {
			return nil, fmt.Errorf("failed to decode BaseFee: %w", err)
		}
	}
	if len(fields) > 16 {
		if err := decodeOptionalHash(fields[16], &h.WithdrawalsHash); err != nil {
			return nil, fmt.Errorf("failed to decode WithdrawalsHash: %w", err)
		}
	}
	if len(fields) > 17 {
		if err := decodeOptionalUint64(fields[17], &h.BlobGasUsed); err != nil {
			return nil, fmt.Errorf("failed to decode BlobGasUsed: %w", err)
		}
	}
	if len(fields) > 18 {
		if err := decodeOptionalUint64(fields[18], &h.ExcessBlobGas); err != nil {
			return nil, fmt.Errorf("failed to decode ExcessBlobGas: %w", err)
		}
	}
	if len(fields) > 19 {
		if err := decodeOptionalHash(fields[19], &h.ParentBeaconRoot); err != nil {
			return nil, fmt.Errorf("failed to decode ParentBeaconRoot: %w", err)
		}
	}

	return h, nil
}

// decodeOptionalHash decodes an optional hash field, treating empty RLP as nil
func decodeOptionalHash(data rlp.RawValue, out **common.Hash) error {
	// Check if it's an empty string (0x80 in RLP)
	if len(data) == 1 && data[0] == 0x80 {
		*out = nil
		return nil
	}

	// Try to decode as hash
	var hash common.Hash
	if err := rlp.DecodeBytes(data, &hash); err != nil {
		// If decode fails and data is effectively empty, treat as nil
		if err == io.EOF || (err != nil && len(data) <= 1) {
			*out = nil
			return nil
		}
		return err
	}

	// Check if it's an empty hash
	if hash == (common.Hash{}) {
		*out = nil
	} else {
		*out = &hash
	}
	return nil
}

// decodeOptionalBigInt decodes an optional big.Int field, treating empty RLP as nil
func decodeOptionalBigInt(data rlp.RawValue, out **big.Int) error {
	// Check if it's an empty string (0x80 in RLP)
	if len(data) == 1 && data[0] == 0x80 {
		*out = nil
		return nil
	}

	var val big.Int
	if err := rlp.DecodeBytes(data, &val); err != nil {
		if err == io.EOF || (err != nil && len(data) <= 1) {
			*out = nil
			return nil
		}
		return err
	}
	*out = &val
	return nil
}

// decodeOptionalUint64 decodes an optional uint64 field, treating empty RLP as nil
func decodeOptionalUint64(data rlp.RawValue, out **uint64) error {
	// Check if it's an empty string (0x80 in RLP)
	if len(data) == 1 && data[0] == 0x80 {
		*out = nil
		return nil
	}

	var val uint64
	if err := rlp.DecodeBytes(data, &val); err != nil {
		if err == io.EOF || (err != nil && len(data) <= 1) {
			*out = nil
			return nil
		}
		return err
	}
	*out = &val
	return nil
}

func init() {
	// Register SubnetEVM exporter factory
	migrate.RegisterExporterFactory(migrate.VMTypeSubnetEVM, func(config migrate.ExporterConfig) (migrate.Exporter, error) {
		exp, err := NewExporter(config)
		if err != nil {
			return nil, err
		}
		// Auto-initialize if database path is provided
		if config.DatabasePath != "" {
			if err := exp.Init(config); err != nil {
				return nil, err
			}
		}
		return exp, nil
	})
}

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

	// Prefixed key support (for Avalanche-style databases)
	keyPrefix []byte // 32-byte chain prefix, nil for standard databases
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

	// Detect key prefix format (Avalanche-style databases use 32-byte chain prefix)
	if err := e.detectKeyPrefix(); err != nil {
		db.Close()
		return fmt.Errorf("failed to detect key format: %w", err)
	}

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
	// Read head block hash
	headHash, err := e.get(headBlockKey)
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
	configData, err := e.get(configKey(genesisHash))
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
		snapshotRoot, err := e.get(snapshotRootKey)
		if err != nil || len(snapshotRoot) == 0 {
			errs <- migrate.ErrStateNotAvailable
			return
		}

		// Iterate through snapshot accounts (using prefixed keys if needed)
		lowerBound := e.prefixKey(snapshotAccountPrefix)
		upperBound := e.prefixKey(append(snapshotAccountPrefix, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff))
		iter, err := e.db.NewIter(&pebble.IterOptions{
			LowerBound: lowerBound,
			UpperBound: upperBound,
		})
		if err != nil {
			errs <- fmt.Errorf("failed to create iterator: %w", err)
			return
		}
		defer iter.Close()

		// Calculate expected key length (with prefix if applicable)
		prefixLen := len(e.keyPrefix)
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

			// Extract account hash (after prefix + snapshotAccountPrefix)
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

	// Build prefixed key bounds
	storageKey := append(snapshotStoragePrefix, accountHash.Bytes()...)
	prefix := e.prefixKey(storageKey)
	upperBound := e.prefixKey(append(storageKey, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff))

	iter, err := e.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: upperBound,
	})
	if err != nil {
		return storage
	}
	defer iter.Close()

	// Calculate expected key length with prefix
	prefixLen := len(e.keyPrefix)
	expectedKeyLen := prefixLen + len(snapshotStoragePrefix) + 2*common.HashLength

	for iter.First(); iter.Valid(); iter.Next() {
		key := iter.Key()
		if len(key) != expectedKeyLen {
			continue
		}

		// Extract storage hash (after prefix + snapshotStoragePrefix + accountHash)
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

	// Read from snapshot
	key := append(snapshotAccountPrefix, accountHash.Bytes()...)
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

// detectKeyPrefix scans the database to detect if keys are prefixed with a chain ID
// This is necessary for Avalanche-style databases where all keys have a 32-byte prefix
func (e *Exporter) detectKeyPrefix() error {
	iter, err := e.db.NewIter(nil)
	if err != nil {
		return err
	}
	defer iter.Close()

	// First, try to find "LastBlock" key directly (standard format)
	_, closer, err := e.db.Get(headBlockKey)
	if err == nil {
		closer.Close()
		e.keyPrefix = nil // Standard format, no prefix needed
		return nil
	}

	// If not found, look for prefixed keys
	// We need to find a key that ends with a known suffix like "LastBlock"
	lastBlockSuffix := []byte("LastBlock")

	for iter.First(); iter.Valid(); iter.Next() {
		key := iter.Key()
		// Look for keys that end with "LastBlock" and have a 32-byte prefix
		if len(key) == 32+len(lastBlockSuffix) {
			suffix := key[32:]
			if string(suffix) == "LastBlock" {
				// Found it - extract the prefix
				prefix := make([]byte, 32)
				copy(prefix, key[:32])
				e.keyPrefix = prefix
				return nil
			}
		}
	}

	// If we still haven't found it, the database might be empty or in an unknown format
	// Return an error to indicate we couldn't detect the format
	return errors.New("could not detect database key format - neither standard nor prefixed LastBlock key found")
}

// prefixKey adds the chain prefix to a key if needed
func (e *Exporter) prefixKey(key []byte) []byte {
	if e.keyPrefix == nil {
		return key
	}
	return append(e.keyPrefix, key...)
}

func (e *Exporter) get(key []byte) ([]byte, error) {
	// Apply prefix if needed
	prefixedKey := e.prefixKey(key)

	val, closer, err := e.db.Get(prefixedKey)
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
	data, err := e.get(headerHashKey(number))
	if err != nil || len(data) == 0 {
		return common.Hash{}
	}
	return common.BytesToHash(data)
}

// readHeaderNumber returns the header number assigned to a hash
func (e *Exporter) readHeaderNumber(hash common.Hash) (uint64, error) {
	data, err := e.get(headerNumberKey(hash))
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
	data, err := e.get(headerKey(number, hash))
	if err != nil || len(data) == 0 {
		return nil
	}

	// Use legacy decoder that handles SubnetEVM format with empty optional fields
	header, err := decodeLegacyHeader(data)
	if err != nil {
		return nil
	}
	return header
}

// readBody retrieves the block body corresponding to the hash
func (e *Exporter) readBody(hash common.Hash, number uint64) *types.Body {
	data, err := e.get(blockBodyKey(number, hash))
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
	data, err := e.get(blockReceiptsKey(number, hash))
	if err != nil {
		return nil
	}
	return data
}

// readCode retrieves the contract code of the provided code hash
func (e *Exporter) readCode(hash common.Hash) []byte {
	// Try prefixed code scheme first
	data, err := e.get(codeKey(hash))
	if err == nil && len(data) > 0 {
		return data
	}

	// Fall back to legacy scheme (hash as key)
	data, _ = e.get(hash.Bytes())
	return data
}

// Package migrate provides generic blockchain import/export functionality
// for migrating data between different VM implementations.
// Supports SubnetEVM→C-Chain, Zoo L2→mainnet, P/X/Q chain migrations.
package migrate

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"strings"
	"time"

	"github.com/luxfi/geth/common"
	"github.com/luxfi/geth/core/types"
)

// HexBytes is a byte slice that marshals/unmarshals as hex (with or without 0x prefix)
type HexBytes []byte

// UnmarshalJSON decodes hex string (with or without 0x prefix) to bytes
func (h *HexBytes) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	s = strings.TrimPrefix(s, "0x")
	if s == "" {
		*h = nil
		return nil
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return err
	}
	*h = b
	return nil
}

// MarshalJSON encodes bytes as hex string with 0x prefix
func (h HexBytes) MarshalJSON() ([]byte, error) {
	if h == nil {
		return []byte(`""`), nil
	}
	return json.Marshal("0x" + hex.EncodeToString(h))
}

// VMType identifies the type of virtual machine
type VMType string

const (
	VMTypeSubnetEVM VMType = "subnet-evm" // SubnetEVM - export via PebbleDB, import via BadgerDB
	VMTypeCChain    VMType = "c-chain"    // C-Chain (Coreth) - export via RPC, import via BadgerDB
	VMTypeZooL2     VMType = "zoo-l2"     // Zoo L2 - export via PebbleDB, import via BadgerDB
	VMTypePChain    VMType = "p-chain"    // P-Chain - export/import via Platform API
	VMTypeXChain    VMType = "x-chain"    // X-Chain - export/import via AVM API
	VMTypeQChain    VMType = "q-chain"    // Q-Chain - export/import via RPC
	VMTypeCustom    VMType = "custom"     // Custom VM
)

// BlockData represents a generic block with all necessary data for migration
type BlockData struct {
	// Core block identifiers
	Number     uint64      `json:"number"`
	Hash       common.Hash `json:"hash"`
	ParentHash common.Hash `json:"parent_hash,omitempty"`
	Timestamp  uint64      `json:"timestamp,omitempty"`

	// State roots
	StateRoot        common.Hash `json:"state_root,omitempty"`
	ReceiptsRoot     common.Hash `json:"receipts_root,omitempty"`
	TransactionsRoot common.Hash `json:"transactions_root,omitempty"`

	// Block metadata
	GasLimit        uint64           `json:"gas_limit,omitempty"`
	GasUsed         uint64           `json:"gas_used,omitempty"`
	Difficulty      *big.Int         `json:"difficulty,omitempty"`
	TotalDifficulty *big.Int         `json:"total_difficulty,omitempty"`
	Coinbase        common.Address   `json:"coinbase,omitempty"`
	Nonce           types.BlockNonce `json:"nonce,omitempty"`
	MixHash         common.Hash      `json:"mix_hash,omitempty"`
	ExtraData       []byte           `json:"extra_data,omitempty"`
	BaseFee         *big.Int         `json:"base_fee,omitempty"` // EIP-1559

	// RLP encoded data (snake_case for JSONL compatibility, HexBytes for hex encoding)
	Header   HexBytes `json:"header_rlp,omitempty"` // RLP encoded header
	Body     HexBytes `json:"body_rlp,omitempty"`   // RLP encoded body
	Receipts HexBytes `json:"receipts_rlp,omitempty"` // RLP encoded receipts

	// Decoded transactions
	Transactions []*Transaction `json:"transactions,omitempty"`
	UncleHeaders [][]byte       `json:"uncle_headers,omitempty"`

	// State changes for this block
	StateChanges map[common.Address]*Account `json:"state_changes,omitempty"`

	// VM-specific extensions
	Extensions map[string]interface{} `json:"extensions,omitempty"`
}

// Transaction represents a generic transaction
type Transaction struct {
	Hash     common.Hash
	Nonce    uint64
	From     common.Address
	To       *common.Address // nil for contract creation
	Value    *big.Int
	Gas      uint64
	GasPrice *big.Int
	Data     []byte
	V, R, S  *big.Int

	// EIP-1559 fields
	GasTipCap  *big.Int
	GasFeeCap  *big.Int
	AccessList types.AccessList

	// Receipt data
	Receipt *Receipt
}

// Receipt represents a transaction receipt
type Receipt struct {
	Status            uint64
	CumulativeGasUsed uint64
	Bloom             types.Bloom
	Logs              []*types.Log
	TransactionHash   common.Hash
	ContractAddress   *common.Address
	GasUsed           uint64
}

// Account represents an account in the state trie
type Account struct {
	Address     common.Address
	Nonce       uint64
	Balance     *big.Int
	CodeHash    common.Hash
	StorageRoot common.Hash
	Code        []byte
	Storage     map[common.Hash]common.Hash
}

// Info contains metadata about a blockchain/VM
type Info struct {
	VMType          VMType
	NetworkID       uint64
	ChainID         *big.Int
	GenesisHash     common.Hash
	CurrentHeight   uint64
	TotalDifficulty *big.Int
	StateRoot       common.Hash

	// VM-specific info
	VMVersion    string
	DatabaseType string // "pebble", "badger", "leveldb"
	IsPruned     bool
	ArchiveMode  bool

	// Feature flags
	HasWarpMessages bool
	HasProposerVM   bool

	// VM-specific extensions (for P-Chain, X-Chain, etc.)
	Extensions map[string]interface{}
}

// Config contains configuration for a blockchain/VM
type Config struct {
	NetworkID uint64
	ChainID   *big.Int

	// Genesis configuration
	GenesisBlock *BlockData
	GenesisAlloc map[common.Address]*Account

	// Fork heights
	HomesteadBlock      *big.Int
	EIP150Block         *big.Int
	EIP155Block         *big.Int
	EIP158Block         *big.Int
	ByzantiumBlock      *big.Int
	ConstantinopleBlock *big.Int
	PetersburgBlock     *big.Int
	IstanbulBlock       *big.Int
	BerlinBlock         *big.Int
	LondonBlock         *big.Int

	// Special configurations
	IsCoreth    bool
	HasNetID    bool
	NetID       string
	Precompiles map[common.Address]string

	// VM-specific extensions (for P-Chain, X-Chain, etc.)
	Extensions map[string]interface{}
}

// ExporterConfig configures an exporter instance
type ExporterConfig struct {
	VMType       VMType
	DatabasePath string
	DatabaseType string // "pebble", "badger", "leveldb"

	// For network-based export
	RPCURL    string
	NetworkID uint64

	// Namespace for multi-chain databases
	NetNamespace []byte
	NetID        string

	// Export options
	ExportState     bool
	ExportReceipts  bool
	VerifyIntegrity bool
	MaxConcurrency  int

	// Compatibility modes
	CorethCompat bool
}

// ImporterConfig configures an importer instance
type ImporterConfig struct {
	VMType       VMType
	DatabasePath string
	DatabaseType string

	// For RPC-based import
	RPCURL string

	// Import options
	ExecuteBlocks  bool
	VerifyState    bool
	BatchSize      int
	MaxConcurrency int

	// State management
	PreserveState bool
	MergeState    bool

	// Compatibility modes
	CorethCompat bool
	GenesisTime  time.Time
}

// MigrationOptions configures the migration process
type MigrationOptions struct {
	// Block range
	StartBlock uint64
	EndBlock   uint64

	// Processing options
	BatchSize       int
	MaxConcurrency  int
	VerifyEachBlock bool

	// State options
	MigrateState bool
	StateHeight  uint64

	// Error handling
	ContinueOnError bool
	RetryFailures   int

	// Progress reporting
	ProgressCallback func(current, total uint64)

	// Special migrations
	RegenesisMode  bool
	PreserveNonces bool
	RemapAddresses map[common.Address]common.Address
}

// MigrationResult contains the result of a migration
type MigrationResult struct {
	Success        bool
	BlocksMigrated uint64
	StatesMigrated uint64
	StartTime      time.Time
	EndTime        time.Time
	Errors         []error

	// Verification results
	SourceStateRoot common.Hash
	DestStateRoot   common.Hash
	StateMatches    bool
}

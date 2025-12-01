// Package migrate provides generic blockchain import/export functionality
// for migrating data between different VM implementations.
// Supports SubnetEVM→C-Chain, Zoo L2→mainnet, P/X/Q chain migrations.
package migrate

import (
	"math/big"
	"time"

	"github.com/luxfi/geth/common"
	"github.com/luxfi/geth/core/types"
)

// VMType identifies the type of virtual machine
type VMType string

const (
	VMTypeSubnetEVM VMType = "subnet-evm"
	VMTypeCChain    VMType = "c-chain"
	VMTypeCoreth    VMType = "coreth"
	VMTypeZooL2     VMType = "zoo-l2"
	VMTypePChain    VMType = "p-chain"
	VMTypeXChain    VMType = "x-chain"
	VMTypeQChain    VMType = "q-chain"
	VMTypeCustom    VMType = "custom"
)

// BlockData represents a generic block with all necessary data for migration
type BlockData struct {
	// Core block identifiers
	Number     uint64
	Hash       common.Hash
	ParentHash common.Hash
	Timestamp  uint64

	// State roots
	StateRoot        common.Hash
	ReceiptsRoot     common.Hash
	TransactionsRoot common.Hash

	// Block metadata
	GasLimit        uint64
	GasUsed         uint64
	Difficulty      *big.Int
	TotalDifficulty *big.Int
	Coinbase        common.Address
	Nonce           types.BlockNonce
	MixHash         common.Hash
	ExtraData       []byte
	BaseFee         *big.Int // EIP-1559

	// RLP encoded data
	Header   []byte // RLP encoded header
	Body     []byte // RLP encoded body
	Receipts []byte // RLP encoded receipts

	// Decoded transactions
	Transactions []*Transaction
	UncleHeaders [][]byte

	// VM-specific extensions
	Extensions map[string]interface{}
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
}

// ExporterConfig configures an exporter instance
type ExporterConfig struct {
	VMType       VMType
	DatabasePath string
	DatabaseType string // "pebble", "badger", "leveldb"

	// For network-based export
	RPCURL string

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

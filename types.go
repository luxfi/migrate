// Package migrate provides generic blockchain import/export functionality
// for migrating data between different VM implementations.
// Supports SubnetEVM→C-Chain, Zoo L2→mainnet, P/X/Q chain migrations.
package migrate

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
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

// BigInt is a wrapper for big.Int that handles JSON unmarshaling from both
// string ("123") and number (123) formats, with optional 0x hex prefix support
type BigInt big.Int

// UnmarshalJSON decodes either a string or number into BigInt
func (b *BigInt) UnmarshalJSON(data []byte) error {
	// Handle null
	if string(data) == "null" {
		return nil
	}

	// Try parsing as string first (handles "123" and "0x7b")
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil
		}
		// Handle hex format
		if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
			i, ok := new(big.Int).SetString(s[2:], 16)
			if !ok {
				return fmt.Errorf("invalid hex big.Int: %s", s)
			}
			*b = BigInt(*i)
			return nil
		}
		// Handle decimal format
		i, ok := new(big.Int).SetString(s, 10)
		if !ok {
			return fmt.Errorf("invalid decimal big.Int: %s", s)
		}
		*b = BigInt(*i)
		return nil
	}

	// Try parsing as raw number
	i := new(big.Int)
	if err := i.UnmarshalJSON(data); err != nil {
		return err
	}
	*b = BigInt(*i)
	return nil
}

// MarshalJSON encodes BigInt as a decimal string
func (b BigInt) MarshalJSON() ([]byte, error) {
	return json.Marshal((*big.Int)(&b).String())
}

// Int returns the underlying *big.Int
func (b *BigInt) Int() *big.Int {
	if b == nil {
		return nil
	}
	return (*big.Int)(b)
}

// ToBigInt converts *BigInt to *big.Int
func (b *BigInt) ToBigInt() *big.Int {
	if b == nil {
		return nil
	}
	return (*big.Int)(b)
}

// FromBigInt creates a *BigInt from *big.Int
func FromBigInt(i *big.Int) *BigInt {
	if i == nil {
		return nil
	}
	b := BigInt(*i)
	return &b
}

// FlexNonce is a block nonce that can unmarshal from hex strings with or without 0x prefix
type FlexNonce types.BlockNonce

// UnmarshalJSON decodes a hex string (with or without 0x prefix) into FlexNonce
func (n *FlexNonce) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	s = strings.TrimPrefix(s, "0x")
	s = strings.TrimPrefix(s, "0X")
	if s == "" {
		*n = FlexNonce{}
		return nil
	}
	// Pad to 16 chars (8 bytes = 16 hex chars)
	for len(s) < 16 {
		s = "0" + s
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return fmt.Errorf("invalid nonce hex: %w", err)
	}
	if len(b) != 8 {
		return fmt.Errorf("nonce must be 8 bytes, got %d", len(b))
	}
	copy(n[:], b)
	return nil
}

// ToBlockNonce converts FlexNonce to types.BlockNonce
func (n FlexNonce) ToBlockNonce() types.BlockNonce {
	return types.BlockNonce(n)
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

// BlockData represents a generic block with all necessary data for migration.
// Field names use lowercase for consistency with MigrateAPI import format.
// The MarshalJSON/UnmarshalJSON methods ensure consistent serialization.
type BlockData struct {
	// Core block identifiers - matches migrate_importBlocks API format
	Number     uint64      `json:"height"`          // "height" to match import API
	Hash       common.Hash `json:"hash"`            // hex string with 0x prefix
	ParentHash common.Hash `json:"parentHash,omitempty"`
	Timestamp  uint64      `json:"timestamp,omitempty"`

	// State roots
	StateRoot        common.Hash `json:"stateRoot,omitempty"`
	ReceiptsRoot     common.Hash `json:"receiptsRoot,omitempty"`
	TransactionsRoot common.Hash `json:"transactionsRoot,omitempty"`

	// Block metadata
	GasLimit        uint64           `json:"gasLimit,omitempty"`
	GasUsed         uint64           `json:"gasUsed,omitempty"`
	Difficulty      *big.Int         `json:"difficulty,omitempty"`
	TotalDifficulty *big.Int         `json:"totalDifficulty,omitempty"`
	Coinbase        common.Address   `json:"coinbase,omitempty"`
	Nonce           types.BlockNonce `json:"nonce,omitempty"`
	MixHash         common.Hash      `json:"mixHash,omitempty"`
	ExtraData       []byte           `json:"extraData,omitempty"`
	BaseFee         *big.Int         `json:"baseFee,omitempty"` // EIP-1559

	// RLP encoded data - matches migrate_importBlocks API format (hex with 0x prefix)
	Header   HexBytes `json:"header,omitempty"`   // RLP encoded header
	Body     HexBytes `json:"body,omitempty"`     // RLP encoded body
	Receipts HexBytes `json:"receipts,omitempty"` // RLP encoded receipts

	// Decoded transactions
	Transactions []*Transaction `json:"transactions,omitempty"`
	UncleHeaders [][]byte       `json:"uncleHeaders,omitempty"`

	// State changes for this block - matches migrate_importBlocks stateChanges format
	StateChanges map[common.Address]*Account `json:"stateChanges,omitempty"`

	// VM-specific extensions
	Extensions map[string]interface{} `json:"extensions,omitempty"`
}

// blockDataJSON is a helper struct for JSON unmarshaling that handles multiple formats:
// 1. New canonical format (camelCase, matches MigrateAPI): height, hash, header, body, receipts
// 2. Legacy export format (PascalCase): Number, Hash, Header, Body, Receipts
// 3. RPC format: number, miner, baseFeePerGas, etc.
type blockDataJSON struct {
	// Primary fields (new canonical format)
	Height     uint64      `json:"height"`
	Hash       common.Hash `json:"hash"`
	ParentHash common.Hash `json:"parentHash"`
	Timestamp  uint64      `json:"timestamp"`

	// State roots
	StateRoot        common.Hash `json:"stateRoot"`
	ReceiptsRoot     common.Hash `json:"receiptsRoot"`
	TransactionsRoot common.Hash `json:"transactionsRoot"`

	// Block metadata
	GasLimit        uint64         `json:"gasLimit"`
	GasUsed         uint64         `json:"gasUsed"`
	Difficulty      *BigInt        `json:"difficulty"`
	TotalDifficulty *BigInt        `json:"totalDifficulty"`
	Coinbase        common.Address `json:"coinbase"`
	Nonce           FlexNonce      `json:"nonce"`
	MixHash         common.Hash    `json:"mixHash"`
	ExtraData       HexBytes       `json:"extraData"`
	BaseFee         *BigInt        `json:"baseFee"`

	// RLP encoded data (new canonical format - matches MigrateAPI)
	Header   HexBytes `json:"header"`
	Body     HexBytes `json:"body"`
	Receipts HexBytes `json:"receipts"`

	// Decoded transactions
	Transactions []*Transaction `json:"transactions"`
	UncleHeaders [][]byte       `json:"uncleHeaders"`

	// State changes
	StateChanges map[common.Address]*Account `json:"stateChanges"`

	// Extensions
	Extensions map[string]interface{} `json:"extensions"`
}

// UnmarshalJSON handles multiple JSON formats for backwards compatibility:
// 1. New canonical format (matches MigrateAPI): height, hash, header, body, receipts
// 2. Legacy export format (PascalCase): Number, Hash, Header, Body, Receipts
// 3. RPC format: number, miner, baseFeePerGas
func (b *BlockData) UnmarshalJSON(data []byte) error {
	// First parse into raw map to detect format
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// Try primary helper struct (new canonical format)
	var helper blockDataJSON
	if err := json.Unmarshal(data, &helper); err != nil {
		return err
	}

	// Copy from helper
	b.Number = helper.Height
	b.Hash = helper.Hash
	b.ParentHash = helper.ParentHash
	b.Timestamp = helper.Timestamp
	b.StateRoot = helper.StateRoot
	b.ReceiptsRoot = helper.ReceiptsRoot
	b.TransactionsRoot = helper.TransactionsRoot
	b.GasLimit = helper.GasLimit
	b.GasUsed = helper.GasUsed
	b.Coinbase = helper.Coinbase
	b.Nonce = helper.Nonce.ToBlockNonce()
	b.MixHash = helper.MixHash
	b.ExtraData = helper.ExtraData
	b.Header = helper.Header
	b.Body = helper.Body
	b.Receipts = helper.Receipts
	b.Transactions = helper.Transactions
	b.UncleHeaders = helper.UncleHeaders
	b.StateChanges = helper.StateChanges
	b.Extensions = helper.Extensions

	// Convert BigInt to *big.Int
	if helper.Difficulty != nil {
		b.Difficulty = helper.Difficulty.ToBigInt()
	}
	if helper.TotalDifficulty != nil {
		b.TotalDifficulty = helper.TotalDifficulty.ToBigInt()
	}
	if helper.BaseFee != nil {
		b.BaseFee = helper.BaseFee.ToBigInt()
	}

	// Handle legacy PascalCase format (Number instead of height)
	if b.Number == 0 {
		if val, ok := raw["Number"]; ok {
			json.Unmarshal(val, &b.Number)
		}
		if val, ok := raw["number"]; ok && b.Number == 0 {
			json.Unmarshal(val, &b.Number)
		}
	}

	// Handle legacy Hash if not set
	if b.Hash == (common.Hash{}) {
		if val, ok := raw["Hash"]; ok {
			json.Unmarshal(val, &b.Hash)
		}
	}

	// Handle legacy ParentHash
	if b.ParentHash == (common.Hash{}) {
		if val, ok := raw["ParentHash"]; ok {
			json.Unmarshal(val, &b.ParentHash)
		}
	}

	// Handle legacy Timestamp
	if b.Timestamp == 0 {
		if val, ok := raw["Timestamp"]; ok {
			json.Unmarshal(val, &b.Timestamp)
		}
	}

	// Handle legacy state roots (PascalCase)
	if b.StateRoot == (common.Hash{}) {
		if val, ok := raw["StateRoot"]; ok {
			json.Unmarshal(val, &b.StateRoot)
		}
	}
	if b.ReceiptsRoot == (common.Hash{}) {
		if val, ok := raw["ReceiptsRoot"]; ok {
			json.Unmarshal(val, &b.ReceiptsRoot)
		}
	}
	if b.TransactionsRoot == (common.Hash{}) {
		if val, ok := raw["TransactionsRoot"]; ok {
			json.Unmarshal(val, &b.TransactionsRoot)
		}
	}

	// Handle legacy gas fields (PascalCase)
	if b.GasLimit == 0 {
		if val, ok := raw["GasLimit"]; ok {
			json.Unmarshal(val, &b.GasLimit)
		}
	}
	if b.GasUsed == 0 {
		if val, ok := raw["GasUsed"]; ok {
			json.Unmarshal(val, &b.GasUsed)
		}
	}

	// Handle legacy coinbase (PascalCase or "miner" from RPC)
	if b.Coinbase == (common.Address{}) {
		if val, ok := raw["Coinbase"]; ok {
			json.Unmarshal(val, &b.Coinbase)
		}
		if val, ok := raw["miner"]; ok && b.Coinbase == (common.Address{}) {
			json.Unmarshal(val, &b.Coinbase)
		}
	}

	// Handle legacy MixHash
	if b.MixHash == (common.Hash{}) {
		if val, ok := raw["MixHash"]; ok {
			json.Unmarshal(val, &b.MixHash)
		}
	}

	// Handle legacy ExtraData
	if len(b.ExtraData) == 0 {
		if val, ok := raw["ExtraData"]; ok {
			var hex HexBytes
			if json.Unmarshal(val, &hex) == nil {
				b.ExtraData = hex
			}
		}
	}

	// Handle legacy BaseFee (PascalCase or "baseFeePerGas" from RPC)
	if b.BaseFee == nil {
		if val, ok := raw["BaseFee"]; ok {
			var bf BigInt
			if json.Unmarshal(val, &bf) == nil {
				b.BaseFee = bf.ToBigInt()
			}
		}
		if val, ok := raw["baseFeePerGas"]; ok && b.BaseFee == nil {
			var bf BigInt
			if json.Unmarshal(val, &bf) == nil {
				b.BaseFee = bf.ToBigInt()
			}
		}
	}

	// Handle legacy Difficulty (PascalCase)
	if b.Difficulty == nil {
		if val, ok := raw["Difficulty"]; ok {
			var d BigInt
			if json.Unmarshal(val, &d) == nil {
				b.Difficulty = d.ToBigInt()
			}
		}
	}

	// Handle legacy TotalDifficulty (PascalCase)
	if b.TotalDifficulty == nil {
		if val, ok := raw["TotalDifficulty"]; ok {
			var td BigInt
			if json.Unmarshal(val, &td) == nil {
				b.TotalDifficulty = td.ToBigInt()
			}
		}
	}

	// Handle legacy RLP fields (PascalCase: Header, Body, Receipts)
	if len(b.Header) == 0 {
		if val, ok := raw["Header"]; ok {
			var hex HexBytes
			if json.Unmarshal(val, &hex) == nil {
				b.Header = hex
			}
		}
	}
	if len(b.Body) == 0 {
		if val, ok := raw["Body"]; ok {
			var hex HexBytes
			if json.Unmarshal(val, &hex) == nil {
				b.Body = hex
			}
		}
	}
	if len(b.Receipts) == 0 {
		if val, ok := raw["Receipts"]; ok {
			var hex HexBytes
			if json.Unmarshal(val, &hex) == nil {
				b.Receipts = hex
			}
		}
	}

	// Handle legacy Transactions (PascalCase)
	if len(b.Transactions) == 0 {
		if val, ok := raw["Transactions"]; ok {
			json.Unmarshal(val, &b.Transactions)
		}
	}

	// Handle legacy UncleHeaders (PascalCase)
	if len(b.UncleHeaders) == 0 {
		if val, ok := raw["UncleHeaders"]; ok {
			json.Unmarshal(val, &b.UncleHeaders)
		}
	}

	// Handle legacy StateChanges (PascalCase)
	if len(b.StateChanges) == 0 {
		if val, ok := raw["StateChanges"]; ok {
			json.Unmarshal(val, &b.StateChanges)
		}
	}

	// Handle legacy Extensions (PascalCase)
	if len(b.Extensions) == 0 {
		if val, ok := raw["Extensions"]; ok {
			json.Unmarshal(val, &b.Extensions)
		}
	}

	return nil
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

// accountJSON is the JSON representation of Account with hex encoding
type accountJSON struct {
	Address     string            `json:"address,omitempty"`
	Nonce       uint64            `json:"nonce,omitempty"`
	Balance     string            `json:"balance,omitempty"`     // hex-encoded
	CodeHash    string            `json:"codeHash,omitempty"`
	StorageRoot string            `json:"storageRoot,omitempty"`
	Code        string            `json:"code,omitempty"`        // hex-encoded
	Storage     map[string]string `json:"storage,omitempty"`     // hex keys and values
}

// MarshalJSON implements json.Marshaler for Account with hex encoding
func (a *Account) MarshalJSON() ([]byte, error) {
	aj := accountJSON{
		Nonce: a.Nonce,
	}

	if a.Address != (common.Address{}) {
		aj.Address = a.Address.Hex()
	}
	if a.Balance != nil && a.Balance.Sign() != 0 {
		aj.Balance = "0x" + a.Balance.Text(16)
	}
	if a.CodeHash != (common.Hash{}) {
		aj.CodeHash = a.CodeHash.Hex()
	}
	if a.StorageRoot != (common.Hash{}) {
		aj.StorageRoot = a.StorageRoot.Hex()
	}
	if len(a.Code) > 0 {
		aj.Code = "0x" + common.Bytes2Hex(a.Code)
	}
	if len(a.Storage) > 0 {
		aj.Storage = make(map[string]string)
		for k, v := range a.Storage {
			aj.Storage[k.Hex()] = v.Hex()
		}
	}

	return json.Marshal(aj)
}

// UnmarshalJSON implements json.Unmarshaler for Account with hex decoding
func (a *Account) UnmarshalJSON(data []byte) error {
	var aj accountJSON
	if err := json.Unmarshal(data, &aj); err != nil {
		return err
	}

	if aj.Address != "" {
		a.Address = common.HexToAddress(aj.Address)
	}
	a.Nonce = aj.Nonce
	if aj.Balance != "" {
		a.Balance = new(big.Int)
		a.Balance.SetString(strings.TrimPrefix(aj.Balance, "0x"), 16)
	}
	if aj.CodeHash != "" {
		a.CodeHash = common.HexToHash(aj.CodeHash)
	}
	if aj.StorageRoot != "" {
		a.StorageRoot = common.HexToHash(aj.StorageRoot)
	}
	if aj.Code != "" {
		a.Code = common.FromHex(aj.Code)
	}
	if len(aj.Storage) > 0 {
		a.Storage = make(map[common.Hash]common.Hash)
		for k, v := range aj.Storage {
			a.Storage[common.HexToHash(k)] = common.HexToHash(v)
		}
	}

	return nil
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

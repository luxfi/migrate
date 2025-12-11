package main

import (
	"encoding/json"
	"flag"
	"log"
	"math/big"
	"os"

	"github.com/luxfi/geth/common"
	"github.com/luxfi/geth/common/hexutil"
	"github.com/luxfi/geth/core"
	"github.com/luxfi/geth/core/types"
	"github.com/luxfi/geth/params"
	"github.com/luxfi/geth/rlp"
)

// GenesisAlloc represents the genesis allocation
type GenesisAlloc map[common.Address]core.GenesisAccount

// Genesis represents a genesis file
type Genesis struct {
	Config     map[string]interface{} `json:"config"`
	Nonce      string                 `json:"nonce"`
	Timestamp  string                 `json:"timestamp"`
	ExtraData  string                 `json:"extraData"`
	GasLimit   string                 `json:"gasLimit"`
	Difficulty string                 `json:"difficulty"`
	MixHash    string                 `json:"mixHash"`
	Coinbase   string                 `json:"coinbase"`
	Alloc      GenesisAlloc           `json:"alloc"`
	Number     string                 `json:"number"`
	GasUsed    string                 `json:"gasUsed"`
	ParentHash string                 `json:"parentHash"`
	BaseFee    string                 `json:"baseFeePerGas,omitempty"`
}

// JSONLBlock represents a block in JSONL format with state
type JSONLBlock struct {
	Number       uint64                   `json:"number"`
	Hash         string                   `json:"hash"`
	HeaderRLP    string                   `json:"header_rlp"`
	BodyRLP      string                   `json:"body_rlp"`
	ReceiptsRLP  string                   `json:"receipts_rlp"`
	StateChanges map[string]*JSONLAccount `json:"state_changes,omitempty"`
}

// JSONLAccount represents an account in JSONL format
type JSONLAccount struct {
	Balance  string            `json:"balance"`
	Nonce    uint64            `json:"nonce"`
	Code     string            `json:"code,omitempty"`
	CodeHash string            `json:"code_hash,omitempty"`
	Storage  map[string]string `json:"storage,omitempty"`
}

func main() {
	genesisFile := flag.String("genesis", "", "Path to genesis.json file")
	output := flag.String("output", "", "Output JSONL file path")
	flag.Parse()

	if *genesisFile == "" || *output == "" {
		log.Fatal("Usage: genesis-to-jsonl --genesis=<genesis.json> --output=<output.jsonl>")
	}

	// Read genesis file
	data, err := os.ReadFile(*genesisFile)
	if err != nil {
		log.Fatalf("Failed to read genesis file: %v", err)
	}

	var genesis Genesis
	if err := json.Unmarshal(data, &genesis); err != nil {
		log.Fatalf("Failed to parse genesis JSON: %v", err)
	}

	log.Printf("=== Genesis Conversion ===")
	log.Printf("Accounts: %d", len(genesis.Alloc))

	// Create a minimal config for genesis
	config := &params.ChainConfig{
		ChainID: big.NewInt(96369),
		HomesteadBlock: big.NewInt(0),
		EIP150Block: big.NewInt(0),
		EIP155Block: big.NewInt(0),
		EIP158Block: big.NewInt(0),
		ByzantiumBlock: big.NewInt(0),
		ConstantinopleBlock: big.NewInt(0),
		PetersburgBlock: big.NewInt(0),
		IstanbulBlock: big.NewInt(0),
		MuirGlacierBlock: big.NewInt(0),
		BerlinBlock: big.NewInt(0),
		LondonBlock: big.NewInt(0),
	}

	// Convert to geth Genesis
	gethGen := &core.Genesis{
		Config:     config,
		Nonce:      hexToUint64(genesis.Nonce),
		Timestamp:  hexToUint64(genesis.Timestamp),
		ExtraData:  hexutil.MustDecode(genesis.ExtraData),
		GasLimit:   hexToUint64(genesis.GasLimit),
		Difficulty: hexToBigInt(genesis.Difficulty),
		Mixhash:    common.HexToHash(genesis.MixHash),
		Coinbase:   common.HexToAddress(genesis.Coinbase),
		Alloc:      make(core.GenesisAlloc),
		Number:     hexToUint64(genesis.Number),
		GasUsed:    hexToUint64(genesis.GasUsed),
		ParentHash: common.HexToHash(genesis.ParentHash),
	}

	if genesis.BaseFee != "" {
		gethGen.BaseFee = hexToBigInt(genesis.BaseFee)
	}

	// Copy alloc
	for addr, acc := range genesis.Alloc {
		gethGen.Alloc[addr] = acc
	}

	// Create genesis block
	block := gethGen.ToBlock()

	log.Printf("Block Number: %d", block.NumberU64())
	log.Printf("Block Hash: %s", block.Hash().Hex())
	log.Printf("State Root: %s", block.Root().Hex())

	// Encode header to RLP
	headerRLP, err := rlp.EncodeToBytes(block.Header())
	if err != nil {
		log.Fatalf("Failed to encode header: %v", err)
	}

	// Encode body to RLP (genesis has no transactions)
	bodyRLP, err := rlp.EncodeToBytes([]*types.Transaction{})
	if err != nil {
		log.Fatalf("Failed to encode body: %v", err)
	}

	// Build JSONL block with state
	jsonlBlock := &JSONLBlock{
		Number:       block.NumberU64(),
		Hash:         block.Hash().Hex(),
		HeaderRLP:    common.Bytes2Hex(headerRLP),
		BodyRLP:      common.Bytes2Hex(bodyRLP),
		ReceiptsRLP:  "", // Genesis has no receipts
		StateChanges: make(map[string]*JSONLAccount),
	}

	// Add state from genesis allocations
	for addr, acc := range genesis.Alloc {
		jsonlAcc := &JSONLAccount{
			Balance: acc.Balance.String(),
			Nonce:   acc.Nonce,
		}

		if len(acc.Code) > 0 {
			jsonlAcc.Code = common.Bytes2Hex(acc.Code)
		}

		if len(acc.Storage) > 0 {
			jsonlAcc.Storage = make(map[string]string)
			for k, v := range acc.Storage {
				jsonlAcc.Storage[k.Hex()] = v.Hex()
			}
		}

		jsonlBlock.StateChanges[addr.Hex()] = jsonlAcc

		log.Printf("Account %s: balance=%s, nonce=%d, code=%d bytes, storage=%d slots",
			addr.Hex(), acc.Balance.String(), acc.Nonce, len(acc.Code), len(acc.Storage))
	}

	// Write to JSONL file
	file, err := os.Create(*output)
	if err != nil {
		log.Fatalf("Failed to create output file: %v", err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	if err := encoder.Encode(jsonlBlock); err != nil {
		log.Fatalf("Failed to write JSONL: %v", err)
	}

	log.Printf("\n=== Conversion Complete ===")
	log.Printf("Output: %s", *output)
	log.Printf("Accounts with state: %d", len(jsonlBlock.StateChanges))
}

func hexToUint64(hex string) uint64 {
	if hex == "" || hex == "0x" || hex == "0x0" {
		return 0
	}
	val, err := hexutil.DecodeUint64(hex)
	if err != nil {
		return 0
	}
	return val
}

func hexToBigInt(hex string) *big.Int {
	if hex == "" || hex == "0x" || hex == "0x0" {
		return big.NewInt(0)
	}
	val, ok := new(big.Int).SetString(hex[2:], 16)
	if !ok {
		return big.NewInt(0)
	}
	return val
}

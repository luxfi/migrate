// extract-genesis extracts genesis state from a SubnetEVM database
// using the subnetevm exporter which handles 32-byte chain ID prefixed keys.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"os"

	"github.com/luxfi/geth/common"
	"github.com/luxfi/geth/common/hexutil"
	"github.com/luxfi/migrate"
	"github.com/luxfi/migrate/subnetevm"
)

// GenesisAlloc is a simplified genesis allocation format
type GenesisAlloc map[common.Address]GenesisAccount

// GenesisAccount represents an account in the genesis allocation
type GenesisAccount struct {
	Balance string            `json:"balance"`
	Code    string            `json:"code,omitempty"`
	Nonce   uint64            `json:"nonce,omitempty"`
	Storage map[string]string `json:"storage,omitempty"`
}

// Genesis is the output format for the extracted genesis
type Genesis struct {
	Config     interface{}  `json:"config"`
	Nonce      string       `json:"nonce"`
	Timestamp  string       `json:"timestamp"`
	ExtraData  string       `json:"extraData"`
	GasLimit   string       `json:"gasLimit"`
	Difficulty string       `json:"difficulty"`
	MixHash    string       `json:"mixHash"`
	Coinbase   string       `json:"coinbase"`
	Alloc      GenesisAlloc `json:"alloc"`
}

func main() {
	dbPath := flag.String("db", "", "Path to PebbleDB database")
	outputPath := flag.String("output", "genesis.json", "Output genesis file path")
	checkAddr := flag.String("check", "", "Optional: address to check balance for (hex)")
	flag.Parse()

	if *dbPath == "" {
		log.Fatal("Usage: extract-genesis -db <pebbledb-path> [-output genesis.json] [-check 0xaddr]")
	}

	log.Printf("Opening database: %s", *dbPath)

	// Create exporter
	exporter, err := subnetevm.NewExporter(migrate.ExporterConfig{
		DatabasePath: *dbPath,
	})
	if err != nil {
		log.Fatalf("Failed to create exporter: %v", err)
	}

	// Initialize exporter
	if err := exporter.Init(migrate.ExporterConfig{DatabasePath: *dbPath}); err != nil {
		log.Fatalf("Failed to initialize exporter: %v", err)
	}
	defer exporter.Close()

	// Get chain info
	info, err := exporter.GetInfo()
	if err != nil {
		log.Fatalf("Failed to get chain info: %v", err)
	}
	log.Printf("Chain ID: %s", info.ChainID)
	log.Printf("Genesis Hash: %s", info.GenesisHash.Hex())
	log.Printf("Current Height: %d", info.CurrentHeight)
	log.Printf("State Root: %s", info.StateRoot.Hex())

	// Export block 0 to get genesis header info
	ctx := context.Background()
	blockChan, errChan := exporter.ExportBlocks(ctx, 0, 0)

	var genesisBlock *migrate.BlockData
	select {
	case block := <-blockChan:
		genesisBlock = block
		log.Printf("Got genesis block %d", block.Number)
	case err := <-errChan:
		if err != nil {
			log.Fatalf("Failed to export genesis block: %v", err)
		}
	}

	if genesisBlock == nil {
		log.Fatal("No genesis block found")
	}

	// Export state at block 0
	log.Printf("Exporting state (this may take a while)...")
	accountChan, stateErrChan := exporter.ExportState(ctx, 0)

	alloc := make(GenesisAlloc)
	accountCount := 0
	totalBalance := new(big.Int)

	var checkAddrParsed common.Address
	if *checkAddr != "" {
		checkAddrParsed = common.HexToAddress(*checkAddr)
		log.Printf("Will check balance for address: %s", checkAddrParsed.Hex())
	}

	for account := range accountChan {
		accountCount++
		if account.Balance != nil {
			totalBalance.Add(totalBalance, account.Balance)
		}

		genAccount := GenesisAccount{
			Balance: "0x" + account.Balance.Text(16),
			Nonce:   account.Nonce,
		}

		if len(account.Code) > 0 {
			genAccount.Code = hexutil.Encode(account.Code)
		}

		if len(account.Storage) > 0 {
			genAccount.Storage = make(map[string]string)
			for k, v := range account.Storage {
				genAccount.Storage[k.Hex()] = v.Hex()
			}
		}

		alloc[account.Address] = genAccount

		// Check specific address
		if *checkAddr != "" && account.Address == checkAddrParsed {
			log.Printf("FOUND target address %s: balance=%s", account.Address.Hex(), account.Balance.String())
		}

		if accountCount%1000 == 0 {
			log.Printf("Processed %d accounts...", accountCount)
		}
	}

	// Check for state export errors
	select {
	case err := <-stateErrChan:
		if err != nil {
			log.Printf("Warning: state export error: %v", err)
		}
	default:
	}

	log.Printf("Exported %d accounts", accountCount)
	log.Printf("Total balance: %s wei", totalBalance.String())

	// Convert to ETH for readability
	eth := new(big.Float).Quo(new(big.Float).SetInt(totalBalance), big.NewFloat(1e18))
	log.Printf("Total balance: %s ETH", eth.Text('f', 2))

	// Build genesis struct
	genesis := Genesis{
		Config: map[string]interface{}{
			"chainId":             info.ChainID,
			"homesteadBlock":      0,
			"eip150Block":         0,
			"eip155Block":         0,
			"eip158Block":         0,
			"byzantiumBlock":      0,
			"constantinopleBlock": 0,
			"petersburgBlock":     0,
			"istanbulBlock":       0,
			"berlinBlock":         0,
			"londonBlock":         0,
		},
		Nonce:      "0x0",
		Timestamp:  fmt.Sprintf("0x%x", genesisBlock.Timestamp),
		ExtraData:  hexutil.Encode(genesisBlock.ExtraData),
		GasLimit:   fmt.Sprintf("0x%x", genesisBlock.GasLimit),
		Difficulty: "0x1",
		MixHash:    genesisBlock.MixHash.Hex(),
		Coinbase:   genesisBlock.Coinbase.Hex(),
		Alloc:      alloc,
	}

	// Write to file
	file, err := os.Create(*outputPath)
	if err != nil {
		log.Fatalf("Failed to create output file: %v", err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(genesis); err != nil {
		log.Fatalf("Failed to encode genesis: %v", err)
	}

	log.Printf("Genesis written to: %s", *outputPath)
	log.Printf("Total accounts: %d", len(alloc))
}

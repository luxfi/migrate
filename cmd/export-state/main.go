package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/luxfi/geth/common"
	"github.com/luxfi/geth/core/types"
	"github.com/luxfi/geth/rlp"
	"github.com/luxfi/migrate"
	"github.com/luxfi/migrate/subnetevm"
)

// JSONLBlock represents a block in JSONL format with state
type JSONLBlock struct {
	Number       uint64                      `json:"number"`
	Hash         string                      `json:"hash"`
	HeaderRLP    string                      `json:"header_rlp"`
	BodyRLP      string                      `json:"body_rlp"`
	ReceiptsRLP  string                      `json:"receipts_rlp"`
	StateChanges map[string]*JSONLAccount    `json:"state_changes,omitempty"`
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
	sourceDB := flag.String("source-db", "", "Path to source PebbleDB database")
	output := flag.String("output", "", "Output JSONL file path")
	startBlock := flag.Uint64("start", 0, "Start block number")
	endBlock := flag.Uint64("end", 0, "End block number (0 = to end)")
	batchSize := flag.Int("batch-size", 1000, "Blocks per output file")
	exportState := flag.Bool("export-state", true, "Export state changes")
	exportReceipts := flag.Bool("export-receipts", true, "Export receipts")
	flag.Parse()

	if *sourceDB == "" || *output == "" {
		log.Fatal("Usage: export-state --source-db=<path> --output=<file> [--start=N] [--end=N] [--batch-size=N]")
	}

	// Create exporter
	config := migrate.ExporterConfig{
		VMType:         migrate.VMTypeSubnetEVM,
		DatabasePath:   *sourceDB,
		DatabaseType:   "pebble",
		ExportState:    *exportState,
		ExportReceipts: *exportReceipts,
	}

	exporter, err := subnetevm.NewExporter(config)
	if err != nil {
		log.Fatalf("Failed to create exporter: %v", err)
	}

	if err := exporter.Init(config); err != nil {
		log.Fatalf("Failed to initialize exporter: %v", err)
	}
	defer exporter.Close()

	// Get chain info
	info, err := exporter.GetInfo()
	if err != nil {
		log.Fatalf("Failed to get chain info: %v", err)
	}

	log.Printf("=== Export Configuration ===")
	log.Printf("Source DB: %s", *sourceDB)
	log.Printf("Chain ID: %s", info.ChainID)
	log.Printf("Genesis Hash: %s", info.GenesisHash)
	log.Printf("Current Height: %d", info.CurrentHeight)
	log.Printf("Export State: %v", *exportState)
	log.Printf("Export Receipts: %v", *exportReceipts)

	// Determine block range
	start := *startBlock
	end := *endBlock
	if end == 0 || end > info.CurrentHeight {
		end = info.CurrentHeight
	}

	log.Printf("\n=== Export Range ===")
	log.Printf("Start Block: %d", start)
	log.Printf("End Block: %d", end)
	log.Printf("Total Blocks: %d", end-start+1)
	log.Printf("Batch Size: %d", *batchSize)

	// Export blocks
	ctx := context.Background()
	startTime := time.Now()
	totalBlocks := uint64(0)
	currentBatch := 0

	// Prepare output directory
	outputDir := filepath.Dir(*output)
	if outputDir != "" && outputDir != "." {
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			log.Fatalf("Failed to create output directory: %v", err)
		}
	}

	var writer *bufio.Writer
	var file *os.File

	// Helper to open new batch file
	openBatchFile := func(batchStart uint64) error {
		if file != nil {
			if err := writer.Flush(); err != nil {
				return err
			}
			file.Close()
		}

		// Generate batch filename
		base := filepath.Base(*output)
		ext := filepath.Ext(base)
		name := base[:len(base)-len(ext)]

		var batchFile string
		if *batchSize == 0 {
			batchFile = *output
		} else {
			batchFile = filepath.Join(outputDir, fmt.Sprintf("%s-part-%06d%s", name, currentBatch, ext))
		}

		f, err := os.Create(batchFile)
		if err != nil {
			return fmt.Errorf("failed to create output file: %w", err)
		}

		file = f
		writer = bufio.NewWriter(f)
		log.Printf("Created batch file: %s (blocks %d-%d)", batchFile, batchStart, min(batchStart+uint64(*batchSize)-1, end))
		return nil
	}

	// Export blocks in batches
	for batchStart := start; batchStart <= end; {
		batchEnd := min(batchStart+uint64(*batchSize)-1, end)

		// Open new batch file
		if err := openBatchFile(batchStart); err != nil {
			log.Fatalf("Failed to open batch file: %v", err)
		}

		log.Printf("\nExporting blocks %d-%d...", batchStart, batchEnd)

		blocksChan, errsChan := exporter.ExportBlocks(ctx, batchStart, batchEnd)

		batchBlocks := uint64(0)
		batchStart = batchEnd + 1

	blockLoop:
		for {
			select {
			case block, ok := <-blocksChan:
				if !ok {
					break blockLoop
				}

				// Convert to JSONL format
				jsonlBlock := &JSONLBlock{
					Number:      block.Number,
					Hash:        block.Hash.Hex(),
					HeaderRLP:   common.Bytes2Hex(block.Header),
					BodyRLP:     common.Bytes2Hex(block.Body),
					ReceiptsRLP: common.Bytes2Hex(block.Receipts),
				}

				// Add state changes if present
				if len(block.StateChanges) > 0 {
					jsonlBlock.StateChanges = make(map[string]*JSONLAccount)
					for addr, acc := range block.StateChanges {
						jsonlAcc := &JSONLAccount{
							Balance:  acc.Balance.String(),
							Nonce:    acc.Nonce,
							CodeHash: acc.CodeHash.Hex(),
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
					}
				}

				// Write to JSONL
				data, err := json.Marshal(jsonlBlock)
				if err != nil {
					log.Printf("Failed to marshal block %d: %v", block.Number, err)
					continue
				}

				if _, err := writer.Write(data); err != nil {
					log.Fatalf("Failed to write block %d: %v", block.Number, err)
				}
				if _, err := writer.WriteString("\n"); err != nil {
					log.Fatalf("Failed to write newline: %v", err)
				}

				batchBlocks++
				totalBlocks++

				if batchBlocks%100 == 0 {
					elapsed := time.Since(startTime)
					rate := float64(totalBlocks) / elapsed.Seconds()
					log.Printf("Progress: %d/%d blocks (%.1f%%), rate: %.1f blocks/sec",
						totalBlocks, end-start+1, float64(totalBlocks)/float64(end-start+1)*100, rate)
				}

			case err, ok := <-errsChan:
				if !ok {
					break blockLoop
				}
				if err != nil {
					log.Printf("Export error: %v", err)
				}
			}
		}

		log.Printf("Batch complete: %d blocks exported", batchBlocks)
		currentBatch++
	}

	// Flush and close final file
	if writer != nil {
		if err := writer.Flush(); err != nil {
			log.Fatalf("Failed to flush writer: %v", err)
		}
	}
	if file != nil {
		file.Close()
	}

	duration := time.Since(startTime)
	rate := float64(totalBlocks) / duration.Seconds()

	log.Printf("\n=== Export Complete ===")
	log.Printf("Total Blocks Exported: %d", totalBlocks)
	log.Printf("Duration: %s", duration)
	log.Printf("Average Rate: %.2f blocks/sec", rate)
	log.Printf("Output Files: %d", currentBatch)
}

func min(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

// decodeTransactions decodes transactions from RLP body
func decodeTransactions(bodyRLP []byte) ([]*migrate.Transaction, error) {
	if len(bodyRLP) == 0 {
		return nil, nil
	}

	type bodyData struct {
		Txs    []*types.Transaction
		Uncles []*types.Header
	}

	var body bodyData
	if err := rlp.DecodeBytes(bodyRLP, &body); err != nil {
		return nil, err
	}

	txs := make([]*migrate.Transaction, len(body.Txs))
	for i, tx := range body.Txs {
		v, r, s := tx.RawSignatureValues()

		var to *common.Address
		if tx.To() != nil {
			addr := *tx.To()
			to = &addr
		}

		txs[i] = &migrate.Transaction{
			Hash:     tx.Hash(),
			Nonce:    tx.Nonce(),
			To:       to,
			Value:    tx.Value(),
			Gas:      tx.Gas(),
			GasPrice: tx.GasPrice(),
			Data:     tx.Data(),
			V:        v,
			R:        r,
			S:        s,
		}

		// Add EIP-1559 fields if present
		if tx.Type() == types.DynamicFeeTxType {
			txs[i].GasTipCap = tx.GasTipCap()
			txs[i].GasFeeCap = tx.GasFeeCap()
		}
	}

	return txs, nil
}

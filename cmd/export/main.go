// Command export exports blockchain data from PebbleDB to JSONL format
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/luxfi/migrate"
	"github.com/luxfi/migrate/jsonl"
	_ "github.com/luxfi/migrate/subnetevm" // Register SubnetEVM exporter
)

func main() {
	var (
		dbPath     = flag.String("db", "", "Path to PebbleDB database")
		outputPath = flag.String("output", "", "Output JSONL file path")
		startBlock = flag.Uint64("start", 0, "Start block number")
		endBlock   = flag.Uint64("end", 0, "End block number (0 = head)")
	)
	flag.Parse()

	if *dbPath == "" || *outputPath == "" {
		fmt.Println("Usage: export -db <path> -output <file.jsonl> [-start N] [-end N]")
		fmt.Println()
		fmt.Println("Export blockchain blocks with full state from PebbleDB to JSONL format.")
		fmt.Println("State is always included with genesis block for balance queries.")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  -db      Path to PebbleDB database directory")
		fmt.Println("  -output  Output JSONL file path")
		fmt.Println("  -start   Start block number (default: 0)")
		fmt.Println("  -end     End block number (default: head block)")
		os.Exit(1)
	}

	// Create exporter
	exporter, err := migrate.NewExporter(migrate.ExporterConfig{
		VMType:       migrate.VMTypeSubnetEVM,
		DatabasePath: *dbPath,
		DatabaseType: "pebble",
	})
	if err != nil {
		log.Fatalf("Failed to create exporter: %v", err)
	}
	defer exporter.Close()

	// Get chain info
	info, err := exporter.GetInfo()
	if err != nil {
		log.Fatalf("Failed to get chain info: %v", err)
	}

	fmt.Printf("Chain Info:\n")
	fmt.Printf("  Chain ID: %v\n", info.ChainID)
	fmt.Printf("  Genesis Hash: %s\n", info.GenesisHash.Hex())
	fmt.Printf("  Head Block: %d\n", info.CurrentHeight)
	fmt.Printf("  State Root: %s\n", info.StateRoot.Hex())
	fmt.Println()

	// Set end block to head if not specified
	end := *endBlock
	if end == 0 {
		end = info.CurrentHeight
	}

	// Create output directory if needed
	if dir := filepath.Dir(*outputPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Fatalf("Failed to create output directory: %v", err)
		}
	}

	// Create JSONL writer
	writer, err := jsonl.NewWriter(*outputPath)
	if err != nil {
		log.Fatalf("Failed to create writer: %v", err)
	}
	defer writer.Close()

	fmt.Printf("Exporting blocks %d to %d with full state...\n", *startBlock, end)

	// Export blocks with full state (state attached to genesis block)
	ctx := context.Background()
	blocks, errs := exporter.ExportBlocksWithState(ctx, *startBlock, end)

	var count uint64
	for block := range blocks {
		if err := writer.WriteBlock(block); err != nil {
			log.Fatalf("Failed to write block %d: %v", block.Number, err)
		}
		count++
		if count%1000 == 0 {
			fmt.Printf("  Exported %d blocks (current: %d)\n", count, block.Number)
		}
	}

	// Check for errors
	for err := range errs {
		if err != nil {
			log.Fatalf("Export error: %v", err)
		}
	}

	// Flush final data
	if err := writer.Flush(); err != nil {
		log.Fatalf("Failed to flush: %v", err)
	}

	fmt.Printf("\nExported %d blocks to %s\n", count, *outputPath)
}

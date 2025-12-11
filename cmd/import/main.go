// import-blocks imports JSONL blocks directly to a SubnetEVM LevelDB database
package main

import (
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/luxfi/geth/common"
	"github.com/luxfi/migrate"
	"github.com/luxfi/migrate/jsonl"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

// Database key prefixes (same as SubnetEVM/geth)
var (
	headerPrefix       = []byte("h") // headerPrefix + num (uint64 big endian) + hash -> header
	headerHashSuffix   = []byte("n") // headerPrefix + num (uint64 big endian) + headerHashSuffix -> hash
	headerNumberPrefix = []byte("H") // headerNumberPrefix + hash -> num (uint64 big endian)
	blockBodyPrefix    = []byte("b") // blockBodyPrefix + num (uint64 big endian) + hash -> block body
	blockReceiptsPrefix = []byte("r") // blockReceiptsPrefix + num (uint64 big endian) + hash -> block receipts
	headBlockKey       = []byte("LastBlock")
	headHeaderKey      = []byte("LastHeader")
)

func encodeBlockNumber(number uint64) []byte {
	enc := make([]byte, 8)
	binary.BigEndian.PutUint64(enc, number)
	return enc
}

func headerKey(number uint64, hash common.Hash) []byte {
	return append(append(headerPrefix, encodeBlockNumber(number)...), hash.Bytes()...)
}

func headerHashKey(number uint64) []byte {
	return append(append(headerPrefix, encodeBlockNumber(number)...), headerHashSuffix...)
}

func headerNumberKey(hash common.Hash) []byte {
	return append(headerNumberPrefix, hash.Bytes()...)
}

func blockBodyKey(number uint64, hash common.Hash) []byte {
	return append(append(blockBodyPrefix, encodeBlockNumber(number)...), hash.Bytes()...)
}

func blockReceiptsKey(number uint64, hash common.Hash) []byte {
	return append(append(blockReceiptsPrefix, encodeBlockNumber(number)...), hash.Bytes()...)
}

func main() {
	jsonlPath := flag.String("jsonl", "", "Path to JSONL blocks file")
	dbPath := flag.String("db", "", "Path to LevelDB database directory")
	flag.Parse()

	if *jsonlPath == "" || *dbPath == "" {
		fmt.Println("Usage: import-blocks -jsonl <path> -db <path>")
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Open JSONL reader
	reader, err := jsonl.NewReader(*jsonlPath)
	if err != nil {
		log.Fatalf("Failed to open JSONL file: %v", err)
	}
	defer reader.Close()

	// Read all blocks
	blocks, err := reader.ReadAllBlocks()
	if err != nil {
		log.Fatalf("Failed to read blocks: %v", err)
	}
	fmt.Printf("Read %d blocks from JSONL\n", len(blocks))

	if len(blocks) == 0 {
		log.Fatal("No blocks to import")
	}

	// Create/open LevelDB database
	db, err := leveldb.OpenFile(*dbPath, &opt.Options{
		ErrorIfMissing: false,
	})
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Import blocks
	batch := new(leveldb.Batch)
	var lastBlock *migrate.BlockData

	for i, block := range blocks {
		// Write header
		if len(block.Header) > 0 {
			batch.Put(headerKey(block.Number, block.Hash), block.Header)
		}

		// Write canonical hash mapping (block number -> hash)
		batch.Put(headerHashKey(block.Number), block.Hash.Bytes())

		// Write hash -> number mapping
		batch.Put(headerNumberKey(block.Hash), encodeBlockNumber(block.Number))

		// Write body if present
		if len(block.Body) > 0 {
			batch.Put(blockBodyKey(block.Number, block.Hash), block.Body)
		}

		// Write receipts if present
		if len(block.Receipts) > 0 {
			batch.Put(blockReceiptsKey(block.Number, block.Hash), block.Receipts)
		}

		lastBlock = block

		// Commit batch every 1000 blocks
		if (i+1)%1000 == 0 {
			if err := db.Write(batch, nil); err != nil {
				log.Fatalf("Failed to write batch at block %d: %v", block.Number, err)
			}
			batch.Reset()
			fmt.Printf("Imported %d/%d blocks\n", i+1, len(blocks))
		}
	}

	// Write remaining batch
	if batch.Len() > 0 {
		if err := db.Write(batch, nil); err != nil {
			log.Fatalf("Failed to write final batch: %v", err)
		}
	}

	// Update head pointers
	if lastBlock != nil {
		if err := db.Put(headBlockKey, lastBlock.Hash.Bytes(), nil); err != nil {
			log.Fatalf("Failed to set head block: %v", err)
		}
		if err := db.Put(headHeaderKey, lastBlock.Hash.Bytes(), nil); err != nil {
			log.Fatalf("Failed to set head header: %v", err)
		}
		fmt.Printf("Set head block to %d (%s)\n", lastBlock.Number, lastBlock.Hash.Hex())
	}

	// Write VM metadata for SubnetEVM
	vmDir := *dbPath + "/../vm"
	if err := os.MkdirAll(vmDir, 0755); err != nil {
		log.Printf("Warning: could not create vm directory: %v", err)
	} else {
		vmDB, err := leveldb.OpenFile(vmDir, nil)
		if err == nil {
			defer vmDB.Close()
			// lastAccepted = hash of tip block
			vmDB.Put([]byte("lastAccepted"), lastBlock.Hash.Bytes(), nil)
			// lastAcceptedHeight = block number
			vmDB.Put([]byte("lastAcceptedHeight"), encodeBlockNumber(lastBlock.Number), nil)
			// initialized = true
			vmDB.Put([]byte("initialized"), []byte{1}, nil)
			fmt.Printf("Set VM metadata in %s\n", vmDir)
		}
	}

	fmt.Printf("\nImport complete: %d blocks imported\n", len(blocks))
	fmt.Printf("Head block: %d (%s)\n", lastBlock.Number, lastBlock.Hash.Hex())

	// Print verification info
	info := map[string]interface{}{
		"blocksImported": len(blocks),
		"headBlock":      lastBlock.Number,
		"headHash":       lastBlock.Hash.Hex(),
		"stateRoot":      lastBlock.StateRoot.Hex(),
	}
	infoJSON, _ := json.MarshalIndent(info, "", "  ")
	fmt.Printf("\nImport info:\n%s\n", infoJSON)
}

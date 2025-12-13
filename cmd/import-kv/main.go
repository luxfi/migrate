// import-kv imports raw state trie KV pairs into a Badger ethdb
// Input format: JSONL with {"k":"0x...","v":"0x..."} per line
// Imports: A* (account trie nodes), O* (storage trie nodes), c* (code blobs), L* (state IDs)
package main

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
)

// KVEntry is the minimal JSON format for raw KV pairs
type KVEntry struct {
	K string `json:"k"` // hex-encoded key with 0x prefix
	V string `json:"v"` // hex-encoded value with 0x prefix
}

func main() {
	dbPath := flag.String("db", "", "Target Badger DB path")
	inPath := flag.String("in", "", "Input JSONL file path")
	batchSize := flag.Int("batch", 10000, "Batch size for writes")
	verifyKey := flag.String("verify-key", "", "Optional hex key to verify exists after import")
	dryRun := flag.Bool("dry-run", false, "Parse input but don't write to DB")
	flag.Parse()

	if *dbPath == "" || *inPath == "" {
		fmt.Println("Usage: import-kv -db <badger-dir> -in <file.jsonl>")
		fmt.Println()
		fmt.Println("Imports raw state trie KV pairs directly into C-Chain ethdb (Badger).")
		fmt.Println("This is the ONLY correct way to import state - RPC cannot create state.")
		fmt.Println()
		fmt.Println("Input format: {\"k\":\"0x...\",\"v\":\"0x...\"} (raw hex, one per line)")
		fmt.Println()
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Open input file
	f, err := os.Open(*inPath)
	if err != nil {
		log.Fatalf("Failed to open input file: %v", err)
	}
	defer f.Close()

	// Open Badger DB
	var db *badger.DB
	if !*dryRun {
		opts := badger.DefaultOptions(*dbPath)
		opts.Logger = nil // Suppress Badger logs

		db, err = badger.Open(opts)
		if err != nil {
			log.Fatalf("Failed to open Badger DB: %v", err)
		}
		defer db.Close()
		fmt.Printf("Target DB: %s\n", *dbPath)
	} else {
		fmt.Println("DRY RUN - parsing only, not writing")
	}

	fmt.Printf("Input: %s\n", *inPath)

	scanner := bufio.NewScanner(f)
	// Increase buffer for large lines
	buf := make([]byte, 0, 16*1024*1024)
	scanner.Buffer(buf, 16*1024*1024)

	startTime := time.Now()
	totalCount := int64(0)
	totalBytes := int64(0)
	batchCount := 0
	lastReport := time.Now()

	// Stats by prefix
	prefixStats := make(map[byte]int64)

	var txn *badger.Txn
	if db != nil {
		txn = db.NewTransaction(true)
	}

	fmt.Println("\nImporting...")

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry KVEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			log.Printf("Parse error at line %d: %v", totalCount+1, err)
			continue
		}

		// Decode hex
		key, err := hex.DecodeString(strings.TrimPrefix(entry.K, "0x"))
		if err != nil {
			log.Printf("Invalid key hex at line %d: %v", totalCount+1, err)
			continue
		}
		val, err := hex.DecodeString(strings.TrimPrefix(entry.V, "0x"))
		if err != nil {
			log.Printf("Invalid value hex at line %d: %v", totalCount+1, err)
			continue
		}

		// Track stats by first byte after namespace (assuming 32-byte namespace)
		if len(key) > 32 {
			prefixStats[key[32]]++
		} else if len(key) > 0 {
			prefixStats[key[0]]++
		}

		if !*dryRun {
			// Write to Badger
			if err := txn.Set(key, val); err == badger.ErrTxnTooBig {
				// Commit current batch
				if err := txn.Commit(); err != nil {
					log.Fatalf("Failed to commit batch: %v", err)
				}
				txn = db.NewTransaction(true)
				if err := txn.Set(key, val); err != nil {
					log.Fatalf("Failed to set key after commit: %v", err)
				}
			} else if err != nil {
				log.Fatalf("Failed to set key: %v", err)
			}
		}

		totalCount++
		totalBytes += int64(len(key) + len(val))
		batchCount++

		// Commit batch periodically
		if !*dryRun && batchCount >= *batchSize {
			if err := txn.Commit(); err != nil {
				log.Fatalf("Failed to commit batch: %v", err)
			}
			txn = db.NewTransaction(true)
			batchCount = 0
		}

		// Progress report every 10 seconds
		if time.Since(lastReport) > 10*time.Second {
			elapsed := time.Since(startTime)
			rate := float64(totalCount) / elapsed.Seconds()
			fmt.Printf("Progress: %d entries, %.2f MB, %.0f entries/sec\n",
				totalCount, float64(totalBytes)/(1024*1024), rate)
			lastReport = time.Now()
		}
	}

	// Final commit
	if !*dryRun && txn != nil {
		if err := txn.Commit(); err != nil {
			log.Fatalf("Failed to commit final batch: %v", err)
		}
	}

	if err := scanner.Err(); err != nil {
		log.Fatalf("Scanner error: %v", err)
	}

	elapsed := time.Since(startTime)
	fmt.Printf("\n=== Import Complete ===\n")
	fmt.Printf("Total entries: %d\n", totalCount)
	fmt.Printf("Total bytes: %.2f MB\n", float64(totalBytes)/(1024*1024))
	fmt.Printf("Import time: %v\n", elapsed)
	fmt.Printf("Rate: %.0f entries/sec\n", float64(totalCount)/elapsed.Seconds())

	// Print stats by prefix
	fmt.Println("\nBy prefix:")
	prefixNames := map[byte]string{
		'A': "AccountTrie",
		'O': "StorageTrie",
		'c': "Code",
		'L': "StateID",
	}
	for prefix, count := range prefixStats {
		name, ok := prefixNames[prefix]
		if !ok {
			name = fmt.Sprintf("Unknown(0x%02x)", prefix)
		}
		fmt.Printf("  %s (%c): %d entries\n", name, prefix, count)
	}

	// Verify key exists if requested
	if *verifyKey != "" && !*dryRun {
		key, err := hex.DecodeString(strings.TrimPrefix(*verifyKey, "0x"))
		if err != nil {
			log.Printf("Invalid verify key: %v", err)
		} else {
			err = db.View(func(txn *badger.Txn) error {
				_, err := txn.Get(key)
				return err
			})
			if err == badger.ErrKeyNotFound {
				fmt.Printf("\nWARNING: Verify key NOT FOUND: %s\n", *verifyKey)
			} else if err != nil {
				fmt.Printf("\nERROR verifying key: %v\n", err)
			} else {
				fmt.Printf("\nVerify key EXISTS: %s\n", *verifyKey)
			}
		}
	}
}

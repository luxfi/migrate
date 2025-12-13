// export-kv exports raw state trie KV pairs from PebbleDB for direct import
// Output format: JSONL with {"k":"0x...","v":"0x..."} per line
// Exports: A* (account trie nodes), O* (storage trie nodes), c* (code blobs), L* (state IDs)
package main

import (
	"bufio"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/cockroachdb/pebble"
)

// Database key prefixes (from geth/core/rawdb/schema.go)
var prefixes = []struct {
	name   string
	prefix byte
}{
	{"AccountTrie", 'A'}, // Account trie: A + hexPath → trie node
	{"StorageTrie", 'O'}, // Storage trie: O + accountHash + hexPath → trie node
	{"Code", 'c'},        // Code: c + codeHash → contract code
	{"StateID", 'L'},     // State ID: L + stateRoot → state ID
}

func main() {
	srcPath := flag.String("db", "", "Source PebbleDB path")
	namespace := flag.String("namespace", "", "Optional 32-byte hex namespace prefix (e.g., C-chain ID)")
	outPath := flag.String("out", "", "Output JSONL file path")
	prefixList := flag.String("prefixes", "A,O,c", "Comma-separated prefixes to export (default: A,O,c)")
	verifyOnly := flag.Bool("verify", false, "Only verify/count, don't export")
	flag.Parse()

	if *srcPath == "" {
		fmt.Println("Usage: export-kv -db <pebble-dir> -out <file.jsonl> [-namespace <hex32>]")
		fmt.Println()
		fmt.Println("Exports raw state trie KV pairs for direct import into C-Chain ethdb.")
		fmt.Println("This is the ONLY correct way to migrate state - RPC block import cannot create state.")
		fmt.Println()
		fmt.Println("Output format: {\"k\":\"0x...\",\"v\":\"0x...\"} (raw hex, one per line)")
		fmt.Println()
		fmt.Println("Prefixes:")
		fmt.Println("  A - Account trie nodes (PathDB)")
		fmt.Println("  O - Storage trie nodes (PathDB)")
		fmt.Println("  c - Contract code blobs")
		fmt.Println("  L - State ID mappings (optional)")
		fmt.Println()
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Parse namespace
	var namespaceBytes []byte
	if *namespace != "" {
		var err error
		namespaceBytes, err = hex.DecodeString(*namespace)
		if err != nil {
			log.Fatalf("Invalid namespace hex: %v", err)
		}
		if len(namespaceBytes) != 32 {
			log.Fatalf("Namespace must be 32 bytes, got %d", len(namespaceBytes))
		}
	}

	// Open source database read-only
	srcDB, err := pebble.Open(*srcPath, &pebble.Options{ReadOnly: true})
	if err != nil {
		log.Fatalf("Failed to open source DB: %v", err)
	}
	defer srcDB.Close()

	fmt.Printf("Source DB: %s\n", *srcPath)
	if len(namespaceBytes) > 0 {
		fmt.Printf("Namespace: 0x%s\n", hex.EncodeToString(namespaceBytes))
	}

	// Parse which prefixes to export
	exportPrefixes := make(map[byte]bool)
	for _, c := range *prefixList {
		if c != ',' {
			exportPrefixes[byte(c)] = true
		}
	}

	// Count entries first
	fmt.Println("\nScanning source database...")
	startTime := time.Now()
	totalCount := int64(0)
	totalSize := int64(0)

	for _, p := range prefixes {
		if !exportPrefixes[p.prefix] {
			continue
		}
		count, size := countPrefix(srcDB, namespaceBytes, p.prefix)
		totalCount += count
		totalSize += size
		fmt.Printf("  %s (%c): %d entries, %.2f MB\n", p.name, p.prefix, count, float64(size)/(1024*1024))
	}
	fmt.Printf("Total: %d entries, %.2f MB\n", totalCount, float64(totalSize)/(1024*1024))
	fmt.Printf("Scan time: %v\n", time.Since(startTime))

	// Check for root node
	rootKey := append(namespaceBytes, 'A')
	if hasKey(srcDB, rootKey) {
		fmt.Println("Root account trie node EXISTS (key length", len(rootKey), ")")
	} else {
		fmt.Println("WARNING: Root account trie node NOT FOUND")
	}

	if *verifyOnly {
		fmt.Println("\nVerify-only mode, exiting.")
		return
	}

	if *outPath == "" {
		log.Fatal("Output path (-out) required for export")
	}

	// Create output file
	f, err := os.Create(*outPath)
	if err != nil {
		log.Fatalf("Failed to create output file: %v", err)
	}
	defer f.Close()

	w := bufio.NewWriterSize(f, 16*1024*1024) // 16MB buffer
	defer w.Flush()

	fmt.Printf("\nExporting to: %s\n", *outPath)
	startTime = time.Now()
	exportedCount := int64(0)
	exportedBytes := int64(0)

	for _, p := range prefixes {
		if !exportPrefixes[p.prefix] {
			continue
		}
		count, bytes := exportPrefix(srcDB, w, namespaceBytes, p.prefix, p.name)
		exportedCount += count
		exportedBytes += bytes
	}

	w.Flush()

	elapsed := time.Since(startTime)
	fmt.Printf("\n=== Export Complete ===\n")
	fmt.Printf("Total entries: %d\n", exportedCount)
	fmt.Printf("Total bytes: %.2f MB\n", float64(exportedBytes)/(1024*1024))
	fmt.Printf("Export time: %v\n", elapsed)
	fmt.Printf("Rate: %.0f entries/sec\n", float64(exportedCount)/elapsed.Seconds())
	fmt.Printf("Output: %s\n", *outPath)
}

func countPrefix(db *pebble.DB, namespace []byte, prefix byte) (int64, int64) {
	// Build key range: namespace + prefix
	lower := append(namespace, prefix)
	upper := append(namespace, prefix+1)

	iter, err := db.NewIter(&pebble.IterOptions{
		LowerBound: lower,
		UpperBound: upper,
	})
	if err != nil {
		return 0, 0
	}
	defer iter.Close()

	var count, size int64
	for iter.First(); iter.Valid(); iter.Next() {
		count++
		size += int64(len(iter.Key()) + len(iter.Value()))
	}
	return count, size
}

func hasKey(db *pebble.DB, key []byte) bool {
	_, closer, err := db.Get(key)
	if err != nil {
		return false
	}
	closer.Close()
	return true
}

func exportPrefix(db *pebble.DB, w *bufio.Writer, namespace []byte, prefix byte, name string) (int64, int64) {
	// Build key range: namespace + prefix
	lower := append(namespace, prefix)
	upper := append(namespace, prefix+1)

	iter, err := db.NewIter(&pebble.IterOptions{
		LowerBound: lower,
		UpperBound: upper,
	})
	if err != nil {
		log.Printf("Failed to create iterator for %s: %v", name, err)
		return 0, 0
	}
	defer iter.Close()

	var count, totalBytes int64
	lastReport := time.Now()

	for iter.First(); iter.Valid(); iter.Next() {
		key := iter.Key()
		val := iter.Value()

		// Write as minimal JSON: {"k":"0x...","v":"0x..."}
		fmt.Fprintf(w, "{\"k\":\"0x%s\",\"v\":\"0x%s\"}\n",
			hex.EncodeToString(key),
			hex.EncodeToString(val))

		count++
		totalBytes += int64(len(key) + len(val))

		// Progress report every 10 seconds
		if time.Since(lastReport) > 10*time.Second {
			fmt.Printf("  %s: %d entries, %.2f MB...\n", name, count, float64(totalBytes)/(1024*1024))
			lastReport = time.Now()
		}
	}

	fmt.Printf("  Exported %s (%c): %d entries, %.2f MB\n", name, prefix, count, float64(totalBytes)/(1024*1024))
	return count, totalBytes
}

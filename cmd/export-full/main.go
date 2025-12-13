// export-full exports complete blockchain state from namespaced PebbleDB
// Includes: blocks (h/b/r/H/B), state trie (A/O/c/L), and metadata
package main

import (
	"bytes"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/cockroachdb/pebble"
)

// All prefixes to export (after 32-byte namespace)
var prefixes = []struct {
	name   string
	prefix []byte
}{
	// State trie (required for eth_getBalance, eth_call, etc.)
	{"AccountTrie (A)", []byte("A")},
	{"StorageTrie (O)", []byte("O")},
	{"Code (c)", []byte("c")},
	{"StateID (L)", []byte("L")},

	// Block data (required for historical queries)
	{"Headers (h)", []byte("h")},       // Block headers: h + number + hash -> RLP(header)
	{"Bodies (b)", []byte("b")},        // Block bodies: b + number + hash -> RLP(body)
	{"Receipts (r)", []byte("r")},      // Receipts: r + number + hash -> RLP(receipts)
	{"HeaderNumber (H)", []byte("H")},  // Hash -> number lookup: H + hash -> number
	{"BlockHash (n)", []byte("n")},     // Number -> hash lookup: n + number -> hash
	{"TxLookup (l)", []byte("l")},      // Tx lookup: l + txhash -> block location
	{"BloomBits (B)", []byte("B")},     // Bloom bits index

	// Metadata (required for chain initialization)
	{"HeadHeader", []byte("LastHeader")},
	{"HeadBlock", []byte("LastBlock")},
	{"HeadFast", []byte("LastFast")},
	{"Config", []byte("ethereum-config-")},
}

func main() {
	srcPath := flag.String("src", "", "Source PebbleDB path")
	dstPath := flag.String("dst", "", "Destination PebbleDB path")
	verifyOnly := flag.Bool("verify", false, "Only verify, don't export")
	namespaceHex := flag.String("namespace", "", "32-byte namespace (auto-detected if empty)")
	flag.Parse()

	if *srcPath == "" {
		fmt.Println("Usage: export-full -src <source-pebble-db> -dst <dest-pebble-db>")
		fmt.Println()
		fmt.Println("Exports complete blockchain data for migration:")
		fmt.Println("  - State trie (A/O/c/L) - for eth_getBalance, eth_call")
		fmt.Println("  - Block data (h/b/r/H/n/l/B) - for historical queries")
		fmt.Println("  - Metadata - for chain initialization")
		fmt.Println()
		flag.PrintDefaults()
		os.Exit(1)
	}

	srcDB, err := pebble.Open(*srcPath, &pebble.Options{ReadOnly: true})
	if err != nil {
		log.Fatalf("Failed to open source DB: %v", err)
	}
	defer srcDB.Close()

	fmt.Printf("Source: %s\n", *srcPath)

	// Detect namespace
	var namespace []byte
	if *namespaceHex != "" {
		namespace, _ = hex.DecodeString(*namespaceHex)
	} else {
		namespace = detectNamespace(srcDB)
	}

	if namespace != nil {
		fmt.Printf("Namespace: 0x%s (%d bytes)\n", hex.EncodeToString(namespace), len(namespace))
	} else {
		fmt.Println("Namespace: none")
	}

	// Scan and report
	fmt.Println("\nScanning database...")
	totalEntries := int64(0)
	totalSize := int64(0)

	for _, p := range prefixes {
		fullPrefix := append(bytes.Clone(namespace), p.prefix...)
		count, size := countPrefix(srcDB, fullPrefix)
		totalEntries += count
		totalSize += size
		if count > 0 {
			fmt.Printf("  %s: %d entries, %.2f MB\n", p.name, count, float64(size)/(1024*1024))
		}
	}
	fmt.Printf("\n  TOTAL: %d entries, %.2f MB\n", totalEntries, float64(totalSize)/(1024*1024))

	if *verifyOnly {
		fmt.Println("\nVerify-only mode complete.")
		return
	}

	if *dstPath == "" {
		log.Fatal("-dst required for export")
	}

	// Create destination
	dstDB, err := pebble.Open(*dstPath, &pebble.Options{})
	if err != nil {
		log.Fatalf("Failed to create destination: %v", err)
	}
	defer dstDB.Close()

	fmt.Printf("\nExporting to: %s\n", *dstPath)

	// Export each prefix
	totalCopied := int64(0)
	for _, p := range prefixes {
		fullPrefix := append(bytes.Clone(namespace), p.prefix...)
		copied, err := copyPrefixStripNamespace(srcDB, dstDB, fullPrefix, len(namespace), p.name)
		if err != nil {
			log.Fatalf("Failed to copy %s: %v", p.name, err)
		}
		if copied > 0 {
			fmt.Printf("  %s: %d entries\n", p.name, copied)
		}
		totalCopied += copied
	}

	fmt.Printf("\n=== Export Complete ===\n")
	fmt.Printf("Total: %d entries\n", totalCopied)
	fmt.Printf("Destination: %s\n", *dstPath)
}

func detectNamespace(db *pebble.DB) []byte {
	iter, err := db.NewIter(nil)
	if err != nil {
		return nil
	}
	defer iter.Close()

	var namespace []byte
	for iter.First(); iter.Valid(); {
		key := iter.Key()
		if len(key) >= 33 {
			namespace = bytes.Clone(key[:32])
			break
		}
		iter.Next()
	}
	return namespace
}

func countPrefix(db *pebble.DB, prefix []byte) (int64, int64) {
	upper := incrementBytes(prefix)
	iter, err := db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
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

func incrementBytes(b []byte) []byte {
	result := make([]byte, len(b))
	copy(result, b)
	for i := len(result) - 1; i >= 0; i-- {
		result[i]++
		if result[i] != 0 {
			break
		}
	}
	return result
}

func copyPrefixStripNamespace(srcDB, dstDB *pebble.DB, prefix []byte, namespaceLen int, name string) (int64, error) {
	upper := incrementBytes(prefix)
	iter, err := srcDB.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: upper,
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	batch := dstDB.NewBatch()
	defer batch.Close()

	var count int64
	batchSize := 0
	maxBatchSize := 100 * 1024 * 1024 // 100MB

	for iter.First(); iter.Valid(); iter.Next() {
		key := iter.Key()
		val := bytes.Clone(iter.Value())

		// Strip namespace
		var newKey []byte
		if namespaceLen > 0 && len(key) > namespaceLen {
			newKey = bytes.Clone(key[namespaceLen:])
		} else {
			newKey = bytes.Clone(key)
		}

		if err := batch.Set(newKey, val, nil); err != nil {
			return count, err
		}
		count++
		batchSize += len(newKey) + len(val)

		if batchSize >= maxBatchSize {
			if err := batch.Commit(pebble.Sync); err != nil {
				return count, err
			}
			batch.Reset()
			batchSize = 0
			if count%500000 == 0 {
				fmt.Printf("    %s: %d entries...\n", name, count)
			}
		}
	}

	if batch.Count() > 0 {
		if err := batch.Commit(pebble.Sync); err != nil {
			return count, err
		}
	}

	return count, iter.Error()
}

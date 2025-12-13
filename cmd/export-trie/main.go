// export-trie exports raw state trie KV pairs from PebbleDB for direct import
// Handles namespaced SubnetEVM databases (32-byte namespace prefix)
// Exports: A* (account trie nodes), O* (storage trie nodes), c* (code blobs), L* (state IDs)
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/cockroachdb/pebble"
)

// Database key prefixes (from geth/core/rawdb/schema.go)
var (
	// PathDB trie node prefixes (after namespace)
	TrieNodeAccountPrefix = []byte("A") // Account trie: A + hexPath -> trie node
	TrieNodeStoragePrefix = []byte("O") // Storage trie: O + accountHash + hexPath -> trie node

	// Code prefix
	CodePrefix = []byte("c") // Code: c + codeHash -> contract code

	// State ID prefix
	stateIDPrefix = []byte("L") // L + stateRoot -> state ID
)

func main() {
	srcPath := flag.String("src", "", "Source PebbleDB path")
	dstPath := flag.String("dst", "", "Destination PebbleDB path (will be created)")
	verifyOnly := flag.Bool("verify", false, "Only verify source DB, don't copy")
	namespaceHex := flag.String("namespace", "", "32-byte namespace prefix (hex, auto-detected if empty)")
	flag.Parse()

	if *srcPath == "" {
		fmt.Println("Usage: export-trie -src <source-pebble-db> -dst <dest-pebble-db>")
		fmt.Println()
		fmt.Println("Exports raw state trie KV pairs for direct import into C-Chain nodes.")
		fmt.Println("This exports the actual trie nodes, not reconstructed state.")
		fmt.Println()
		fmt.Println("Handles namespaced SubnetEVM databases with 32-byte namespace prefix.")
		fmt.Println()
		fmt.Println("Prefixes exported (after stripping namespace):")
		fmt.Println("  A* - Account trie nodes (PathDB)")
		fmt.Println("  O* - Storage trie nodes (PathDB)")
		fmt.Println("  c* - Contract code blobs")
		fmt.Println("  L* - State ID mappings")
		fmt.Println()
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Open source database read-only
	srcDB, err := pebble.Open(*srcPath, &pebble.Options{ReadOnly: true})
	if err != nil {
		log.Fatalf("Failed to open source DB: %v", err)
	}
	defer srcDB.Close()

	fmt.Printf("Source DB: %s\n", *srcPath)

	// Detect namespace
	var namespace []byte
	if *namespaceHex != "" {
		namespace, err = hex.DecodeString(*namespaceHex)
		if err != nil {
			log.Fatalf("Invalid namespace hex: %v", err)
		}
	} else {
		namespace = detectNamespace(srcDB)
	}

	if namespace != nil {
		fmt.Printf("Namespace: 0x%s (%d bytes)\n", hex.EncodeToString(namespace), len(namespace))
	} else {
		fmt.Println("Namespace: none (direct keys)")
	}

	// Count entries by prefix
	prefixes := []struct {
		name   string
		prefix []byte
	}{
		{"AccountTrie (A)", TrieNodeAccountPrefix},
		{"StorageTrie (O)", TrieNodeStoragePrefix},
		{"Code (c)", CodePrefix},
		{"StateID (L)", stateIDPrefix},
	}

	fmt.Println("\nScanning source database...")
	totalEntries := int64(0)
	totalSize := int64(0)
	for _, p := range prefixes {
		fullPrefix := append(bytes.Clone(namespace), p.prefix...)
		count, size := countPrefix(srcDB, fullPrefix)
		totalEntries += count
		totalSize += size
		fmt.Printf("  %s: %d entries, %.2f MB\n", p.name, count, float64(size)/(1024*1024))
	}
	fmt.Printf("  TOTAL: %d entries, %.2f MB\n", totalEntries, float64(totalSize)/(1024*1024))

	// Find state root for head block
	headRoot := findHeadStateRoot(srcDB, namespace)
	if headRoot != nil {
		fmt.Printf("\nHead state root: 0x%s\n", hex.EncodeToString(headRoot))

		// Check if root node exists
		// Root node in PathDB is: namespace + 'A' (no path after A for root)
		rootKey := append(bytes.Clone(namespace), 'A')
		if hasKey(srcDB, rootKey) {
			fmt.Println("Root node EXISTS (PathDB: namespace + 'A')")
		} else {
			// Try with path = hash of root
			rootKeyWithPath := append(bytes.Clone(namespace), append([]byte("A"), headRoot...)...)
			if hasKey(srcDB, rootKeyWithPath) {
				fmt.Println("Root node EXISTS (PathDB: namespace + 'A' + root hash)")
			} else {
				fmt.Println("WARNING: Root node NOT FOUND")
			}
		}
	}

	if *verifyOnly {
		fmt.Println("\nVerify-only mode, exiting.")
		return
	}

	if *dstPath == "" {
		log.Fatal("Destination path (-dst) required for export")
	}

	// Create destination database
	dstDB, err := pebble.Open(*dstPath, &pebble.Options{})
	if err != nil {
		log.Fatalf("Failed to create destination DB: %v", err)
	}
	defer dstDB.Close()

	fmt.Printf("\nExporting to: %s\n", *dstPath)
	fmt.Println("Stripping namespace and copying state trie keys...")

	// Export each prefix, stripping namespace
	totalCopied := int64(0)
	for _, p := range prefixes {
		fullPrefix := append(bytes.Clone(namespace), p.prefix...)
		copied, err := copyPrefixStripNamespace(srcDB, dstDB, fullPrefix, len(namespace), p.name)
		if err != nil {
			log.Fatalf("Failed to copy %s: %v", p.name, err)
		}
		totalCopied += copied
		fmt.Printf("  Copied %s: %d entries\n", p.name, copied)
	}

	fmt.Printf("\n=== Export Complete ===\n")
	fmt.Printf("Total entries copied: %d\n", totalCopied)
	fmt.Printf("Destination: %s\n", *dstPath)
}

func detectNamespace(db *pebble.DB) []byte {
	// Scan first few keys to find common 32-byte prefix
	iter, err := db.NewIter(nil)
	if err != nil {
		return nil
	}
	defer iter.Close()

	var namespace []byte
	count := 0
	for iter.First(); iter.Valid() && count < 100; iter.Next() {
		key := iter.Key()
		if len(key) < 33 {
			continue
		}
		if namespace == nil {
			namespace = bytes.Clone(key[:32])
		} else if !bytes.Equal(key[:32], namespace) {
			// Multiple namespaces - can't auto-detect
			return nil
		}
		count++
	}

	if count > 0 {
		return namespace
	}
	return nil
}

func countPrefix(db *pebble.DB, prefix []byte) (int64, int64) {
	// Build upper bound
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

func hasKey(db *pebble.DB, key []byte) bool {
	_, closer, err := db.Get(key)
	if err != nil {
		return false
	}
	closer.Close()
	return true
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
	maxBatchSize := 50 * 1024 * 1024 // 50MB batches

	for iter.First(); iter.Valid(); iter.Next() {
		key := iter.Key()
		val := bytes.Clone(iter.Value())

		// Strip namespace prefix from key
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

		// Flush batch periodically
		if batchSize >= maxBatchSize {
			if err := batch.Commit(pebble.Sync); err != nil {
				return count, err
			}
			batch.Reset()
			batchSize = 0
			if count%100000 == 0 {
				fmt.Printf("    %s: %d entries...\n", name, count)
			}
		}
	}

	// Final flush
	if batch.Count() > 0 {
		if err := batch.Commit(pebble.Sync); err != nil {
			return count, err
		}
	}

	return count, iter.Error()
}

func findHeadStateRoot(db *pebble.DB, namespace []byte) []byte {
	// Try to find head block hash and get its state root
	headBlockKey := append(bytes.Clone(namespace), []byte("LastBlock")...)
	headHash, closer, err := db.Get(headBlockKey)
	if err != nil {
		// Try alternative key
		headBlockKey = append(bytes.Clone(namespace), []byte("LastAccepted")...)
		headHash, closer, err = db.Get(headBlockKey)
		if err != nil {
			return nil
		}
	}
	headHashCopy := bytes.Clone(headHash)
	closer.Close()

	fmt.Printf("Head block hash: 0x%s\n", hex.EncodeToString(headHashCopy))

	// Find block number for this hash
	headerNumberKey := append(bytes.Clone(namespace), append([]byte("H"), headHashCopy...)...)
	numData, closer, err := db.Get(headerNumberKey)
	if err != nil {
		return nil
	}
	numCopy := bytes.Clone(numData)
	closer.Close()

	var blockNum uint64
	if len(numCopy) == 8 {
		blockNum = binary.BigEndian.Uint64(numCopy)
	} else if len(numCopy) == 4 {
		blockNum = uint64(binary.BigEndian.Uint32(numCopy))
	}
	fmt.Printf("Head block number: %d\n", blockNum)

	// Try to find stateID entries which map root -> id
	stateIDIterPrefix := append(bytes.Clone(namespace), stateIDPrefix...)
	stateIDIterUpper := incrementBytes(stateIDIterPrefix)

	stateIDIter, err := db.NewIter(&pebble.IterOptions{
		LowerBound: stateIDIterPrefix,
		UpperBound: stateIDIterUpper,
	})
	if err == nil {
		defer stateIDIter.Close()
		for stateIDIter.First(); stateIDIter.Valid(); stateIDIter.Next() {
			key := stateIDIter.Key()
			// Expected: namespace (32) + 'L' (1) + stateRoot (32) = 65 bytes
			expectedLen := len(namespace) + 1 + 32
			if len(key) == expectedLen {
				// Found a state ID entry
				stateRoot := key[len(namespace)+1:]
				fmt.Printf("Found state root in DB: 0x%s\n", hex.EncodeToString(stateRoot))
				return stateRoot
			}
		}
	}

	return nil
}

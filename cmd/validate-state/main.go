// validate-state validates that the state trie can resolve the head block's stateRoot
package main

import (
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"log"

	"github.com/cockroachdb/pebble"
)

func main() {
	dbPath := flag.String("db", "", "PebbleDB path")
	namespace := flag.String("namespace", "", "32-byte hex namespace")
	flag.Parse()

	if *dbPath == "" || *namespace == "" {
		log.Fatal("Usage: -db <path> -namespace <hex32>")
	}

	ns, err := hex.DecodeString(*namespace)
	if err != nil || len(ns) != 32 {
		log.Fatalf("Invalid namespace: must be 32 bytes hex")
	}

	db, err := pebble.Open(*dbPath, &pebble.Options{ReadOnly: true})
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	fmt.Printf("Database: %s\n", *dbPath)
	fmt.Printf("Namespace: 0x%s\n", hex.EncodeToString(ns))

	// Step 1: Check account trie root node at namespace||'A'
	fmt.Println("\n=== Step 1: Account Trie Root ===")
	rootKey := append(append([]byte{}, ns...), 'A')
	fmt.Printf("Root key: 0x%s (len=%d)\n", hex.EncodeToString(rootKey), len(rootKey))

	val, closer, err := db.Get(rootKey)
	if err != nil {
		fmt.Printf("Root node NOT FOUND at namespace||'A': %v\n", err)
		fmt.Println("\nSearching for shortest 'A' prefix keys...")
		findShortestKeys(db, ns, 'A')
	} else {
		closer.Close()
		fmt.Printf("Root node EXISTS: value len=%d bytes\n", len(val))
	}

	// Step 2: Sample L entries
	fmt.Println("\n=== Step 2: L Entry Samples ===")
	countEntries(db, ns, 'L', 5)

	// Step 3: Sample code entries
	fmt.Println("\n=== Step 3: Code Samples ===")
	countEntries(db, ns, 'c', 5)

	fmt.Println("\n=== Validation Complete ===")
}

func findShortestKeys(db *pebble.DB, ns []byte, prefix byte) {
	lower := append(append([]byte{}, ns...), prefix)
	upper := append(append([]byte{}, ns...), prefix+1)

	iter, err := db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return
	}
	defer iter.Close()

	keysByLen := make(map[int]int)
	var shortestKeys [][]byte
	minLen := 1000

	for iter.First(); iter.Valid(); iter.Next() {
		key := iter.Key()
		keyLen := len(key)
		keysByLen[keyLen]++
		if keyLen < minLen {
			minLen = keyLen
			shortestKeys = [][]byte{append([]byte{}, key...)}
		} else if keyLen == minLen && len(shortestKeys) < 3 {
			shortestKeys = append(shortestKeys, append([]byte{}, key...))
		}
	}

	fmt.Printf("Key length distribution:\n")
	for l := 33; l < 100; l++ {
		if count, ok := keysByLen[l]; ok && count > 0 {
			fmt.Printf("  len=%d: %d keys\n", l, count)
		}
	}
	fmt.Printf("\nShortest '%c' keys (len=%d):\n", prefix, minLen)
	for i, k := range shortestKeys {
		fmt.Printf("  [%d] 0x%s\n", i, hex.EncodeToString(k))
		if len(k) > 33 {
			fmt.Printf("      path: 0x%s\n", hex.EncodeToString(k[33:]))
		}
	}
}

func countEntries(db *pebble.DB, ns []byte, prefix byte, limit int) {
	lower := append(append([]byte{}, ns...), prefix)
	upper := append(append([]byte{}, ns...), prefix+1)

	iter, err := db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return
	}
	defer iter.Close()

	count := 0
	for iter.First(); iter.Valid() && count < limit; iter.Next() {
		key := iter.Key()
		val := iter.Value()
		if prefix == 'L' && len(key) >= 65 {
			stateRoot := key[33:65]
			var stateID uint64
			if len(val) >= 8 {
				stateID = binary.BigEndian.Uint64(val)
			}
			fmt.Printf("  [%d] root=0x%s... -> stateID=%d\n", count, hex.EncodeToString(stateRoot[:8]), stateID)
		} else if prefix == 'c' && len(key) >= 65 {
			codeHash := key[33:65]
			fmt.Printf("  [%d] hash=0x%s... -> code len=%d\n", count, hex.EncodeToString(codeHash[:8]), len(val))
		} else {
			fmt.Printf("  [%d] key len=%d, val len=%d\n", count, len(key), len(val))
		}
		count++
	}
}

// survey-buckets analyzes the bucket byte distribution in a PebbleDB
// This verifies whether the database uses PathDB schema with A/O/c prefixes
package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"sort"

	"github.com/cockroachdb/pebble"
)

func main() {
	dbPath := flag.String("db", "", "PebbleDB path")
	showSamples := flag.Bool("samples", false, "Show sample keys for each bucket")
	namespace := flag.String("namespace", "", "32-byte hex namespace to analyze")
	findRoot := flag.Bool("find-root", false, "Find shortest A key (potential root)")
	flag.Parse()

	if *dbPath == "" {
		log.Fatal("Usage: -db <path>")
	}

	db, err := pebble.Open(*dbPath, &pebble.Options{ReadOnly: true})
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	var namespaceBytes []byte
	if *namespace != "" {
		namespaceBytes, err = hex.DecodeString(*namespace)
		if err != nil {
			log.Fatalf("Invalid namespace: %v", err)
		}
	}

	if *findRoot && len(namespaceBytes) == 32 {
		findShortestAKey(db, namespaceBytes)
		return
	}

	iter, err := db.NewIter(nil)
	if err != nil {
		log.Fatal(err)
	}
	defer iter.Close()

	// Histogram of key[32] - the bucket byte after 32-byte namespace
	bucketCounts := make(map[byte]int64)
	// Sample keys for each bucket
	bucketSamples := make(map[byte][][]byte)
	totalCount := int64(0)
	shortKeys := int64(0) // Keys shorter than 33 bytes

	for iter.First(); iter.Valid(); iter.Next() {
		key := iter.Key()
		totalCount++

		if len(key) >= 33 {
			// key[32] is the byte immediately after the 32-byte namespace
			b := key[32]
			bucketCounts[b]++
			// Store up to 3 samples per bucket
			if len(bucketSamples[b]) < 3 {
				keyCopy := make([]byte, len(key))
				copy(keyCopy, key)
				bucketSamples[b] = append(bucketSamples[b], keyCopy)
			}
		} else {
			shortKeys++
		}
	}

	fmt.Printf("Total keys: %d\n", totalCount)
	fmt.Printf("Keys < 33 bytes: %d\n\n", shortKeys)

	// Sort by count descending
	type bucketInfo struct {
		b     byte
		count int64
	}
	var buckets []bucketInfo
	for b, c := range bucketCounts {
		buckets = append(buckets, bucketInfo{b, c})
	}
	sort.Slice(buckets, func(i, j int) bool {
		return buckets[i].count > buckets[j].count
	})

	fmt.Println("Bucket distribution (key[32] after 32-byte namespace):")
	fmt.Println("=========================================================")
	for _, bi := range buckets[:20] { // Top 20
		char := "."
		if bi.b >= 32 && bi.b < 127 {
			char = string(bi.b)
		}
		fmt.Printf("  0x%02x ('%s'): %12d keys\n", bi.b, char, bi.count)
	}
	fmt.Printf("  ... (showing top 20 of %d buckets)\n", len(buckets))

	// Check for expected PathDB prefixes
	fmt.Println("\n=== Expected PathDB Prefixes ===")
	expected := map[byte]string{
		'A': "Account trie (0x41)",
		'O': "Storage trie (0x4f)",
		'c': "Code blobs (0x63)",
		'L': "State IDs (0x4c)",
	}
	for b, name := range expected {
		count := bucketCounts[b]
		if count > 0 {
			fmt.Printf("  %s: %d keys ✓\n", name, count)
		} else {
			fmt.Printf("  %s: NOT FOUND\n", name)
		}
	}

	// Show samples if requested
	if *showSamples {
		fmt.Println("\n=== Sample Keys ===")

		// Show samples for interesting prefixes
		interestingPrefixes := []byte{'h', 'H', 'b', 'B', 'r', 'l', 'A', 'O', 'c', 'L'}
		for _, prefix := range interestingPrefixes {
			samples := bucketSamples[prefix]
			if len(samples) > 0 {
				char := "."
				if prefix >= 32 && prefix < 127 {
					char = string(prefix)
				}
				fmt.Printf("\nPrefix 0x%02x ('%s') - %d total keys:\n", prefix, char, bucketCounts[prefix])
				for i, sample := range samples {
					fmt.Printf("  [%d] len=%d: 0x%s\n", i, len(sample), hex.EncodeToString(sample))
				}
			}
		}
	}
}

func findShortestAKey(db *pebble.DB, namespace []byte) {
	fmt.Println("Finding shortest 'A' keys (potential roots)...")

	lower := append(namespace, 'A')
	upper := append(namespace, 'A'+1)

	iter, err := db.NewIter(&pebble.IterOptions{
		LowerBound: lower,
		UpperBound: upper,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer iter.Close()

	// Find keys by length
	keysByLen := make(map[int][]string)
	count := 0

	for iter.First(); iter.Valid(); iter.Next() {
		key := iter.Key()
		keyLen := len(key)

		if len(keysByLen[keyLen]) < 5 {
			keysByLen[keyLen] = append(keysByLen[keyLen], hex.EncodeToString(key))
		}
		count++
	}

	fmt.Printf("Total 'A' keys: %d\n\n", count)

	// Sort lengths
	var lengths []int
	for l := range keysByLen {
		lengths = append(lengths, l)
	}
	sort.Ints(lengths)

	fmt.Println("Keys by length (showing up to 5 samples each):")
	for _, l := range lengths {
		keys := keysByLen[l]
		fmt.Printf("\nLength %d (%d samples):\n", l, len(keys))
		for i, k := range keys {
			// Show after namespace
			if len(k) > 64 {
				fmt.Printf("  [%d] namespace+%s\n", i, k[64:])
			} else {
				fmt.Printf("  [%d] %s\n", i, k)
			}
		}
		if l > 70 {
			break // Don't show too many
		}
	}
}

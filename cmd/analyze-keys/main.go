// analyze-keys analyzes the structure of keys in the database to understand layout
package main

import (
	"bytes"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"sort"

	"github.com/cockroachdb/pebble"
)

func main() {
	dbPath := flag.String("db", "", "PebbleDB path")
	flag.Parse()

	if *dbPath == "" {
		log.Fatal("Usage: -db <path>")
	}

	db, err := pebble.Open(*dbPath, &pebble.Options{ReadOnly: true})
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Detect namespace
	namespace := detectNamespace(db)
	fmt.Printf("Namespace: 0x%s (%d bytes)\n\n", hex.EncodeToString(namespace), len(namespace))

	// Analyze key lengths by bucket byte (position 32)
	fmt.Println("=== Key Length Distribution by Bucket ===")
	buckets := map[byte]map[int]int{} // bucket -> length -> count
	totalByBucket := map[byte]int{}

	iter, _ := db.NewIter(nil)
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		key := iter.Key()
		if len(key) < 33 {
			continue
		}
		bucket := key[32]
		keyLen := len(key)

		if buckets[bucket] == nil {
			buckets[bucket] = make(map[int]int)
		}
		buckets[bucket][keyLen]++
		totalByBucket[bucket]++
	}

	// Show relevant buckets
	interestingBuckets := []byte{'A', 'O', 'c', 'L', 'h', 'b', 'r', 'H', 'n'}
	for _, bucket := range interestingBuckets {
		if lengths, ok := buckets[bucket]; ok {
			fmt.Printf("\nBucket '%c' (0x%02x): %d total keys\n", bucket, bucket, totalByBucket[bucket])

			// Sort lengths
			var lens []int
			for l := range lengths {
				lens = append(lens, l)
			}
			sort.Ints(lens)

			for _, l := range lens {
				count := lengths[l]
				suffix := l - 32 // length after namespace
				pct := float64(count) * 100 / float64(totalByBucket[bucket])
				fmt.Printf("  len=%d (suffix=%d): %d keys (%.1f%%)\n", l, suffix, count, pct)
			}
		}
	}

	// Sample some specific keys for each bucket
	fmt.Println("\n=== Sample Keys ===")
	for _, bucket := range interestingBuckets {
		if totalByBucket[bucket] == 0 {
			continue
		}
		fmt.Printf("\n--- Bucket '%c' ---\n", bucket)
		sampleBucket(db, namespace, bucket, 3)
	}

	// Check if this is hash-based trie
	fmt.Println("\n=== Hash-Based Trie Detection ===")

	// In hash-based trie, keys after namespace should be 32-byte hashes
	// Key format: namespace (32) + bucket (1) + hash (32) = 65 bytes for trie nodes
	aKeys65 := buckets['A'][65]
	aTotal := totalByBucket['A']
	if aKeys65 > 0 && float64(aKeys65)/float64(aTotal) > 0.9 {
		fmt.Println("✓ Account trie appears to be hash-based (most keys are 65 bytes)")
		fmt.Printf("  65-byte keys: %d / %d (%.1f%%)\n", aKeys65, aTotal, float64(aKeys65)*100/float64(aTotal))
	} else {
		fmt.Println("? Account trie key format unclear")
	}

	oKeys65 := buckets['O'][65]
	oTotal := totalByBucket['O']
	if oKeys65 > 0 && float64(oKeys65)/float64(oTotal) > 0.9 {
		fmt.Println("✓ Storage trie appears to be hash-based (most keys are 65 bytes)")
	}
}

func detectNamespace(db *pebble.DB) []byte {
	iter, _ := db.NewIter(nil)
	defer iter.Close()
	for iter.First(); iter.Valid(); {
		key := iter.Key()
		if len(key) >= 32 {
			return bytes.Clone(key[:32])
		}
		iter.Next()
	}
	return nil
}

func sampleBucket(db *pebble.DB, namespace []byte, bucket byte, n int) {
	prefix := append(bytes.Clone(namespace), bucket)
	upper := append(bytes.Clone(namespace), bucket+1)

	iter, _ := db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: upper,
	})
	defer iter.Close()

	count := 0
	for iter.First(); iter.Valid() && count < n; iter.Next() {
		key := iter.Key()
		val := iter.Value()
		suffix := key[32:] // after namespace

		fmt.Printf("Key[%d]: total_len=%d, suffix_len=%d\n", count, len(key), len(suffix))
		fmt.Printf("  Suffix: 0x%s\n", hex.EncodeToString(suffix))
		fmt.Printf("  Value: %d bytes\n", len(val))
		count++
	}
}

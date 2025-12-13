// key-sniff analyzes key structure to find the real prefix byte offset
// Creates histograms for bytes at multiple offsets to identify where A/O/c/L prefixes live
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
	namespace := flag.String("namespace", "", "32-byte hex namespace")
	maxKeys := flag.Int("max", 1000000, "Max keys to scan")
	flag.Parse()

	if *dbPath == "" {
		log.Fatal("Usage: -db <path> [-namespace <hex>]")
	}

	db, err := pebble.Open(*dbPath, &pebble.Options{ReadOnly: true})
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	var iterOpts *pebble.IterOptions
	if *namespace != "" {
		ns, err := hex.DecodeString(*namespace)
		if err != nil {
			log.Fatalf("Invalid namespace: %v", err)
		}
		// Iterate only keys starting with namespace
		upper := make([]byte, len(ns))
		copy(upper, ns)
		upper[len(upper)-1]++
		iterOpts = &pebble.IterOptions{
			LowerBound: ns,
			UpperBound: upper,
		}
	}

	iter, err := db.NewIter(iterOpts)
	if err != nil {
		log.Fatal(err)
	}
	defer iter.Close()

	// Create histograms for bytes at offsets 32-50
	// (after the 32-byte namespace)
	const minOffset = 32
	const maxOffset = 50
	histograms := make([]map[byte]int64, maxOffset+1)
	for i := range histograms {
		histograms[i] = make(map[byte]int64)
	}

	// Also collect sample keys
	sampleKeys := make([][]byte, 0, 10)

	count := 0
	for iter.First(); iter.Valid() && count < *maxKeys; iter.Next() {
		key := iter.Key()

		// Count byte values at each offset
		for offset := minOffset; offset <= maxOffset && offset < len(key); offset++ {
			histograms[offset][key[offset]]++
		}

		// Collect samples
		if count < 10 {
			keyCopy := make([]byte, len(key))
			copy(keyCopy, key)
			sampleKeys = append(sampleKeys, keyCopy)
		}

		count++
	}

	fmt.Printf("Scanned %d keys\n\n", count)

	// Print sample keys
	fmt.Println("=== Sample Keys ===")
	for i, key := range sampleKeys {
		fmt.Printf("[%d] len=%d\n", i, len(key))
		fmt.Printf("    full: 0x%s\n", hex.EncodeToString(key))
		if len(key) > 32 {
			fmt.Printf("    after_ns: 0x%s\n", hex.EncodeToString(key[32:]))
		}
	}

	// Find offsets where A/O/c/L show spikes (not uniform)
	fmt.Println("\n=== Finding prefix byte offset ===")
	fmt.Println("Looking for offsets where 0x41('A'), 0x4f('O'), 0x63('c'), 0x4c('L') show spikes\n")

	targetBytes := []byte{'A', 'O', 'c', 'L', 'h', 'b', 'r', 'H', 'l', 'B'}

	for offset := minOffset; offset <= maxOffset; offset++ {
		hist := histograms[offset]
		if len(hist) == 0 {
			continue
		}

		// Calculate total and average
		var total int64
		for _, c := range hist {
			total += c
		}
		avg := float64(total) / float64(len(hist))

		// Check if target bytes show significant spikes (>3x average)
		fmt.Printf("Offset %d: ", offset)

		spikes := []string{}
		for _, b := range targetBytes {
			c := hist[b]
			ratio := float64(c) / avg
			if c > 0 && ratio > 2.0 {
				spikes = append(spikes, fmt.Sprintf("'%c'=%.1fx", b, ratio))
			}
		}

		if len(spikes) > 0 {
			fmt.Printf("SPIKES: %v\n", spikes)
		} else {
			// Show top 5 bytes at this offset
			type byteCount struct {
				b byte
				c int64
			}
			var counts []byteCount
			for b, c := range hist {
				counts = append(counts, byteCount{b, c})
			}
			sort.Slice(counts, func(i, j int) bool {
				return counts[i].c > counts[j].c
			})

			fmt.Printf("uniform (top 5: ")
			for i := 0; i < 5 && i < len(counts); i++ {
				bc := counts[i]
				char := "."
				if bc.b >= 32 && bc.b < 127 {
					char = string(bc.b)
				}
				fmt.Printf("0x%02x('%s')=%d ", bc.b, char, bc.c)
			}
			fmt.Println(")")
		}
	}

	// Show detailed breakdown at each offset
	fmt.Println("\n=== Detailed counts at each offset ===")
	for offset := minOffset; offset <= 45 && offset <= maxOffset; offset++ {
		hist := histograms[offset]
		fmt.Printf("\nOffset %d:", offset)

		// Show counts for ASCII printable chars of interest
		for _, b := range []byte{'A', 'O', 'c', 'L', 'h', 'b', 'r', 'H', 'l', 'B'} {
			c := hist[b]
			fmt.Printf(" '%c'=%d", b, c)
		}
		fmt.Println()
	}
}

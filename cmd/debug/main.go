// Command debug inspects PebbleDB database keys
package main

import (
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/cockroachdb/pebble"
)

var knownPrefixes = map[string]string{
	"h":                 "header",
	"H":                 "headerNumber",
	"b":                 "blockBody",
	"r":                 "receipts",
	"c":                 "code",
	"l":                 "txLookup",
	"a":                 "snapshotAccount",
	"o":                 "snapshotStorage",
	"LastBlock":         "headBlock",
	"LastHeader":        "headHeader",
	"ethereum-config-":  "chainConfig",
	"SnapshotRoot":      "snapshotRoot",
	"SecureTrie":        "secureTrie",
	"DatabaseVersion":   "dbVersion",
	"HeadBlockHash":     "headBlockHash",
	"HeadHeaderHash":    "headHeaderHash",
	"HeadFastBlockHash": "headFastBlockHash",
}

func main() {
	var (
		dbPath = flag.String("db", "", "Path to PebbleDB database")
		limit  = flag.Int("limit", 100, "Maximum number of keys to show")
		prefix = flag.String("prefix", "", "Filter by key prefix (hex)")
	)
	flag.Parse()

	if *dbPath == "" {
		fmt.Println("Usage: debug -db <path> [-limit N] [-prefix hex]")
		os.Exit(1)
	}

	// Open database read-only
	db, err := pebble.Open(*dbPath, &pebble.Options{ReadOnly: true})
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Look for specific keys
	fmt.Println("=== Looking for known keys ===")
	knownKeys := []string{
		"LastBlock",
		"LastHeader",
		"HeadBlockHash",
		"HeadHeaderHash",
		"HeadFastBlockHash",
		"SnapshotRoot",
		"DatabaseVersion",
	}
	for _, key := range knownKeys {
		val, closer, err := db.Get([]byte(key))
		if err == nil {
			defer closer.Close()
			fmt.Printf("Found: %s = %s\n", key, hex.EncodeToString(val))
		}
	}

	// Try to find genesis (block 0)
	fmt.Println("\n=== Looking for genesis block ===")
	// headerPrefix + num (0) + headerHashSuffix
	genesisHashKey := []byte("h")
	genesisHashKey = append(genesisHashKey, make([]byte, 8)...) // block 0
	genesisHashKey = append(genesisHashKey, []byte("n")...)

	val, closer, err := db.Get(genesisHashKey)
	if err == nil {
		defer closer.Close()
		fmt.Printf("Genesis hash (h0n): %s\n", hex.EncodeToString(val))
	} else {
		fmt.Printf("Genesis hash key not found: %v\n", err)
	}

	// Scan all keys
	fmt.Printf("\n=== Scanning keys (limit: %d) ===\n", *limit)

	var lower, upper []byte
	if *prefix != "" {
		lower, _ = hex.DecodeString(*prefix)
		upper = append(lower, 0xff)
	}

	iter, err := db.NewIter(&pebble.IterOptions{
		LowerBound: lower,
		UpperBound: upper,
	})
	if err != nil {
		log.Fatalf("Failed to create iterator: %v", err)
	}
	defer iter.Close()

	prefixCounts := make(map[string]int)
	count := 0

	for iter.First(); iter.Valid() && count < *limit; iter.Next() {
		key := iter.Key()
		keyStr := string(key)
		keyHex := hex.EncodeToString(key)

		// Identify prefix
		prefixName := "unknown"
		for p, name := range knownPrefixes {
			if strings.HasPrefix(keyStr, p) {
				prefixName = name
				break
			}
		}
		prefixCounts[prefixName]++

		// Print key details
		if count < 50 { // Only print first 50
			valLen := len(iter.Value())

			// Decode special cases
			extra := ""
			if prefixName == "header" && len(key) == 1+8+1 {
				// h + number + n = canonical hash lookup
				num := binary.BigEndian.Uint64(key[1:9])
				extra = fmt.Sprintf(" (block %d hash key)", num)
			} else if prefixName == "header" && len(key) == 1+8+32 {
				// h + number + hash = header
				num := binary.BigEndian.Uint64(key[1:9])
				extra = fmt.Sprintf(" (block %d header)", num)
			} else if prefixName == "blockBody" && len(key) == 1+8+32 {
				num := binary.BigEndian.Uint64(key[1:9])
				extra = fmt.Sprintf(" (block %d body)", num)
			}

			fmt.Printf("%s: %s (len=%d, val_len=%d)%s\n", prefixName, keyHex, len(key), valLen, extra)
		}
		count++
	}

	fmt.Printf("\n=== Key Prefix Counts (first %d keys) ===\n", *limit)
	for name, cnt := range prefixCounts {
		fmt.Printf("  %s: %d\n", name, cnt)
	}

	// Try to find the highest block by iterating headers
	fmt.Println("\n=== Finding block range ===")
	headerIter, err := db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("h"),
		UpperBound: []byte("i"),
	})
	if err != nil {
		log.Fatalf("Failed to create header iterator: %v", err)
	}
	defer headerIter.Close()

	var minBlock, maxBlock uint64 = ^uint64(0), 0
	headerCount := 0
	for headerIter.First(); headerIter.Valid(); headerIter.Next() {
		key := headerIter.Key()
		// Looking for h + 8 bytes + "n" pattern (canonical hash keys)
		if len(key) == 10 && key[9] == 'n' {
			num := binary.BigEndian.Uint64(key[1:9])
			if num < minBlock {
				minBlock = num
			}
			if num > maxBlock {
				maxBlock = num
			}
			headerCount++
		}
	}

	if headerCount > 0 {
		fmt.Printf("Found %d canonical header entries\n", headerCount)
		fmt.Printf("Block range: %d to %d\n", minBlock, maxBlock)
	} else {
		fmt.Println("No canonical header entries found")
	}
}

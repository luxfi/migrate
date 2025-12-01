// Package zool2 provides Zoo L2 chain export/import functionality.
package zool2

import (
	"encoding/binary"

	"github.com/luxfi/geth/common"
)

// encodeBlockNumber encodes a block number as big endian uint64
func encodeBlockNumber(number uint64) []byte {
	enc := make([]byte, 8)
	binary.BigEndian.PutUint64(enc, number)
	return enc
}

// headerKeyFor generates the header key for a block
// Key format: h + num (uint64 big endian) + hash
func headerKeyFor(number uint64, hash common.Hash) []byte {
	key := make([]byte, 1+8+32)
	key[0] = 'h'
	copy(key[1:9], encodeBlockNumber(number))
	copy(key[9:41], hash.Bytes())
	return key
}

// headerNumberKeyFor generates the hash -> number key
// Key format: H + hash
func headerNumberKeyFor(hash common.Hash) []byte {
	key := make([]byte, 1+32)
	key[0] = 'H'
	copy(key[1:33], hash.Bytes())
	return key
}

// headerHashKeyFor generates the number -> canonical hash key
// Key format: h + num (uint64 big endian) + n
func headerHashKeyFor(number uint64) []byte {
	key := make([]byte, 1+8+1)
	key[0] = 'h'
	copy(key[1:9], encodeBlockNumber(number))
	key[9] = 'n'
	return key
}

// blockBodyKeyFor generates the body key
// Key format: b + num (uint64 big endian) + hash
func blockBodyKeyFor(number uint64, hash common.Hash) []byte {
	key := make([]byte, 1+8+32)
	key[0] = 'b'
	copy(key[1:9], encodeBlockNumber(number))
	copy(key[9:41], hash.Bytes())
	return key
}

// blockReceiptsKeyFor generates the receipts key
// Key format: r + num (uint64 big endian) + hash
func blockReceiptsKeyFor(number uint64, hash common.Hash) []byte {
	key := make([]byte, 1+8+32)
	key[0] = 'r'
	copy(key[1:9], encodeBlockNumber(number))
	copy(key[9:41], hash.Bytes())
	return key
}

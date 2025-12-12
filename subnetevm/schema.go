// Package subnetevm provides SubnetEVM-specific import/export functionality
package subnetevm

import (
	"encoding/binary"

	"github.com/luxfi/geth/common"
)

// ChainIDPrefixLen is the length of the chain ID prefix (32 bytes)
// Some SubnetEVM databases have keys prefixed with a 32-byte chain ID
const ChainIDPrefixLen = 32

// Database key prefixes (matching rawdb/schema.go)
var (
	headerPrefix        = []byte("h") // headerPrefix + num (uint64 big endian) + hash -> header
	headerHashSuffix    = []byte("n") // headerPrefix + num (uint64 big endian) + headerHashSuffix -> hash
	headerNumberPrefix  = []byte("H") // headerNumberPrefix + hash -> num (uint64 big endian)
	blockBodyPrefix     = []byte("b") // blockBodyPrefix + num (uint64 big endian) + hash -> block body
	blockReceiptsPrefix = []byte("r") // blockReceiptsPrefix + num (uint64 big endian) + hash -> block receipts
	headBlockKey        = []byte("LastBlock")
	headHeaderKey       = []byte("LastHeader")
	configPrefix        = []byte("ethereum-config-")
	codePrefix          = []byte("c") // CodePrefix + code hash -> account code
	txLookupPrefix      = []byte("l") // txLookupPrefix + hash -> transaction lookup metadata
	preimagePrefix      = []byte("secure-key-") // PreimagePrefix + hash -> preimage (address)

	// Snapshot prefixes
	snapshotAccountPrefix = []byte("a") // SnapshotAccountPrefix + account hash -> account trie value
	snapshotStoragePrefix = []byte("o") // SnapshotStoragePrefix + account hash + storage hash -> storage trie value
	snapshotRootKey       = []byte("SnapshotRoot")
)

// Key encoding functions

func encodeBlockNumber(number uint64) []byte {
	enc := make([]byte, 8)
	binary.BigEndian.PutUint64(enc, number)
	return enc
}

func headerKey(number uint64, hash common.Hash) []byte {
	return append(append(headerPrefix, encodeBlockNumber(number)...), hash.Bytes()...)
}

func headerHashKey(number uint64) []byte {
	return append(append(headerPrefix, encodeBlockNumber(number)...), headerHashSuffix...)
}

func headerNumberKey(hash common.Hash) []byte {
	return append(headerNumberPrefix, hash.Bytes()...)
}

func blockBodyKey(number uint64, hash common.Hash) []byte {
	return append(append(blockBodyPrefix, encodeBlockNumber(number)...), hash.Bytes()...)
}

func blockReceiptsKey(number uint64, hash common.Hash) []byte {
	return append(append(blockReceiptsPrefix, encodeBlockNumber(number)...), hash.Bytes()...)
}

func configKey(hash common.Hash) []byte {
	return append(configPrefix, hash.Bytes()...)
}

func codeKey(hash common.Hash) []byte {
	return append(codePrefix, hash.Bytes()...)
}

func txLookupKey(hash common.Hash) []byte {
	return append(txLookupPrefix, hash.Bytes()...)
}

func preimageKey(hash common.Hash) []byte {
	return append(preimagePrefix, hash.Bytes()...)
}

func accountSnapshotKey(hash common.Hash) []byte {
	return append(snapshotAccountPrefix, hash.Bytes()...)
}

func storageSnapshotKey(accountHash, storageHash common.Hash) []byte {
	buf := make([]byte, len(snapshotStoragePrefix)+common.HashLength+common.HashLength)
	n := copy(buf, snapshotStoragePrefix)
	n += copy(buf[n:], accountHash.Bytes())
	copy(buf[n:], storageHash.Bytes())
	return buf
}

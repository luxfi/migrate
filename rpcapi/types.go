// Copyright (C) 2019-2025, Lux Industries, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

// Package rpcapi provides shared types and utilities for the migrate RPC API.
// This package is imported by both coreth (C-Chain) and evm (SubnetEVM) to ensure
// consistent block import/export functionality across all VM implementations.
package rpcapi

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/luxfi/geth/common"
	"github.com/luxfi/geth/core/types"
	"github.com/luxfi/geth/rlp"
)

// BlockEntry represents a single block to import.
// This is the canonical format for block migration, compatible with JSONL export.
//
// For correct state migration, stateChanges MUST be present on every block:
//   - Block 0 (genesis): full account snapshot (not a diff)
//   - Block N>0: post-state diff relative to parent
type BlockEntry struct {
	Height   uint64 `json:"height"`   // Block number
	Hash     string `json:"hash"`     // Block hash (hex with 0x prefix)
	Header   string `json:"header"`   // RLP-encoded header (hex with 0x prefix)
	Body     string `json:"body"`     // RLP-encoded body (hex with 0x prefix)
	Receipts string `json:"receipts"` // RLP-encoded receipts (hex with 0x prefix)

	// StateChanges contains per-block state diffs for diff-replay import.
	// Key is account address (hex with 0x prefix), value is the state change.
	// This field is REQUIRED for correct balance/state migration.
	// If absent, importer falls back to EVM execution (requires matching genesis).
	StateChanges map[string]*StateChangeEntry `json:"stateChanges,omitempty"`

	// Metadata
	Meta *BlockMeta `json:"meta,omitempty"`
}

// StateChangeEntry represents a state change for a single account.
// This is the diff format - only fields that changed are included.
type StateChangeEntry struct {
	// Nonce - current nonce after this block
	Nonce uint64 `json:"nonce"`

	// Balance - hex-encoded balance after this block
	Balance string `json:"balance"`

	// Code - hex-encoded contract code (only if code changed, e.g., contract creation)
	Code string `json:"code,omitempty"`

	// Storage - map of slot (hex) -> value (hex) for changed slots
	// Zero value (0x0...0) means delete the slot from storage trie
	Storage map[string]string `json:"storage,omitempty"`

	// Deleted - true if account was self-destructed/deleted this block
	// If true, remove account from state trie entirely
	Deleted bool `json:"deleted,omitempty"`
}

// BlockMeta contains metadata about the block's chain context
type BlockMeta struct {
	ChainID     uint64 `json:"chainId,omitempty"`
	GenesisHash string `json:"genesisHash,omitempty"`
}

// ImportBlocksArgs is the request for ImportBlocks RPC (used by coreth HTTP-style RPC)
type ImportBlocksArgs struct {
	Blocks []BlockEntry `json:"blocks"`
}

// ImportBlocksReply is the response from ImportBlocks RPC
type ImportBlocksReply struct {
	Imported      int      `json:"imported"`
	Failed        int      `json:"failed"`
	FirstHeight   uint64   `json:"firstHeight,omitempty"`
	LastHeight    uint64   `json:"lastHeight,omitempty"`
	Errors        []string `json:"errors,omitempty"`
	HeadBlockHash string   `json:"headBlockHash,omitempty"`
	StateRoot     string   `json:"stateRoot,omitempty"`
}

// ChainInfo contains basic chain information for debugging
type ChainInfo struct {
	ChainID       uint64 `json:"chainId"`
	NetworkID     uint64 `json:"networkId,omitempty"`
	GenesisHash   string `json:"genesisHash,omitempty"`
	CurrentHeight uint64 `json:"currentHeight"`
	CurrentHash   string `json:"currentHash"`
	StateRoot     string `json:"stateRoot"`
	VMVersion     string `json:"vmVersion,omitempty"`
}

// BlockChainInserter is the interface that blockchain implementations must satisfy
// for block import. Both coreth and SubnetEVM blockchains implement this.
type BlockChainInserter interface {
	InsertChain(chain types.Blocks) (int, error)
	CurrentBlock() *types.Header
}

// Logger is the interface for logging during block import
type Logger interface {
	Info(msg string, ctx ...interface{})
}

// DecodeHexString decodes a hex string with or without 0x prefix
func DecodeHexString(s string) ([]byte, error) {
	s = strings.TrimPrefix(s, "0x")
	s = strings.TrimPrefix(s, "0X")
	if s == "" {
		return nil, nil
	}
	return hex.DecodeString(s)
}

// EncodeHexString encodes bytes to hex string with 0x prefix
func EncodeHexString(b []byte) string {
	if len(b) == 0 {
		return "0x"
	}
	return "0x" + hex.EncodeToString(b)
}

// ImportBlocks is the core implementation for importing blocks with full execution.
// It uses InsertChain to properly:
// 1. Validate each block
// 2. Execute all transactions via StateProcessor.Process()
// 3. Commit state changes to the database
// 4. Update canonical chain pointers
//
// This function is used by both coreth and SubnetEVM's MigrateAPI.
func ImportBlocks(bc BlockChainInserter, logger Logger, blocks []BlockEntry, reply *ImportBlocksReply) error {
	if len(blocks) == 0 {
		return fmt.Errorf("no blocks provided")
	}

	reply.FirstHeight = blocks[0].Height
	reply.LastHeight = blocks[len(blocks)-1].Height

	startTime := time.Now()

	for _, entry := range blocks {
		// Decode header
		headerBytes, err := DecodeHexString(entry.Header)
		if err != nil {
			reply.Failed++
			reply.Errors = append(reply.Errors, fmt.Sprintf("block %d: header decode: %v", entry.Height, err))
			continue
		}

		var header types.Header
		if err := rlp.DecodeBytes(headerBytes, &header); err != nil {
			reply.Failed++
			reply.Errors = append(reply.Errors, fmt.Sprintf("block %d: header RLP: %v", entry.Height, err))
			continue
		}

		// Decode body
		bodyBytes, err := DecodeHexString(entry.Body)
		if err != nil {
			reply.Failed++
			reply.Errors = append(reply.Errors, fmt.Sprintf("block %d: body decode: %v", entry.Height, err))
			continue
		}

		var body types.Body
		if len(bodyBytes) > 0 {
			if err := rlp.DecodeBytes(bodyBytes, &body); err != nil {
				reply.Failed++
				reply.Errors = append(reply.Errors, fmt.Sprintf("block %d: body RLP: %v", entry.Height, err))
				continue
			}
		}

		// Create full block
		block := types.NewBlockWithHeader(&header).WithBody(body)

		// Verify hash if provided
		if entry.Hash != "" {
			expectedHash := common.HexToHash(entry.Hash)
			if block.Hash() != expectedHash {
				reply.Failed++
				reply.Errors = append(reply.Errors, fmt.Sprintf("block %d: hash mismatch (expected %s, got %s)", entry.Height, expectedHash.Hex(), block.Hash().Hex()))
				continue
			}
		}

		// Execute block with full consensus using InsertChain
		n, err := bc.InsertChain(types.Blocks{block})
		if err != nil {
			reply.Failed++
			reply.Errors = append(reply.Errors, fmt.Sprintf("block %d: insert failed: %v", entry.Height, err))
			continue
		}

		if n == 0 {
			// Block already exists or wasn't inserted
			reply.Failed++
			reply.Errors = append(reply.Errors, fmt.Sprintf("block %d: not inserted", entry.Height))
			continue
		}

		reply.Imported++

		// Log progress every 1000 blocks
		if reply.Imported%1000 == 0 && logger != nil {
			elapsed := time.Since(startTime)
			rate := float64(reply.Imported) / elapsed.Seconds()
			logger.Info("ImportBlocks progress",
				"imported", reply.Imported,
				"failed", reply.Failed,
				"rate", fmt.Sprintf("%.1f/sec", rate),
				"height", entry.Height,
			)
		}
	}

	// Get final state
	head := bc.CurrentBlock()
	if head != nil {
		reply.HeadBlockHash = head.Hash().Hex()
		reply.StateRoot = head.Root.Hex()
	}

	elapsed := time.Since(startTime)
	if logger != nil {
		logger.Info("ImportBlocks complete",
			"imported", reply.Imported,
			"failed", reply.Failed,
			"elapsed", elapsed.Round(time.Millisecond),
		)
	}

	return nil
}

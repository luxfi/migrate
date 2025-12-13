# Lux Network Migration Guide

**Last Updated**: 2025-12-12

## Overview

This guide describes the **one and only** way to migrate blockchain data between Lux network VMs: using the **MigrateAPI** (`migrate_importBlocks`). This API is implemented in both `cchainvm` (C-Chain) and `evm` (SubnetEVM).

## Architecture: geth vs coreth vs evm

Understanding the relationship between these packages is essential:

### Package Hierarchy

```
┌─────────────────────────────────────────────────────────────────┐
│                         luxfi/node                              │
│   ┌─────────────────┐  ┌─────────────────┐  ┌───────────────┐  │
│   │    cchainvm     │  │      evm        │  │   other vms   │  │
│   │  (C-Chain VM)   │  │  (SubnetEVM)    │  │               │  │
│   │  + MigrateAPI   │  │  + MigrateAPI   │  │               │  │
│   └────────┬────────┘  └────────┬────────┘  └───────────────┘  │
└────────────┼────────────────────┼───────────────────────────────┘
             │                    │
             ▼                    ▼
┌────────────────────┐  ┌────────────────────┐
│    luxfi/coreth    │  │    luxfi/evm       │
│   (C-Chain impl)   │  │  (SubnetEVM impl)  │
│   Uses geth        │  │   Uses geth        │
└─────────┬──────────┘  └─────────┬──────────┘
          │                       │
          └───────────┬───────────┘
                      ▼
          ┌────────────────────┐
          │    luxfi/geth      │
          │  (go-ethereum fork)│
          │  Pure EVM engine   │
          └────────────────────┘
```

### Package Descriptions

| Package | Repository | Purpose |
|---------|------------|---------|
| **geth** | `github.com/luxfi/geth` | Pure go-ethereum fork. Core EVM execution, RLP encoding, state trie, transaction types. **No migration logic here.** |
| **coreth** | `github.com/luxfi/coreth` | C-Chain implementation built on geth. Handles C-Chain consensus integration. Used by cchainvm. |
| **evm** | `github.com/luxfi/evm` | SubnetEVM implementation built on geth. For L1/subnet chains. Contains its own MigrateAPI. |
| **cchainvm** | `github.com/luxfi/node/vms/cchainvm` | C-Chain VM wrapper. Contains MigrateAPI for C-Chain. |
| **migrate** | `github.com/luxfi/migrate` | Migration tools: export from PebbleDB, import via RPC. |

### Key Points

1. **geth** is ONLY the EVM engine - it has no migration APIs
2. **MigrateAPI** exists in `cchainvm` (for C-Chain) and `evm` (for SubnetEVM)
3. **coreth** is used internally by cchainvm, not directly exposed
4. All migration goes through `migrate_importBlocks` RPC endpoint

## Migration Flow

The canonical migration process:

```
┌──────────────────┐     ┌──────────────────┐     ┌──────────────────┐
│  Source Chain    │     │     JSONL        │     │  Target Chain    │
│  (PebbleDB)      │────▶│    (blocks)      │────▶│  (BadgerDB)      │
│                  │     │                  │     │                  │
│  Export tool     │     │  Standard format │     │  migrate_import  │
│  (read-only)     │     │  with RLP data   │     │  Blocks API      │
└──────────────────┘     └──────────────────┘     └──────────────────┘
```

### Step 1: Export Blocks

Export from source database (PebbleDB) to JSONL:

```bash
# Build export tool
cd /home/z/work/lux/migrate
go build -o bin/export ./cmd/export/

# Export all blocks
./bin/export \
  -db /path/to/pebbledb \
  -output blocks.jsonl \
  -start 0 \
  -end 0  # 0 = head block
```

### Step 2: Import via MigrateAPI

Import to target chain using the `migrate_importBlocks` RPC:

```bash
# Build import tool
go build -o bin/import-rpc ./cmd/import-rpc/

# Import blocks
./bin/import-rpc \
  -jsonl blocks.jsonl \
  -rpc http://127.0.0.1:9630/ext/bc/C/rpc \
  -batch 100 \
  -start 1 \
  -reload 10000
```

## JSONL Format Specification

The canonical JSONL format uses **camelCase** field names matching the MigrateAPI:

```json
{
  "height": 12345,
  "hash": "0x1234...abcd",
  "parentHash": "0xabcd...1234",
  "timestamp": 1702400000,
  "stateRoot": "0x...",
  "receiptsRoot": "0x...",
  "transactionsRoot": "0x...",
  "gasLimit": 8000000,
  "gasUsed": 21000,
  "difficulty": "1",
  "coinbase": "0x0000000000000000000000000000000000000000",
  "nonce": "0x0000000000000000",
  "mixHash": "0x0000000000000000000000000000000000000000000000000000000000000000",
  "extraData": "0x",
  "baseFee": "25000000000",
  "header": "0xf90213...",
  "body": "0xf90210...",
  "receipts": "0xf901ae...",
  "stateChanges": {
    "0x1234...": {
      "balance": "1000000000000000000",
      "nonce": 5,
      "code": "0x608060...",
      "storage": {
        "0x0": "0x1"
      }
    }
  }
}
```

### Field Reference

| Field | Type | Description |
|-------|------|-------------|
| `height` | uint64 | Block number (matches `Number` in BlockData) |
| `hash` | hex string | Block hash with 0x prefix |
| `header` | hex string | RLP-encoded block header |
| `body` | hex string | RLP-encoded block body (transactions, uncles) |
| `receipts` | hex string | RLP-encoded transaction receipts |
| `stateChanges` | object | Optional: account state to import |

### Backwards Compatibility

The UnmarshalJSON handler supports multiple formats:

1. **New canonical** (camelCase): `height`, `hash`, `header`, `body`, `receipts`
2. **Legacy export** (PascalCase): `Number`, `Hash`, `Header`, `Body`, `Receipts`
3. **RPC format**: `number`, `miner`, `baseFeePerGas`

## MigrateAPI Reference

### migrate_importBlocks

Import RLP-encoded blocks with optional state changes.

**Request:**
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "migrate_importBlocks",
  "params": [[
    {
      "height": 1,
      "hash": "0x...",
      "header": "0x...",
      "body": "0x...",
      "receipts": "0x...",
      "stateChanges": {}
    }
  ]]
}
```

**Response:**
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "imported": 100,
    "failed": 0,
    "errors": []
  }
}
```

### migrate_setGenesis

Set genesis block (block 0). Must be called before importing other blocks if genesis needs modification.

**Request:**
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "migrate_setGenesis",
  "params": [{
    "height": 0,
    "hash": "0x...",
    "header": "0x...",
    "body": "0x...",
    "receipts": "0x..."
  }]
}
```

### lux_reloadBlockchain

Force blockchain to recognize database changes after import.

**Request:**
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "lux_reloadBlockchain",
  "params": []
}
```

### lux_verifyBlockchain

Verify blockchain integrity after import.

**Request:**
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "lux_verifyBlockchain",
  "params": []
}
```

## State Migration

### With State Changes

To preserve account balances, contract code, and storage:

1. **Export with state**: Include `stateChanges` in each block's JSONL
2. **Import**: MigrateAPI writes state to trie database
3. **Verify**: Check balances with `eth_getBalance`

### Without State (Block-only)

For block history without state:

1. Export blocks only (no `stateChanges`)
2. Import creates block records
3. State must come from genesis or separate migration

## Database Considerations

| Database | Use Case | Location |
|----------|----------|----------|
| **PebbleDB** | Legacy SubnetEVM chains (export source) | `/path/to/chain/db/pebbledb` |
| **BadgerDB** | New C-Chain and subnets (import target) | `~/.avalanche-cli/runs/.../db` |
| **LevelDB** | Not recommended | Legacy only |

**Important**: Always use BadgerDB for target chains. PebbleDB is only for reading legacy data.

## Complete Migration Example

### Migrate SubnetEVM to C-Chain

```bash
# 1. Stop source chain (if running)

# 2. Export blocks from SubnetEVM PebbleDB
cd /home/z/work/lux/migrate
./bin/export \
  -db /home/z/work/lux/state/chaindata/lux-mainnet-96369/db/pebbledb \
  -output /tmp/blocks.jsonl \
  -start 0 \
  -end 0

# 3. Start target C-Chain node with migrate API enabled
luxd --network-id=96369 \
  --staking-enabled=false \
  --api-admin-enabled=true

# 4. Verify migrate API is available
curl -s http://127.0.0.1:9630/ext/bc/C/rpc \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"rpc_modules","params":[]}' \
  | grep migrate

# 5. Import blocks (skip genesis if using same genesis config)
./bin/import-rpc \
  -jsonl /tmp/blocks.jsonl \
  -rpc http://127.0.0.1:9630/ext/bc/C/rpc \
  -start 1 \
  -batch 100 \
  -reload 10000

# 6. Verify import
curl -s http://127.0.0.1:9630/ext/bc/C/rpc \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}'

# 7. Verify state (check a known balance)
curl -s http://127.0.0.1:9630/ext/bc/C/rpc \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0x8db97C7cEcE249c2b98bDC0226Cc4C2A57BF52FC","latest"]}'
```

## Troubleshooting

### Import Fails with "migrate API not available"

Ensure node is started with admin API enabled:
```bash
luxd --api-admin-enabled=true
```

### State Root Mismatch

If importing blocks produces state root mismatches:
1. Ensure genesis configuration matches source chain
2. Include `stateChanges` in JSONL export
3. Verify state trie commits complete

### Slow Import Performance

Optimize with:
- Larger batch size: `-batch 500`
- Less frequent reloads: `-reload 50000`
- SSD storage for database

### Memory Issues

If running out of memory:
- Reduce batch size: `-batch 50`
- Import in chunks (use `-start` and `-end`)

## Files Reference

| Path | Purpose |
|------|---------|
| `/home/z/work/lux/migrate/` | Migration package |
| `/home/z/work/lux/migrate/types.go` | BlockData, Account types |
| `/home/z/work/lux/migrate/cmd/export/` | Export tool |
| `/home/z/work/lux/migrate/cmd/import-rpc/` | Import tool |
| `/home/z/work/lux/node/vms/cchainvm/api.go` | C-Chain MigrateAPI |
| `/home/z/work/lux/evm/plugin/evm/api_migrate.go` | SubnetEVM MigrateAPI |

## Version History

- **2025-12-12**: Unified field naming (camelCase), removed snake_case variants
- **2025-12-04**: Added stateChanges support
- **2025-11-30**: Initial MigrateAPI implementation

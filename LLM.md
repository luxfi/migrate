# AI Assistant Knowledge Base - Lux Migrate

**Last Updated**: 2025-11-30
**Project**: Lux Migrate
**Organization**: Lux Industries

## Project Overview

The Lux Migrate package (`github.com/luxfi/migrate`) provides a generic framework for blockchain data migration between different VM implementations. It is the centralized solution for all import/export operations across the Lux ecosystem.

## Architecture

### Core Interfaces

```go
// Exporter - exports data from any VM
type Exporter interface {
    Init(config ExporterConfig) error
    GetInfo() (*Info, error)
    ExportBlocks(ctx context.Context, start, end uint64) (<-chan *BlockData, <-chan error)
    ExportState(ctx context.Context, blockNumber uint64) (<-chan *Account, <-chan error)
    ExportAccount(ctx context.Context, address common.Address, blockNumber uint64) (*Account, error)
    ExportConfig() (*Config, error)
    VerifyExport(blockNumber uint64) error
    Close() error
}

// Importer - imports data to any VM
type Importer interface {
    Init(config ImporterConfig) error
    ImportConfig(config *Config) error
    ImportBlock(block *BlockData) error
    ImportBlocks(blocks []*BlockData) error
    ImportState(accounts []*Account, blockNumber uint64) error
    FinalizeImport(blockNumber uint64) error
    VerifyImport(blockNumber uint64) error
    ExecuteBlock(block *BlockData) error
    Close() error
}

// Migrator - orchestrates migrations
type Migrator interface {
    Migrate(ctx context.Context, source Exporter, dest Importer, options MigrationOptions) (*MigrationResult, error)
    MigrateRange(ctx context.Context, source Exporter, dest Importer, start, end uint64) (*MigrationResult, error)
    MigrateState(ctx context.Context, source Exporter, dest Importer, blockNumber uint64) error
    Verify(source Exporter, dest Importer, blockNumber uint64) error
}
```

### Supported VM Types

| VM Type | Export | Import | Description |
|---------|--------|--------|-------------|
| `subnet-evm` | ✅ | ⚠️ | SubnetEVM (PebbleDB) |
| `c-chain` | ⚠️ | ✅ | C-Chain (BadgerDB + RPC) |
| `coreth` | ⚠️ | ⚠️ | Legacy Coreth VM |
| `zoo-l2` | ⚠️ | ⚠️ | Zoo L2 chains |
| `p-chain` | ⚠️ | ⚠️ | Platform chain |
| `x-chain` | ⚠️ | ⚠️ | Exchange chain |
| `q-chain` | ⚠️ | ⚠️ | Quantum chain |

✅ = Implemented, ⚠️ = Stub/Partial

### Directory Structure

```
~/work/lux/migrate/
├── go.mod              # Module definition
├── types.go            # Core data types (BlockData, Account, etc.)
├── exporter.go         # Exporter interface + factory
├── importer.go         # Importer interface + factory
├── migrator.go         # Migrator implementation
├── errors.go           # Error definitions
├── factory.go          # VM-specific constructors
├── subnetevm/          # SubnetEVM exporter (PebbleDB)
│   └── exporter.go
├── cchain/             # C-Chain importer (RPC)
│   └── importer.go
├── coreth/             # Legacy Coreth support
├── zool2/              # Zoo L2 chains
├── pchain/             # Platform chain
├── xchain/             # Exchange chain
├── qchain/             # Quantum chain
└── jsonl/              # JSONL format handlers
    └── format.go
```

## Usage

### Export SubnetEVM to JSONL

```go
import (
    "github.com/luxfi/migrate"
    "github.com/luxfi/migrate/jsonl"
)

// Create exporter
exporter, err := migrate.NewExporter(migrate.ExporterConfig{
    VMType:       migrate.VMTypeSubnetEVM,
    DatabasePath: "/path/to/pebbledb",
    DatabaseType: "pebble",
})
if err != nil {
    log.Fatal(err)
}
defer exporter.Close()

// Export to JSONL
writer, _ := jsonl.NewWriter("blocks.jsonl")
defer writer.Close()

blocks, errs := exporter.ExportBlocks(ctx, 0, 1000000)
for block := range blocks {
    writer.WriteBlock(block)
}
```

### Import JSONL to C-Chain

```go
// Create RPC importer
importer, err := migrate.NewImporter(migrate.ImporterConfig{
    VMType: migrate.VMTypeCChain,
    RPCURL: "http://localhost:9650/ext/bc/C/rpc",
})
defer importer.Close()

// Read from JSONL
reader := jsonl.NewStreamReader("blocks.jsonl")
blocks, errs := reader.ReadBlocks()

for block := range blocks {
    if err := importer.ImportBlock(block); err != nil {
        log.Printf("Failed to import block %d: %v", block.Number, err)
    }
}
```

### Full Migration

```go
// Create migrator
migrator := migrate.NewMigrator()

// Create source and destination
source, _ := migrate.NewExporter(migrate.ExporterConfig{
    VMType:       migrate.VMTypeSubnetEVM,
    DatabasePath: "/path/to/subnet/db",
})

dest, _ := migrate.NewImporter(migrate.ImporterConfig{
    VMType: migrate.VMTypeCChain,
    RPCURL: "http://localhost:9650/ext/bc/C/rpc",
})

// Run migration
result, err := migrator.Migrate(ctx, source, dest, migrate.MigrationOptions{
    StartBlock:      0,
    EndBlock:        1000000,
    BatchSize:       100,
    MaxConcurrency:  4,
    MigrateState:    true,
    VerifyEachBlock: false,
    ProgressCallback: func(current, total uint64) {
        fmt.Printf("Progress: %d/%d (%.1f%%)\n", current, total, float64(current)/float64(total)*100)
    },
})
```

## CLI Integration

The CLI (`lux`) uses this package for migration commands:

```bash
# Export SubnetEVM to JSONL
lux network export data \
  --source-type=subnet-evm \
  --source-path=/path/to/pebbledb \
  --output=blocks.jsonl

# Import JSONL to C-Chain
lux network import data \
  --id=C \
  --input=blocks.jsonl \
  --rpc=http://127.0.0.1:9650/ext/bc/C/rpc
```

## Data Types

### BlockData
Core block representation for all VMs:
- Block header fields (number, hash, parent, timestamp, etc.)
- RLP-encoded data (header, body, receipts)
- Decoded transactions
- VM-specific extensions map

### Account
Account state for state migration:
- Address, nonce, balance
- Code and code hash
- Storage trie root and slots

### JSONL Format
Line-delimited JSON for streaming:
```json
{"number":0,"hash":"0x...","parentHash":"0x...","timestamp":1234567890,...}
{"number":1,"hash":"0x...","parentHash":"0x...","timestamp":1234567891,...}
```

## Related Documentation

- **LP-0326**: Regenesis specification at `/Users/z/work/lux/lps/LPs/lp-0326-blockchain-regenesis-and-state-migration.md`
- **State docs**: SubnetEVM format at `/Users/z/work/lux/state/LLM.md`
- **Genesis tools**: `/Users/z/work/lux/genesis/LLM.md`
- **CLI docs**: `/Users/z/work/lux/cli/LLM.md`

## Testing Results (2025-12-04)

### migrate_importBlocks API with State - ⚠️ PARTIAL

**Test Setup:**
- Node: luxd v1.21.4 on network 96369
- Genesis data: mainnet genesis with treasury (1.99T LUX)
- Import tool: `/Users/z/work/lux/migrate/cmd/import-jsonl/`
- State format: stateChanges map in JSONL

**Results:**
```
✅ Block import succeeds
✅ StateChanges field parsed correctly
✅ importStateChanges() called
✅ State committed to trie DB
⚠️  State root mismatch warning logged
❌ eth_getBalance still returns 0x0
```

**Root Cause - Critical Discovery:**

The `migrate_importBlocks` API **CANNOT** be used for genesis (block 0) because:
1. Blockchain is already initialized with a genesis from the node's genesis file
2. Importing block 0 via API creates a NEW genesis block with different hash
3. State is written to trie DB but blockchain still uses ORIGINAL genesis state root
4. Result: Two genesis blocks exist, queries use wrong state root

**Solution:**

For **block 0 (genesis)**: Use `migrate_setGenesis` API or restart node with correct genesis file
For **blocks 1+**: Use `migrate_importBlocks` with stateChanges

**Correct Import Sequence:**
1. Stop node
2. Update genesis file with correct allocations
3. Delete existing chain database
4. Restart node (loads correct genesis)
5. Import blocks 1+ with stateChanges via `migrate_importBlocks`

**Alternative Using Existing Database:**
1. Use `migrate_setGenesis` to replace genesis block 0
2. Blockchain reloads with new genesis state root
3. Import remaining blocks with `migrate_importBlocks`

**Tools Created (2025-12-04):**
- `/Users/z/work/lux/migrate/cmd/genesis-to-jsonl/main.go` - Converts genesis.json to JSONL with state
- `/Users/z/work/lux/migrate/cmd/import-jsonl/main.go` - Updated to support stateChanges field
- `/Users/z/work/lux/migrate/cmd/export-genesis/main.go` - Direct state trie export (incomplete)

**Status**: Tools ready, but genesis replacement via API blocked by node initialization order.

## Rules for AI Assistants

1. **ALWAYS** use this package for any migration work - don't create duplicate code
2. **NEVER** access database layers directly - use Exporter/Importer interfaces
3. **NEVER** commit random summary files - update THIS file
4. Use JSONL as the intermediate format for all migrations
5. All new VM types must implement both Exporter and Importer interfaces
6. **IMPORTANT**: Block import alone is NOT sufficient - state trie must be migrated too

---

**Note**: This package is the single source of truth for blockchain migration in the Lux ecosystem.

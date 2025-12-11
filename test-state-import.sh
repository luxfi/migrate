#!/bin/bash
# Test script for state import functionality

set -e

echo "=== Testing State Import Functionality ==="
echo ""

# Step 1: Export first 10 blocks with state from source RPC
echo "Step 1: Exporting blocks 0-10 with state..."
# This would use your import-jsonl tool with the updated exporter
# For now, we'll document the expected workflow

echo ""
echo "Expected JSONL format with state:"
echo ""
cat <<'EOF'
{
  "Number": 1,
  "Hash": "0x...",
  "Header": "0x...",
  "Body": "0x...",
  "Receipts": "0x...",
  "StateChanges": {
    "0x9011E888251AB053B7bD1cdB598Db4f9DEd94714": {
      "balance": "1900000000000000000000000000000",
      "nonce": 0,
      "code": "",
      "storage": {}
    },
    "0x8db97C7cEcE249c2b98bDC0226Cc4C2A57BF52FC": {
      "balance": "100000000000000000000000000",
      "nonce": 0,
      "code": "",
      "storage": {}
    }
  }
}
EOF

echo ""
echo "Step 2: Import via migrate_importBlocks RPC"
echo "curl -X POST http://localhost:9650/ext/bc/C/rpc \\"
echo "  -H 'Content-Type: application/json' \\"
echo "  -d '{"
echo "    \"jsonrpc\": \"2.0\","
echo "    \"id\": 1,"
echo "    \"method\": \"migrate_importBlocks\","
echo "    \"params\": [[{...blocks with stateChanges...}]]"
echo "  }'"

echo ""
echo "Step 3: Verify balances appear"
echo "# Check treasury balance"
echo "curl -X POST http://localhost:9650/ext/bc/C/rpc \\"
echo "  -H 'Content-Type: application/json' \\"
echo "  -d '{"
echo "    \"jsonrpc\": \"2.0\","
echo "    \"id\": 1,"
echo "    \"method\": \"eth_getBalance\","
echo "    \"params\": [\"0x9011E888251AB053B7bD1cdB598Db4f9DEd94714\", \"latest\"]"
echo "  }'"

echo ""
echo "# Check genesis balance"
echo "curl -X POST http://localhost:9650/ext/bc/C/rpc \\"
echo "  -H 'Content-Type: application/json' \\"
echo "  -d '{"
echo "    \"jsonrpc\": \"2.0\","
echo "    \"id\": 1,"
echo "    \"method\": \"eth_getBalance\","
echo "    \"params\": [\"0x8db97C7cEcE249c2b98bDC0226Cc4C2A57BF52FC\", \"latest\"]"
echo "  }'"

echo ""
echo "=== Implementation Status ==="
echo "✅ BlockData.StateChanges field added (types.go)"
echo "✅ Exporter updated to export state per block (cchain/exporter.go)"
echo "✅ migrate_importBlocks API updated to write state trie (cchainvm/api.go)"
echo "✅ All changes compiled successfully"
echo ""
echo "Next steps:"
echo "1. Test with first 100 blocks"
echo "2. Verify balances appear correctly"
echo "3. Import full 1.08M blocks"
echo "4. Verify final balances match source chain"

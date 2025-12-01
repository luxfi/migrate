package migrate

import "errors"

// Common migration errors
var (
	// ErrUnsupportedVMType indicates the VM type is not supported
	ErrUnsupportedVMType = errors.New("unsupported VM type")

	// ErrDatabaseNotFound indicates the database path doesn't exist
	ErrDatabaseNotFound = errors.New("database not found")

	// ErrInvalidBlockRange indicates an invalid block range
	ErrInvalidBlockRange = errors.New("invalid block range: start > end")

	// ErrBlockNotFound indicates a block was not found
	ErrBlockNotFound = errors.New("block not found")

	// ErrStateNotAvailable indicates state is not available (pruned)
	ErrStateNotAvailable = errors.New("state not available at this height")

	// ErrStateMismatch indicates source and destination state don't match
	ErrStateMismatch = errors.New("state mismatch between source and destination")

	// ErrImportFailed indicates the import operation failed
	ErrImportFailed = errors.New("import operation failed")

	// ErrExportFailed indicates the export operation failed
	ErrExportFailed = errors.New("export operation failed")

	// ErrNotInitialized indicates the exporter/importer was not initialized
	ErrNotInitialized = errors.New("not initialized, call Init() first")

	// ErrAlreadyInitialized indicates the exporter/importer was already initialized
	ErrAlreadyInitialized = errors.New("already initialized")

	// ErrStateImportNotSupported indicates state import is not supported
	ErrStateImportNotSupported = errors.New("state import not supported for this importer type")

	// ErrMigrationInProgress indicates a migration is already in progress
	ErrMigrationInProgress = errors.New("migration already in progress")

	// ErrRPCConnectionFailed indicates RPC connection failed
	ErrRPCConnectionFailed = errors.New("RPC connection failed")
)

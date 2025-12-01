package migrate

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Migrator orchestrates the migration between VMs
type Migrator interface {
	// Migrate performs the full migration
	Migrate(ctx context.Context, source Exporter, dest Importer, options MigrationOptions) (*MigrationResult, error)

	// MigrateRange migrates a specific block range
	MigrateRange(ctx context.Context, source Exporter, dest Importer, start, end uint64) (*MigrationResult, error)

	// MigrateState migrates state at a specific height
	MigrateState(ctx context.Context, source Exporter, dest Importer, blockNumber uint64) error

	// Verify verifies the migration was successful
	Verify(source Exporter, dest Importer, blockNumber uint64) error
}

// DefaultMigrator provides the default migration implementation
type DefaultMigrator struct {
	mu sync.Mutex
}

// NewMigrator creates a new default migrator
func NewMigrator() *DefaultMigrator {
	return &DefaultMigrator{}
}

func (m *DefaultMigrator) Migrate(ctx context.Context, source Exporter, dest Importer, options MigrationOptions) (*MigrationResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := &MigrationResult{
		StartTime: time.Now(),
	}

	// Get source info
	info, err := source.GetInfo()
	if err != nil {
		result.Errors = append(result.Errors, err)
		return result, err
	}

	// Set end block if not specified
	endBlock := options.EndBlock
	if endBlock == 0 {
		endBlock = info.CurrentHeight
	}

	// Export and import configuration
	config, err := source.ExportConfig()
	if err != nil {
		result.Errors = append(result.Errors, err)
		return result, err
	}

	if err := dest.ImportConfig(config); err != nil {
		result.Errors = append(result.Errors, err)
		return result, err
	}

	// Migrate blocks
	rangeResult, err := m.MigrateRange(ctx, source, dest, options.StartBlock, endBlock)
	if err != nil && !options.ContinueOnError {
		result.Errors = append(result.Errors, err)
		return result, err
	}
	result.BlocksMigrated = rangeResult.BlocksMigrated

	// Migrate state if requested
	if options.MigrateState {
		stateHeight := options.StateHeight
		if stateHeight == 0 {
			stateHeight = endBlock
		}
		if err := m.MigrateState(ctx, source, dest, stateHeight); err != nil && !options.ContinueOnError {
			result.Errors = append(result.Errors, err)
			return result, err
		}
		result.StatesMigrated = 1
	}

	// Finalize import
	if err := dest.FinalizeImport(endBlock); err != nil {
		result.Errors = append(result.Errors, err)
		return result, err
	}

	// Verify if requested
	if options.VerifyEachBlock || options.MigrateState {
		if err := m.Verify(source, dest, endBlock); err != nil {
			result.Errors = append(result.Errors, err)
		} else {
			result.StateMatches = true
		}
	}

	result.EndTime = time.Now()
	result.Success = len(result.Errors) == 0
	return result, nil
}

func (m *DefaultMigrator) MigrateRange(ctx context.Context, source Exporter, dest Importer, start, end uint64) (*MigrationResult, error) {
	result := &MigrationResult{
		StartTime: time.Now(),
	}

	// Export blocks
	blocksChan, errChan := source.ExportBlocks(ctx, start, end)

	var batch []*BlockData
	batchSize := 100

	for {
		select {
		case <-ctx.Done():
			result.EndTime = time.Now()
			return result, ctx.Err()

		case err, ok := <-errChan:
			if ok && err != nil {
				result.Errors = append(result.Errors, err)
				result.EndTime = time.Now()
				return result, err
			}

		case block, ok := <-blocksChan:
			if !ok {
				// Channel closed, process remaining batch
				if len(batch) > 0 {
					if err := dest.ImportBlocks(batch); err != nil {
						result.Errors = append(result.Errors, err)
						result.EndTime = time.Now()
						return result, err
					}
					result.BlocksMigrated += uint64(len(batch))
				}
				result.EndTime = time.Now()
				result.Success = len(result.Errors) == 0
				return result, nil
			}

			batch = append(batch, block)

			if len(batch) >= batchSize {
				if err := dest.ImportBlocks(batch); err != nil {
					result.Errors = append(result.Errors, err)
					result.EndTime = time.Now()
					return result, err
				}
				result.BlocksMigrated += uint64(len(batch))
				batch = batch[:0]
			}
		}
	}
}

func (m *DefaultMigrator) MigrateState(ctx context.Context, source Exporter, dest Importer, blockNumber uint64) error {
	accountsChan, errChan := source.ExportState(ctx, blockNumber)

	var accounts []*Account
	batchSize := 1000

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case err, ok := <-errChan:
			if ok && err != nil {
				return err
			}

		case account, ok := <-accountsChan:
			if !ok {
				// Channel closed, import remaining accounts
				if len(accounts) > 0 {
					if err := dest.ImportState(accounts, blockNumber); err != nil {
						return err
					}
				}
				return nil
			}

			accounts = append(accounts, account)

			if len(accounts) >= batchSize {
				if err := dest.ImportState(accounts, blockNumber); err != nil {
					return err
				}
				accounts = accounts[:0]
			}
		}
	}
}

func (m *DefaultMigrator) Verify(source Exporter, dest Importer, blockNumber uint64) error {
	// Verify export and import match
	if err := source.VerifyExport(blockNumber); err != nil {
		return fmt.Errorf("source verification failed: %w", err)
	}

	if err := dest.VerifyImport(blockNumber); err != nil {
		return fmt.Errorf("destination verification failed: %w", err)
	}

	return nil
}

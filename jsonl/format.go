// Package jsonl provides JSONL format handling for block migration
package jsonl

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/luxfi/migrate"
)

// Writer writes blocks to JSONL format
type Writer struct {
	file   *os.File
	writer *bufio.Writer
}

// NewWriter creates a new JSONL writer
func NewWriter(path string) (*Writer, error) {
	file, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("failed to create file: %w", err)
	}

	return &Writer{
		file:   file,
		writer: bufio.NewWriter(file),
	}, nil
}

// WriteBlock writes a single block in JSONL format
func (w *Writer) WriteBlock(block *migrate.BlockData) error {
	data, err := json.Marshal(block)
	if err != nil {
		return fmt.Errorf("failed to marshal block: %w", err)
	}

	if _, err := w.writer.Write(data); err != nil {
		return fmt.Errorf("failed to write block: %w", err)
	}

	if _, err := w.writer.WriteString("\n"); err != nil {
		return fmt.Errorf("failed to write newline: %w", err)
	}

	return nil
}

// Flush flushes buffered data to disk
func (w *Writer) Flush() error {
	return w.writer.Flush()
}

// Close closes the writer
func (w *Writer) Close() error {
	if err := w.writer.Flush(); err != nil {
		return err
	}
	return w.file.Close()
}

// Reader reads blocks from JSONL format
type Reader struct {
	file    *os.File
	scanner *bufio.Scanner
}

// NewReader creates a new JSONL reader
func NewReader(path string) (*Reader, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}

	scanner := bufio.NewScanner(file)
	// Increase buffer size for large blocks
	const maxSize = 100 * 1024 * 1024 // 100MB
	buf := make([]byte, maxSize)
	scanner.Buffer(buf, maxSize)

	return &Reader{
		file:    file,
		scanner: scanner,
	}, nil
}

// ReadBlock reads the next block from JSONL
func (r *Reader) ReadBlock() (*migrate.BlockData, error) {
	if !r.scanner.Scan() {
		if err := r.scanner.Err(); err != nil {
			return nil, fmt.Errorf("scanner error: %w", err)
		}
		return nil, io.EOF
	}

	var block migrate.BlockData
	if err := json.Unmarshal(r.scanner.Bytes(), &block); err != nil {
		return nil, fmt.Errorf("failed to unmarshal block: %w", err)
	}

	return &block, nil
}

// Close closes the reader
func (r *Reader) Close() error {
	return r.file.Close()
}

// ReadAllBlocks reads all blocks from the JSONL file
func (r *Reader) ReadAllBlocks() ([]*migrate.BlockData, error) {
	var blocks []*migrate.BlockData
	for {
		block, err := r.ReadBlock()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
}

// StreamReader provides channel-based block reading
type StreamReader struct {
	path string
}

// NewStreamReader creates a new stream reader
func NewStreamReader(path string) *StreamReader {
	return &StreamReader{path: path}
}

// ReadBlocks returns a channel of blocks for streaming processing
func (s *StreamReader) ReadBlocks() (<-chan *migrate.BlockData, <-chan error) {
	blocks := make(chan *migrate.BlockData, 100)
	errs := make(chan error, 1)

	go func() {
		defer close(blocks)
		defer close(errs)

		reader, err := NewReader(s.path)
		if err != nil {
			errs <- err
			return
		}
		defer reader.Close()

		for {
			block, err := reader.ReadBlock()
			if err == io.EOF {
				return
			}
			if err != nil {
				errs <- err
				return
			}
			blocks <- block
		}
	}()

	return blocks, errs
}

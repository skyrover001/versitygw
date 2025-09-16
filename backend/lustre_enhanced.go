// Copyright 2023 Versity Software
// This file is licensed under the Apache License, Version 2.0
// (the "License"); you may not use this file except in compliance
// with the License.  You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package backend

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/versity/versitygw/s3response"
)

// LustreEnhancedBackend wraps a POSIX backend with Lustre-specific optimizations
type LustreEnhancedBackend struct {
	Backend
	lustreConfig *LustreConfig
	mu           sync.RWMutex
}

// LustreStripeInfo contains Lustre striping information
type LustreStripeInfo struct {
	StripeCount int   `json:"stripe_count"`
	StripeSize  int64 `json:"stripe_size"`
	StripeIndex int   `json:"stripe_index"`
	OSTs        []int `json:"osts"`
}

// LustrePoolInfo contains Lustre pool information
type LustrePoolInfo struct {
	PoolName string `json:"pool_name"`
	OSTs     []int  `json:"osts"`
}

// NewLustreEnhancedBackend creates a new Lustre-enhanced backend
func NewLustreEnhancedBackend(backend Backend, config *LustreConfig) *LustreEnhancedBackend {
	return &LustreEnhancedBackend{
		Backend:      backend,
		lustreConfig: config,
	}
}

// PutObject implements optimized PutObject with Lustre striping
func (l *LustreEnhancedBackend) PutObject(ctx context.Context, input s3response.PutObjectInput) (s3response.PutObjectOutput, error) {
	// Get the target file path
	bucket := *input.Bucket
	key := *input.Key
	filePath := filepath.Join(bucket, key)

	// Determine optimal striping based on object size
	contentLength := input.ContentLength
	if contentLength == nil {
		// Use default backend for unknown size
		return l.Backend.PutObject(ctx, input)
	}

	stripeConfig := l.calculateOptimalStriping(*contentLength)

	// Set Lustre striping for the directory if it doesn't exist
	dir := filepath.Dir(filePath)
	if err := l.ensureDirectoryStriping(dir, stripeConfig); err != nil {
		// Log warning but continue with default backend
		fmt.Printf("Warning: Failed to set directory striping: %v\n", err)
	}

	// For large files, use parallel writing
	if *contentLength > l.getLargeFileThreshold() {
		return l.putLargeObjectWithStriping(ctx, input, stripeConfig)
	}

	// Use default backend for small files
	return l.Backend.PutObject(ctx, input)
}

// GetObject implements optimized GetObject with parallel reading
func (l *LustreEnhancedBackend) GetObject(ctx context.Context, input *s3.GetObjectInput) (*s3.GetObjectOutput, error) {
	// Get file info first
	bucket := *input.Bucket
	key := *input.Key
	filePath := filepath.Join(bucket, key)

	stat, err := os.Stat(filePath)
	if err != nil {
		return l.Backend.GetObject(ctx, input)
	}

	// For large files, use parallel reading
	if stat.Size() > l.getLargeFileThreshold() {
		stripeInfo, err := l.getFileStripeInfo(filePath)
		if err == nil && stripeInfo.StripeCount > 1 {
			return l.getLargeObjectWithStriping(ctx, input, stripeInfo)
		}
	}

	// Use default backend for small files or non-striped files
	return l.Backend.GetObject(ctx, input)
}

// calculateOptimalStriping determines optimal striping based on file size
func (l *LustreEnhancedBackend) calculateOptimalStriping(size int64) *LustreStripeInfo {
	stripeInfo := &LustreStripeInfo{
		StripeCount: l.lustreConfig.StripeCount,
		StripeSize:  l.lustreConfig.StripeSize,
		StripeIndex: -1, // Let Lustre choose
	}

	// Adjust stripe count based on file size
	if size < 1*1024*1024 { // < 1MB
		stripeInfo.StripeCount = 1
	} else if size < 100*1024*1024 { // < 100MB
		stripeInfo.StripeCount = 2
	} else if size < 1*1024*1024*1024 { // < 1GB
		stripeInfo.StripeCount = 4
	} else { // >= 1GB
		stripeInfo.StripeCount = 8
	}

	// Don't exceed configured maximum
	if l.lustreConfig.StripeCount > 0 && stripeInfo.StripeCount > l.lustreConfig.StripeCount {
		stripeInfo.StripeCount = l.lustreConfig.StripeCount
	}

	return stripeInfo
}

// ensureDirectoryStriping sets up Lustre striping for a directory
func (l *LustreEnhancedBackend) ensureDirectoryStriping(dirPath string, stripeInfo *LustreStripeInfo) error {
	// Check if directory exists
	if _, err := os.Stat(dirPath); os.IsNotExist(err) {
		// Create directory first
		if err := os.MkdirAll(dirPath, 0755); err != nil {
			return fmt.Errorf("failed to create directory: %w", err)
		}
	}

	// Set Lustre striping using lfs setstripe
	cmd := []string{"lfs", "setstripe"}

	if stripeInfo.StripeCount > 0 {
		cmd = append(cmd, "-c", strconv.Itoa(stripeInfo.StripeCount))
	}

	if stripeInfo.StripeSize > 0 {
		cmd = append(cmd, "-S", strconv.FormatInt(stripeInfo.StripeSize, 10))
	}

	if stripeInfo.StripeIndex >= 0 {
		cmd = append(cmd, "-i", strconv.Itoa(stripeInfo.StripeIndex))
	}

	cmd = append(cmd, dirPath)

	// Execute command
	execCmd := exec.Command(cmd[0], cmd[1:]...)
	output, err := execCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("lfs setstripe failed: %s: %w", string(output), err)
	}

	return nil
}

// getFileStripeInfo gets striping information for a file
func (l *LustreEnhancedBackend) getFileStripeInfo(filePath string) (*LustreStripeInfo, error) {
	cmd := exec.Command("lfs", "getstripe", "-c", "-S", "-i", filePath)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("lfs getstripe failed: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 3 {
		return nil, fmt.Errorf("unexpected lfs getstripe output")
	}

	stripeCount, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return nil, fmt.Errorf("invalid stripe count: %w", err)
	}

	stripeSize, err := strconv.ParseInt(strings.TrimSpace(lines[1]), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid stripe size: %w", err)
	}

	stripeIndex, err := strconv.Atoi(strings.TrimSpace(lines[2]))
	if err != nil {
		return nil, fmt.Errorf("invalid stripe index: %w", err)
	}

	return &LustreStripeInfo{
		StripeCount: stripeCount,
		StripeSize:  stripeSize,
		StripeIndex: stripeIndex,
	}, nil
}

// putLargeObjectWithStriping implements parallel writing for large objects
func (l *LustreEnhancedBackend) putLargeObjectWithStriping(ctx context.Context, input s3response.PutObjectInput, stripeInfo *LustreStripeInfo) (s3response.PutObjectOutput, error) {
	// For now, delegate to the underlying backend
	// In a full implementation, this would implement parallel chunk writing
	// across multiple OSTs based on the stripe configuration

	// Get the target file path
	bucket := *input.Bucket
	key := *input.Key
	filePath := filepath.Join(bucket, key)

	// Ensure directory has proper striping
	dir := filepath.Dir(filePath)
	if err := l.ensureDirectoryStriping(dir, stripeInfo); err != nil {
		fmt.Printf("Warning: Failed to set directory striping: %v\n", err)
	}

	// For now, use the default backend
	// TODO: Implement parallel writing across stripes
	return l.Backend.PutObject(ctx, input)
}

// getLargeObjectWithStriping implements parallel reading for large objects
func (l *LustreEnhancedBackend) getLargeObjectWithStriping(ctx context.Context, input *s3.GetObjectInput, stripeInfo *LustreStripeInfo) (*s3.GetObjectOutput, error) {
	// Get the file path
	bucket := *input.Bucket
	key := *input.Key
	filePath := filepath.Join(bucket, key)

	// Open the file
	file, err := os.Open(filePath)
	if err != nil {
		return l.Backend.GetObject(ctx, input)
	}
	defer file.Close()

	// Get file info
	stat, err := file.Stat()
	if err != nil {
		return l.Backend.GetObject(ctx, input)
	}

	// For now, use default backend
	// TODO: Implement parallel reading using stripe information
	// This would involve:
	// 1. Reading from multiple OSTs in parallel
	// 2. Assembling the data in correct order
	// 3. Handling byte ranges properly across stripes

	return l.Backend.GetObject(ctx, input)
}

// Parallel I/O implementation for Lustre striping

// ParallelReader implements parallel reading across Lustre stripes
type ParallelReader struct {
	file       *os.File
	stripeInfo *LustreStripeInfo
	fileSize   int64
	currentPos int64
	mu         sync.Mutex
}

// NewParallelReader creates a new parallel reader for striped files
func NewParallelReader(file *os.File, stripeInfo *LustreStripeInfo) (*ParallelReader, error) {
	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}

	return &ParallelReader{
		file:       file,
		stripeInfo: stripeInfo,
		fileSize:   stat.Size(),
		currentPos: 0,
	}, nil
}

// Read implements io.Reader with parallel stripe reading
func (pr *ParallelReader) Read(p []byte) (n int, err error) {
	pr.mu.Lock()
	defer pr.mu.Unlock()

	if pr.currentPos >= pr.fileSize {
		return 0, io.EOF
	}

	// Calculate read size
	remaining := pr.fileSize - pr.currentPos
	readSize := int64(len(p))
	if readSize > remaining {
		readSize = remaining
	}

	// For now, use simple sequential read
	// TODO: Implement parallel reading across stripes
	n, err = pr.file.ReadAt(p[:readSize], pr.currentPos)
	pr.currentPos += int64(n)

	return n, err
}

// ParallelWriter implements parallel writing across Lustre stripes
type ParallelWriter struct {
	file       *os.File
	stripeInfo *LustreStripeInfo
	currentPos int64
	mu         sync.Mutex
}

// NewParallelWriter creates a new parallel writer for striped files
func NewParallelWriter(file *os.File, stripeInfo *LustreStripeInfo) *ParallelWriter {
	return &ParallelWriter{
		file:       file,
		stripeInfo: stripeInfo,
		currentPos: 0,
	}
}

// Write implements io.Writer with parallel stripe writing
func (pw *ParallelWriter) Write(p []byte) (n int, err error) {
	pw.mu.Lock()
	defer pw.mu.Unlock()

	// For now, use simple sequential write
	// TODO: Implement parallel writing across stripes
	n, err = pw.file.WriteAt(p, pw.currentPos)
	pw.currentPos += int64(n)

	return n, err
}

// Advanced parallel I/O implementation

// parallelReadStripes reads data from multiple stripes in parallel
func (l *LustreEnhancedBackend) parallelReadStripes(file *os.File, offset, size int64, stripeInfo *LustreStripeInfo) ([]byte, error) {
	if stripeInfo.StripeCount <= 1 {
		// Not striped, use regular read
		data := make([]byte, size)
		_, err := file.ReadAt(data, offset)
		return data, err
	}

	// Calculate stripe-aligned chunks
	chunks := l.calculateStripeChunks(offset, size, stripeInfo)

	// Read chunks in parallel
	results := make([][]byte, len(chunks))
	errors := make([]error, len(chunks))
	var wg sync.WaitGroup

	// Limit concurrency
	maxConcurrency := runtime.NumCPU()
	if maxConcurrency > stripeInfo.StripeCount {
		maxConcurrency = stripeInfo.StripeCount
	}

	sem := make(chan struct{}, maxConcurrency)

	for i, chunk := range chunks {
		wg.Add(1)
		go func(index int, c StripeChunk) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			data := make([]byte, c.Size)
			_, err := file.ReadAt(data, c.Offset)
			results[index] = data
			errors[index] = err
		}(i, chunk)
	}

	wg.Wait()

	// Check for errors
	for _, err := range errors {
		if err != nil {
			return nil, err
		}
	}

	// Combine results
	totalSize := int64(0)
	for _, chunk := range chunks {
		totalSize += chunk.Size
	}

	result := make([]byte, 0, totalSize)
	for _, data := range results {
		result = append(result, data...)
	}

	return result, nil
}

// StripeChunk represents a chunk of data within a stripe
type StripeChunk struct {
	Offset int64
	Size   int64
	Stripe int
}

// calculateStripeChunks calculates the chunks to read based on striping
func (l *LustreEnhancedBackend) calculateStripeChunks(offset, size int64, stripeInfo *LustreStripeInfo) []StripeChunk {
	var chunks []StripeChunk

	stripeSize := stripeInfo.StripeSize
	stripeCount := int64(stripeInfo.StripeCount)

	currentOffset := offset
	remaining := size

	for remaining > 0 {
		// Calculate which stripe this offset belongs to
		stripeIndex := (currentOffset / stripeSize) % stripeCount

		// Calculate offset within the stripe
		stripeOffset := currentOffset % stripeSize

		// Calculate how much to read from this stripe
		chunkSize := stripeSize - stripeOffset
		if chunkSize > remaining {
			chunkSize = remaining
		}

		chunks = append(chunks, StripeChunk{
			Offset: currentOffset,
			Size:   chunkSize,
			Stripe: int(stripeIndex),
		})

		currentOffset += chunkSize
		remaining -= chunkSize
	}

	return chunks
}

// getLargeFileThreshold returns the threshold for considering a file "large"
func (l *LustreEnhancedBackend) getLargeFileThreshold() int64 {
	// Default to 10MB
	threshold := int64(10 * 1024 * 1024)

	// If stripe size is configured, use 2x stripe size as threshold
	if l.lustreConfig.StripeSize > 0 {
		threshold = l.lustreConfig.StripeSize * 2
	}

	return threshold
}

// Lustre-specific utilities

// GetLustreFileStats returns Lustre-specific file statistics
func (l *LustreEnhancedBackend) GetLustreFileStats(filePath string) (*LustreStripeInfo, error) {
	return l.getFileStripeInfo(filePath)
}

// SetLustreFileStriping sets striping for a specific file
func (l *LustreEnhancedBackend) SetLustreFileStriping(filePath string, stripeInfo *LustreStripeInfo) error {
	// Remove existing file if it exists (Lustre doesn't allow changing striping of existing files)
	if _, err := os.Stat(filePath); err == nil {
		if err := os.Remove(filePath); err != nil {
			return fmt.Errorf("failed to remove existing file: %w", err)
		}
	}

	// Set striping for the directory
	dir := filepath.Dir(filePath)
	return l.ensureDirectoryStriping(dir, stripeInfo)
}

// LustrePoolManager manages Lustre OST pools
type LustrePoolManager struct {
	filesystem string
}

// NewLustrePoolManager creates a new pool manager
func NewLustrePoolManager(filesystem string) *LustrePoolManager {
	return &LustrePoolManager{filesystem: filesystem}
}

// CreatePool creates a new OST pool
func (lpm *LustrePoolManager) CreatePool(poolName string, osts []int) error {
	cmd := exec.Command("lctl", "pool_new", fmt.Sprintf("%s.%s", lpm.filesystem, poolName))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to create pool: %w", err)
	}

	// Add OSTs to pool
	for _, ost := range osts {
		cmd = exec.Command("lctl", "pool_add",
			fmt.Sprintf("%s.%s", lpm.filesystem, poolName),
			fmt.Sprintf("%s-OST%04x", lpm.filesystem, ost))
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to add OST %d to pool: %w", ost, err)
		}
	}

	return nil
}

// DeletePool deletes an OST pool
func (lpm *LustrePoolManager) DeletePool(poolName string) error {
	cmd := exec.Command("lctl", "pool_destroy", fmt.Sprintf("%s.%s", lpm.filesystem, poolName))
	return cmd.Run()
}

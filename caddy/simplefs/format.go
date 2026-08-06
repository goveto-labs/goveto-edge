package simplefs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"sync"

	"github.com/pierrec/lz4/v4"
)

const (
	objectMagic      = "GVCOBJ01"
	objectVersion    = uint16(1)
	objectChunkSize  = 64 << 10
	legacyChunkSize  = 256 << 10
	objectHeaderSize = 28
	objectEntrySize  = 24
	codecRaw         = byte(0)
	codecLZ4         = byte(1)
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

var (
	objectChunkBuffers   = sync.Pool{New: func() any { return make([]byte, objectChunkSize) }}
	objectEncodedBuffers = sync.Pool{New: func() any {
		return make([]byte, lz4.CompressBlockBound(objectChunkSize))
	}}
	legacyChunkBuffers   = sync.Pool{New: func() any { return make([]byte, legacyChunkSize) }}
	legacyEncodedBuffers = sync.Pool{New: func() any {
		return make([]byte, lz4.CompressBlockBound(legacyChunkSize))
	}}
	lz4HashTables = sync.Pool{New: func() any { return make([]int, 1<<16) }}
)

type objectChunk struct {
	codec  byte
	offset uint64
	stored uint32
	raw    uint32
	crc    uint32
}

type objectReader struct {
	source    io.ReaderAt
	chunks    []objectChunk
	length    int64
	chunkSize int64
	pos       int64
	index     int
	data      []byte
	stored    []byte
}

type objectLayout struct {
	chunks    []objectChunk
	length    int64
	chunkSize int64
}

func writeObject(target string, value []byte) (path string, size uint64, err error) {
	return writeObjectReader(target, bytes.NewReader(value), uint64(len(value)))
}

func writeObjectReader(target string, source io.Reader, logicalSize uint64) (path string, size uint64, err error) {
	temporary, err := os.CreateTemp(filepathDir(target), "."+filepathBase(target)+".tmp-")
	if err != nil {
		return "", 0, err
	}
	path = temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(path)
	}
	if err = temporary.Chmod(0o640); err != nil {
		cleanup()
		return "", 0, err
	}
	if logicalSize > uint64(^uint(0)>>1) {
		cleanup()
		return "", 0, errors.New("cache object is too large for this platform")
	}
	count := int((logicalSize + objectChunkSize - 1) / objectChunkSize)
	chunks := make([]objectChunk, count)
	offset := uint64(objectHeaderSize + count*objectEntrySize)
	header := make([]byte, objectHeaderSize+count*objectEntrySize)
	if _, err = temporary.Write(header); err != nil {
		cleanup()
		return "", 0, err
	}
	remaining := logicalSize
	rawBuffer := objectChunkBuffers.Get().([]byte)
	defer objectChunkBuffers.Put(rawBuffer)
	var compressedBuffer []byte
	var hashTable []int
	if logicalSize > objectChunkSize {
		compressedBuffer = objectEncodedBuffers.Get().([]byte)
		hashTable = lz4HashTables.Get().([]int)
		defer objectEncodedBuffers.Put(compressedBuffer)
		defer lz4HashTables.Put(hashTable)
	}
	for index := range count {
		rawSize := int(min(remaining, objectChunkSize))
		raw := rawBuffer[:rawSize]
		if _, err = io.ReadFull(source, raw); err != nil {
			cleanup()
			return "", 0, err
		}
		stored := raw
		codec := codecRaw
		if logicalSize > objectChunkSize {
			compressed := compressedBuffer[:lz4.CompressBlockBound(len(raw))]
			clear(hashTable)
			compressedSize, compressErr := lz4.CompressBlock(raw, compressed, hashTable)
			if compressErr != nil {
				cleanup()
				return "", 0, compressErr
			}
			if compressedSize > 0 && compressedSize*8 <= len(raw)*7 {
				stored = compressed[:compressedSize]
				codec = codecLZ4
			}
		}
		chunks[index] = objectChunk{
			codec: codec, offset: offset, stored: uint32(len(stored)), raw: uint32(len(raw)),
			crc: crc32.Checksum(raw, crcTable),
		}
		if _, err = temporary.Write(stored); err != nil {
			cleanup()
			return "", 0, err
		}
		offset += uint64(len(stored))
		remaining -= uint64(rawSize)
	}
	var extra [1]byte
	if countRead, readErr := source.Read(extra[:]); countRead != 0 || (readErr != nil && !errors.Is(readErr, io.EOF)) {
		cleanup()
		return "", 0, errors.New("cache object source exceeds declared length")
	}
	copy(header, objectMagic)
	binary.BigEndian.PutUint16(header[8:10], objectVersion)
	binary.BigEndian.PutUint32(header[12:16], objectChunkSize)
	binary.BigEndian.PutUint64(header[16:24], logicalSize)
	binary.BigEndian.PutUint32(header[24:28], uint32(count))
	for index, chunk := range chunks {
		entry := header[objectHeaderSize+index*objectEntrySize:]
		entry[0] = chunk.codec
		binary.BigEndian.PutUint64(entry[4:12], chunk.offset)
		binary.BigEndian.PutUint32(entry[12:16], chunk.stored)
		binary.BigEndian.PutUint32(entry[16:20], chunk.raw)
		binary.BigEndian.PutUint32(entry[20:24], chunk.crc)
	}
	if _, err = temporary.WriteAt(header, 0); err != nil {
		cleanup()
		return "", 0, err
	}
	info, err := temporary.Stat()
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
		return "", 0, err
	}
	return path, uint64(info.Size()), nil
}

func newObjectReader(source io.ReaderAt, encodedSize uint64) (*objectReader, error) {
	layout, err := readObjectLayout(source, encodedSize)
	if err != nil {
		return nil, err
	}
	return newObjectReaderFromLayout(source, layout), nil
}

func readObjectLayout(source io.ReaderAt, encodedSize uint64) (*objectLayout, error) {
	header := make([]byte, objectHeaderSize)
	if _, err := source.ReadAt(header, 0); err != nil {
		return nil, err
	}
	chunkSize := binary.BigEndian.Uint32(header[12:16])
	if string(header[:8]) != objectMagic || binary.BigEndian.Uint16(header[8:10]) != objectVersion ||
		(chunkSize != objectChunkSize && chunkSize != legacyChunkSize) {
		return nil, errors.New("unsupported cache object format")
	}
	length := binary.BigEndian.Uint64(header[16:24])
	count := binary.BigEndian.Uint32(header[24:28])
	expectedCount := uint64(0)
	if length > 0 {
		expectedCount = (length-1)/uint64(chunkSize) + 1
	}
	if expectedCount > uint64(^uint32(0)) || uint64(count) != expectedCount || length > uint64(^uint64(0)>>1) {
		return nil, errors.New("invalid cache object chunk count")
	}
	tableBytes := uint64(count) * objectEntrySize
	if tableBytes > encodedSize || objectHeaderSize+tableBytes > encodedSize {
		return nil, errors.New("cache object table exceeds file")
	}
	table := make([]byte, tableBytes)
	if _, err := source.ReadAt(table, objectHeaderSize); err != nil {
		return nil, err
	}
	chunks := make([]objectChunk, count)
	expectedOffset := uint64(objectHeaderSize) + tableBytes
	var rawTotal uint64
	for index := range count {
		entry := table[index*objectEntrySize:]
		chunk := objectChunk{
			codec: entry[0], offset: binary.BigEndian.Uint64(entry[4:12]), stored: binary.BigEndian.Uint32(entry[12:16]),
			raw: binary.BigEndian.Uint32(entry[16:20]), crc: binary.BigEndian.Uint32(entry[20:24]),
		}
		expectedRaw := uint32(min(uint64(chunkSize), length-rawTotal))
		if (chunk.codec != codecRaw && chunk.codec != codecLZ4) || chunk.offset != expectedOffset || chunk.raw != expectedRaw ||
			uint64(chunk.stored) > encodedSize-expectedOffset {
			return nil, errors.New("invalid cache object chunk table")
		}
		if chunk.codec == codecRaw && chunk.stored != chunk.raw {
			return nil, errors.New("invalid raw cache chunk")
		}
		if chunk.codec == codecLZ4 && (chunk.stored == 0 || int(chunk.stored) > lz4.CompressBlockBound(int(chunkSize))) {
			return nil, errors.New("invalid compressed cache chunk")
		}
		chunks[index] = chunk
		expectedOffset += uint64(chunk.stored)
		rawTotal += uint64(chunk.raw)
	}
	if expectedOffset != encodedSize || rawTotal != length {
		return nil, errors.New("cache object lengths do not match")
	}
	return &objectLayout{chunks: chunks, length: int64(length), chunkSize: int64(chunkSize)}, nil
}

func newObjectReaderFromLayout(source io.ReaderAt, layout *objectLayout) *objectReader {
	return &objectReader{
		source: source, chunks: layout.chunks, length: layout.length, chunkSize: layout.chunkSize, index: -1,
	}
}

func (r *objectReader) Read(target []byte) (int, error) {
	if r.pos >= r.length {
		return 0, io.EOF
	}
	written := 0
	for len(target) > 0 && r.pos < r.length {
		chunkIndex := int(r.pos / r.chunkSize)
		if r.index != chunkIndex {
			if err := r.load(chunkIndex); err != nil {
				return written, err
			}
		}
		inside := int(r.pos % r.chunkSize)
		count := copy(target, r.data[inside:])
		target = target[count:]
		r.pos += int64(count)
		written += count
	}
	return written, nil
}

func (r *objectReader) Seek(offset int64, whence int) (int64, error) {
	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = r.pos + offset
	case io.SeekEnd:
		target = r.length + offset
	default:
		return 0, errors.New("invalid seek origin")
	}
	if target < 0 || target > r.length {
		return 0, errors.New("cache object seek out of bounds")
	}
	r.pos = target
	return target, nil
}

func (r *objectReader) load(index int) error {
	chunk := r.chunks[index]
	if chunk.codec == codecRaw {
		if r.data == nil {
			r.data = acquireChunkBuffer(r.chunkSize)
		}
		r.data = r.data[:chunk.raw]
		if _, err := r.source.ReadAt(r.data, int64(chunk.offset)); err != nil {
			return err
		}
	} else {
		if r.data == nil {
			r.data = acquireChunkBuffer(r.chunkSize)
		}
		if r.stored == nil {
			r.stored = acquireEncodedBuffer(r.chunkSize)
		}
		r.data = r.data[:chunk.raw]
		stored := r.stored[:chunk.stored]
		if _, err := r.source.ReadAt(stored, int64(chunk.offset)); err != nil {
			return err
		}
		decoded, err := lz4.UncompressBlock(stored, r.data)
		if err != nil || decoded != int(chunk.raw) {
			return errors.New("corrupt compressed cache chunk")
		}
	}
	if crc32.Checksum(r.data, crcTable) != chunk.crc {
		return errors.New("cache chunk checksum mismatch")
	}
	r.index = index
	return nil
}

func (r *objectReader) release() {
	if r.data != nil {
		releaseChunkBuffer(r.chunkSize, r.data)
		r.data = nil
	}
	if r.stored != nil {
		releaseEncodedBuffer(r.chunkSize, r.stored)
		r.stored = nil
	}
}

func acquireChunkBuffer(size int64) []byte {
	if size == legacyChunkSize {
		return legacyChunkBuffers.Get().([]byte)
	}
	return objectChunkBuffers.Get().([]byte)
}

func releaseChunkBuffer(size int64, buffer []byte) {
	if size == legacyChunkSize {
		legacyChunkBuffers.Put(buffer[:legacyChunkSize])
		return
	}
	objectChunkBuffers.Put(buffer[:objectChunkSize])
}

func acquireEncodedBuffer(size int64) []byte {
	if size == legacyChunkSize {
		return legacyEncodedBuffers.Get().([]byte)
	}
	return objectEncodedBuffers.Get().([]byte)
}

func releaseEncodedBuffer(size int64, buffer []byte) {
	if size == legacyChunkSize {
		legacyEncodedBuffers.Put(buffer[:lz4.CompressBlockBound(legacyChunkSize)])
		return
	}
	objectEncodedBuffers.Put(buffer[:lz4.CompressBlockBound(objectChunkSize)])
}

// Kept local to avoid importing filepath on the hot reader path.
func filepathDir(path string) string {
	for index := len(path) - 1; index >= 0; index-- {
		if os.IsPathSeparator(path[index]) {
			return path[:index]
		}
	}
	return "."
}

func filepathBase(path string) string {
	for index := len(path) - 1; index >= 0; index-- {
		if os.IsPathSeparator(path[index]) {
			return path[index+1:]
		}
	}
	return path
}

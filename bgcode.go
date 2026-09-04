package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"math"
	"strconv"
	"strings"
)

const (
	bgCodeVersion uint32 = 1

	bgCodeChecksumNone  uint16 = 0
	bgCodeChecksumCRC32 uint16 = 1

	bgCodeBlockFileMetadata    uint16 = 0
	bgCodeBlockGCode           uint16 = 1
	bgCodeBlockSlicerMetadata  uint16 = 2
	bgCodeBlockPrinterMetadata uint16 = 3
	bgCodeBlockPrintMetadata   uint16 = 4
	bgCodeBlockThumbnail       uint16 = 5

	bgCodeCompressionNone          uint16 = 0
	bgCodeCompressionDeflate       uint16 = 1
	bgCodeCompressionHeatshrink114 uint16 = 2
	bgCodeCompressionHeatshrink124 uint16 = 3

	maxBGCodeMetadataBytes           = 1 << 20
	maxBGCodeCompressedMetadataBytes = 1 << 20
	maxBGCodeBlockBytes              = 64 << 20
	maxBGCodeScanBytes               = 128 << 20
	maxBGCodeBlocks                  = 256
	maxBGCodeTools                   = 64
)

type bgCodeParser struct {
	reader    io.Reader
	bytesRead uint64
}

type bgCodeBlockHeader struct {
	blockType        uint16
	compression      uint16
	uncompressedSize uint32
	compressedSize   uint32
	raw              []byte
}

// ParseBGCodeFilamentUsage extracts positive per-tool gram values from the
// Print Metadata block of a BGCode v1 stream.
func ParseBGCodeFilamentUsage(reader io.Reader) (map[int]float64, error) {
	if reader == nil {
		return nil, fmt.Errorf("BGCode reader is nil")
	}

	parser := &bgCodeParser{reader: reader}
	checksumType, err := parser.readFileHeader()
	if err != nil {
		return nil, err
	}

	for blockNumber := 0; blockNumber < maxBGCodeBlocks; blockNumber++ {
		header, err := parser.readBlockHeader()
		if err == io.EOF {
			return nil, fmt.Errorf("BGCode print metadata block not found")
		}
		if err != nil {
			return nil, err
		}
		if err := validateBGCodeBlockSize(header); err != nil {
			return nil, fmt.Errorf("BGCode block %d: %w", blockNumber, err)
		}

		isPrintMetadata := header.blockType == bgCodeBlockPrintMetadata
		if isPrintMetadata {
			if header.uncompressedSize > maxBGCodeMetadataBytes {
				return nil, fmt.Errorf("BGCode print metadata uncompressed size %d exceeds limit %d", header.uncompressedSize, maxBGCodeMetadataBytes)
			}
			if header.compression != bgCodeCompressionNone && header.compressedSize > maxBGCodeCompressedMetadataBytes {
				return nil, fmt.Errorf("BGCode print metadata compressed size %d exceeds limit %d", header.compressedSize, maxBGCodeCompressedMetadataBytes)
			}
			if header.compression != bgCodeCompressionNone && header.compression != bgCodeCompressionDeflate {
				return nil, fmt.Errorf("unsupported print metadata compression %d", header.compression)
			}
		}

		parameters, data, err := parser.readBlock(header, checksumType, isPrintMetadata)
		if err != nil {
			return nil, fmt.Errorf("BGCode block %d: %w", blockNumber, err)
		}
		if !isPrintMetadata {
			continue
		}

		if binary.LittleEndian.Uint16(parameters) != 0 {
			return nil, fmt.Errorf("unsupported BGCode print metadata encoding %d", binary.LittleEndian.Uint16(parameters))
		}

		metadata, err := decodeBGCodePrintMetadata(header, data)
		if err != nil {
			return nil, err
		}
		return parseBGCodeFilamentGrams(metadata)
	}

	return nil, fmt.Errorf("BGCode block limit %d exceeded before print metadata", maxBGCodeBlocks)
}

func (p *bgCodeParser) readFileHeader() (uint16, error) {
	header := make([]byte, 10)
	if err := p.readFull(header); err != nil {
		return 0, fmt.Errorf("truncated BGCode file header: %w", err)
	}
	if string(header[:4]) != "GCDE" {
		return 0, fmt.Errorf("invalid BGCode magic %q", header[:4])
	}
	if version := binary.LittleEndian.Uint32(header[4:8]); version != bgCodeVersion {
		return 0, fmt.Errorf("unsupported BGCode version %d", version)
	}
	checksumType := binary.LittleEndian.Uint16(header[8:10])
	if checksumType != bgCodeChecksumNone && checksumType != bgCodeChecksumCRC32 {
		return 0, fmt.Errorf("unsupported BGCode checksum type %d", checksumType)
	}
	return checksumType, nil
}

func (p *bgCodeParser) readBlockHeader() (bgCodeBlockHeader, error) {
	base := make([]byte, 8)
	if err := p.readFull(base); err != nil {
		if err == io.EOF {
			return bgCodeBlockHeader{}, io.EOF
		}
		return bgCodeBlockHeader{}, fmt.Errorf("truncated BGCode block header: %w", err)
	}

	header := bgCodeBlockHeader{
		blockType:        binary.LittleEndian.Uint16(base[0:2]),
		compression:      binary.LittleEndian.Uint16(base[2:4]),
		uncompressedSize: binary.LittleEndian.Uint32(base[4:8]),
		raw:              base,
	}
	if header.blockType > bgCodeBlockThumbnail {
		return bgCodeBlockHeader{}, fmt.Errorf("invalid BGCode block type %d", header.blockType)
	}
	if header.compression > bgCodeCompressionHeatshrink124 {
		return bgCodeBlockHeader{}, fmt.Errorf("invalid BGCode compression %d", header.compression)
	}
	if header.compression != bgCodeCompressionNone {
		compressedSize := make([]byte, 4)
		if err := p.readFull(compressedSize); err != nil {
			return bgCodeBlockHeader{}, fmt.Errorf("truncated BGCode compressed block header: %w", err)
		}
		header.compressedSize = binary.LittleEndian.Uint32(compressedSize)
		header.raw = append(header.raw, compressedSize...)
	}
	return header, nil
}

func validateBGCodeBlockSize(header bgCodeBlockHeader) error {
	if header.uncompressedSize > maxBGCodeBlockBytes {
		return fmt.Errorf("uncompressed size %d exceeds limit %d", header.uncompressedSize, maxBGCodeBlockBytes)
	}
	if header.compression != bgCodeCompressionNone && header.compressedSize > maxBGCodeBlockBytes {
		return fmt.Errorf("compressed size %d exceeds limit %d", header.compressedSize, maxBGCodeBlockBytes)
	}
	return nil
}

func (p *bgCodeParser) readBlock(header bgCodeBlockHeader, checksumType uint16, retainData bool) ([]byte, []byte, error) {
	parameterSize := bgCodeBlockParameterSize(header.blockType)
	parameters := make([]byte, parameterSize)
	checksum := crc32.NewIEEE()
	_, _ = checksum.Write(header.raw)
	if err := p.readAndHash(parameters, checksum); err != nil {
		return nil, nil, fmt.Errorf("truncated parameters: %w", err)
	}

	dataSize := header.uncompressedSize
	if header.compression != bgCodeCompressionNone {
		dataSize = header.compressedSize
	}
	var data []byte
	if retainData {
		data = make([]byte, int(dataSize))
		if err := p.readAndHash(data, checksum); err != nil {
			return nil, nil, fmt.Errorf("truncated data: %w", err)
		}
	} else if err := p.discardAndHash(uint64(dataSize), checksum); err != nil {
		return nil, nil, fmt.Errorf("truncated data: %w", err)
	}

	if checksumType == bgCodeChecksumCRC32 {
		storedChecksum := make([]byte, 4)
		if err := p.readFull(storedChecksum); err != nil {
			return nil, nil, fmt.Errorf("truncated checksum: %w", err)
		}
		got := binary.LittleEndian.Uint32(storedChecksum)
		want := checksum.Sum32()
		if got != want {
			return nil, nil, fmt.Errorf("checksum mismatch: got %08x, want %08x", got, want)
		}
	}
	return parameters, data, nil
}

func (p *bgCodeParser) readAndHash(destination []byte, checksum hash.Hash32) error {
	if err := p.readFull(destination); err != nil {
		return err
	}
	_, _ = checksum.Write(destination)
	return nil
}

func (p *bgCodeParser) discardAndHash(size uint64, checksum hash.Hash32) error {
	buffer := make([]byte, 32<<10)
	for size > 0 {
		chunkSize := uint64(len(buffer))
		if size < chunkSize {
			chunkSize = size
		}
		chunk := buffer[:int(chunkSize)]
		if err := p.readAndHash(chunk, checksum); err != nil {
			return err
		}
		size -= chunkSize
	}
	return nil
}

func (p *bgCodeParser) readFull(destination []byte) error {
	if p.bytesRead+uint64(len(destination)) > maxBGCodeScanBytes {
		return fmt.Errorf("BGCode scan size exceeds limit %d", maxBGCodeScanBytes)
	}
	read, err := io.ReadFull(p.reader, destination)
	p.bytesRead += uint64(read)
	return err
}

func bgCodeBlockParameterSize(blockType uint16) int {
	if blockType == bgCodeBlockThumbnail {
		return 6
	}
	return 2
}

func decodeBGCodePrintMetadata(header bgCodeBlockHeader, data []byte) ([]byte, error) {
	if header.compression == bgCodeCompressionNone {
		return data, nil
	}

	compressed := bytes.NewReader(data)
	reader, err := zlib.NewReader(compressed)
	if err != nil {
		return nil, fmt.Errorf("invalid BGCode print metadata Deflate stream: %w", err)
	}
	limited := io.LimitReader(reader, maxBGCodeMetadataBytes+1)
	metadata, readErr := io.ReadAll(limited)
	closeErr := reader.Close()
	if readErr != nil {
		return nil, fmt.Errorf("decompress BGCode print metadata: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("finish BGCode print metadata decompression: %w", closeErr)
	}
	if len(metadata) > maxBGCodeMetadataBytes {
		return nil, fmt.Errorf("BGCode print metadata exceeds decompressed limit %d", maxBGCodeMetadataBytes)
	}
	if uint32(len(metadata)) != header.uncompressedSize {
		return nil, fmt.Errorf("BGCode print metadata decompressed size %d does not match declared size %d", len(metadata), header.uncompressedSize)
	}
	return metadata, nil
}

func parseBGCodeFilamentGrams(metadata []byte) (map[int]float64, error) {
	for _, line := range strings.Split(string(metadata), "\n") {
		key, value, found := strings.Cut(strings.TrimSuffix(line, "\r"), "=")
		if !found || strings.TrimSpace(key) != "filament used [g]" {
			continue
		}

		weights := strings.Split(value, ",")
		if len(weights) > maxBGCodeTools {
			return nil, fmt.Errorf("BGCode filament usage contains %d tools, limit is %d", len(weights), maxBGCodeTools)
		}
		usage := make(map[int]float64, len(weights))
		for tool, rawWeight := range weights {
			weight, err := strconv.ParseFloat(strings.TrimSpace(rawWeight), 64)
			if err != nil || math.IsNaN(weight) || math.IsInf(weight, 0) || weight < 0 {
				return nil, fmt.Errorf("invalid BGCode filament usage for tool %d: %q", tool, strings.TrimSpace(rawWeight))
			}
			if weight > 0 {
				usage[tool] = weight
			}
		}
		if len(usage) == 0 {
			return nil, nil
		}
		return usage, nil
	}
	return nil, nil
}

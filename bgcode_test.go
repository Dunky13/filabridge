package main

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
	"os"
	"strings"
	"testing"
)

func TestParseBGCodeFilamentUsageReadsOfficialAlpha11LibBGCodeFixture(t *testing.T) {
	fixture, err := os.ReadFile("testdata/bgcode/official-alpha11-source.bgcode")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	usage, err := ParseBGCodeFilamentUsage(bytes.NewReader(fixture))
	if err != nil {
		t.Fatalf("ParseBGCodeFilamentUsage() error = %v", err)
	}
	if len(usage) != 2 || usage[0] != 1.25 || usage[2] != 2.75 {
		t.Fatalf("usage = %#v, want map[0:1.25 2:2.75]", usage)
	}
}

func TestParseBGCodeFilamentUsageReadsDeflatedPrintMetadata(t *testing.T) {
	fixture := buildBGCodeFixture(t, bgCodeFixtureOptions{
		metadata:    "filament used [mm]=416.5, 0, 92.1\nfilament used [g]=1.25, 0, 2.75\n",
		compression: bgCodeCompressionDeflate,
		checksum:    bgCodeChecksumCRC32,
	})

	usage, err := ParseBGCodeFilamentUsage(bytes.NewReader(fixture))
	if err != nil {
		t.Fatalf("ParseBGCodeFilamentUsage() error = %v", err)
	}
	if len(usage) != 2 {
		t.Fatalf("usage = %#v, want two used tools", usage)
	}
	if usage[0] != 1.25 || usage[2] != 2.75 {
		t.Fatalf("usage = %#v, want map[0:1.25 2:2.75]", usage)
	}
}

func TestParseBGCodeFilamentUsageReadsUncompressedMetadataWithoutChecksums(t *testing.T) {
	fixture := buildBGCodeFixture(t, bgCodeFixtureOptions{
		metadata:    "filament used [g]=3.5\n",
		compression: bgCodeCompressionNone,
		checksum:    bgCodeChecksumNone,
	})

	usage, err := ParseBGCodeFilamentUsage(bytes.NewReader(fixture))
	if err != nil {
		t.Fatalf("ParseBGCodeFilamentUsage() error = %v", err)
	}
	if usage[0] != 3.5 {
		t.Fatalf("usage = %#v, want map[0:3.5]", usage)
	}
}

func TestParseBGCodeFilamentUsagePreservesINDXEightToolIndexes(t *testing.T) {
	fixture := buildBGCodeFixture(t, bgCodeFixtureOptions{
		metadata:    "filament used [g]=1, 0, 2, 0, 3, 0, 4, 8\n",
		compression: bgCodeCompressionDeflate,
		checksum:    bgCodeChecksumCRC32,
	})

	usage, err := ParseBGCodeFilamentUsage(bytes.NewReader(fixture))
	if err != nil {
		t.Fatalf("ParseBGCodeFilamentUsage() error = %v", err)
	}
	if len(usage) != 5 || usage[0] != 1 || usage[2] != 2 || usage[4] != 3 || usage[6] != 4 || usage[7] != 8 {
		t.Fatalf("usage = %#v, want populated INDX indexes 0, 2, 4, 6, and 7", usage)
	}
}

func TestParseBGCodeFilamentUsageRejectsInvalidFileHeader(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func([]byte)
		wantErr string
	}{
		{
			name: "magic",
			mutate: func(data []byte) {
				copy(data[:4], "NOPE")
			},
			wantErr: "magic",
		},
		{
			name: "version",
			mutate: func(data []byte) {
				binary.LittleEndian.PutUint32(data[4:8], bgCodeVersion+1)
			},
			wantErr: "version",
		},
		{
			name: "checksum type",
			mutate: func(data []byte) {
				binary.LittleEndian.PutUint16(data[8:10], 99)
			},
			wantErr: "checksum type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := buildBGCodeFixture(t, bgCodeFixtureOptions{
				metadata:    "filament used [g]=1\n",
				compression: bgCodeCompressionDeflate,
				checksum:    bgCodeChecksumCRC32,
			})
			tt.mutate(fixture)

			_, err := ParseBGCodeFilamentUsage(bytes.NewReader(fixture))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseBGCodeFilamentUsageValidatesSkippedBlockChecksum(t *testing.T) {
	fixture := buildBGCodeFixture(t, bgCodeFixtureOptions{
		metadata:    "filament used [g]=1\n",
		compression: bgCodeCompressionDeflate,
		checksum:    bgCodeChecksumCRC32,
	})
	fixture[20] ^= 0xff

	_, err := ParseBGCodeFilamentUsage(bytes.NewReader(fixture))
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("error = %v, want checksum failure", err)
	}
}

func TestParseBGCodeFilamentUsageValidatesPrintMetadataChecksum(t *testing.T) {
	fixture := buildBGCodeFixture(t, bgCodeFixtureOptions{
		metadata:    "filament used [g]=1\n",
		compression: bgCodeCompressionDeflate,
		checksum:    bgCodeChecksumCRC32,
	})
	fixture[len(fixture)-1] ^= 0xff

	_, err := ParseBGCodeFilamentUsage(bytes.NewReader(fixture))
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("error = %v, want checksum failure", err)
	}
}

func TestParseBGCodeFilamentUsageRejectsOversizedMetadataBeforeAllocating(t *testing.T) {
	tests := []struct {
		name             string
		uncompressedSize uint32
		compressedSize   uint32
		wantErr          string
	}{
		{
			name:             "uncompressed",
			uncompressedSize: uint32(maxBGCodeMetadataBytes + 1),
			compressedSize:   1,
			wantErr:          "uncompressed size",
		},
		{
			name:             "compressed",
			uncompressedSize: 1,
			compressedSize:   uint32(maxBGCodeCompressedMetadataBytes + 1),
			wantErr:          "compressed size",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := bgCodeHeader(bgCodeChecksumNone)
			fixture = append(fixture, rawBGCodeBlockHeader(
				bgCodeBlockPrintMetadata,
				bgCodeCompressionDeflate,
				tt.uncompressedSize,
				tt.compressedSize,
			)...)

			_, err := ParseBGCodeFilamentUsage(bytes.NewReader(fixture))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseBGCodeFilamentUsageRejectsInvalidBlocks(t *testing.T) {
	tests := []struct {
		name        string
		blockType   uint16
		compression uint16
		wantErr     string
	}{
		{name: "block type", blockType: 99, compression: bgCodeCompressionNone, wantErr: "block type"},
		{name: "compression", blockType: bgCodeBlockPrintMetadata, compression: 99, wantErr: "compression"},
		{name: "unsupported metadata compression", blockType: bgCodeBlockPrintMetadata, compression: bgCodeCompressionHeatshrink114, wantErr: "unsupported print metadata compression"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := bgCodeHeader(bgCodeChecksumNone)
			fixture = append(fixture, rawBGCodeBlockHeader(tt.blockType, tt.compression, 0, 0)...)

			_, err := ParseBGCodeFilamentUsage(bytes.NewReader(fixture))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseBGCodeFilamentUsageRejectsTruncatedBlock(t *testing.T) {
	fixture := bgCodeHeader(bgCodeChecksumNone)
	fixture = append(fixture, rawBGCodeBlockHeader(bgCodeBlockPrintMetadata, bgCodeCompressionNone, 20, 0)...)
	fixture = append(fixture, 0, 0, 'x')

	_, err := ParseBGCodeFilamentUsage(bytes.NewReader(fixture))
	if err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("error = %v, want truncated block failure", err)
	}
}

func TestParseBGCodeFilamentUsageRejectsDeflateSizeMismatch(t *testing.T) {
	fixture := buildBGCodeFixture(t, bgCodeFixtureOptions{
		metadata:         "filament used [g]=1\n",
		compression:      bgCodeCompressionDeflate,
		checksum:         bgCodeChecksumCRC32,
		uncompressedSize: 1,
	})

	_, err := ParseBGCodeFilamentUsage(bytes.NewReader(fixture))
	if err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("error = %v, want decompressed size failure", err)
	}
}

func TestParseBGCodeFilamentUsageStopsAfterBoundedNumberOfBlocks(t *testing.T) {
	fixture := bgCodeHeader(bgCodeChecksumNone)
	for range maxBGCodeBlocks + 1 {
		fixture = append(fixture, buildBGCodeBlock(t, bgCodeBlockThumbnail, bgCodeCompressionNone, make([]byte, 6), nil, bgCodeChecksumNone, 0)...)
	}

	_, err := ParseBGCodeFilamentUsage(bytes.NewReader(fixture))
	if err == nil || !strings.Contains(err.Error(), "block limit") {
		t.Fatalf("error = %v, want block limit failure", err)
	}
}

type bgCodeFixtureOptions struct {
	metadata         string
	compression      uint16
	checksum         uint16
	uncompressedSize uint32
}

func buildBGCodeFixture(t *testing.T, options bgCodeFixtureOptions) []byte {
	t.Helper()

	fixture := bgCodeHeader(options.checksum)
	fixture = append(fixture, buildBGCodeBlock(
		t,
		bgCodeBlockPrinterMetadata,
		bgCodeCompressionNone,
		[]byte{0, 0},
		[]byte("printer_model=COREONE\n"),
		options.checksum,
		0,
	)...)

	uncompressedSize := options.uncompressedSize
	if uncompressedSize == 0 {
		uncompressedSize = uint32(len(options.metadata))
	}
	fixture = append(fixture, buildBGCodeBlock(
		t,
		bgCodeBlockPrintMetadata,
		options.compression,
		[]byte{0, 0},
		[]byte(options.metadata),
		options.checksum,
		uncompressedSize,
	)...)
	return fixture
}

func bgCodeHeader(checksum uint16) []byte {
	header := make([]byte, 10)
	copy(header[:4], "GCDE")
	binary.LittleEndian.PutUint32(header[4:8], bgCodeVersion)
	binary.LittleEndian.PutUint16(header[8:10], checksum)
	return header
}

func buildBGCodeBlock(
	t *testing.T,
	blockType uint16,
	compression uint16,
	parameters []byte,
	data []byte,
	checksumType uint16,
	uncompressedSize uint32,
) []byte {
	t.Helper()

	if uncompressedSize == 0 {
		uncompressedSize = uint32(len(data))
	}
	payloadData := data
	if compression == bgCodeCompressionDeflate {
		var compressed bytes.Buffer
		writer := zlib.NewWriter(&compressed)
		if _, err := writer.Write(data); err != nil {
			t.Fatalf("compress fixture: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("close fixture compressor: %v", err)
		}
		payloadData = compressed.Bytes()
	}

	block := rawBGCodeBlockHeader(blockType, compression, uncompressedSize, uint32(len(payloadData)))
	block = append(block, parameters...)
	block = append(block, payloadData...)
	if checksumType == bgCodeChecksumCRC32 {
		checksum := make([]byte, 4)
		binary.LittleEndian.PutUint32(checksum, crc32.ChecksumIEEE(block))
		block = append(block, checksum...)
	}
	return block
}

func rawBGCodeBlockHeader(blockType uint16, compression uint16, uncompressedSize uint32, compressedSize uint32) []byte {
	headerSize := 8
	if compression != bgCodeCompressionNone {
		headerSize = 12
	}
	header := make([]byte, headerSize)
	binary.LittleEndian.PutUint16(header[0:2], blockType)
	binary.LittleEndian.PutUint16(header[2:4], compression)
	binary.LittleEndian.PutUint32(header[4:8], uncompressedSize)
	if compression != bgCodeCompressionNone {
		binary.LittleEndian.PutUint32(header[8:12], compressedSize)
	}
	return header
}

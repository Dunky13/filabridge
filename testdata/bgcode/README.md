# BGCode fixture provenance

`official-alpha11-source.gcode` is the minimal input for the compatibility fixture.

Generate it with the exact LibBGCode revision pinned by PrusaSlicer
`version_3.0.0-alpha11` (`d4da9073616d70a43c151e8c1d7fbff879d2e08a`):

```sh
bgcode official-alpha11-source.gcode \
  --checksum=1 \
  --file_metadata_compression=0 \
  --printer_metadata_compression=0 \
  --print_metadata_compression=1 \
  --slicer_metadata_compression=1 \
  --gcode_compression=3 \
  --gcode_encoding=2 \
  --metadata_encoding=0
```

The generated binary uses format version 1, CRC32 checksums, and zlib-wrapped
Deflate for its Print Metadata block. Tests also construct malformed variants
directly from the public format specification so each rejection path is explicit.

Generated fixture SHA-256:
`851746f1149cbe4c7e38723cd61157537fdae7522cb4ee0b55a2e762efd73c54`.

# PrusaSlicer 3.0.0-alpha11 fixture provenance

The ASCII files were generated on macOS from the official
`PrusaSlicer-3.0.0-alpha11.dmg` release asset and are complete slicer outputs,
not reduced parser fixtures. The BGCode file is a separately attributed
libbgcode conversion of the single-tool ASCII output; it is not claimed as a
PrusaSlicer-produced BGCode file.

- PrusaSlicer tag: `version_3.0.0-alpha11`
- PrusaSlicer commit: `6f510128d7c2e543b62919b74bea7e876f564205`
- DMG SHA-256: `eafa05fdea9b87f2c1b8d7b80f767b85c1e584dc840008aa5b4a24f9963f205a`
- Source STL SHA-256: `2c02f1ccc1effb42d27e9b17919c71994850db88efb9856ae12dfda5e0efece9`
- libbgcode commit pinned by alpha11: `d4da9073616d70a43c151e8c1d7fbff879d2e08a`

The single-tool ASCII file used the `Prusa CORE One 0.4 HF` printer,
`0.20mm STRUCTURAL @COREONE 0.4HF` print, `no tool` ToolPrint, and
`Prusament PLA @COREONE HF0.4@COREONE 0.4` material profiles.

The eight-tool ASCII file used the
`Prusa CORE One INDX 8T 0.4 HF, 0.4 HF, 0.4 HF, 0.4 HF, 0.4 HF, 0.4 HF, 0.4 HF, 0.4 HF`
printer, `0.20mm` print, `0.20mm Balanced ★ @COREONEINDX 0.4HF` ToolPrint,
and `Prusament PLA @COREONEINDX HF0.4@COREONEINDX 0.4` material profiles.
The resulting metadata identifies `COREONE_INDX8T` and contains eight tool
slots (`3.74, 0, 0, 0, 0, 0, 0, 0` grams). This proves the official profile's
eight-position metadata cardinality, but it does not prove multi-tool
consumption because only tool 0 was assigned to the model.

Alpha11's CLI action currently writes ASCII G-code even when `--output` ends
in `.bgcode`. The binary file was therefore encoded from that exact alpha11
ASCII output using the libbgcode revision pinned in alpha11, with CRC32,
Deflate slicer metadata, Heatshrink 12/4 G-code compression, and MeatPack
comment-preserving encoding. It was then decoded and compared successfully.

Checksums, exact artifact paths, producer lineage, full logical-tool vectors,
and used-tool counts are enforced by `../golden-fixtures.json`. Firmware
captures and physical-printer attestations remain separate per-family and
per-model release requirements and must not be inferred from these files.

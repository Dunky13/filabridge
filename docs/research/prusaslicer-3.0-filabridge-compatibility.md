# PrusaSlicer 3.0 and FilaBridge compatibility

Research date: 2026-09-04  
PrusaSlicer baseline: `version_3.0.0-alpha11` (`6f510128d7c2e543b62919b74bea7e876f564205`)  
Scope: official Prusa and Spoolman repositories, releases, specifications, and issue trackers. No third-party summaries were used.

## Executive verdict

PrusaSlicer 3.0 does **not** replace FilaBridge or Spoolman. The alpha adds a new UI, new configuration/profile architecture, multi-bed and multi-project workflows, Prusa Connect onboarding, and an experimental Lua plugin system. It does not add a durable physical-spool inventory equivalent to Spoolman, nor a supported accounting integration that maps a physical spool on each tool to completed PrusaLink jobs.

The phrase “complete rewrite” needs one qualification: Prusa says the UI was rewritten from scratch and the internal architecture was redesigned, but it also says the slicing backend was retained and substantially changed. It is therefore a major application rewrite, not a replacement of every slicing/output contract.

FilaBridge's architectural boundary remains sound: observe the printer through PrusaLink, determine filament consumed per tool, and update the explicitly mapped Spoolman spools. The main work is protocol hardening, not a PrusaSlicer plugin rewrite:

1. Add real BGCode metadata decoding instead of treating `.bgcode` as plain text.
2. Negotiate PrusaLink capabilities, Digest/API-key authentication, HTTP/HTTPS, and certificate settings.
3. Add fixture-based compatibility tests against PrusaSlicer 2.9.6 and each 3.0 pre-release plus supported firmware families.
4. Make job completion/reconciliation persistent and idempotent across restarts and expanded firmware states.
5. Prepare an optional OpenPrintTag-to-Spoolman synchronization layer with one explicit consumption authority, so tagged filament is never decremented twice.

## What is confirmed to have changed in PrusaSlicer 3.0

The [official alpha11 release notes](https://github.com/prusa3d/PrusaSlicer/releases/tag/version_3.0.0-alpha11) confirm the following:

| Change | Compatibility significance for FilaBridge |
|---|---|
| UI rewritten; internal architecture and profile system redesigned | No direct dependency. FilaBridge is a separate service and does not automate the slicer UI. |
| Multiple beds with distinct printer/configuration choices, plus multiple projects in tabs | A user can prepare jobs for different physical printers in one session. FilaBridge must continue to treat the printer/job API as authoritative rather than assuming one slicer session or profile. |
| Hardware-aware printer profiles, freely selectable nozzle/HF parameters, and a new per-tool `ToolPrint` profile layer | Filament settings are more flexible, but these profiles still describe printing configuration, not uniquely identified physical spools. Per-tool accounting remains necessary. |
| Prusa Connect onboarding and automatic selection of a matching printer profile | This improves slicer-to-printer selection; it does not expose a physical-spool ledger to FilaBridge. |
| Lua-based calibration plugins with an experimental, explicitly unstable plugin API | Do not make the 3.0 migration depend on a Lua plugin. Network permissions, signing, and distribution are still evolving. |
| CLI parser/evaluation refactored, with atypical scripts potentially breaking | Relevant only if FilaBridge later invokes PrusaSlicer as a CLI. It does not today. |
| 2.x 3MF projects are imported; opening a 3.x project in 2.x retains geometry but loses configuration | No direct impact: FilaBridge does not parse 3MF. This is useful only for test-fixture provenance and user support. |
| GCode/BGCode selection moved to the Save dialog; colour visualization moved from presets to the project | Users can switch output format more readily, increasing the importance of handling both formats correctly. Project colours must not be interpreted as spool identities. |
| `filament_load_time` and `filament_unload_time` replaced by printer-level `filament_change_time` | No consumption-accounting change. FilaBridge should not infer spool use from these timing settings. |
| Post-processing scripts, the custom G-code editor, SLA, third-party profiles, and several other features are unavailable in this alpha | A post-processing-script migration path cannot currently be the foundation of FilaBridge 3.0 support. |

The release is explicitly unstable and incomplete relative to 2.9.6. Compatibility should therefore be gated per alpha/beta/RC rather than declared once for “3.0”.

## The output contract FilaBridge actually depends on

FilaBridge does not integrate with PrusaSlicer directly. In the current repository it reads PrusaLink `/api/v1/status`, `/api/v1/job`, `/api/v1/info`, `/api/v1/storage`, and `/api/v1/files/...`; it obtains print-file metadata or downloads file content; then it parses total/per-tool grams and updates Spoolman.

### ASCII G-code remains compatible

At alpha11, PrusaSlicer's exact output source still emits these comments:

```gcode
; filament used [mm] = ...
; filament used [cm3] = ...
; filament used [g] = ...
; filament cost = ...
; total filament used [g] = ...
```

The per-extruder gram values remain ordered as a vector. This is confirmed in [`PostProcessor.cpp` at the alpha11 tag](https://github.com/prusa3d/PrusaSlicer/blob/version_3.0.0-alpha11/src/libslic3r/src/libslic3r/GCode/PostProcessor.cpp#L405-L453). Current FilaBridge's legacy ASCII parser therefore still matches the 3.0-alpha11 text contract.

That finding is deliberately narrow: it confirms emitted ASCII comments, not the whole printer-to-Spoolman path.

### BGCode is the immediate compatibility risk

PrusaSlicer alpha11 finalizes BGCode with Deflate-compressed print/slicer metadata, Heatshrink-compressed G-code, `MeatPackComments` encoding, and CRC32 checksums. See the tagged [`ResultExportDataFinalizer.cpp`](https://github.com/prusa3d/PrusaSlicer/blob/version_3.0.0-alpha11/src/slic3r-shared/src/Slic3r/Biz/ResultExport/ResultExportDataFinalizer.cpp#L38-L70) and the official [LibBGCode format documentation](https://github.com/prusa3d/libbgcode/blob/main/doc/bgcode.md).

Current FilaBridge's download fallback scans `.bgcode` bytes with an ASCII regular expression. That is not a BGCode decoder and cannot reliably find compressed print metadata. Metadata supplied by PrusaLink can hide this problem, but an open report in the official tracker says CORE One firmware 6.5.3 omits `meta` for BGCode while returning it for G-code: [Prusa-Link-Web issue #527](https://github.com/prusa3d/Prusa-Link-Web/issues/527). The issue is evidence of observed firmware behaviour, not a normative API guarantee.

Required implementation: read the official BGCode container, validate magic/version/block sizes/checksums, locate `PrintMetadata`, decompress Deflate data, parse its INI fields, and impose strict input/decompression size limits. A maintained Go implementation can be used if one is verified against Prusa's fixtures; otherwise implement the minimal metadata reader rather than a full G-code decoder.

### PrusaLink remains the stable integration boundary, but FilaBridge supports too little of it

The official [PrusaLink OpenAPI specification](https://github.com/prusa3d/Prusa-Link-Web/blob/master/spec/openapi.yaml) still defines the endpoints FilaBridge consumes. Its print-file metadata schema uses the legacy spaced keys `filament used [g]` and `filament used [g] per tool`; current FilaBridge already accepts those plus underscore aliases. Its printer-state enum includes `IDLE`, `BUSY`, `PRINTING`, `PAUSED`, `FINISHED`, `STOPPED`, `ERROR`, `ATTENTION`, and `READY`.

The spec's default security scheme is HTTP Digest. PrusaSlicer 3.0's own [PrusaLink print-host implementation](https://github.com/prusa3d/PrusaSlicer/blob/version_3.0.0-alpha11/src/slic3r-shared/src/Slic3r/Biz/PrintHost/PrintHostPrusaLink.cpp) supports API-key or Digest authentication, explicit HTTP or HTTPS, CA configuration, `/api/version` discovery, and capability-dependent upload behaviour. Current FilaBridge supports only `X-Api-Key` and prepends `http://` to the configured address.

Required implementation: make the configured endpoint a validated URL, support HTTPS and a CA trust option, support Digest and API-key authentication, probe `/api/version`, and record the API/server/firmware version and advertised capabilities in diagnostic logs. This is forward-compatibility work for updated firmware, independent of the slicer's new UI.

The 3.0 physical-printer configuration model continues to carry host, UUID, hardware configuration, server type, API key, username/password, CA file, port, and auth type. See [`PhysicalPrinterConfig.hpp`](https://github.com/prusa3d/PrusaSlicer/blob/version_3.0.0-alpha11/src/slic3r-shared/include/Slic3r/Biz/PhysicalPrinter/PhysicalPrinterConfig.hpp#L11-L38). FilaBridge should not import these private slicer settings; it should model the same protocol choices in its own explicit printer configuration.

## Are FilaBridge and Spoolman still necessary?

### Confirmed facts

- Neither the 3.0 alpha release notes nor the alpha11 source announces a Spoolman integration or a durable physical-spool inventory. A filament/profile is a recipe; it is not a uniquely identified spool with remaining stock, location, history, or cross-printer state.
- [Spoolman](https://github.com/Donkie/Spoolman) remains a central, self-hosted inventory with spool identity, remaining material, usage history, labels, locations, multi-printer integrations, and an API. Its latest release at the research date is [v0.26.1](https://github.com/Donkie/Spoolman/releases/tag/v0.26.1).
- FilaBridge supplies a separate missing relationship: “this physical spool is on this printer tool now”, followed by job-completion accounting against Spoolman.

### Verdict

For the user's stated goal—central inventory plus automatic accounting across Prusa printers—**both remain necessary today**. PrusaSlicer 3.0 changes neither responsibility.

Spoolman would become optional only in a narrower future setup where every spool is a compatible writable smart tag, every printer has a supported reader, and storing remaining filament on each tag is sufficient. FilaBridge's decrement logic could then become redundant for those tagged spools, but its mapping, reconciliation, cross-system history, and compatibility-adapter roles would still be useful.

## OpenPrintTag changes the future design, not the current answer

Prusa's [OpenPrintTag documentation](https://help.prusa3d.com/article/openprinttag_978161) describes an open writable NFC spool tag containing material, colour, print settings, and remaining filament. It says remaining length can be written after printing, and that Prusament has included tags since October 2025. The [open specification](https://github.com/OpenPrintTag/openprinttag-specification) defines nominal/actual quantity fields and auxiliary consumption fields.

Current Prusa-Firmware-Buddy source already contains an OpenPrintTag filament-usage tracker which reads tag quantities, calculates extrusion, updates consumed weight, and flushes usage before finalizing a print. See the pinned [`filament_usage_tracker.cpp`](https://github.com/prusa3d/Prusa-Firmware-Buddy/blob/1ce23f33ed3b94e26aa33a44557d6c4a4be11eb6/src/feature/openprinttag/filament_usage_tracker/filament_usage_tracker.cpp) and [`marlin_server.cpp`](https://github.com/prusa3d/Prusa-Firmware-Buddy/blob/1ce23f33ed3b94e26aa33a44557d6c4a4be11eb6/src/common/marlin_server.cpp#L1057-L1066).

However, this is not proof of a released automatic-reader feature. At the same pinned commit, [`ProjectOptions.cmake`](https://github.com/prusa3d/Prusa-Firmware-Buddy/blob/1ce23f33ed3b94e26aa33a44557d6c4a4be11eb6/ProjectOptions.cmake#L800-L804) enables `HAS_ANFC` only for development builds on CORE One/CORE One L and disables it otherwise. Treat the firmware code as a future integration signal, not a production dependency.

Spoolman's documented [tag-scanner design](https://github.com/Donkie/Spoolman/wiki/Tag-scanners) for v0.27 associates a tag UID with a Spoolman spool and exposes lookup/scan endpoints; it does not write OpenPrintTag contents. The feature was merged in [PR #1096](https://github.com/Donkie/Spoolman/pull/1096), while a separate [OpenPrintTag content-support PR #880](https://github.com/Donkie/Spoolman/pull/880) was closed unmerged. Because v0.27 was not released at the research date, FilaBridge must feature-detect this API rather than assume it.

### Recommended future model

FilaBridge should evolve into an adapter between PrusaLink/OpenPrintTag and Spoolman, with a per-spool authority mode:

| Mode | Consumption authority | FilaBridge behaviour |
|---|---|---|
| Spoolman-led | Completed PrusaLink job | Current deduction flow; optionally write the resulting remainder to a tag. |
| Tag-led | Printer/tag tracker | Read/synchronize the tag's consumption into Spoolman; do not deduct the job again. |
| Observed-only | Neither automatically | Report drift and require confirmation; useful while firmware/tag support is experimental. |

The invariant is one completed extrusion event produces one inventory adjustment. Tag updates and job metadata must carry provenance/idempotency keys so reconciliation cannot double-count.

## Adaptation plan

### P0 — support PrusaSlicer 3.0 and current firmware safely

1. **Compatibility fixtures:** generate single-tool and multi-tool ASCII G-code and BGCode with 2.9.6 and each 3.0 pre-release. Preserve files plus expected per-tool grams and zero/unused tool positions.
2. **BGCode metadata reader:** implement bounded, checksum-validating PrintMetadata extraction using the official format. Keep PrusaLink JSON metadata first, decoded file metadata second, and ASCII scanning only for actual G-code.
3. **PrusaLink transport negotiation:** probe `/api/version`; support API key and Digest; support HTTP/HTTPS and CA trust; retain unknown capabilities and fields without failing.
4. **Durable job reconciliation:** identify a print by printer plus stable job/file identity, persist pending/completed accounting, handle `FINISHED`, `STOPPED`, `ERROR`, `READY`, restart/resume, and retry Spoolman updates idempotently.
5. **Release matrix:** run the fixtures and mocked API variants in CI, then perform one real-printer smoke test per supported firmware family before marking a new slicer/firmware version supported.

### P1 — prepare for smart-spool firmware

Add optional OpenPrintTag UID/content support only behind capability detection. Map a UID to a Spoolman spool when the released Spoolman API supports it, retain untagged/manual mappings, and implement the authority modes above. Do not couple the core service to an unreleased Firmware Buddy development flag or unreleased Spoolman API.

### Do not do yet

Do not rewrite FilaBridge as a PrusaSlicer Lua plugin, depend on alpha post-processing hooks, parse 3MF as the accounting source, or infer a physical spool from profile/colour names. Those paths are either unavailable, experimental, or unable to provide durable physical identity.

## Compatibility conclusion

No breaking change was found in alpha11's emitted ASCII per-tool weight statistics, so FilaBridge's core accounting concept survives PrusaSlicer 3.0. The actual weak points are already visible around that contract: compressed BGCode, inconsistent firmware metadata availability, limited authentication/transport support, and volatile job state. Fixing those points yields a version-tolerant bridge that can follow 3.0 and new firmware without binding the project to unstable slicer internals.

OpenPrintTag is the strategic feature to watch. It can eventually become a second authoritative observation source, but released hardware/firmware coverage and Spoolman content synchronization are not yet sufficient to replace either FilaBridge or Spoolman.

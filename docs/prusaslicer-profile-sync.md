# PrusaSlicer 3 profile sync

FilaBridge exports active **Spoolman filament definitions** to one managed
PrusaSlicer 3 user-profile file. It does not export physical spools, remaining
weight, locations, tags, or tool assignments.

## Run a sync

Close PrusaSlicer before changing its profile directory. Use the same native
FilaBridge release binary that runs the service:

```bash
export FILABRIDGE_SYNC_USERNAME='operator'
export FILABRIDGE_SYNC_PASSWORD='management-password'

./filabridge profile-sync \
  --url 'https://filabridge.example/api/prusaslicer/profiles.zip' \
  --data-dir '/path/to/your/PrusaSlicer-data' \
  --prusa-slicer '/path/to/prusa-slicer'
```

`--data-dir` is intentionally required. Pointing at an inferred platform path
could modify the wrong alpha, beta, or stable installation. The command runs
`prusa-slicer --version` and accepts major version 3 only.

Plain HTTP is rejected by default. For a trusted local network without TLS,
add `--allow-insecure-http`. Credentials cannot be embedded in the URL and the
password has no command-line flag, keeping it out of process listings.

## What gets installed

The authenticated endpoint `GET /api/prusaslicer/profiles.zip` returns a
deterministic ZIP containing a JSON manifest and this file:

```text
presets/user/prusa-research-fff/PrusaResearch/
  preset-filament-filabridge-spoolman.yaml
```

The helper validates archive shape, size, schema, target path, and SHA-256
before writing. It stages the complete multi-document YAML beside the target
and renames it into place. If the target exists without FilaBridge's ownership
marker, sync stops and leaves it unchanged. Repeating an unchanged sync makes
no write.

The endpoint is protected by the same management Basic authentication as all
other `/api/*` routes. Responses use `Cache-Control: no-store`, a versioned
download filename, an ETag, and a content-derived 16-character bundle version.

## Base profile mapping

Spoolman does not contain every safe print parameter. Each generated profile
therefore inherits an official Generic profile and overrides catalog metadata:
vendor, material, colour, density, diameter, and spool tare. Temperature,
cooling, flow, retraction, and printer/tool variants remain inherited from
PrusaSlicer. Price is converted from the full-filament price and weight to
PrusaSlicer's cost-per-kilogram field; it is omitted if either input is missing.

Verified automatic mappings are:

| Spoolman material | PrusaSlicer base |
|---|---|
| PLA | Generic PLA |
| PETG | Generic PETG |
| ABS, ASA | Generic ABS, Generic ASA |
| TPU, TPE, FLEX | Generic FLEX |
| PA, NYLON, PC, PCCF | Generic PA, Generic PC, Generic PC-CF |
| PCTG, CPE, PEBA, HIPS | Matching Generic profile |
| PP, PPCF, PVA, PVB, BVOH | Matching Generic profile |

An unknown material fails the whole export instead of guessing temperatures or
cooling. Add a string value named `prusaslicer_base_profile` to that filament's
Spoolman `extra` data after validating the base in PrusaSlicer. Plain PET is
intentionally not treated as PETG.

## Limits and ownership

- Sync is one-way: Spoolman definition to PrusaSlicer profile.
- One profile is generated per filament definition, not per physical spool.
- Generic-profile inheritance requires Prusa's FFF presets to be installed.
- FilaBridge never writes Prusa's local-repository manifest or invokes updater
  mutations; doing so could deselect the official source with the same vendor.
- PrusaSlicer must be closed during installation because alpha11 provides no
  stable profile CRUD or reload API.
- A 3.x release that changes profile paths or inheritance needs a compatibility
  update; major version 4 is rejected until explicitly supported.

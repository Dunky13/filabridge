# Compatibility policy

## Automated baseline

| Component | Tested version | Coverage |
|---|---|---|
| PrusaSlicer | 2.9.6 | ASCII single- and multi-tool metadata contracts; BGCode container single/multi contracts |
| PrusaSlicer | 3.0.0-alpha11 | Synthetic ASCII/BGCode single/multi parser contracts; real PrusaSlicer ASCII exports; pinned-libbgcode conversion; sparse INDX 8-slot vector |
| Firmware Buddy | 6.10.1 | PrusaLink state/auth/metadata variants through mocked API tests |
| Spoolman | 0.26.1 | Usage accounting; tag APIs must be capability-detected and fail closed |

Earlier PrusaSlicer 3.0 alphas are not claimed as supported baselines. Alpha11
is the first pinned 3.0 baseline for this branch. The weekly upstream workflow
fails when a newer 3.0/Firmware Buddy/Spoolman release appears; support is only
advanced after its fixtures and mocked protocol behavior are added.

## Hardware release gate

Before publishing support for a new firmware family/model pair, run one
real-printer print using ASCII G-code and one using BGCode. Confirm the
printer/job identity,
logical-to-physical tool routes, per-tool grams, single Spoolman adjustment, and
restart reconciliation. This gate needs printer credentials and hardware and is
therefore intentionally not automated in public CI.

## Enforced release evidence

`testdata/compatibility/golden-fixtures.json` is the machine-readable register
for real PrusaSlicer exports, explicitly attributed libbgcode conversions,
sanitized firmware captures, and hardware attestations. Normal CI validates its
structure, producer lineage, logical-tool cardinality, and every checksum. The
tagged-release workflow additionally requires the currently pinned slicer to
have a real ASCII export, an honestly attributed BGCode, and an eight-slot
PrusaSlicer export. It then requires matching firmware captures and physical
gates for COREONE, COREONE_INDX, COREONEL, and preview COREONEL_INDX family/model
rows. Stable releases require the non-preview rows; preview rows remain
schema-validated but nonblocking until promoted to supported. Evidence for an
unrelated printer cannot unblock a row. Until every required stable row exists,
release publication is intentionally blocked. See
`testdata/compatibility/README.md` for the capture procedure.

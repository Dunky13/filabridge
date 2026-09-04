# Compatibility policy

## Automated baseline

| Component | Tested version | Coverage |
|---|---|---|
| PrusaSlicer | 2.9.6 | ASCII single- and multi-tool metadata contracts; BGCode container single/multi contracts |
| PrusaSlicer | 3.0.0-alpha11 | ASCII and BGCode single/multi contracts, preserved official-library BGCode, sparse INDX 8T vector |
| Firmware Buddy | 6.10.1 | PrusaLink state/auth/metadata variants through mocked API tests |
| Spoolman | 0.26.1 | Usage accounting; tag APIs must be capability-detected and fail closed |

Earlier PrusaSlicer 3.0 alphas are not claimed as supported baselines. Alpha11
is the first pinned 3.0 baseline for this branch. The weekly upstream workflow
fails when a newer 3.0/Firmware Buddy/Spoolman release appears; support is only
advanced after its fixtures and mocked protocol behavior are added.

## Hardware release gate

Before publishing support for a new firmware family, run one real-printer print
using ASCII G-code and one using BGCode. Confirm the printer/job identity,
logical-to-physical tool routes, per-tool grams, single Spoolman adjustment, and
restart reconciliation. This gate needs printer credentials and hardware and is
therefore intentionally not automated in public CI.

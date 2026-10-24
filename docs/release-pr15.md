# PR #15 security release handoff

The proposed next patch is `v0.3.4`. PR #15 updates browser tooling to Node.js
26.12.1 and repairs the reachable Go vulnerability gate with Go 1.26.10 and
`golang.org/x/net v0.61.0` plus its required companion modules. No application
configuration or printer compatibility claim changes.

Go 1.26.10 is the current security patch on the existing Go 1.26 line. In
particular, [GO-2026-6809](https://pkg.go.dev/vuln/GO-2026-6809) requires 1.26.10;
1.26.9 does not contain that certificate distrust update. See the official
[Go downloads](https://go.dev/dl/) and
[x/net module metadata](https://proxy.golang.org/golang.org/x/net/@v/v0.61.0.info).
The builder uses the multi-platform index digest for the explicit
`golang:1.26.10-alpine3.23` image, covering Linux amd64 and arm64.

## Source qualification

Run from the repository root with Go 1.26.10, Node.js 26.12.1, npm, jq,
`sha256sum`, a C compiler, and Chromium installed. No printer or Spoolman
configuration is needed. Test servers use temporary databases and mocks.

```sh
go version
node --version
go mod verify
git ls-files -z '*.go' | xargs -0 gofmt -l
go vet -p 2 ./...
go test -p 2 -parallel 2 -race ./...
go install golang.org/x/vuln/cmd/govulncheck@v1.7.0
govulncheck ./...
go build -p 2 -trimpath -o filabridge .
git ls-files -z 'static/js/*.js' | xargs -0 -n1 node --check
npm ci --ignore-scripts --no-audit --no-fund
npx --no-install playwright install chromium
npm run test:browser -- --workers=1
bash scripts/check-release-contract.sh
bash scripts/verify-golden-fixtures.sh --structure
bash scripts/verify-golden-fixtures.sh --release
bash scripts/test-release-evidence.sh
bash scripts/test-latest-promotion.sh
bash scripts/smoke-release-artifact.sh ./filabridge
```

Formatting must print no files; all other commands must exit zero. Run expensive
checks through the operator's shared validation queue when required locally.
Do not commit the local binary or browser results.

The manifest has three hashed slicer artifacts and four explicitly preview
printer models. Firmware capture and hardware attestation arrays are empty.
The release gate passes because it requires family-specific hardware evidence
only for non-preview models. This is source and fixture qualification, and does
not establish real-printer acceptance. Preserve the preview flags and the gate.
The scheduled upstream compatibility watch is separate from CI; this patch does
not advance the pinned slicer, firmware, or Spoolman versions.

## Release sequence owned by the parent operator

1. Verify the reviewed PR head's hosted CI and required reviews, then merge PR
   #15 normally. Recheck the merged `main` commit and any required merge checks.
2. Recheck that `v0.3.4` does not exist as either a tag or a GitHub release. Verify
   every PR commit's configured author, matching DCO trailer, and valid signature.
3. Run the release preflight and retain its exact commit. Create an annotated,
   GPG-signed `v0.3.4` tag on that qualified merged commit. Verify its signature,
   then push only that tag. Do not create a GitHub release manually.
4. Follow the tag-triggered Release workflow through full validation, all five
   target-native CGO builds and SQLite/HTTP smoke tests, then container validation
   and publication, GitHub release publication, and rolling-latest promotion.
5. Read back the published tag target, five binaries, SPDX SBOM, checksums, GHCR
   versioned manifest (Linux amd64 and arm64), provenance and SBOM attestations,
   and promoted `latest` digest. Download artifacts and verify checksums and the
   relevant target runtime before claiming artifact acceptance.

The release workflow builds on Linux amd64/arm64, macOS amd64/arm64, and Windows
amd64 runners because SQLite requires CGO. A build on another architecture or
an emulated container is not proof of native target runtime acceptance. GitHub
release publication waits for all five native smoke tests and versioned container
publication; `latest` promotion waits for the GitHub release and independently
checks highest-stable-tag eligibility. Repository Actions permissions and package
writes remain hosted authorities that must succeed in the actual release run.

No source qualification authorizes a production deployment or printer connection.

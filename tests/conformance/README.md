# Scoped MCP conformance checks

This runner exercises the **production MCP Handler and shipped skill bundles**
against a pinned upstream conformance checkout. It runs separate 2025-11-25 and
2026-07-28 wire profiles. It does not claim full requirement-set conformance.

The default fixture binds an ephemeral loopback port and injects an admin test
principal. Its inert domain services return empty catalogs or explicit errors;
it does not emulate Valkey, source mutation, search, or authentication. Never
build or deploy this fixture as the Vecdex server. Storage and authentication
contracts are covered by the native integration tests and live MCP E2E suite.

## Run

Use Go and Node with npm. Fetch the exact upstream revision recorded in
`manifest.json`, then install its locked dependencies:

```bash
git clone https://github.com/modelcontextprotocol/conformance /tmp/vecdex-mcp-conformance
git -C /tmp/vecdex-mcp-conformance checkout --detach 7169291ec0b68eb370fddcd9947313ab0d5e4156
npm --prefix /tmp/vecdex-mcp-conformance ci
python3 tests/conformance/run.py --upstream /tmp/vecdex-mcp-conformance
```

The runner verifies the checkout's commit, builds its local test fixture, runs
selected scenarios, and closes the fixture. Set `GOCACHE` if the default Go
cache is not writable. `--fixture-binary` accepts a prebuilt fixture.
`--url` accepts an existing test endpoint with an already-established principal;
the upstream harness cannot attach a Vecdex Bearer token. Do not disable
production authentication to use that option.

Results go to a unique directory under `tests/reports/conformance/`. Every
scenario retains the unmodified upstream `checks.json` and console log.
`summary.json` adds a Vecdex classification beside each original status and
always sets `full_conformance` to `false`.

## What the result means

The manifest selects initialization/ping for the legacy profile; tool,
resource and prompt catalogs for both profiles; and modern stateless wire,
caching, missing-resource, and Skills enumeration/manifest scenarios. The
Skills scenarios are extensions outside the dated applicability timeline, so
only those two receive `--force` with an explicit modern wire version.

The upstream catalog scenarios inspect the first response page only; their
reported counts are page counts. Vecdex uses five-item discovery pages, so a
`toolCount` of 5 does not mean the server offers only five tools. Product transport
and live E2E tests follow `nextCursor` and verify the complete 17-tool admin
catalog and role-specific catalogs. The fixture registers the production prompts
and skills directly; it supplies no canned protocol catalog responses.

Four checks within upstream `server-stateless` require artificial
`test_missing_capability`, `test_streaming_elicitation`, or `test_logging_tool`
tools. Vecdex does not offer these. Their **original FAILURE status is retained**
and classified as a fixture limitation only if the exact check ID is listed in
the manifest and upstream explicitly returns `details.untestable: true`.
An actual failure on the same ID still fails this runner. Skips for unadvertised
dynamic prompt/tool catalog changes are separately reported and never counted
as passes. Every other failure, warning, unexpected skip, timeout, or missing
result fails the scoped run.

The manifest also records excluded requirement groups. Product tools, source
reads, prompts, completions, resource updates, cancellation, and errors need
Vecdex-specific input and are tested in the product test suites. No synthetic
production tools are added to make unrelated upstream fixtures pass. A green
scoped run establishes only the selected checks; it is not a Tier 1/full-suite
certification and does not validate the production HTTP perimeter.

Check the classification guard independently:

```bash
python3 -m unittest discover -s tests/conformance -p 'test_*.py'
```

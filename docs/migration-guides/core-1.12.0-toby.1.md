# Core 1.12.0 / Framework 1.9.0 fork upgrade

## Scope and frozen inputs

This release upgrades the official modules and preserves the existing fork contracts. It does not add fixes for upstream SSE framing, malformed JSON, timeout policy, or recovery. Production data and historical billing-pending executions are outside this release.

| Module | Previous official base | Previous released fork | New official base |
|---|---|---|---|
| Core | `6d60595b574e5a89289e1449825d0854aa99eabb` (`core/v1.8.4`) | `33c53a9fc7f307a1516ddac487d4b4431a742fc3` (`core/v1.8.4-toby.3`) | `2a4c51b2cc25f995bb8d20825fe7a34be902ae60` (`core/v1.12.0`) |
| Framework | `c6466fcaaa97be3c997fa203a83ac98f03c14b50` (`framework/v1.6.0`) | `475da911f5e2fbf16792ca1bc0dfc86750c21db2` (`framework/v1.6.0-toby.3`) | `a86d489d99536adda3dc85869bc7782d682370d1` (`framework/v1.9.0`) |

The upgrade branch starts at the Framework target: its Core tree is identical to the Core target (`70f50023e4699cd468c6b088c77bc5b55762e9c1`). The old checkout at `core/v1.7.4-toby.9` is not the migration source.

## Patch preservation ledger

The machine-readable `bifrost-1.12.0-toby.1/patch-manifest.json` records every old changed file, its upstream/fork Git blob IDs, target location, source patch group, and verification disposition. The frozen inventory contains 47 Core files (39 non-Gate and eight Gate), seven Framework files, and seven cross-module files. File counts describe the old diff, not a limit on required adaptation to newly added official providers.

| Group | Source commits | Contract and new-base treatment |
|---|---|---|
| Provider lifecycle | `aadeb2cbb` → `4b68e381d` | Publish queue/worker state only after successful provider construction; keep shutdown/lazy-init synchronization. Preserve the target's retired-worker cleanup. |
| Configurable stale connection retries | `b7a387daa` → `d33fbc399` | Preserve the default-false flag, serialization, explicit no-retry callback and separation from Core retries. Thread full NetworkConfig through all 33 non-Gate provider constructors and Gate. Preserve official context transport and header-timeout behavior. |
| Explicit-only provider initialization | `734337e79` → `8b656ba8e` | Preserve the default-false switch and request-path restrictions; startup and explicit updates remain available. |
| Gate video provider | `2290d0122`, `33961446b`, `33c53a9fc` plus prior `.7`/`.8`/`.9` contracts | Preserve all eight released files, named-provider registration, operation restrictions, isolated direct-link retrieval/cancellation, two-hop download ownership, exact billing sidecar and standard settled Usage.Cost. Only add unsupported stubs for new ModelRetrieve and Decision methods. |
| Pure pricing API | `f062245f0` → `b1b9b7bc6`, `1796cb94d` | Preserve public signatures, the 48-field schema, Chat/ChatStream-only calculator, explicit pricing and immutable inputs. Adapt legacy search pricing inside this wrapper because the new native calculator no longer consumes its old fields; preserve all original expectations. |
| Official changes already included | `73b0ceef7`, `ab29cb3b2` | BilledUsage tracing and release changelog differences are upstream changes already present in the target. Do not replay old files over their newer official implementations. |
| Schema and harness | Existing released cross-module patches | Preserve Gate and retry configuration schema, Gate schema tests, both historical Gate harness groups by stable request identity, filter mapping, and all 11 local harness contract tests. Retain historical migration documents. |

## Validation contract

Only local fixtures and isolated infrastructure are used; no paid provider calls or production configuration. Expected upstream live-provider/fixed skips are itemized separately from the required test set. In the approved continuation, a failure independently reproduced on the unchanged official tag is recorded as a known upstream issue rather than fixed or treated as a fork regression. Unexplained failures, environment gaps, unexpected skips and migration regressions still block publication and dependency switching.

- [x] Freeze exact module tags, commits and complete source diffs.
- [x] Complete every preservation disposition with zero unclassified entries.
- [x] Review both directions: all old contracts preserved; target-relative diff contains only approved patches/adaptations.
- [x] Run Core and Framework complete local test sets and race checks, including the 12/40/7 old custom tests and official transport/terminal/tracing regressions.
- [x] Run schema and local harness tests, including both Gate groups.
- [x] Verify TokenEx against this candidate with an external modfile: production build, affected contract/race/coverage gates, local Responses/Chat/video/standard-cost/native-pricing E2E.
- [x] Freeze tested source and module-tree hashes; publish new immutable fork tags only after all required gates pass.
- [x] Download released modules through the public proxy/checksum path; compare their source with the tested trees.
- [x] Switch TokenEx's formal pins, repeat required consumption tests and RPC/API Docker builds, and inspect binary build information.

## Release status

Both immutable fork tags are published at `505ba99a3ca742f5f7ac7354b23b7821e5736424`: `core/v1.12.0-toby.1` and `framework/v1.9.0-toby.1`. Candidate gates, fresh remote archive verification and formal TokenEx consumer validation are complete. The hard cut is committed as `1ec703c2420bdfaf88a986a1bd5b671e8abbbd39`; generated Graphify artifacts are separately committed as `0a36746511d095674a7a62b960abcfd0c21d5cc8`. No deployment was performed.

Tested implementation commit: `84d9fb743e0dbb5c4b0c9659450d7423b55349e2`. Core tree: `594c7f04a5b267a9e87cf73b72d3a890f712f6da`; Framework tree: `774d62524377814c2c296067bbc10dbb93861d1f`. Documentation-only commits may follow without changing these tested module trees.

| Validation | Completed result |
|---|---|
| Core complete ordinary / race coverage | All 51 packages covered in each mode; 19,135 passing test/subtest events, 77 classified skips, four raw host assertion failures per mode. Three environment failures pass in isolated Linux CGO/race; the remaining assertion is the independently reproduced official Bedrock issue. |
| Framework complete Linux ordinary | 5,218 passing events, zero failures, three explicit performance opt-in skips. |
| Framework complete Linux race | 5,217 passing events, zero failures or race reports, the same three performance skips and one unchanged official tracing race skip. |
| Complete MCP ordinary / race | Each run has 709 passing events and 29 classified skips, with zero failures/race reports. These are a subset of the Core total. |
| Preserved custom tests | All original 12 Core, 40 Gate and seven pricing tests retained; pricing has two necessary new compatibility tests. No old assertion was removed. |
| Perimeter | Schema ordinary/race; 11 Gate, 21 chain, 43 shard, three HTTP status and two decision contract tests passed. Both old Gate harness group objects and all 299 official groups are preserved. |

Core skips are 29 unchanged fixed cases and 48 cases requiring external provider/model credentials or instances. The full Framework runs use Go 1.27.0, CGO and matching plugin toolchains, eight real local service categories, PostgreSQL with fsync enabled on temporary memory storage, and the required free/occupied port layout. No missing service is accepted as a test skip.

## Published source and formal consumer verification

A new empty module cache downloaded both tags through `https://proxy.golang.org,direct` with `sum.golang.org` enabled. Every file, file set and `go.mod` byte was compared with archives generated from the published source; both module trees equal the tested implementation. The annotated tag objects are `dea60a690e9e99e2187fafa2e9d87b86f4b88b4f` (Core) and `2b6d454cb6b31feecf52eb8e9b13b8e788e35a7e` (Framework), both peeled to the release commit above.

| Released module | Verified files | Module checksum | go.mod checksum |
|---|---:|---|---|
| Core `v1.12.0-toby.1` | 1,029 | `h1:2du5Pka044MGQxi5Pfs2/wV9elGf32wW0dP0HUndIM0=` | `h1:1jv4eNS9kAv36PWrXu02XZ+w4rhTQkXpXgZGvNlYl0A=` |
| Framework `v1.9.0-toby.1` | 473 | `h1:UB5wY2cOwfMQE6EQ6uAtDbyf61k5DP/zzg21gdBsnlQ=` | `h1:Zi0jN2ch+eL6eo2v5Cuz1JmG8WsX4qxM6MLuHvS/eTQ=` |

| Validation using formal remote pins | Completed result |
|---|---|
| Production compilation and module integrity | All production packages compile with `-mod=readonly`; `go mod verify` reports all modules verified. |
| Frozen coverage proof identities | All 44 source hashes and remote module/checksum identities pass the original validator; exception set, target scope and 98% threshold remain unchanged. The complete coverage profile was measured before publication against byte-equivalent candidate modules; it is not relabeled as a new formal coverage run. |
| Complete affected contracts, ordinary and race | Each run passes 1,958 test/subtest events across six packages, with zero failures, skips or race reports. |
| Responses local HTTP/RPC E2E | 29 events pass; nine settled executions and three expected missing-evidence pending outcomes. All 16 frozen inputs are unchanged. |
| Chat with real Account service | Three completed local-fixture calls and six verified account receipts; expected balances and zero residual reserves. |
| Native pricing and Gate video E2E | Nine events pass with zero failures/skips; actual host service binaries contain the remote tags and exact checksums. |
| RPC/API production Docker artifacts | Both original production Dockerfiles build successfully. Extracted binaries equal verified formal artifacts, including buildinfo; non-root entrypoints and help probes pass. |

Formal testing made no source changes. The recorded `go.mod` SHA-256 is `6b8f545cb89366edb79978665615de9f2a89790a58369615fbdf212d31a39406`, and `go.sum` is `cdadb553be411083d2ed91f68ed535ac465de1e321ef62a103ef60b720b8d79b`. Candidate and formal raw evidence remain separately archived and linked in the JSON ledger. These post-publication documentation updates do not move either tag or modify either module tree.

## Recorded upstream outcomes

`TestAnthropicIngressMantleReplayUsesResponsesInputShapes` fails at `core/providers/bedrock/bedrock_test.go:8039`: an assistant history item lacks the required Responses `status`. Untouched official `a86d489d99536adda3dc85869bc7782d682370d1` reproduces the same assertion; its Core tree equals official Core v1.12.0. No fix or weakened assertion was added.

The independently run official `TestToolsToExecute_* -race` subset reproduces concurrent initialization of `mcpServerPaths` in `core/internal/mcptests/fixtures.go`. The unchanged complete suite initializes that state earlier and passes; both outcomes are retained. The Framework `TestPopulateLLMResponseAttributes_RacesReleaseTrace` skip in race builds is also unchanged official code, not a new passing result.

## Consumer adaptation and prior attempts

The consumer directly checks the new Conversation string/object union: every object and every nonblank string remains rejected; omitted/null/blank strings are accepted and cleared. The user also approved the required mirror of four new provider types and three technical permissions. Existing product request sets stay unchanged; no business HTTP/RPC endpoint or credential branch is added. The unavailable Cloudflare credential form is rejected before runtime construction. No old-SDK or running-request compatibility layer is introduced.

The Conversation adaptation, required technical mirror, formal `go.mod`/`go.sum` and updated pricing proof identities are committed together in the consumer hard cut. It retains official module/import paths, replaces only the two modules with the remote fork tags and contains no local-path replacement. Candidate production compilation, 349 complete Responses cases, 62 request-contract race cases, 529 enum/provider cases in ordinary and race, real-Account Chat, and nine video/native-account integration events have passed. The original complete Go coverage gate passes 4,603 events across 29 owner packages plus Account quota, with all 32 fixed targets at the unchanged 98% effective threshold and zero failure/skip. The five consumer flow packages pass 2,087 race events with zero failure/skip/race report. Responses HTTP/RPC E2E passes 29 events, with nine settled executions and three expected evidence-pending outcomes. All inputs remain frozen.

Earlier diagnostic attempts remain in the raw records: the initial Framework run had missing services (4,545 passes / 61 failures / 210 skips), macOS plugin loading failed, and early Core/MCP runs were incomplete or lacked fixtures. They are superseded by the complete Linux and fixture-backed results above. The host's reserved-name/TEST-NET behavior was separately reproduced and resolved by running the exact affected tests in network-isolated Linux. Slow PostgreSQL disk-backed attempts are explicitly incomplete; the final ordinary and race runs both use fresh, fully configured fixtures.

The machine-readable manifest links all 61 old files and the four new constructor adaptations to their test groups. `bifrost-1.12.0-toby.1/validation-summary.json` records current results, retained attempts, source hashes and log locations. No paid upstream provider, production configuration, historical pending execution or automatic deployment is part of this work.

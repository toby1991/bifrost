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

Only local fixtures and isolated infrastructure are used; no paid provider calls or production configuration. Expected upstream live-provider/fixed skips are itemized separately from the required test set. A failure or unexpected skip in the required set blocks publication and dependency switching; upstream product bugs are reported, not fixed in this upgrade.

- [x] Freeze exact module tags, commits and complete source diffs.
- [x] Complete every preservation disposition with zero unclassified entries.
- [x] Review both directions: all old contracts preserved; target-relative diff contains only approved patches/adaptations.
- [ ] Run Core and Framework complete local test sets and race checks, including the 12/40/7 old custom tests and official transport/terminal/tracing regressions.
- [x] Run schema and local harness tests, including both Gate groups.
- [ ] Verify TokenEx against this candidate with an external modfile: production build, affected contract/race/coverage gates, local Responses/Chat/video/standard-cost/native-pricing E2E.
- [ ] Freeze tested source and module-tree hashes; publish new immutable fork tags only after all required gates pass.
- [ ] Download released modules through the public proxy/checksum path; compare their source with the tested trees.
- [ ] Switch TokenEx's formal pins, repeat required consumption tests and RPC/API Docker builds, and inspect binary build information.

## Release status

Implementation is complete, but publication is blocked by an upstream test failure. Neither new module tag has been published and TokenEx's formal dependencies have not been changed.

`TestAnthropicIngressMantleReplayUsesResponsesInputShapes` fails at `core/providers/bedrock/bedrock_test.go:8039`: an assistant history message reaches the Responses wire without its required `status` field. The same assertion fails on an untouched export of official commit `a86d489d99536adda3dc85869bc7782d682370d1` (whose Core tree is exactly Core v1.12.0), so this is not a fork migration regression. The supplied fixture is entirely local. No upstream fix or test weakening has been added.

The passing focused gates are: all 12 legacy Core custom tests under race; all 40 Gate top-level tests (163 test/subtest events) normally and under race; all 577 datasheet test/subtest events normally and under race; all 431 utils top-level tests (5627 test/subtest events) in a network-isolated Linux container; schema tests normally and under race; and the local Gate/harness contract tests. These results do not mean the complete Core/Framework gate passed.

The prepublication TokenEx build using an external candidate modfile also exposed a required consumer adaptation: `llmgw/internal/logic/responses/request.go:159` treats `OpenAIResponsesRequest.Conversation` as `*string`, while official Core v1.12.0 uses the `ResponsesResponseConversation` string/object union. The compiler rejects the existing `strings.TrimSpace(*request.Conversation)` expression. TokenEx source and formal dependency pins remain untouched; this incompatibility is recorded rather than hidden by modifying the official schema.

Initial full-module runs also exposed missing local test infrastructure. The source-wide allocation audit passed after the unchanged transports/plugins trees were checked out. The two host-specific DNS/network assertions also failed on the untouched official tree and passed in the network-isolated container. Other missing MCP/vector/plugin fixtures are tracked as incomplete, not relabeled as passed. Full logs and their hashes are listed in `bifrost-1.12.0-toby.1/validation-summary.json`.

The initial Framework run recorded 4545 passing, 61 failing, and 210 skipped test events. Local vector services were absent. After the plugin examples were checked out, the plugin retry still hit a macOS `dlopen` chained-fixups error (17 passing / 14 failing events); this has not been established as an upstream product bug. All newly created vector-service setup was stopped and cleaned when the deterministic official Core failure closed the publication gate. The complete service-backed Framework and full-module race gates remain unfulfilled.

The first Core full run was interrupted after the official blocker was reproduced and its remaining host-network cases stalled. It is an incomplete diagnostic run, not a passing suite. MCP fixture preparation and targeted checks are reported separately; no initial missing-fixture failure is counted as a successful test.

After six Go and two Node local MCP fixtures were built, all five targeted `TestToolsToExecute_*` top-level tests and two subtests passed without skips. The same targeted race run found concurrent initialization of the unmodified official `mcpServerPaths` global in `core/internal/mcptests/fixtures.go:42,49-55`. No official test-helper fix was added. The complete MCP suite/race gate is not declared passed.

Next release attempt must start by rerunning the official failing assertion against an explicitly approved corrected upstream base. Then repeat the remaining full-module and TokenEx consumption gates before creating tags. Do not silently move the frozen official base or publish this candidate while the required gate is red.

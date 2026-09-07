# Core v1.8.4-toby.2 and Framework v1.6.0-toby.2

This release completes the fork upgrade against the latest published legacy
patches: Core `v1.7.4-toby.9` and Framework `v1.5.4-toby.1`. It retains the
official whole-tree baseline at Core `v1.8.4`. The earlier local `.1` candidates
are superseded; no published tag is moved.

## Preserved fork behavior

| Area | Review outcome |
| --- | --- |
| Provider lifecycle | Constructor failures do not publish a queue; initialization, removal and shutdown retain their locking and closed-state checks. |
| Dynamic initialization | The opt-out still blocks implicit provider creation while allowing initialization and explicit updates. |
| Stale-connection retries | All original changes across 39 non-Gate files are retained. All 29 non-Gate Provider constructors use the configured transport helper, and streaming clients retain its retry callback. |
| Gate submission and billing | Provider-private DTOs, response identity, exact billing evidence, status mapping, named instances and operation permissions are retained. |
| Gate retrieval | Restore the `.8/.9` independent header-only lookup transport, bounded lookup budget and cancellation across DNS, dialing, TLS, HTTP CONNECT and SOCKS5. Never follow the lookup redirect or read its body. |
| Gate errors | Restore HTTP 400 / `invalid_request_error` / `request_not_dispatched` only for conversion and serialization failures before dispatch. |
| Gate v1.8.4 compatibility | Keep the new video interface methods, moved input fields and explicit rejection of unsupported new parameters. |
| Pure chat pricing | Keep the public API, all 43 previous price fields and the 5 upstream additions, snapshot-only calculation, tier fallback and single per-request fee. |

The Gate header copy now uses the existing shared `SetExtraHeadersHTTP` helper
instead of duplicating its equivalent logic. A wire-level Go regression checks
context/config precedence, authorization replacement, multiple values and
hop-by-hop filtering.

## Pure calculator request boundary

The official Framework normalizer now categorizes batch results as `chat`.
That must not broaden the pure synchronous Chat calculator: batch pricing uses
a different formula. Restore its original raw request allowlist,
`ChatCompletionRequest` and `ChatCompletionStreamRequest`, while leaving the
official normalizer unchanged. Fifteen request-type cases lock this boundary.

## Verification

- Twelve original non-Gate fork regression tests pass with `-race -count=1`.
- Gate's complete free package tests pass, including every original `.9` test;
  its complete race run also passes. The restored budget, error shape and six
  handshake-cancellation cases were observed failing before the fix.
- Framework datasheet's complete tests and race run pass. The new batch
  rejection test was observed failing before the fix. An independent public
  API probe verifies normal Chat pricing, rejected batch input, tier fallback,
  per-request fee and preservation of the original usage cost.
- All Core and Framework production packages compile with Go 1.27 and a
  temporary workspace containing these exact two source modules.
- Gate provider-harness coverage includes named submission, bounded retrieval
  and binary download. Eleven free assertion/chain checks, twelve existing
  chain checks, collection augmentation and provider/feature filtering pass.
  The paid harness remains opt-in via `[PREVIEW]`; it was not run live.

An expanded upstream dialer test,
`TestConfigureDialer_SSRFMultiIPAllFail`, fails identically on both the legacy
`.9` snapshot and the new candidate in the validation environment: its direct
connection to TEST-NET address `192.0.2.1:9` succeeds despite the fixture's
unreachable-address assumption. This is recorded separately from the passing
fork regression tests; no source or assertion is changed to hide it.

## Upstream retry distinction

The new official Core can retry once after stripping rejected encrypted
reasoning, including when `MaxRetries` is zero. This protocol recovery remains
upstream behavior. `DisableStaleConnectionRetry` controls the transport retry
layer; it is not a promise that all Core recovery paths issue only one call.

## Consumption

Keep the official module import paths and replace both modules with the released
fork versions. Consumers must regenerate their own dependency graph and sums,
then build from the remote pins without a local workspace or replacement.

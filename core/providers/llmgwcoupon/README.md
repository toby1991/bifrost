# LLMGW Coupon Provider

`llmgw-coupon` maps coupon allocation onto `VideoGeneration` and lookup onto
`VideoRetrieve`. The host supplies its generated RPC adapter; this package does
not import an application service, duplicate its proto, or create connections.

Configure `NetworkConfig.BaseURL` as a gRPC `host:port` such as `llmgw:8080`.
Configure the existing custom-provider `IsKeyLess` option to skip Core credential
selection. Coupon accepts a zero-value key; no placeholder credential is needed.
The adapter's optional token argument remains for source compatibility and may be
empty. The provider applies the configured request timeout to each adapter call.
The host owns connection reuse, the internal RPC trust boundary, and outcome
classification.

Inject a reusable `Client` under exported `ClientContextKey`. Before submission,
set `schemas.BifrostContextKey("Idempotency-Key")` to the decimal Execution ID
string. There is no automatic HTTP header forwarding. The provider parses a
positive uint64 and rejects a missing, invalid or overflowing value before RPC.

`Claim` returns the persisted inventory row ID. The submission result contains
only that ID with the normal Bifrost provider suffix and `in_progress` status.
Retrieval calls `Get` once and returns the original code in `videos[].url` with
`type: url`, `content_type: text/plain`, and `completed` status. This URL field is
an opaque coupon string, not a playable video or an Agent page URL. Lookup may
omit the Execution context value; when provided, it must match the persisted
claim. Coupon allocation, ownership, accounting, and delivery authorization
remain host responsibilities. Unsupported operations fail before invoking RPC.

The provider never retries adapter calls itself. Adapter failures are returned
unchanged, preserving the distinction between definitive rejection and unknown
allocation outcome. Hosts must not label unknown communication outcomes as
`request_not_dispatched`.

Gate's separate support for the same internal context value is provider-local:
its video submission sends `Idempotency-Key: execution-<value>` unless an
explicit header already configures it. Other Providers do not send this context
value automatically. Query and media-download calls do not derive the header.

Verification uses fake clients and local HTTP servers only. These context values
and the injected RPC adapter have no current Bifrost HTTP ingress, so the
provider-harness HTTP suite cannot construct this integration. The host's Task
and RPC integration tests verify that boundary instead.

// Free assertion/chain tests for the Gate Postman rows. This evaluates the real
// collection scripts against synthetic wire responses; it does not start Bifrost
// or claim to replace core/providers/gate's HTTP, cancellation and security tests.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { buildProducerIndex, chainedDependencies, walkRequests } from "./chained-vars.mjs";

const collection = JSON.parse(readFileSync(new URL("../../collections/provider-harness.json", import.meta.url)));
const folder = collection.item.find((item) => item.name === "61. Gate video contract restoration");
assert(folder, "Gate regression folder missing");
const [submit, retrieve, download] = folder.item;
const jobId = "video_harness:gate-harness";
const encodedId = encodeURIComponent(jobId);
const variables = new Map();
let passed = 0;

function test(name, fn) {
  variables.clear();
  fn();
  passed++;
  console.log(`  ok - ${name}`);
}

// Only the Chai operations exercised by these rows and the collection-level
// status/content assertions. Missing operations throw instead of passing silently.
function expect(value, message) {
  const to = {
    eql: (expected) => assert.deepStrictEqual(value, expected, message),
    match: (pattern) => assert.match(value, pattern, message),
    be: {
      within: (min, max) => assert(value >= min && value <= max, message),
      above: (min) => assert(value > min, message),
      below: (max) => assert(value < max, message),
      get true() { assert.strictEqual(value, true, message); return true; },
    },
  };
  return { to };
}
expect.fail = (message) => assert.fail(message);

function run(item, body, { code = 200, contentType = "application/json", preview = "1" } = {}) {
  const failures = [];
  const delays = [];
  let nextRequest;
  let skipped = false;
  const text = Buffer.isBuffer(body) ? body.toString("utf8") : JSON.stringify(body);
  const pm = {
    info: { requestName: item.name },
    environment: { get: (key) => key === "include_preview" ? preview : undefined },
    variables: { get: (key) => variables.get(key) },
    collectionVariables: {
      get: (key) => variables.get(key),
      set: (key, value) => variables.set(key, value),
      unset: (key) => variables.delete(key),
    },
    request: {
      name: item.name,
      headers: { upsert() {} },
      url: { toString: () => item.request.url.raw.replace(/\{\{([^}]+)\}\}/g, (_, key) => key === "baseUrl" ? "http://localhost:8080" : variables.get(key) || "") },
    },
    response: {
      code,
      text: () => text,
      json: () => JSON.parse(text),
      headers: { get: (key) => key.toLowerCase() === "content-type" ? contentType : undefined },
      size: () => ({ body: Buffer.byteLength(text) }),
    },
    expect,
    test: (name, fn) => {
      try { fn(); } catch (error) { failures.push({ name, message: error.message }); }
    },
    execution: {
      skipRequest: () => { skipped = true; },
      setNextRequest: (name) => { nextRequest = name; },
    },
  };
  for (const listen of ["prerequest", "test"]) {
    for (const owner of [collection, folder, item]) {
      for (const event of owner.event || []) {
        if (event.listen !== listen) continue;
        new Function("pm", "setTimeout", event.script.exec.join("\n"))(pm, (fn, delay) => { delays.push(delay); fn(); });
        if (skipped) return { failures, skipped, nextRequest, delays };
      }
    }
  }
  return { failures, skipped, nextRequest, delays };
}

const submission = () => ({
  object: "video", id: jobId, model: "bytedance/seedance-2.0", status: "queued",
  extra_fields: { raw_request: { model: "bytedance/seedance-2.0", duration: 6, resolution: "720p", aspect_ratio: "16:9", generate_audio: false } },
});
const completion = () => ({
  object: "video", id: jobId, model: "bytedance/seedance-2.0", status: "completed", size: "720p", seconds: "6",
  videos: [{ type: "url", url: "https://cdn.example.test/result.mp4?signature=mock", content_type: "video/mp4" }],
  extra_fields: { raw_response: { code: 200, data: {
    job_id: "video_harness", status: "completed", resolution: "720p", duration: 6,
    download_url: "https://api.gate.ai/api/v1/videos/video_harness/content",
  } } },
});
const prepareRetrieve = () => variables.set("gateRestorationVideoId", encodedId);
const names = (result) => result.failures.map((failure) => failure.name);

test("preview rows do not submit unless explicitly enabled", () => {
  assert.equal(run(submit, submission(), { preview: "" }).skipped, true);
});

test("submit, completed retrieve and binary download preserve one named task", () => {
  assert.deepEqual(run(submit, submission()).failures, []);
  assert.equal(variables.get("gateRestorationVideoId"), encodedId);
  assert.deepEqual(run(retrieve, completion()).failures, []);
  assert.equal(variables.get("gateRestorationCompletedVideoId"), encodedId);
  // Starts with printable ASCII, so the collection must identify the binary
  // route rather than happen to accept the first byte of a particular MP4 file.
  assert.deepEqual(run(download, Buffer.from("mock-video-content"), { contentType: "video/mp4" }).failures, []);
});

test("failed resubmission clears IDs from an earlier run", () => {
  variables.set("gateRestorationVideoId", "stale");
  variables.set("gateRestorationCompletedVideoId", "stale");
  const result = run(submit, { error: { message: "named Gate unavailable" } }, { code: 400 });
  assert(names(result).includes("Named Gate submit succeeds"));
  assert.equal(variables.has("gateRestorationVideoId"), false);
  assert.equal(variables.has("gateRestorationCompletedVideoId"), false);
});

test("base-provider suffix regression fails and cannot produce a retrieval ID", () => {
  const body = submission();
  body.id = "video_harness:gate";
  assert(names(run(submit, body)).includes("Submit preserves the named provider in the public video ID"));
  assert.equal(variables.has("gateRestorationVideoId"), false);
});

test("lost request resolution is detected independently of the returned job receipt", () => {
  const body = submission();
  delete body.extra_fields.raw_request.resolution;
  assert(names(run(submit, body)).includes("Neutral submit parameters reach the Gate wire request"));
});

test("pending polling is bounded and cannot unlock download", () => {
  prepareRetrieve();
  const body = completion();
  body.status = "queued";
  body.extra_fields.raw_response.data.status = "pending";
  delete body.videos;
  const pending = run(retrieve, body);
  assert.deepEqual(pending.failures, []);
  assert.equal(pending.nextRequest, retrieve.name);
  assert.deepEqual(pending.delays, [10000]);
  assert.equal(variables.has("gateRestorationCompletedVideoId"), false);
  variables.set("gateRestorationPollCount", 59);
  const exhausted = run(retrieve, body);
  assert(names(exhausted).includes("Gate video completes within 60 queries"));
  assert.equal(exhausted.nextRequest, undefined);
});

test("completed retrieval cannot hide a lost resolution mapping", () => {
  prepareRetrieve();
  const body = completion();
  delete body.size;
  const result = run(retrieve, body);
  assert(names(result).includes("Gate resolution and duration project into neutral response fields"));
  assert(names(result).includes("Completed Gate response includes the actual resolution"));
});

test("missing, insecure, credential-bearing and unresolved content URLs fail", () => {
  for (const url of [undefined, "http://cdn.example.test/video.mp4", "https://user:secret@cdn.example.test/video.mp4", "https://api.gate.ai/api/v1/videos/video_harness/content"]) {
    prepareRetrieve();
    const body = completion();
    if (url === undefined) delete body.videos;
    else body.videos[0].url = url;
    assert(names(run(retrieve, body)).includes("Completed Gate result contains the first-hop HTTPS URL"));
    assert.equal(variables.has("gateRestorationCompletedVideoId"), false);
  }
});

test("failed generation remains a failing outcome and never downloads", () => {
  prepareRetrieve();
  const body = completion();
  body.status = "failed";
  body.extra_fields.raw_response.data.status = "failed";
  delete body.videos;
  const result = run(retrieve, body);
  assert(names(result).includes("Gate job reaches successful completion"));
  assert.equal(result.nextRequest, undefined);
  assert.equal(variables.has("gateRestorationCompletedVideoId"), false);
});

test("download rejects an empty response, an envelope and the wrong content type", () => {
  for (const [body, contentType] of [[Buffer.alloc(0), "video/mp4"], [{ code: 200 }, "application/json"], [Buffer.from("video"), "text/plain"]]) {
    assert(names(run(download, body, { contentType })).includes("Named Gate download returns video bytes"));
  }
});

test("download links transitively to retrieve and submit for filtering and guards", () => {
  const entries = walkRequests(collection.item);
  const index = buildProducerIndex(entries);
  const downloadDeps = chainedDependencies(download, index);
  assert.deepEqual(downloadDeps.map((dep) => dep.variable), ["gateRestorationCompletedVideoId"]);
  assert.equal(downloadDeps[0].producerItem, retrieve);
  const retrieveDeps = chainedDependencies(retrieve, index);
  assert.deepEqual(retrieveDeps.map((dep) => dep.variable), ["gateRestorationVideoId"]);
  assert.equal(retrieveDeps[0].producerItem, submit);
});

console.log(`\n${passed} Gate video contract script/chain tests passed (no network).`);

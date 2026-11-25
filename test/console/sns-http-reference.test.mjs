import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import vm from "node:vm";

const base = "http://127.0.0.1:12345";
const reference = "/_simulator/docs/sns-http-subscriptions.html";
const specPath = "/_simulator/docs/sns/http-subscriptions.json";
const asset = (name) => new URL("../../internal/server/apidocs/" + name, import.meta.url);
const spec = JSON.parse(readFileSync(asset("sns/http-subscriptions.json"), "utf8"));
const json = spec.paths["/"].post.requestBody.content["application/x-www-form-urlencoded"];
const load = () => {
  let config;
  const calls = [];
  const globals = {
    Request, URL, URLSearchParams, location: new URL(base + reference),
    Scalar: { createApiReference(selector, options) {
      assert.equal(selector, "#reference");
      config = options;
    } },
    fetch(url, init) { calls.push([url, init]); return Promise.resolve({ status: 200 }); },
  };
  vm.runInNewContext(readFileSync(asset("sns-http-subscriptions-init.js"), "utf8"), globals);
  assert.equal(config.url, specPath);
  return { config, calls };
};

test("scoped fetch retains the exact native form and uses only the local cookie", async () => {
  const { config, calls } = load();
  assert.equal(config.persistAuth, false);
  await config.customFetch(specPath);
  assert.equal(calls[0][0].href, base + specPath);
  for (const { value } of Object.values(json.examples)) {
    const body = new URLSearchParams(value).toString();
    await config.customFetch("/", {
      method: "POST", body, headers: {
        "Content-Type": "application/x-www-form-urlencoded; charset=UTF-8",
        "Authorization": "discard",
        "X-Simulator-Token": "discard",
        "X-Simulator-Region": "discard",
      },
    });
    const [url, dispatched] = calls.at(-1);
    assert.equal(url.href, base + "/");
    assert.equal(dispatched.body, body);
    assert.equal(dispatched.credentials, "same-origin");
    assert.equal(dispatched.redirect, "error");
    assert.equal(dispatched.headers["x-simulator-service"], "sns");
    assert.equal(dispatched.headers["x-simulator-region"], "us-east-1");
    assert.equal(Object.keys(dispatched.headers).length, 3);
  }
  assert.equal(calls.length, 4);
});

test("separate AWS actions, scope, credentials routing and unrelated URLs are refused before dispatch", async () => {
  const { config, calls } = load();
  const fields = json.examples.httpsEndpoint.value;
  const form = (extra) => new URLSearchParams({ ...fields, ...extra }).toString();
  const post = (body) => ({ method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded" }, body });
  for (const [path, init] of [
    ["https://example.invalid/", post(form({}))],
    ["/?Action=Publish", post(form({}))],
    ["/#fragment", post(form({}))],
    ["/_simulator/providers/gcp/", post(form({}))],
    ["/", post(form({ Action: "Publish" }))],
    ["/", post(form({ Version: "2012-11-05" }))],
    ["/", post(form({ TopicArn: fields.TopicArn.replace("us-east-1", "us-west-2") }))],
    ["/", post(form({ Protocol: "sqs" }))],
    ["/", post(form({ Extra: "not part of this operation" }))],
    ["/", post(form({}) + "&Action=Subscribe")],
    ["/", { method: "POST", body: form({}) }],
    ["/_simulator/docs/openapi.json", undefined],
  ]) await assert.rejects(() => config.customFetch(path, init), /local SNS subscription reference/);
  assert.equal(calls.length, 0);
});

// Opt-in, role-only contract with the pinned Host, not a Battle rehearsal.
// Run with Bun (the Host's TS runtime) and its installed SDKs. No network dependency.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { createRequire } from "node:module";
import { devNull, tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { setTimeout as sleep } from "node:timers/promises";
import { pathToFileURL } from "node:url";

const [binary, host, ...extra] = process.argv.slice(2);
if (!binary || !host || extra.length) {
  throw new Error("usage: bun --no-env-file --no-install test/battle/host-bootstrap.mjs SIMULATOR_BINARY TENKACLOUD_CHECKOUT");
}
const fixture = new URL("../../internal/aws/testdata/host-battle-bootstrap/", import.meta.url);
const readFixture = (name) => readFileSync(new URL(name, fixture));
const pin = JSON.parse(readFixture("provenance.json"));
const hash = (bytes) => createHash("sha256").update(bytes).digest("hex");
const rel = "scripts/local-host/simulator-bootstrap.ts", src = resolve(host, rel);
assert.equal(hash(readFileSync(src)), pin.sourceFiles[rel], "wrong Host bootstrap source");
// Resolve local packages first: this verifier never installs dependencies or uses cloud defaults.
const fromHost = createRequire(src);
const { CloudFormationClient, DescribeStacksCommand, DeleteStackCommand } = fromHost("@aws-sdk/client-cloudformation");
const { STSClient, AssumeRoleCommand, GetCallerIdentityCommand } = fromHost("@aws-sdk/client-sts");
Object.assign(process.env, {
  AWS_CONFIG_FILE: devNull, AWS_SHARED_CREDENTIALS_FILE: devNull, AWS_EC2_METADATA_DISABLED: "true",
});
const { bootstrapSimulatorRole, simulatorBootstrapTemplate } = await import(pathToFileURL(src).href);
const settings = pin.parameters;
const rendered = simulatorBootstrapTemplate(settings.operatorAccountId, settings.externalId, settings.region);
assert.equal(hash(rendered), pin.templateSHA256, "Host template digest changed");
assert.deepEqual(Buffer.from(rendered), readFixture("template.json"), "fixture differs from Host output");

const dir = mkdtempSync(join(tmpdir(), "tc-host-bootstrap-"));
const tokenPath = join(dir, "token");
const sim = spawn(resolve(binary), ["--listen", "127.0.0.1:0", "--state", join(dir, "state.db"), "--token-file", tokenPath], {
  cwd: dir, env: { PATH: process.env.PATH, CI: "1" }, stdio: ["ignore", "pipe", "inherit"],
});
let stdout = "";
sim.stdout.on("data", (b) => { stdout = (stdout + b).slice(-4096); });
const exited = new Promise((res, rej) => { sim.on("error", rej); sim.on("exit", (code) => res({ code })); });
const clients = [];
const requestTimeoutMs = 5_000;
try {
  // The OS chooses the port. Only ever use this child's numeric loopback origin.
  const until = Date.now() + 15000;
  let endpoint;
  while (!(endpoint = /AWS simulation endpoint (http:\/\/127\.0\.0\.1:\d+)\r?\n/.exec(stdout)?.[1])) {
    if (Date.now() >= until) throw new Error("Simulator did not announce a loopback endpoint");
    await Promise.race([sleep(25), exited.then(() => { throw new Error("Simulator exited before ready"); })]);
  }
  const cfg = { region: settings.region, endpoint, maxAttempts: 1, credentials: {
    accessKeyId: "TCSIMOPERATOR", secretAccessKey: readFileSync(tokenPath, "utf8").trim(),
  }};
  const make = (Ctor, config) => {
    const client = new Ctor(config);
    clients.push(client);
    // The Host only calls send; route its CloudFormation requests and our STS
    // calls through the same bounded SDK path.
    return { send: (command) => client.send(command, { abortSignal: AbortSignal.timeout(requestTimeoutMs) }) };
  };
  const sts = make(STSClient, cfg), cfn = make(CloudFormationClient, cfg);
  assert.equal((await sts.send(new GetCallerIdentityCommand({}))).Account, settings.operatorAccountId);
  const stackName = { StackName: "tc-simulator-bootstrap" };
  async function stack() {
    const { Stacks } = await cfn.send(new DescribeStacksCommand(stackName));
    assert.equal(Stacks?.length, 1);
    assert.equal(Stacks[0].StackStatus, "CREATE_COMPLETE");
    return Stacks[0];
  }
  await bootstrapSimulatorRole(cfn, settings, { battle: false });
  const initial = await stack();
  assert.deepEqual(initial.Outputs.map((x) => x.OutputKey), ["RoleArn"]);

  // Exercise the Host's owned-stack upgrade, then its non-mutating replay.
  await bootstrapSimulatorRole(cfn, settings, { battle: true });
  const upgraded = await stack();
  assert.ok(upgraded.StackId && initial.StackId && upgraded.StackId !== initial.StackId);
  assert.ok(upgraded.Tags.some((x) => x.Key === "tenkacloud:simulator-bootstrap" && x.Value === pin.templateSHA256));
  const roles = Object.fromEntries(upgraded.Outputs.map((x) => [x.OutputKey, x.OutputValue]));
  assert.deepEqual(Object.keys(roles).sort(), ["CommandRoleArn", "RoleArn"]);
  assert.notEqual(roles.CommandRoleArn, roles.RoleArn);
  await bootstrapSimulatorRole(cfn, settings, { battle: true });
  assert.equal((await stack()).StackId, upgraded.StackId);

  for (const arn of Object.values(roles)) {
    const session = "host-contract";
    const { Credentials: c } = await sts.send(new AssumeRoleCommand({
      RoleArn: arn, RoleSessionName: session, DurationSeconds: 900, ExternalId: settings.externalId,
    }));
    const scoped = make(STSClient, { ...cfg, credentials: {
      accessKeyId: c.AccessKeyId, secretAccessKey: c.SecretAccessKey, sessionToken: c.SessionToken,
    }});
    assert.equal((await scoped.send(new GetCallerIdentityCommand({}))).Arn,
      arn.replace(":iam:", ":sts:").replace(":role/", ":assumed-role/") + "/" + session);
  }
  await cfn.send(new DeleteStackCommand(stackName));
  await assert.rejects(() => cfn.send(new DescribeStacksCommand(stackName)),
    (e) => e.name === "ValidationError" && e.message.includes("does not exist"));
} finally {
  for (const client of clients) client.destroy();
  sim.kill("SIGTERM");
  const timer = setTimeout(() => sim.kill("SIGKILL"), 10000);
  try {
    const { code } = await exited;
    assert.equal(code, 0, "Simulator did not shut down cleanly");
  } finally {
    clearTimeout(timer);
    rmSync(dir, { recursive: true, force: true });
  }
}
console.log("PASS: pinned Host rendered its fixture; native CFN created, upgraded, reused and deleted its bootstrap; STS assumed both roles. No guest or score was exercised.");

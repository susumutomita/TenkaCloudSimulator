// Development-only, reproducible rebuild. npm/Node are never runtime dependencies.
import { build } from "esbuild";
import { createHash } from "node:crypto";
import { readFile, writeFile, mkdir } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createCodeInputLiteAdapter } from "./collapsed-state-transform.mjs";
const root = path.dirname(fileURLToPath(import.meta.url));
const output = path.resolve(root, "../../internal/server/apidocs");
const scalarLockSource = await readFile(path.join(root, "package-lock.json"));
const collapsedStateAdapter = createCodeInputLiteAdapter({
  root,
  lock: JSON.parse(scalarLockSource),
});
await mkdir(output, { recursive: true });
const result = await build({
  absWorkingDir: root,
  entryPoints: [path.join(root, "entry.js")],
  outfile: path.join(output, "scalar.js"),
  bundle: true,
  minify: true,
  format: "iife",
  platform: "browser",
  target: ["es2022"],
  preserveSymlinks: true,
  metafile: true,
  plugins: [collapsedStateAdapter.plugin],
  legalComments: "external",
  treeShaking: true,
  define: {
    "process.env.NODE_ENV": '"production"',
    __VUE_OPTIONS_API__: "true",
    __VUE_PROD_DEVTOOLS__: "false",
    __VUE_PROD_HYDRATION_MISMATCH_DETAILS__: "false",
  },
  alias: {
    "@replit/codemirror-css-color-picker": path.join(
      root,
      "no-color-picker.js",
    ),
  },
  loader: {
    ".svg": "dataurl",
    ".woff2": "dataurl",
    ".woff": "dataurl",
    ".ttf": "dataurl",
  },
});
// Record the actual transformed input, not just the immutable installed module.
// Keep normal esbuild inputs/outputs intact for exclusion and notice collection.
result.metafile["x-simulator-source-transforms"] = [{
  ...collapsedStateAdapter.provenance(),
  adapterPath: "tools/scalar/collapsed-state-transform.mjs",
  adapterSHA256: createHash("sha256")
    .update(await readFile(path.join(root, "collapsed-state-transform.mjs")))
    .digest("hex"),
  lockSHA256: createHash("sha256").update(scalarLockSource).digest("hex"),
}];
const forbidden = Object.keys(result.metafile.inputs).filter((p) =>
  p.includes("@replit/codemirror-css-color-picker"),
);
if (forbidden.length)
  throw Error(
    "Excluded color-picker still present in bundle inputs: " +
      forbidden.join(", "),
  );
// Body reset belongs only to this standalone docs page, not the embedded console.
const standalone = await readFile(
  path.join(
    root,
    "node_modules/@scalar/api-reference/dist/styles/standalone.css",
  ),
  "utf8",
);
await writeFile(
  path.join(output, "scalar.css"),
  (await readFile(path.join(output, "scalar.css"), "utf8")) + "\n" + standalone,
);
const digests = {};
for (const file of [
  "scalar.js",
  "scalar.css",
  "scalar.js.LEGAL.txt",
  "scalar.css.LEGAL.txt",
]) {
  try {
    digests[file] = createHash("sha256")
      .update(await readFile(path.join(output, file)))
      .digest("hex");
  } catch (error) {
    if (error.code !== "ENOENT") throw error;
  }
}
await writeFile(
  path.join(root, "bundle-inputs.json"),
  JSON.stringify(result.metafile, null, 2) + "\n",
);
await writeFile(
  path.join(root, "bundle-digests.json"),
  JSON.stringify(digests, null, 2) + "\n",
);
console.log(
  "Built local Scalar API reference without Replit color-picker inputs.",
  digests,
);
// Keep committed verification metadata synchronized with the actual local outputs.
const metadataPath = path.resolve(root, "../../third_party/scalar.json");
const metadata = JSON.parse(await readFile(metadataPath, "utf8"));
metadata.assets = digests;
metadata.bundleSha256 = digests["scalar.js"];
await writeFile(metadataPath, JSON.stringify(metadata, null, 2) + "\n");
const removalPath = path.resolve(
  root,
  "../../third_party/color-picker-removal.json",
);
const removal = JSON.parse(await readFile(removalPath, "utf8"));
removal.verification.inputGraphSha256 = createHash("sha256")
  .update(await readFile(path.join(root, "bundle-inputs.json")))
  .digest("hex");
removal.verification.bundleSha256 = digests["scalar.js"];
await writeFile(removalPath, JSON.stringify(removal, null, 2) + "\n");

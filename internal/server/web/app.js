"use strict";
const $ = (id) => document.getElementById(id);
const state = {
  buckets: [],
  objects: [],
  bucket: null,
  selected: null,
  filter: "",
  busy: false,
  revision: 0,
  principal: "simulator-admin",
};
const formatDate = (value) =>
  new Date(value).toLocaleString("en-US", {
    month: "short",
    day: "numeric",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
const formatSize = (size) =>
  size < 1024
    ? `${size} B`
    : size < 1048576
      ? `${(size / 1024).toFixed(1)} KiB`
      : `${(size / 1048576).toFixed(1)} MiB`;
const text = (tag, value, cls) => {
  const e = document.createElement(tag);
  e.textContent = value;
  if (cls) e.className = cls;
  return e;
};
const keyPath = (bucket, key) =>
  `/${bucket}/${key.split("/").map(encodeURIComponent).join("/")}`;
const elements = (xml, name) => [...xml.getElementsByTagName(name)];
const value = (node, name) =>
  node.getElementsByTagName(name)[0]?.textContent || "";
async function request(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: { "x-simulator-principal": state.principal, ...options.headers },
  });
  if (!response.ok) {
    const body = await response.text();
    const xml = new DOMParser().parseFromString(body, "application/xml");
    let message = value(xml, "Message");
    try {
      message ||= JSON.parse(body).error;
    } catch {}
    throw new Error(
      `${value(xml, "Code") || response.status}: ${message || body || response.statusText}`,
    );
  }
  return response;
}
async function xmlRequest(path, options) {
  const r = await request(path, options);
  return new DOMParser().parseFromString(await r.text(), "application/xml");
}
function feedback(message, error = false) {
  $("feedback").textContent = message;
  $("feedback").classList.toggle("error", error);
  $("feedback").hidden = false;
}
function currentRegion() {
  return $("region").value;
}
function setBusy(busy) {
  state.busy = busy;
  $("loading").hidden = !busy;
  $("refresh").disabled = busy;
  $("primary").disabled = busy;
  $("delete").disabled = busy || !state.selected;
}
async function load() {
  const revision = ++state.revision;
  setBusy(true);
  state.selected = null;
  $("rows").replaceChildren();
  $("empty").hidden = true;
  try {
    if (state.bucket) {
      let token = "";
      const result = [];
      do {
        const xml = await xmlRequest(
          `/${state.bucket}?list-type=2&max-keys=1000${token ? "&continuation-token=" + encodeURIComponent(token) : ""}`,
        );
        if (revision !== state.revision) return;
        for (const o of elements(xml, "Contents"))
          result.push({
            name: value(o, "Key"),
            size: Number(value(o, "Size")),
            modified: value(o, "LastModified"),
            etag: value(o, "ETag"),
          });
        token = value(xml, "NextContinuationToken");
      } while (token);
      state.objects = result;
    } else {
      const xml = await xmlRequest("/");
      if (revision !== state.revision) return;
      state.buckets = elements(xml, "Bucket").map((b) => ({
        name: value(b, "Name"),
        region: value(b, "BucketRegion"),
        created: value(b, "CreationDate"),
      }));
    }
    render();
  } catch (error) {
    if (revision === state.revision) {
      feedback(error.message, true);
      $("empty").hidden = false;
      $("empty-title").textContent = "Could not load resources";
      $("empty-copy").textContent =
        "Check the error above and retry with Refresh.";
      $("empty-action").hidden = true;
    }
  } finally {
    if (revision === state.revision) setBusy(false);
  }
}
function render() {
  const inBucket = !!state.bucket;
  const resources = inBucket
    ? state.objects
    : state.buckets.filter((b) => b.region === currentRegion());
  const rows = resources.filter((r) =>
    r.name.toLowerCase().includes(state.filter.toLowerCase()),
  );
  $("title").textContent = inBucket ? state.bucket : "Buckets";
  $("subtitle").textContent = inBucket
    ? "Upload, inspect, and download objects. Changes are immediately available through the S3 API."
    : "Organize your objects. Build with the same state through the console or API.";
  $("bucket-crumb").textContent = inBucket ? `/ ${state.bucket}` : "";
  $("metric-label").textContent = inBucket
    ? "Objects in this bucket"
    : "Buckets in this region";
  $("metric-count").textContent = resources.length;
  $("metric-region").textContent = currentRegion();
  $("tabs").hidden = !inBucket;
  $("table-title").replaceChildren(
    document.createTextNode(inBucket ? "Objects " : "General purpose buckets "),
    text("span", `(${resources.length})`, "muted"),
  );
  $("table-description").textContent = inBucket
    ? "Objects are identified by their full key, including any folder prefixes."
    : "Buckets store your files as objects, each identified by a unique key.";
  $("primary").textContent = inBucket ? "Upload object" : "Create bucket";
  $("search").placeholder = inBucket
    ? "Find objects by key"
    : "Find buckets by name";
  $("selection").textContent = state.selected
    ? `1 ${inBucket ? "object" : "bucket"} selected`
    : `Select ${inBucket ? "an object" : "a bucket"} to see available actions`;
  $("delete").disabled = state.busy || !state.selected;
  $("table-head").replaceChildren();
  const head = document.createElement("tr");
  for (const h of [
    "",
    inBucket ? "Object key" : "Bucket name",
    inBucket ? "Size" : "Region",
    inBucket ? "Last modified" : "Created",
    inBucket ? "ETag" : "Storage class",
  ])
    head.append(text("th", h));
  $("table-head").append(head);
  $("rows").replaceChildren();
  for (const row of rows) {
    const tr = document.createElement("tr");
    tr.classList.toggle("selected-row", state.selected === row.name);
    const cell = document.createElement("td"),
      radio = document.createElement("input");
    radio.type = "radio";
    radio.name = "resource";
    radio.setAttribute("aria-label", `Select ${row.name}`);
    radio.checked = state.selected === row.name;
    radio.addEventListener("change", () => {
      state.selected = row.name;
      render();
    });
    cell.append(radio);
    tr.append(cell);
    const nameCell = document.createElement("td"),
      wrap = text("div", "", "resource-name");
    wrap.append(text("span", inBucket ? "▤" : "▱", "resource-glyph"));
    const link = text("button", row.name, "resource-link");
    link.addEventListener("click", () =>
      inBucket ? inspectObject(row) : openBucket(row),
    );
    wrap.append(link);
    nameCell.append(wrap);
    tr.append(nameCell);
    tr.append(
      text("td", inBucket ? formatSize(row.size) : row.region),
      text("td", formatDate(inBucket ? row.modified : row.created)),
    );
    const last = document.createElement("td");
    last.append(
      text(
        "span",
        inBucket ? row.etag : "Standard",
        inBucket ? "" : "type-tag",
      ),
    );
    tr.append(last);
    $("rows").append(tr);
  }
  $("empty").hidden = rows.length > 0;
  $("empty-title").textContent = state.filter
    ? "No matching resources"
    : inBucket
      ? "This bucket is empty"
      : "No buckets yet";
  $("empty-copy").textContent = state.filter
    ? "Try a different name or clear your filter."
    : inBucket
      ? "Upload your first object, or add one through the S3 API."
      : "Create a bucket to start working with object storage.";
  $("empty-action").hidden = !!state.filter;
  $("empty-action").textContent = inBucket ? "Upload object" : "Create bucket";
  $("result-count").textContent =
    `${rows.length} of ${resources.length} ${inBucket ? "objects" : "buckets"}`;
}
function goHome() {
  state.bucket = null;
  state.filter = "";
  $("search").value = "";
  $("properties").hidden = true;
  $("resource-panel").hidden = false;
  load();
}
function openBucket(bucket) {
  state.bucket = bucket.name;
  state.filter = "";
  $("search").value = "";
  $("properties").hidden = true;
  $("resource-panel").hidden = false;
  $("objects-tab").className = "selected";
  $("properties-tab").className = "";
  load();
}
function modal(title, submit, build, onSubmit) {
  $("modal-title").textContent = title;
  $("submit").textContent = submit;
  $("submit").className = "primary";
  $("submit").hidden = false;
  $("modal-error").hidden = true;
  $("modal-body").replaceChildren();
  build($("modal-body"));
  $("modal").showModal();
  $("modal-body").querySelector("input, textarea, select")?.focus();
  $("modal-form").onsubmit = async (event) => {
    event.preventDefault();
    $("submit").disabled = true;
    $("cancel").disabled = true;
    $("close-modal").disabled = true;
    $("modal-error").hidden = true;
    try {
      await onSubmit();
      $("modal").close();
    } catch (error) {
      $("modal-error").textContent = error.message;
      $("modal-error").hidden = false;
    } finally {
      $("submit").disabled = false;
      $("cancel").disabled = false;
      $("close-modal").disabled = false;
    }
  };
}
function labelInput(body, label, id, options = {}) {
  const l = text("label", label);
  l.htmlFor = id;
  const input = document.createElement("input");
  input.id = id;
  Object.assign(input, options);
  body.append(l, input);
  return input;
}
function createBucket() {
  let name;
  modal(
    "Create bucket",
    "Create bucket",
    (body) => {
      name = labelInput(body, "Bucket name", "bucket-name", {
        required: true,
        minLength: 3,
        maxLength: 63,
        placeholder: "my-practice-bucket",
        pattern: /[a-z0-9][a-z0-9.\x2d]{1,61}[a-z0-9]/.source,
      });
      body.append(
        text(
          "small",
          "Use 3–63 lowercase letters, numbers, dots, and hyphens. The name must be unique in this simulator.",
        ),
      );
      const l = text("label", "Region");
      l.htmlFor = "create-region";
      const select = $("region").cloneNode(true);
      select.id = "create-region";
      select.value = currentRegion();
      body.append(
        l,
        select,
        text(
          "div",
          "Basic object storage only. Public access controls, versioning, encryption, and lifecycle rules are not modeled in this build.",
          "inline-note",
        ),
      );
    },
    async () => {
      const region = $("create-region").value;
      const body =
        region === "us-east-1"
          ? ""
          : `<CreateBucketConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><LocationConstraint>${region}</LocationConstraint></CreateBucketConfiguration>`;
      await request(`/${name.value}`, {
        method: "PUT",
        body,
        headers: { "Content-Type": "application/xml" },
      });
      $("region").value = region;
      feedback(`Bucket “${name.value}” created.`);
      await load();
    },
  );
}
function upload() {
  let file, key;
  modal(
    "Upload object",
    "Upload object",
    (body) => {
      file = labelInput(body, "Choose a file", "upload-file", {
        type: "file",
        required: true,
        className: "upload-file",
      });
      body.append(
        text("small", "Maximum 8 MiB per object. Uploads stay in memory."),
      );
      key = labelInput(body, "Object key", "object-key", {
        required: true,
        maxLength: 1024,
        placeholder: "documents/example.txt",
      });
      body.append(
        text(
          "small",
          "Use slashes to organize keys into prefixes. An existing object with the same key will be replaced.",
        ),
      );
      file.addEventListener("change", () => {
        if (file.files[0]) key.value = file.files[0].name;
      });
    },
    async () => {
      const f = file.files[0];
      if (!f) throw new Error("Choose a file.");
      if (f.size > 8388608)
        throw new Error("This file exceeds the 8 MiB simulator limit.");
      if (new TextEncoder().encode(key.value).length > 1024)
        throw new Error("Object keys must be at most 1024 bytes.");
      await request(keyPath(state.bucket, key.value), {
        method: "PUT",
        body: f,
        headers: { "Content-Type": f.type || "application/octet-stream" },
      });
      feedback(`Object “${key.value}” uploaded.`);
      await load();
    },
  );
}
function removeSelected() {
  if (!state.selected) return;
  const selected = state.selected,
    bucket = state.bucket;
  let confirmation;
  modal(
    bucket ? "Delete object" : "Delete bucket",
    "Delete permanently",
    (body) => {
      body.append(
        text(
          "p",
          `Delete ${bucket ? "object" : "bucket"} “${selected}”? This cannot be undone.`,
        ),
      );
      if (!bucket)
        body.append(
          text(
            "div",
            "A bucket must be empty before it can be deleted.",
            "inline-note",
          ),
        );
      confirmation = labelInput(
        body,
        `Type ${selected} to confirm`,
        "confirmation",
        { required: true, autocomplete: "off" },
      );
    },
    async () => {
      if (confirmation.value !== selected)
        throw new Error("Enter the exact name to confirm deletion.");
      await request(bucket ? keyPath(bucket, selected) : `/${selected}`, {
        method: "DELETE",
      });
      feedback(`${bucket ? "Object" : "Bucket"} “${selected}” deleted.`);
      await load();
    },
  );
  $("submit").classList.add("danger");
}
function inspectObject(obj) {
  modal(
    "Object details",
    "Download object",
    (body) => {
      for (const [k, v] of Object.entries({
        Key: obj.name,
        Size: formatSize(obj.size),
        "Last modified": formatDate(obj.modified),
        ETag: obj.etag,
      })) {
        body.append(text("label", k), text("p", v));
      }
    },
    async () => {
      const r = await request(keyPath(state.bucket, obj.name));
      const href = URL.createObjectURL(await r.blob());
      const a = document.createElement("a");
      a.href = href;
      a.download = obj.name.split("/").pop() || "object";
      a.click();
      setTimeout(() => URL.revokeObjectURL(href), 1000);
      feedback(`Downloaded “${obj.name}”.`);
    },
  );
}
$("primary").onclick = $("empty-action").onclick = () =>
  state.bucket ? upload() : createBucket();
$("delete").onclick = removeSelected;
$("refresh").onclick = load;
$("search").oninput = (e) => {
  state.filter = e.target.value;
  render();
};
$("region").onchange = goHome;
$("home").onclick = $("buckets-crumb").onclick = goHome;
$("close-modal").onclick = $("cancel").onclick = () => $("modal").close();
$("modal").addEventListener("cancel", (e) => {
  if ($("submit").disabled) e.preventDefault();
});
$("objects-tab").onclick = () => {
  $("objects-tab").className = "selected";
  $("properties-tab").className = "";
  $("resource-panel").hidden = false;
  $("properties").hidden = true;
};
$("properties-tab").onclick = async () => {
  try {
    const xml = await xmlRequest(`/${state.bucket}?location`);
    $("property-values").replaceChildren();
    for (const [k, v] of Object.entries({
      Name: state.bucket,
      Region: xml.documentElement.textContent || "us-east-1",
      ARN: `arn:aws:s3:::${state.bucket}`,
      "State lifetime": state.persistent
        ? "Persistent SQLite file"
        : "Until this simulator process exits",
    }))
      $("property-values").append(text("dt", k), text("dd", v));
    $("objects-tab").className = "";
    $("properties-tab").className = "selected";
    $("resource-panel").hidden = true;
    $("properties").hidden = false;
  } catch (error) {
    feedback(error.message, true);
  }
};
$("endpoint").textContent = location.origin;
$("cli-example").textContent = `export AWS_ACCESS_KEY_ID=TCSIMEXAMPLE
export AWS_SECRET_ACCESS_KEY=simulator-only
export AWS_DEFAULT_REGION=us-east-1

aws --endpoint-url ${location.origin} s3api list-buckets
aws --endpoint-url ${location.origin} s3api create-bucket \\
  --bucket my-practice-bucket`;
$("scope-button").onclick = $("api-help").onclick = () =>
  $("scope").showModal();
$("close-scope").onclick = () => $("scope").close();
$("identity-button").onclick = () => {
  let input;
  modal(
    "Choose simulator identity",
    "Use identity",
    (body) => {
      input = labelInput(body, "Principal name", "identity-name", {
        value: state.principal,
        required: true,
        pattern: /[a-zA-Z0-9_\x2d]{1,64}/.source,
        maxLength: 64,
      });
      body.append(
        text(
          "small",
          "Use simulator-admin for unrestricted setup. Other principals require an attached identity policy; unknown principals implicitly deny. This selection is simulation context, not authentication.",
        ),
      );
    },
    async () => {
      state.principal = input.value;
      $("identity-button").textContent = state.principal;
      feedback(`Using principal “${state.principal}”.`);
      goHome();
    },
  );
};
$("identity-policies").onclick = () => {
  let name, policy;
  modal(
    "Identity policy",
    "Save policy",
    (body) => {
      name = labelInput(body, "Principal name", "policy-principal", {
        required: true,
        pattern: /[a-zA-Z0-9_\x2d]{1,64}/.source,
        maxLength: 64,
        placeholder: "reader",
      });
      body.append(
        text(
          "small",
          "Saving replaces the complete policy for this identity. simulator-admin is reserved.",
        ),
      );
      const label = text("label", "Identity policy JSON");
      label.htmlFor = "policy-json";
      policy = document.createElement("textarea");
      policy.id = "policy-json";
      policy.required = true;
      policy.rows = 13;
      policy.value = JSON.stringify(
        {
          Version: "2012-10-17",
          Statement: [
            {
              Effect: "Allow",
              Action: [
                "s3:ListAllMyBuckets",
                "s3:ListBucket",
                "s3:GetObject",
                "s3:GetBucketLocation",
              ],
              Resource: ["*"],
            },
          ],
        },
        null,
        2,
      );
      body.append(
        label,
        policy,
        text(
          "div",
          "Supports Action, Resource, Effect, and optional Sid. Explicit Deny wins. Without a matching Allow, access is denied. Unsupported policy fields are rejected.",
          "inline-note",
        ),
      );
    },
    async () => {
      let parsed;
      try {
        parsed = JSON.parse(policy.value);
      } catch {
        throw new Error("Enter valid policy JSON.");
      }
      await request(
        `/_simulator/v1/iam/principals/${encodeURIComponent(name.value)}/policy`,
        {
          method: "PUT",
          body: JSON.stringify(parsed),
          headers: {
            "Content-Type": "application/json",
            "x-simulator-principal": "simulator-admin",
          },
        },
      );
      feedback(
        `Policy saved for “${name.value}”. Use Choose identity to test it.`,
      );
    },
  );
};
async function initialize() {
  try {
    const c = await (await request("/_simulator/v1/capabilities")).json();
    state.persistent = c.persistence === "file";
    if (state.persistent) {
      document.querySelector(".notice div").textContent =
        "Persistent simulator state. Resources are stored in SQLite and survive restart. No real cloud resources are created.";
      document.querySelector(".sidebar-bottom small").textContent =
        "Persistent SQLite · independent simulator";
      document.querySelector(".workspace-name small").textContent =
        "Persistent environment";
    }
    await load();
  } catch (error) {
    feedback(error.message, true);
  }
}
initialize();

$("scope-policies").onclick = () => {
  $("scope").close();
  $("identity-policies").click();
};

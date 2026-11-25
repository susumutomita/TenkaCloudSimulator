import {parseTagLines} from './catalog-input.mjs';

// S3 references are metadata only. Preserve object keys exactly, including
// embedded slash/space, and never convert these inputs to a request URL.
function s3Reference(value, objectRequired) {
  const match = /^s3:\/\/([a-z0-9][a-z0-9.-]{1,61}[a-z0-9])(?:\/(.*))?$/u.exec(value);
  if (!match || (objectRequired && !match[2]) || (!objectRequired && match[2]) ||
      (match[2] && new TextEncoder().encode(match[2]).length > 1024)) return null;
  return {S3Bucket: match[1], ...(match[2] ? {S3Key: match[2]} : {})};
}

export function isAppStreamAppBlock(service, resource) {
  return service?.model === 'appstream' && resource?.kind === 'appBlock';
}

export function appStreamAppBlockDetailInput(resource, row) {
  const list = resource?.operations?.list;
  return list?.name === 'DescribeAppBlocks' && list.names === 'Arns' && typeof row?.Arn === 'string' && row.Arn
    ? {Arns: [row.Arn]} : null;
}

export function appStreamAppBlockInput(values) {
  const packaging = values.packaging;
  const custom = packaging === 'CUSTOM';
  const errors = {};
  if (!custom && packaging !== 'APPSTREAM2') errors.packaging = 'Choose an app block packaging type.';
  const name = values.name ?? '';
  if (!/^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,100}$/.test(name)) errors.name = 'Enter a valid unique name (1 to 101 characters).';
  const source = s3Reference(values.source ?? '', custom || values.importMode === 'existing');
  if (!source) errors.source = custom || values.importMode === 'existing' ? 'Enter an S3 object URI.' : 'Enter an S3 bucket URI.';
  const input = {Name: name, PackagingType: packaging, SourceS3Location: source};
  if (values.displayName) {
    if (Array.from(values.displayName).length > 100) errors.displayName = 'Use at most 100 characters.';
    else input.DisplayName = values.displayName;
  }
  if (values.description) {
    if (Array.from(values.description).length > 256) errors.description = 'Use at most 256 characters.';
    else input.Description = values.description;
  }
  if (custom || values.withPostSetup) {
    const script = custom ? values.setup ?? {} : values.postSetup ?? {};
    const location = s3Reference(script.uri ?? '', true);
    const timeout = Number(script.timeout);
    if (!location) errors.uri = 'Enter an S3 URI for the script object.';
    if (!(script.executable ?? '').trim()) errors.executable = 'Enter the script executable.';
    if (!/^[0-9]+$/.test(script.timeout ?? '') || !Number.isSafeInteger(timeout) || timeout < 1 || timeout > 2147483647) errors.timeout = 'Enter a positive integer.';
    input[custom ? 'SetupScriptDetails' : 'PostSetupScriptDetails'] = {
      ScriptS3Location: location, ExecutablePath: script.executable, TimeoutInSeconds: timeout,
      ...(script.args ? {ExecutableParameters: script.args} : {}),
    };
  }
  if (values.tags?.trim()) {
    try {
      const pairs = parseTagLines(values.tags);
      if (pairs.length) input.Tags = Object.fromEntries(pairs);
    } catch (err) {errors.tags = err.message;}
  }
  return {errors, input: Object.keys(errors).length ? null : input};
}

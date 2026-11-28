export const supportOperations = Object.freeze([
  'CreateCase', 'DescribeCases', 'AddCommunicationToCase', 'DescribeCommunications',
  'ResolveCase', 'AddAttachmentsToSet', 'DescribeAttachment',
]);
export const amsIncidentCode = 'sentinel-report-incident';
export const supportFileLimit = 65536;

export function supportAdvertised(capabilities) {
  const service = capabilities?.services?.support;
  return !capabilities?.consoleScope && service?.console === true && service.region === 'us-east-1'
    && service.protocol === 'AWS JSON 1.1' && service.scope === 'team'
    && service.attachmentBytes === supportFileLimit && service.attachmentFilesPerSet === 3
    && Array.isArray(service.operations) && supportOperations.every(op => service.operations.includes(op));
}

const invalid = () => new Error('Invalid local AWS Support response.');
const string = value => typeof value === 'string' && value.length > 0;
const fileMeta = file => string(file?.fileName) && string(file.attachmentId);
const caseRow = row => string(row?.caseId) && string(row.displayId) && string(row.subject)
  && (row.serviceCode === undefined || row.serviceCode === amsIncidentCode)
  && ['opened', 'resolved'].includes(row.status) && string(row.timeCreated);

function page(result, key, valid) {
  if (!Array.isArray(result?.[key]) || result[key].length > 10 || !result[key].every(valid)
      || (result.nextToken !== undefined && !string(result.nextToken))) throw invalid();
  return {rows: result[key], next: result.nextToken || ''};
}

export function supportClient(request) {
  async function call(operation, input, signal) {
    signal.throwIfAborted();
    const res = await request('/', {
      method: 'POST', signal,
      headers: {'Content-Type':'application/x-amz-json-1.1', 'X-Amz-Target':'AWSSupport_20130415.' + operation,
        'X-Simulator-Service':'support', 'X-Simulator-Region':'us-east-1'},
      body: JSON.stringify(input),
    });
    signal.throwIfAborted();
    if (!res.ok) {
      const body = await res.json().catch(() => ({}));
      const error = new Error([res.status, body?.__type, body?.message].filter(Boolean).join(' '));
      error.rejected = res.status >= 400 && res.status < 500;
      throw error;
    }
    const result = await res.json();
    signal.throwIfAborted();
    return result;
  }
  async function cases(includeResolvedCases, next, signal, id) {
    const input = {includeResolvedCases, includeCommunications:false, maxResults:10};
    if (id) input.caseIdList = [id];
    if (next) input.nextToken = next;
    const result = page(await call('DescribeCases', input, signal), 'cases', caseRow);
    if (id && (result.next || result.rows.length !== 1 || result.rows[0].caseId !== id)) throw invalid();
    return result;
  }
  async function messages(id, next, signal) {
    const input = {caseId:id, maxResults:10};
    if (next) input.nextToken = next;
    const valid = row => row?.caseId === id && string(row.body) && string(row.timeCreated)
      && string(row.submittedBy) && (row.attachmentSet === undefined ||
        (Array.isArray(row.attachmentSet) && row.attachmentSet.every(fileMeta)));
    return page(await call('DescribeCommunications', input, signal), 'communications', valid);
  }
  return {
    cases, messages,
    async detail(id, signal) {
      const [found, history] = await Promise.all([cases(true, '', signal, id), messages(id, '', signal)]);
      return {item:found.rows[0], history};
    },
    async stage(file, signal) {
      if (!validSupportFile(file)) throw Error('Select one file of at most 64 KiB.');
      const bytes = new Uint8Array(await file.arrayBuffer());
      signal.throwIfAborted();
      let raw = '';
      for (const byte of bytes) raw += String.fromCharCode(byte);
      const result = await call('AddAttachmentsToSet', {attachments:[{fileName:file.name, data:btoa(raw)}]}, signal);
      if (!string(result.attachmentSetId) || !Number.isFinite(Date.parse(result.expiryTime))) throw invalid();
      return {file, id:result.attachmentSetId, expires:Date.parse(result.expiryTime)};
    },
    async send(id, subject, body, setId, language, signal, incident = false) {
      const input = id ? {caseId:id, communicationBody:body} : {subject, communicationBody:body, language};
      if (!id && incident) input.serviceCode = amsIncidentCode;
      if (setId) input.attachmentSetId = setId;
      const result = await call(id ? 'AddCommunicationToCase' : 'CreateCase', input, signal);
      if (id ? result.result !== true : !string(result.caseId)) throw invalid();
      return id || result.caseId;
    },
    async resolve(id, signal) {
      const result = await call('ResolveCase', {caseId:id}, signal);
      if (!['opened', 'resolved'].includes(result.initialCaseStatus) || result.finalCaseStatus !== 'resolved') throw invalid();
    },
    async download(file, signal) {
      const {attachment} = await call('DescribeAttachment', {attachmentId:file.attachmentId}, signal);
      if (attachment?.fileName !== file.fileName || typeof attachment.data !== 'string'
          || attachment.data.length > 4 * Math.ceil(supportFileLimit / 3)) throw invalid();
      const raw = atob(attachment.data);
      if (raw.length > supportFileLimit || btoa(raw) !== attachment.data) throw invalid();
      return new Blob([Uint8Array.from(raw, char => char.charCodeAt(0))], {type:'application/octet-stream'});
    },
  };
}

export function validSupportFile(file) {
  return !!file && string(file.name) && [...file.name].length <= 255 && Number.isInteger(file.size)
    && file.size >= 0 && file.size <= supportFileLimit;
}

export function localDownloadName(name) {
  const leaf = name.split(/[\\/]/).pop().replace(/[<>:"|?*\x00-\x1f\x7f\u202a-\u202e\u2066-\u2069]/g, '_');
  return leaf && !/^\.+$/.test(leaf) ? leaf : 'attachment';
}

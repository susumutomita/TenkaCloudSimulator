const tokenPattern = /^[a-z2-7]{32}$/;
const domainPattern = /^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+test$/;

export const mailbox = 'inbox@inbox.ses.test';
export const deliveryPath = '/v2/email/outbound-emails';
export const identitiesPath = '/v2/email/identities';

export function canonicalDomain(value) {
  const domain = value.trim().toLowerCase();
  if (domain.length > 253 || !domainPattern.test(domain)) throw Error('Enter a local .test domain.');
  return domain;
}

export function domainProof(name, data) {
  const status = data?.VerificationStatus;
  const attributes = data?.DkimAttributes;
  const tokens = attributes?.Tokens;
  if (data?.IdentityType !== 'DOMAIN' || !['PENDING','SUCCESS'].includes(status) ||
      attributes?.Status !== status || data.VerifiedForSendingStatus !== (status === 'SUCCESS') ||
      !Array.isArray(tokens) || tokens.length !== 3 || new Set(tokens).size !== 3 ||
      !tokens.every(token => typeof token === 'string' && tokenPattern.test(token))) {
    throw Error('Invalid domain identity response.');
  }
  return {name,status,records:tokens.map(token=>({name:token+'._domainkey.'+name+'.',value:token+'.dkim.amazonses.com.'}))};
}

export function composeMessage(form) {
  const from = form.from.trim(), to = form.to.trim();
  if (!from || !to) throw Error('Enter both email addresses.');
  let content;
  if (form.mode === 'template') {
    const data = JSON.parse(form.data);
    if (!form.template.trim() || !data || Array.isArray(data) || typeof data !== 'object') throw Error('Enter a template name and JSON object.');
    content = {Template:{TemplateName:form.template.trim(),TemplateData:form.data}};
  } else {
    if (!form.subject || (!form.text && !form.html)) throw Error('Enter a subject and message body.');
    const Body = {};
    if (form.text) Body.Text = {Data:form.text,Charset:'UTF-8'};
    if (form.html) Body.Html = {Data:form.html,Charset:'UTF-8'};
    content = {Simple:{Subject:{Data:form.subject,Charset:'UTF-8'},Body}};
  }
  return {FromEmailAddress:from,Destination:{ToAddresses:[to]},Content:content};
}

export function messageId(data) {
  if (typeof data?.MessageId !== 'string' || !tokenPattern.test(data.MessageId)) throw Error('Missing submission identifier.');
  return data.MessageId;
}

export function checkedReceipt(row) {
  if (!row || !tokenPattern.test(row.messageId) || typeof row.from !== 'string' || typeof row.to !== 'string' ||
      !Number.isFinite(Date.parse(row.acceptedAt)) || !Number.isInteger(row.contentBytes)) throw Error('Invalid receipt.');
  if (row.outcome === 'delivered' && row.to === mailbox && row.contentBytes > 0 && !row.reason) return row;
  if (row.outcome === 'rejected' && row.reason === 'mailbox-not-found' && row.contentBytes === 0 && row.to !== mailbox) return row;
  throw Error('Incomplete local mailbox outcome.');
}

export function receiptList(data) {
  if (!Array.isArray(data?.messages) || data.messages.length > 100) throw Error('Invalid mailbox response.');
  const rows = data.messages.map(checkedReceipt);
  if (new Set(rows.map(row => row.messageId)).size !== rows.length) throw Error('Duplicate submission identifier.');
  return rows;
}

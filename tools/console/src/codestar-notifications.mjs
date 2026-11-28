export const service = 'codestar-notifications';
export const actions = ['CreateNotificationRule', 'DescribeNotificationRule', 'ListNotificationRules', 'UpdateNotificationRule', 'DeleteNotificationRule'];
export const branchCreated = 'codecommit-repository-branches-and-tags-created';
export const branchUpdated = 'codecommit-repository-branches-and-tags-updated';

export function notificationRulesAvailable(capabilities) {
  const config = capabilities?.services?.[service];
  return !capabilities?.consoleScope && config?.console === true && config.protocol === 'REST JSON' &&
    Array.isArray(config.operations) && actions.every(action => config.operations.includes(action));
}

export function ruleDraft(detail) {
  return {
    name: detail?.Name ?? '', source: detail?.Resource ?? '', enabled: detail?.Status !== 'DISABLED',
    created: detail ? detail.EventTypes.some(event => event.EventTypeId === branchCreated) : true,
    updated: detail ? detail.EventTypes.some(event => event.EventTypeId === branchUpdated) : true,
    topics: detail?.Targets.map(target => target.TargetAddress).join('\n') ?? '',
  };
}

export function ruleInput(draft, region, arn) {
  const account = region + ':000000000000:';
  const repo = 'arn:aws:codecommit:' + account, topic = 'arn:aws:sns:' + account;
  const targets = draft.topics.split('\n').map(line => line.trim()).filter(Boolean);
  const eventIds = [draft.created && branchCreated, draft.updated && branchUpdated].filter(Boolean);
  if (!/^[A-Za-z0-9_ -]{1,64}$/.test(draft.name) || !draft.source.startsWith(repo) ||
      !/^[A-Za-z0-9._-]{1,100}$/.test(draft.source.slice(repo.length)) || eventIds.length === 0 ||
      targets.length > 10 || new Set(targets).size !== targets.length ||
      targets.some(value => !value.startsWith(topic) || !/^[A-Za-z0-9_-]{1,256}$/.test(value.slice(topic.length)))) return null;
  return { ...(arn ? {Arn: arn} : {Resource: draft.source}), Name: draft.name, EventTypeIds: eventIds,
    DetailType: 'BASIC', Status: draft.enabled ? 'ENABLED' : 'DISABLED',
    Targets: targets.map(TargetAddress => ({TargetType: 'SNS', TargetAddress})) };
}

export function matchesSavedRule(detail, input) {
  const sorted = values => [...values].sort().join('\n');
  return detail.Name === input.Name && detail.Status === input.Status && detail.DetailType === input.DetailType &&
    (!input.Resource || detail.Resource === input.Resource) &&
    sorted(detail.EventTypes.map(event => event.EventTypeId)) === sorted(input.EventTypeIds) &&
    sorted(detail.Targets.map(target => target.TargetAddress)) === sorted(input.Targets.map(target => target.TargetAddress));
}

export function notificationClient(request, region) {
  const prefix = 'arn:aws:' + service + ':' + region + ':000000000000:notificationrule/';
  const validArn = value => typeof value === 'string' && value.startsWith(prefix) &&
    /^[0-9a-f]{40}$/.test(value.slice(prefix.length));
  function requireValue(value) {
    if (!value) throw Error('Invalid notification rule response.');
  }
  async function call(action, body, signal) {
    const response = await request('/' + action[0].toLowerCase() + action.slice(1), {method: 'POST', signal,
      headers: {'Content-Type': 'application/json', 'X-Simulator-Service': service, 'X-Simulator-Region': region},
      body: JSON.stringify(body)});
    const result = await response.json();
    signal.throwIfAborted();
    requireValue(result && typeof result === 'object' && !Array.isArray(result));
    return result;
  }
  return {
    async list(token, signal) {
      const r = await call('ListNotificationRules', {MaxResults: 20, ...(token && {NextToken: token})}, signal);
      requireValue(Array.isArray(r.NotificationRules) && r.NotificationRules.every(row => validArn(row?.Arn) &&
        row.Arn === prefix + row.Id) && (r.NextToken === undefined || typeof r.NextToken === 'string' && !!r.NextToken));
      return r;
    },
    async detail(arn, signal) {
      const r = await call('DescribeNotificationRule', {Arn: arn}, signal);
      requireValue(validArn(r.Arn) && r.Arn === arn && typeof r.Name === 'string' && typeof r.Resource === 'string' &&
        typeof r.CreatedBy === 'string' && r.DetailType === 'BASIC' && ['ENABLED', 'DISABLED'].includes(r.Status) &&
        Array.isArray(r.EventTypes) && r.EventTypes.every(e => [branchCreated, branchUpdated].includes(e?.EventTypeId)) &&
        Array.isArray(r.Targets) && r.Targets.every(t => t?.TargetType === 'SNS' && typeof t.TargetAddress === 'string' &&
          ['ACTIVE', 'UNREACHABLE'].includes(t.TargetStatus)));
      return r;
    },
    async write(action, body, signal) {
      const r = await call(action, body, signal);
      if (action === 'UpdateNotificationRule') requireValue(Object.keys(r).length === 0);
      else requireValue(validArn(r.Arn) && (action !== 'DeleteNotificationRule' || r.Arn === body.Arn));
      return r.Arn ?? body.Arn;
    },
  };
}

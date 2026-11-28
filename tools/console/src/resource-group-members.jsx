import React, {useCallback, useEffect, useRef, useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import Container from '@cloudscape-design/components/container';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';

const repositoryType = 'AWS::ECR::Repository';
const pageSize = 10;

export function resourceGroupDetail(service, resource, detail, region) {
  if (service?.model !== 'resource-groups' || resource?.kind !== 'group' ||
      !['GetGroupQuery', 'UpdateGroupQuery', 'ListGroupResources'].every(op => service.operations.includes(op))) return false;
  return !!detail?.Name && detail.GroupArn === 'arn:aws:resource-groups:' + region + ':' + service.account + ':group/' + detail.Name;
}

function readFilter(groupQuery, name) {
  if (groupQuery?.GroupName !== name || groupQuery.ResourceQuery?.Type !== 'TAG_FILTERS_1_0') throw Error('Invalid group query response.');
  const query = JSON.parse(groupQuery.ResourceQuery.Query);
  const tag = query.TagFilters?.[0];
  if (query.ResourceTypeFilters?.length !== 1 || query.ResourceTypeFilters[0] !== repositoryType ||
      query.TagFilters?.length !== 1 || typeof tag.Key !== 'string' || !tag.Key ||
      tag.Values?.length !== 1 || typeof tag.Values[0] !== 'string' || !tag.Values[0]) throw Error('Invalid repository tag filter.');
  return {key: tag.Key, value: tag.Values[0]};
}

export default function ResourceGroupMembers({name, arn, region, request, write, busy, t}) {
  const [query, setQuery] = useState(null), [page, setPage] = useState(null);
  const [edit, setEdit] = useState(null), [error, setError] = useState(''), [plain, setPlain] = useState(false);
  const [loading, setLoading] = useState(false);
  const current = useRef(null);
  const post = useCallback(async (path, fields, signal) => {
    const response = await request(path, {method: 'POST', signal,
      headers: {'Content-Type': 'application/json', 'X-Simulator-Service': 'resource-groups', 'X-Simulator-Region': region},
      body: JSON.stringify({Group: arn, ...fields})});
    return response.json();
  }, [arn, region, request]);

  const load = useCallback(async (token = '') => {
    current.current?.abort();
    const controller = new AbortController();
    current.current = controller;
    const {signal} = controller;
    setError(''); setPlain(false); setPage(null); setEdit(null); setQuery(null); setLoading(true);
    try {
      let response;
      try {
        response = await post('/get-group-query', {}, signal);
      } catch (error) {
        const missing = 'The group has no supported resource query.';
        if (error.message === '400: ' + JSON.stringify({Message: missing}) || error.message === '400: ' + missing) {
          if (!signal.aborted) setPlain(true);
          return;
        }
        throw error;
      }
      const saved = readFilter(response.GroupQuery, name);
      if (signal.aborted) return;
      setQuery(saved);
      const listed = await post('/list-group-resources', {MaxResults: pageSize, ...(token && {NextToken: token})}, signal);
      const rows = listed.Resources?.map(row => row.Identifier);
      if (!rows || rows.some(row => row?.ResourceType !== repositoryType || typeof row.ResourceArn !== 'string') ||
          (listed.NextToken !== undefined && (typeof listed.NextToken !== 'string' || !listed.NextToken || listed.NextToken === token))) {
        throw Error('Invalid resource membership response.');
      }
      if (!signal.aborted) setPage({rows, next: listed.NextToken, first: !token});
    } catch (error) {
      if (!signal.aborted) setError(error.message);
    } finally {
      if (!signal.aborted) setLoading(false);
    }
  }, [name, post]);

  useEffect(() => {
    load();
    return () => current.current?.abort();
  }, [load]);

  async function save(event) {
    event.preventDefault();
    if (busy || loading || !canSave) return;
    const release = write();
    if (!release) return;
    current.current?.abort();
    const controller = new AbortController();
    current.current = controller;
    const expected = edit;
    setPage(null); setQuery(null); setEdit(null); setError(''); setLoading(true);
    try {
      const Query = JSON.stringify({ResourceTypeFilters: [repositoryType], TagFilters: [{Key: expected.key, Values: [expected.value]}]});
      const result = await post('/update-group-query', {ResourceQuery: {Type: 'TAG_FILTERS_1_0', Query}}, controller.signal);
      const saved = readFilter(result.GroupQuery, name);
      if (saved.key !== expected.key || saved.value !== expected.value) throw Error('The response did not confirm the requested filter.');
      if (!controller.signal.aborted) await load();
    } catch (error) {
      if (!controller.signal.aborted) {
        setError(error.message + ' ' + t('Refresh before editing again.', 'もう一度編集する前に更新してください。'));
        setLoading(false);
      }
    } finally {
      release();
    }
  }

  const disabled = loading || busy;
  const canSave = query && edit && edit.key && edit.value && [...edit.key].length <= 128 && [...edit.value].length <= 256 &&
    (edit.key !== query.key || edit.value !== query.value);
  return <SpaceBetween size="l">
    <Container header={<Header variant="h2" actions={<SpaceBetween direction="horizontal" size="xs">
      <Button disabled={disabled} onClick={() => load()}>{t('Refresh query and members', 'クエリとメンバーを更新')}</Button>
      {query && !edit && <Button disabled={disabled} onClick={() => setEdit({...query})}>{t('Edit query', 'クエリを編集')}</Button>}
    </SpaceBetween>}>{t('Resource query', 'リソースクエリ')}</Header>}>
      <SpaceBetween size="s">
        {error && <Alert type="error">{error}</Alert>}
        {loading && <StatusIndicator type="loading">{t('Loading', '読み込み中')}</StatusIndicator>}
        {plain && <Box>{t('This group has no resource query.', 'このグループにはリソースクエリがありません。')}</Box>}
        {edit ? <form onSubmit={save}><SpaceBetween size="s"><ColumnLayout columns={2}>
          <FormField label={t('Query tag key', 'クエリのタグキー')} controlId="resource-group-query-key">
            <Input value={edit.key} disabled={disabled} onChange={e => setEdit({...edit, key: e.detail.value})}/>
          </FormField>
          <FormField label={t('Query tag value', 'クエリのタグ値')} controlId="resource-group-query-value">
            <Input value={edit.value} disabled={disabled} onChange={e => setEdit({...edit, value: e.detail.value})}/>
          </FormField>
        </ColumnLayout><SpaceBetween direction="horizontal" size="xs">
          <Button formAction="none" disabled={disabled} onClick={() => setEdit(null)}>{t('Cancel', 'キャンセル')}</Button>
          <Button formAction="submit" variant="primary" disabled={disabled || !canSave}>{t('Save query', 'クエリを保存')}</Button>
        </SpaceBetween></SpaceBetween></form> : query && <KeyValuePairs columns={3} items={[
          {label: t('Resource type', 'リソースタイプ'), value: repositoryType},
          {label: t('Query tag key', 'クエリのタグキー'), value: query.key},
          {label: t('Query tag value', 'クエリのタグ値'), value: query.value}
        ]}/>}
      </SpaceBetween>
    </Container>
    {page && <Table items={page.rows} trackBy="ResourceArn"
      header={<Header variant="h2" actions={<SpaceBetween direction="horizontal" size="xs">
        {!page.first && <Button disabled={disabled} onClick={() => load()}>{t('First membership page', 'メンバーの最初のページ')}</Button>}
        {page.next && <Button disabled={disabled} onClick={() => load(page.next)}>{t('Next membership page', 'メンバーの次のページ')}</Button>}
      </SpaceBetween>}>{t('Matching repositories', '一致するリポジトリ')}</Header>}
      columnDefinitions={[{id: 'arn', header: t('Repository ARN', 'リポジトリ ARN'), cell: row => <span className="catalog-key">{row.ResourceArn}</span>, isRowHeader: true}]}
      empty={<Box textAlign="center">{t('No matching repositories', '一致するリポジトリはありません')}</Box>}/>}
  </SpaceBetween>;
}

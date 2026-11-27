import React, {useEffect, useMemo, useRef, useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import ContentLayout from '@cloudscape-design/components/content-layout';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import Link from '@cloudscape-design/components/link';
import Modal from '@cloudscape-design/components/modal';
import Pagination from '@cloudscape-design/components/pagination';
import Select from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';
import Tabs from '@cloudscape-design/components/tabs';
import TextFilter from '@cloudscape-design/components/text-filter';
import {useLocale} from './i18n.jsx';
import VPCNativeRouteTables from './ec2-vpc-native-route-tables.jsx';
import {
  createVpcNativeRouteTable, nativeRouteTableFailure, validNativeRouteTableName,
  vpcNativeIPv4RoutesLimit, vpcNativeRouteTablesAvailable, vpcNativeRouteTableAssociationsAvailable,
  vpcNativeRouteTableReplacementAvailable,
} from './ec2-native-route-tables.mjs';
import {
  loadRouteTableConsoleInventory, loadRouteTableImplicitSubnets, loadRouteTableVpcContext,
  routeTableConsoleAvailable, routeTableConsoleFilter, routeTableConsoleHref, routeTableMainStatus,
  validRouteTableConsoleID,
} from './ec2-route-table-console.mjs';

export {routeTableConsoleAvailable};

const pageSize = 20;
const empty = Object.freeze([]);
const identifier = value => <span className="resource-identifier">{value}</span>;

function RouteReference({kind, id, vpcId, region, hrefForResource, onPick, disabled}) {
  const href = disabled ? '' : routeTableConsoleHref(hrefForResource, {kind, id, vpcId, region});
  if (href) return <Link href={href}>{identifier(id)}</Link>;
  if (onPick && !disabled) return <Button variant="inline-link" onClick={onPick}>{identifier(id)}</Button>;
  return identifier(id);
}

function MainLabel({status}) {
  const {t} = useLocale();
  return status === 'yes' ? t('Yes', 'はい') : status === 'no' ? t('No', 'いいえ') :
    t('Not reported', '記録なし');
}

function RouteTableDetails({table, owner, main, subnets, subnetStatus, reference, tab, onTab, onClose, busy}) {
  const {t} = useLocale();
  const routes = table.localRoute ? [{...table.localRoute, local: true}, ...table.routes] : table.routes;
  const managed = owner?.managedBy === 'ec2' ? 'EC2' : owner?.managedBy === 'cloudformation' ? 'CloudFormation' : t('Unconfirmed', '未確認');
  return <SpaceBetween size="s">
    <Header variant="h2" actions={<Button onClick={onClose} disabled={busy}>{t('Close details', '詳細を閉じる')}</Button>}>
      {table.name ? <>{table.name} ({identifier(table.id)})</> : identifier(table.id)}
    </Header>
    <Tabs activeTabId={tab} onChange={({detail}) => onTab(detail.activeTabId)} tabs={[
      {id: 'overview', label: t('Details', '詳細'), content: <SpaceBetween size="m">
        <KeyValuePairs columns={2} minColumnWidth={240} items={[
          {label: t('Route table ID', 'ルートテーブル ID'), value: identifier(table.id)},
          {label: t('VPC', 'VPC'), value: reference('vpc', table.vpc, table.vpc)},
          {label: t('Main route table', 'メインルートテーブル'), value: <MainLabel status={main}/>},
          {label: t('Managed by', '管理元'), value: managed},
        ]}/>
        <Table variant="embedded" trackBy="key" items={table.tags} header={<Header variant="h3">{t('Tags', 'タグ')}</Header>}
          columnDefinitions={[
            {id: 'key', header: t('Key', 'キー'), cell: row => row.key},
            {id: 'value', header: t('Value', '値'), cell: row => row.value},
          ]} empty={t('No tags are saved.', '保存済みのタグはありません。')}/>
      </SpaceBetween>},
      {id: 'routes', label: t('Routes', 'ルート'), content: <Table variant="embedded" trackBy="cidr" items={routes}
        header={<Header variant="h3" counter={'('+routes.length+')'}>{t('Routes', 'ルート')}</Header>}
        columnDefinitions={[
          {id: 'destination', header: t('Destination', '送信先'), cell: row => identifier(row.cidr)},
          {id: 'target', header: t('Target', 'ターゲット'), cell: row => row.local ? identifier('local') : reference('internet-gateway', row.gateway, table.vpc)},
          {id: 'origin', header: t('Origin', '作成元'), cell: row => row.local ? t('System (read-only)', 'システム（読み取り専用）') : t('Saved static route', '保存済みの静的ルート')},
        ]} empty={t('No routes are recorded in this table.', 'このテーブルに記録されたルートはありません。')}/>},
      {id: 'associations', label: t('Subnet associations', 'サブネットの関連付け'), content: <SpaceBetween size="m">
        <Table variant="embedded" trackBy="id" items={table.associations}
          header={<Header variant="h3" counter={'('+table.associations.length+')'}>{t('Explicit subnet associations', '明示的なサブネットの関連付け')}</Header>}
          columnDefinitions={[
            {id: 'subnet', header: t('Subnet ID', 'サブネット ID'), cell: row => reference('subnet', row.subnet, table.vpc)},
            {id: 'id', header: t('Association ID', '関連付け ID'), cell: row => identifier(row.id)},
          ]} empty={t('No explicit subnet associations are saved.', '明示的なサブネットの関連付けはありません。')}/>
        {subnetStatus === 'denied' ? <Alert type="info">{t('Implicit associations not verified: ec2:DescribeSubnets is denied.',
          '暗黙的な関連付けは未確認です。ec2:DescribeSubnets が拒否されました。')}</Alert> :
        subnetStatus === 'error' ? <Alert type="error">{t('Could not verify implicit associations. Refresh to try again.',
          '暗黙的な関連付けを確認できませんでした。更新して再試行してください。')}</Alert> :
        subnetStatus === 'loading' ? <StatusIndicator type="loading">{t('Verifying implicit associations', '暗黙的な関連付けを確認中')}</StatusIndicator> :
        !subnets ? <Box color="text-body-secondary">{t('Implicit associations are not reported for this table.',
          'このテーブルの暗黙的な関連付けは記録されていません。')}</Box> :
        <Table variant="embedded" trackBy="id" items={subnets.items}
          header={<Header variant="h3" counter={'('+subnets.items.length+')'}
            description={t('These subnets use the main table implicitly.', 'これらのサブネットにはメインルートテーブルが暗黙的に適用されます。')}>
            {t('Subnets without explicit associations', '明示的な関連付けのないサブネット')}</Header>}
          columnDefinitions={[
            {id: 'subnet', header: t('Subnet ID', 'サブネット ID'), cell: row => reference('subnet', row.id, table.vpc)},
            {id: 'cidr', header: t('IPv4 CIDR', 'IPv4 CIDR'), cell: row => identifier(row.cidr)},
            {id: 'effective', header: t('Effective route table', '適用されるルートテーブル'),
              cell: () => reference('route-table', subnets.mainId, table.vpc)},
          ]} empty={t('Every known subnet has an explicit association.', '確認できたすべてのサブネットには明示的な関連付けがあります。')}/>}
      </SpaceBetween>},
    ]}/>
  </SpaceBetween>;
}

export default function EC2RouteTableConsole({
  request, region, capabilities, authenticationRequired, routeTableId = '', vpcId = '',
  onSaving, hrefForResource, onSelectRouteTable,
}) {
  const {t} = useLocale();
  const available = routeTableConsoleAvailable(capabilities, authenticationRequired);
  const native = vpcNativeRouteTablesAvailable(capabilities, authenticationRequired);
  const associations = vpcNativeRouteTableAssociationsAvailable(capabilities, authenticationRequired);
  const replacement = vpcNativeRouteTableReplacementAvailable(capabilities, authenticationRequired);
  const routeLimit = vpcNativeIPv4RoutesLimit(capabilities, authenticationRequired);
  const operations = capabilities?.services?.ec2?.operations;
  const vpcRead = Array.isArray(operations) && operations.includes('DescribeVpcs');
  const subnetRead = Array.isArray(operations) && operations.includes('DescribeSubnets');
  const [revision, refresh] = useState(0);
  const scope = useMemo(() => ({request, region, revision, available, vpcRead}), [request, region, revision, available, vpcRead]);
  const viewKey = useMemo(() => ({request, region, routeTableId, vpcId}), [request, region, routeTableId, vpcId]);
  const [data, setData] = useState(null), [vpcData, setVpcData] = useState(null), [subnetData, setSubnetData] = useState(null);
  const [filter, setFilter] = useState(''), [page, setPage] = useState(1);
  const [selection, setSelection] = useState(null), [tabChoice, setTabChoice] = useState(null);
  const [createDraft, setCreateDraft] = useState(null), [notice, setNotice] = useState(null), [busy, setBusy] = useState(false);
  const write = useRef(null), activeCreate = useRef(null);
  const current = data?.scope === scope ? data : null;
  const contexts = vpcData?.scope === scope ? vpcData : null;
  const tables = current?.rows ?? empty;
  const selected = selection?.key === viewKey ? selection.id : routeTableId;
  const detail = validRouteTableConsoleID(selected) ? tables.find(row => row.id === selected && (!vpcId || row.vpc === vpcId)) : null;
  const tab = tabChoice?.key === viewKey && tabChoice.id === selected ? tabChoice.tab : 'overview';
  const owner = detail && contexts?.rows?.find(vpc => vpc.id === detail.vpc);

  function acquireWrite() {
    if (write.current || current?.status !== 'ready') return null;
    const lease = {};
    write.current = lease;
    setBusy(true);
    onSaving?.(true);
    return () => {
      if (write.current !== lease) return;
      write.current = null;
      setBusy(false);
      onSaving?.(false);
    };
  }

  useEffect(() => {
    if (!scope.available) return;
    const controller = new AbortController(), options = {signal: controller.signal};
    loadRouteTableConsoleInventory(scope.request, scope.region, options).then(rows => {
      if (!controller.signal.aborted) setData({scope, status: 'ready', rows});
    }, error => {
      if (!controller.signal.aborted) setData({scope, status: nativeRouteTableFailure(error) === 'denied' ? 'denied' : 'error'});
    });
    if (scope.vpcRead) loadRouteTableVpcContext(scope.request, scope.region, options).then(rows => {
      if (!controller.signal.aborted) setVpcData({scope, status: 'ready', rows});
    }, error => {
      if (!controller.signal.aborted) setVpcData({scope, status: nativeRouteTableFailure(error) === 'denied' ? 'denied' : 'error'});
    });
    return () => {
      controller.abort();
      const operation = activeCreate.current;
      if (operation?.scope === scope) {
        activeCreate.current = null;
        operation.controller.abort();
        operation.release();
      }
    };
  }, [scope]);

  const subnetScope = useMemo(() => scope.available && subnetRead && owner?.managedBy === 'ec2' && tab === 'associations' ?
    {scope, owner, tables} : null, [scope, owner, tables, tab, subnetRead]);
  useEffect(() => {
    if (!subnetScope) return;
    const {scope: active, owner: target, tables: matching} = subnetScope;
    const controller = new AbortController();
    loadRouteTableImplicitSubnets(active.request, active.region, target, matching, {signal: controller.signal})
      .then(value => {
        if (!controller.signal.aborted) setSubnetData({scope: subnetScope, status: 'ready', value});
      }, error => {
        if (!controller.signal.aborted) setSubnetData({scope: subnetScope,
          status: nativeRouteTableFailure(error) === 'denied' ? 'denied' : 'error'});
      });
    return () => controller.abort();
  }, [subnetScope]);

  function select(id, targetVpc = vpcId, replace = false) {
    if (!write.current) {
      if (onSelectRouteTable) {onSelectRouteTable(id, targetVpc, replace);return;}
      setSelection({key: viewKey, id});
      setTabChoice(null);
    }
  }

  function reference(kind, id, targetVpc, localAction) {
    return <RouteReference kind={kind} id={id} vpcId={targetVpc} region={region}
      hrefForResource={hrefForResource} disabled={busy}
      onPick={localAction ?? (kind === 'route-table' ? () => select(id) : undefined)}/>;
  }

  function reload() {
    if (write.current) return;
    setPage(1);
    setNotice(null);
    setCreateDraft(null);
    refresh(value => value + 1);
  }

  function mutation(change) {
    setNotice({key: viewKey, ...change});
    if (change.confirmed && change.kind === 'delete') select('', vpcId, true);
    refresh(value => value + 1);
  }

  const nativeVpcs = (contexts?.rows ?? empty).filter(row => row.managedBy === 'ec2' && (!vpcId || row.id === vpcId));
  const draft = createDraft?.scope === scope ? createDraft : null;
  const draftVpc = draft && nativeVpcs.find(row => row.id === draft.vpcId);
  const createEnabled = current?.status === 'ready' && native && nativeVpcs.length > 0 && !busy;
  function editDraft(key, value) {
    setCreateDraft(previous => previous?.scope === scope ? {...previous, [key]: value} : previous);
  }

  async function create(event) {
    event.preventDefault();
    if (!createEnabled || !draftVpc || !validNativeRouteTableName(draft.name)) return;
    const release = acquireWrite();
    if (!release) return;
    const controller = new AbortController(), operation = {scope, controller, release};
    activeCreate.current = operation;
    let change;
    try {
      const token = Array.from(globalThis.crypto.getRandomValues(new Uint8Array(16)), byte =>
        byte.toString(16).padStart(2, '0')).join('');
      const {created} = await createVpcNativeRouteTable(request, region, draftVpc, draft.name, token,
        {signal: controller.signal, associations, replacement, routeLimit});
      change = {kind: 'create', id: created.id, confirmed: true};
    } catch (error) {
      change = {kind: 'create', confirmed: false, error: nativeRouteTableFailure(error)};
    }
    if (activeCreate.current !== operation || controller.signal.aborted) return;
    activeCreate.current = null;
    release();
    setCreateDraft(null);
    if (change.confirmed) select(change.id, draftVpc.id, true);
    mutation(change);
  }

  const title = t('Route tables', 'ルートテーブル');
  const heading = <Header variant="h1">{title}</Header>;
  if (!available) return <ContentLayout header={heading}>
    <Alert type="info">{t('Route table discovery is unavailable in this console scope.',
      '現在のコンソールの範囲では、ルートテーブルを参照できません。')}</Alert>
  </ContentLayout>;
  const loading = !current;
  const filtered = routeTableConsoleFilter(tables, filter, vpcId);
  const pages = Math.max(1, Math.ceil(filtered.length / pageSize));
  const displayed = Math.min(page, pages);
  const confirmedSubnets = subnetScope && subnetData?.scope === subnetScope ? subnetData : null;
  const activeNotice = notice?.key === viewKey ? notice : null;
  const canShowEditor = native && owner?.managedBy === 'ec2' && detail &&
    (tab === 'overview' || tab === 'routes' && routeLimit || tab === 'associations' && associations);
  const createTitle = t('Create route table', 'ルートテーブルを作成');
  const close = t('Cancel', 'キャンセル');
  return <ContentLayout header={heading}><SpaceBetween size="l">
    <Alert type="info">{t('This page shows saved IPv4 routing and subnet associations. The simulator does not forward packets or test network reachability.',
      '保存済みの IPv4 ルートとサブネットの関連付けを表示します。シミュレーターはパケットを転送せず、通信の到達性も検証しません。')}</Alert>
    {activeNotice && <Alert type={activeNotice.confirmed ? 'success' : 'error'} dismissible
      dismissAriaLabel={t('Dismiss', '閉じる')} onDismiss={() => setNotice(null)}>
      {activeNotice.confirmed ? <>{t('Saved route table change verified: ', 'ルートテーブルの保存済み変更を確認しました: ')}{identifier(activeNotice.id)}</> :
        activeNotice.error === 'denied' ? t('The selected identity cannot complete this route table action.', '現在の ID ではルートテーブルを操作できません。') :
        t('The change was not confirmed. Verify the refreshed inventory before trying again.', '変更を確認できませんでした。再試行する前に更新後の設定を確認してください。')}
    </Alert>}
    {current?.status === 'denied' && <Alert type="error">{t('Route table access denied: ec2:DescribeRouteTables is required.',
      'ルートテーブルへのアクセスが拒否されました。ec2:DescribeRouteTables が必要です。')}</Alert>}
    {current?.status === 'error' && <Alert type="error">{t('Could not read the complete route table inventory. Refresh to try again.',
      'ルートテーブルの一覧をすべて取得できませんでした。更新して再試行してください。')}</Alert>}
    {current?.status === 'ready' && (!scope.vpcRead || contexts && contexts.status !== 'ready') && <Alert type="info">
      {t('VPC ownership could not be verified. Route tables are read-only.', 'VPC の管理元を確認できなかったため、ルートテーブルは読み取り専用です。')}
    </Alert>}
    {vpcId && <SpaceBetween direction="horizontal" size="m"><Box>{t('VPC filter: ', 'VPC の絞り込み: ')}
      {reference('vpc', vpcId, vpcId)}</Box>
      {onSelectRouteTable&&<Button disabled={busy} onClick={()=>select('', '')}>{t('Clear VPC filter','VPC の絞り込みを解除')}</Button>}
    </SpaceBetween>}
    <Table trackBy="id" items={filtered.slice((displayed-1)*pageSize, displayed*pageSize)}
      loading={loading} loadingText={t('Reading route tables', 'ルートテーブルを読み込み中')}
      ariaLabels={{tableLabel: title}} header={<Header counter={current?.status === 'ready' ? '('+filtered.length+')' : undefined}
        actions={<SpaceBetween direction="horizontal" size="xs">
          <Button iconName="refresh" disabled={busy} onClick={reload}>{t('Refresh route tables', 'ルートテーブルを更新')}</Button>
          {native && <Button variant="primary" disabled={!createEnabled} onClick={() => setCreateDraft({scope,
            vpcId: nativeVpcs.length === 1 ? nativeVpcs[0].id : '', name: ''})}>{createTitle}</Button>}
        </SpaceBetween>}>{title}</Header>}
      filter={<TextFilter filteringText={filter} disabled={busy} onChange={({detail: change}) => {setFilter(change.filteringText);setPage(1);}}
        filteringAriaLabel={t('Filter route tables', 'ルートテーブルを絞り込む')}
        filteringPlaceholder={t('Find route tables', 'ルートテーブルを検索')}/>}
      pagination={pages > 1 && <Pagination currentPageIndex={displayed} pagesCount={pages} disabled={busy}
        onChange={({detail: change}) => setPage(change.currentPageIndex)}
        ariaLabels={{previousPageLabel:t('Previous page','前のページ'),nextPageLabel:t('Next page','次のページ'),pageLabel:n=>String(n)}}/>}
      columnDefinitions={[
        {id: 'name', header: t('Name', '名前'), cell: row => row.name || '—'},
        {id: 'id', header: t('Route table ID', 'ルートテーブル ID'), cell: row => reference('route-table', row.id, row.vpc)},
        {id: 'vpc', header: 'VPC', cell: row => reference('vpc', row.vpc, row.vpc)},
        {id: 'main', header: t('Main', 'メイン'), cell: row => <MainLabel status={routeTableMainStatus(tables, row)}/>},
        {id: 'explicit', header: t('Explicit subnet associations', '明示的なサブネットの関連付け'), cell: row => row.associations.length},
      ]}
      empty={current?.status !== 'ready' ? t('Route table inventory unavailable.', 'ルートテーブルの一覧を取得できません。') :
        filter.trim() ? t('No route tables match this filter.', '絞り込みに一致するルートテーブルはありません。') :
        t('No route tables are recorded for this selection.', 'この範囲に記録されたルートテーブルはありません。')}/>
    {selected && current?.status === 'ready' && !detail && <Alert type="info">{t('The selected route table is not in this inventory.',
      '選択したルートテーブルは、この一覧にはありません。')}</Alert>}
    {detail && <SpaceBetween size="m">
      <RouteTableDetails table={detail} owner={owner} main={routeTableMainStatus(tables, detail)}
        tab={tab} onTab={next => {if (!write.current) setTabChoice({key: viewKey, id: detail.id, tab: next});}}
        onClose={() => select('')} reference={reference} busy={busy} subnets={confirmedSubnets?.value}
        subnetStatus={subnetScope ? confirmedSubnets?.status ?? 'loading' : 'unreported'}/>
      {canShowEditor && <VPCNativeRouteTables vpc={owner} request={request} region={region} acquireWrite={acquireWrite}
        focusedTableId={detail.id} actionView={tab} onMutation={mutation}
        associations={associations} replacement={replacement} routeLimit={routeLimit}/>}
      {owner?.managedBy === 'cloudformation' && <Box color="text-body-secondary">
        {t('Manage changes through the owning CloudFormation stack.', '変更は所有元の CloudFormation スタックで管理してください。')}
      </Box>}
    </SpaceBetween>}
    {draft && <Modal visible header={createTitle} closeAriaLabel={close}
      onDismiss={() => {if (!write.current) setCreateDraft(null);}}>
      <form onSubmit={create}><SpaceBetween size="m">
        <FormField label="VPC">
          <Select options={nativeVpcs.map(row => ({label: row.id, value: row.id, description: row.name || undefined}))}
            selectedOption={draftVpc ? {label: draftVpc.id, value: draftVpc.id} : null} disabled={busy}
            placeholder={t('Choose an EC2-managed VPC', 'EC2 が管理する VPC を選択')}
            onChange={({detail: change}) => editDraft('vpcId', change.selectedOption.value)}/>
        </FormField>
        <FormField label={t('Name (optional)', '名前（任意）')}
          errorText={!validNativeRouteTableName(draft.name) ? t('Enter valid text of up to 256 UTF-8 bytes.',
            'UTF-8 で 256 バイト以内の有効な文字を入力してください。') : undefined}>
          <Input value={draft.name} disabled={busy} onChange={({detail: change}) => editDraft('name', change.value)}/>
        </FormField>
        <SpaceBetween direction="horizontal" size="xs">
          <Button formAction="none" disabled={busy} onClick={() => setCreateDraft(null)}>{close}</Button>
          <Button variant="primary" formAction="submit" loading={busy}
            disabled={!createEnabled || !draftVpc || !validNativeRouteTableName(draft.name)}>{createTitle}</Button>
        </SpaceBetween>
      </SpaceBetween></form>
    </Modal>}
  </SpaceBetween></ContentLayout>;
}

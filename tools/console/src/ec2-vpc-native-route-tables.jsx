import React,{useEffect,useMemo,useRef,useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Modal from '@cloudscape-design/components/modal';
import Pagination from '@cloudscape-design/components/pagination';
import Select from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import {useLocale} from './i18n.jsx';
import {associateVpcRouteSubnet,createVpcNativeRouteTable,deleteVpcNativeRouteTable,disassociateVpcRouteSubnet,
  loadVpcNativeRouteTables,loadVpcRouteAssociationTargets,nativeRouteTableFailure,nativeRouteTableTarget,
  validNativeRouteTableName} from './ec2-native-route-tables.mjs';

const pageSize=20;
const identifier=value=><span className="resource-identifier">{value}</span>;
const newToken=()=>Array.from(globalThis.crypto.getRandomValues(new Uint8Array(16)),b=>b.toString(16).padStart(2,'0')).join('');

export default function VPCNativeRouteTables({request,region,vpc,acquireWrite,writeBlocked=false,associations=false}) {
  const {t}=useLocale();
  const {id,managedBy,cidr}=vpc??{};
  const scope=useMemo(()=>({request,region,vpc:{id,managedBy,cidr},associations}),[request,region,id,managedBy,cidr,associations]);
  const validScope=nativeRouteTableTarget(scope.vpc);
  const active=useRef(null);
  const [result,setResult]=useState(null),[modal,setModal]=useState(null),[notice,setNotice]=useState(null),[page,setPage]=useState(1);
  const current=result?.scope===scope ? result : null;
  const dialog=modal?.scope===scope ? modal : null;
  const ready=current?.status==='ready',pending=current?.status==='writing';
  const canWrite=typeof acquireWrite==='function'&&!writeBlocked;

  function retire() {
    const previous=active.current;
    if (!previous) return;
    active.current=null;
    previous.controller.abort();
    previous.release?.();
  }
  function begin(writing) {
    if (active.current?.release) return null;
    const release=writing ? acquireWrite?.() : null;
    if (writing && typeof release!=='function') return null;
    retire();
    return active.current={controller:new AbortController(),release};
  }
  function finish(operation) {
    if (active.current!==operation || operation.controller.signal.aborted) return false;
    active.current=null;
    operation.release?.();
    return true;
  }
  async function refresh() {
    if (!validScope) return;
    const operation=begin(false);
    if (!operation) return;
    setResult({scope,status:'loading'});setModal(null);setNotice(null);setPage(1);
    try {
      const rows=await loadVpcNativeRouteTables(scope.request,scope.region,scope.vpc,{signal:operation.controller.signal,associations:scope.associations});
      if (finish(operation)) setResult({scope,status:'ready',rows});
    } catch(error) {
      if (finish(operation)) setResult({scope,status:nativeRouteTableFailure(error)});
    }
  }
  useEffect(()=>{void refresh();return retire;},[scope]);

  // Load only on opening the association form. A DescribeSubnets denial
  // must not hide readable tables or their saved associations.
  const draft=dialog?.kind==='associate' ? dialog.draft : null;
  useEffect(()=>{
    if (!draft || !scope.associations) return;
    const controller=new AbortController();
    function save(choices,error) {
      if (!controller.signal.aborted) setModal(old=>old?.draft===draft ? {...old,choices,choiceError:error} : old);
    }
    loadVpcRouteAssociationTargets(scope.request,scope.region,scope.vpc,draft.row,{signal:controller.signal,associations:true})
      .then(choices=>save(choices,null),error=>save(null,nativeRouteTableFailure(error)));
    return ()=>controller.abort();
  },[draft,scope]);

  const attaching=dialog?.kind==='associate', detaching=dialog?.kind==='disassociate';
  const selections=attaching ? dialog.choices : detaching ? dialog.row.associations : null;
  const target=selections?.find(choice=>choice.id===dialog.selected);
  const currentRow=dialog?.row&&current?.rows?.includes(dialog.row);
  const valid=dialog?.kind==='create' ? validNativeRouteTableName(dialog.name) :
    currentRow&&(attaching ? scope.associations&&target :
      detaching ? scope.associations&&target&&dialog.confirm===target.id :
      dialog?.kind==='delete'&&!dialog.row.associations?.length&&dialog.confirm===dialog.row.id);
  async function submit(event) {
    event.preventDefault();
    if (!validScope || !ready || !canWrite || !valid) return;
    const operation=begin(true);
    if (!operation) return;
    setResult({...current,status:'writing'});setNotice(null);
    try {
      const args=[scope.request,scope.region,scope.vpc];
      const options={signal:operation.controller.signal,associations:scope.associations};
      const {created,removed,associated,disassociated,rows}=dialog.kind==='create'
        ? await createVpcNativeRouteTable(...args,dialog.name,newToken(),options)
        : attaching ? await associateVpcRouteSubnet(...args,dialog.row,target,options)
          : detaching ? await disassociateVpcRouteSubnet(...args,dialog.row,target,options)
            : await deleteVpcNativeRouteTable(...args,dialog.row,options);
      if (!finish(operation)) return;
      setResult({scope,status:'ready',rows});setModal(null);
      setNotice({scope,kind:dialog.kind,id:(created??removed??associated??disassociated).id});
    } catch(error) {
      if (!finish(operation)) return;
      // The write may have happened without a reliable reply or readback.
      // Hide stale rows and require fresh state rather than replaying it.
      setResult({scope,status:nativeRouteTableFailure(error),refreshBeforeWrite:true});setModal(null);
    }
  }
  if (!validScope) return null;

  const errors={
    denied:t('The selected identity cannot complete this route table request. Check its EC2 permissions.','現在の ID ではルートテーブルを操作できません。EC2 の権限を確認してください。'),
    'not-found':t('The selected VPC, route table, subnet or association is no longer available.','選択した VPC、ルートテーブル、サブネット、または関連付けは利用できなくなりました。'),
    associated:t('This subnet already has an explicit association. No association was replaced.','このサブネットには既に明示的な関連付けがあります。関連付けの置き換えは行っていません。'),
    dependency:t('A saved dependency prevents deletion. The route table was not removed.','保存済みの依存関係があるため、ルートテーブルを削除できませんでした。'),
    limit:t('The local route table resource or request limit was reached.','ローカルのルートテーブルのリソース数またはリクエスト数が上限に達しました。'),
    unconfirmed:t('Could not confirm the saved route table state. No result was reported.','ルートテーブルの保存状態を確認できませんでした。結果は表示していません。'),
  };
  const rows=current?.rows??[],pages=Math.max(1,Math.ceil(rows.length/pageSize)),shown=Math.min(page,pages);
  const loading=!current || current.status==='loading';
  const refreshLabel=t('Refresh route tables','ルートテーブルを更新');
  const createLabel=t('Create route table','ルートテーブルを作成');
  const deleteLabel=t('Delete route table','ルートテーブルを削除');
  const associateLabel=t('Associate subnet','サブネットを関連付ける');
  const disassociateLabel=t('Disassociate subnet','サブネットの関連付けを解除する');
  const modalTitle={create:createLabel,delete:deleteLabel,associate:associateLabel,disassociate:disassociateLabel}[dialog?.kind];
  const notices={
    create:t('Saved route table: ','ルートテーブルを保存しました: '),
    delete:t('Deleted route table: ','ルートテーブルを削除しました: '),
    associate:t('Saved explicit subnet association: ','明示的なサブネットの関連付けを保存しました: '),
    disassociate:t('Removed explicit subnet association: ','明示的なサブネットの関連付けを解除しました: '),
  };
  const associationOptions=(selections??[]).map(choice=>({value:choice.id,label:attaching?choice.id:choice.subnet,
    description:attaching?choice.cidr:choice.id}));
  function change(key,value){setModal(old=>old?.scope===scope ? {...old,[key]:value} : old);}
  return <SpaceBetween size="s">
    <Header variant="h3" actions={<SpaceBetween direction="horizontal" size="xs">
      <Button iconName="refresh" disabled={pending} loading={loading} onClick={refresh}>{refreshLabel}</Button>
      <Button disabled={!ready||!canWrite} onClick={()=>setModal({scope,kind:'create',name:''})}>{createLabel}</Button>
    </SpaceBetween>}>{t('Route tables','ルートテーブル')}</Header>
    <Box color="text-body-secondary">{scope.associations
      ? t('Saved configuration only. Explicit subnet associations do not create routes, a main table or packet forwarding.','保存済みの設定のみです。サブネットの明示的な関連付けは、ルートやメインテーブルを作成せず、パケットも転送しません。')
      : t('Detached configuration only. These tables have no routes or subnet associations and do not forward traffic.','関連付けのない設定のみです。ルートやサブネットの関連付けは含まれず、通信は転送しません。')}</Box>
    {notice?.scope===scope&&<Alert type="success">{notices[notice.kind]}{identifier(notice.id)}</Alert>}
    {errors[current?.status]&&<Alert type="error">{errors[current.status]} {current.refreshBeforeWrite&&
      t('Refresh route tables before trying again.','再試行する前にルートテーブルを更新してください。')}</Alert>}
    <Table variant="embedded" trackBy="id" items={rows.slice((shown-1)*pageSize,shown*pageSize)} loading={loading||pending}
      loadingText={t('Reading route tables','ルートテーブルを読み込み中')}
      ariaLabels={{tableLabel:scope.associations?t('Saved route tables','保存済みのルートテーブル'):t('Saved detached route tables','保存済みの関連付けのないルートテーブル')}}
      columnDefinitions={[
        {id:'id',header:t('Route table ID','ルートテーブル ID'),cell:item=>identifier(item.id)},
        {id:'name',header:t('Name','名前'),cell:item=>item.name||'—'},
        ...(scope.associations?[{id:'associations',header:t('Explicit subnet associations','明示的なサブネットの関連付け'),
          cell:item=>item.associations.length ? item.associations.map(row=><div key={row.id}>{identifier(row.subnet)}</div>)
            : t('None','なし')}]:[]),
        {id:'actions',header:t('Actions','操作'),cell:item=><SpaceBetween size="xxs">
          {scope.associations&&<>
            <Button disabled={!ready||!canWrite} ariaLabel={associateLabel+': '+item.id}
              onClick={()=>setModal({scope,kind:'associate',row:item,draft:{row:item},selected:''})}>{associateLabel}</Button>
            <Button disabled={!ready||!canWrite||!item.associations.length} ariaLabel={disassociateLabel+': '+item.id}
              onClick={()=>setModal({scope,kind:'disassociate',row:item,selected:'',confirm:''})}>{disassociateLabel}</Button>
          </>}
          <Button disabled={!ready||!canWrite||!!item.associations?.length} ariaLabel={deleteLabel+': '+item.id}
            disabledReason={item.associations?.length?t('Remove the explicit associations before deleting this table.','このテーブルを削除するには明示的な関連付けを先に解除してください。'):undefined}
            onClick={()=>setModal({scope,kind:'delete',row:item,confirm:''})}>{deleteLabel}</Button>
        </SpaceBetween>},
      ]}
      pagination={pages>1&&<Pagination currentPageIndex={shown} pagesCount={pages} onChange={({detail})=>setPage(detail.currentPageIndex)}
        ariaLabels={{previousPageLabel:t('Previous page','前のページ'),nextPageLabel:t('Next page','次のページ'),pageLabel:n=>String(n)}}/>}
      empty={ready ? scope.associations
        ?t('No route tables are saved for this VPC.','この VPC に保存されたルートテーブルはありません。')
        :t('No detached route tables are saved for this VPC.','この VPC に関連付けのないルートテーブルはありません。')
        :t('Route tables are unavailable.','ルートテーブルを取得できません。')}/>
    {dialog&&<Modal visible header={modalTitle}
      closeAriaLabel={t('Close route table dialog','ルートテーブルのダイアログを閉じる')}
      onDismiss={()=>{if (!active.current?.release) setModal(null);}}>
      <form onSubmit={submit}><Form actions={<SpaceBetween direction="horizontal" size="xs">
        <Button formAction="none" disabled={pending} onClick={()=>setModal(null)}>{t('Cancel','キャンセル')}</Button>
        <Button variant="primary" formAction="submit" loading={pending} disabled={!valid||!canWrite||!ready}>
          {modalTitle}
        </Button>
      </SpaceBetween>}><SpaceBetween size="m">
        {dialog.kind==='create'?<>
          <Box>{t('Selected VPC: ','選択済み VPC: ')}{identifier(id)}</Box>
          <FormField label={t('Name (optional)','名前（任意）')}
            constraintText={t('The Name tag can use up to 256 UTF-8 bytes.','Name タグは UTF-8 で 256 バイトまで入力できます。')}
            errorText={!valid?t('Use valid text of at most 256 UTF-8 bytes.','UTF-8 で 256 バイト以内の有効な文字を入力してください。'):undefined}>
            <Input value={dialog.name} disabled={pending} onChange={({detail})=>change('name',detail.value)}/>
          </FormField>
        </>:attaching||detaching?<>
          <Box>{t('Selected route table: ','選択済みルートテーブル: ')}{identifier(dialog.row.id)}</Box>
          {dialog.choiceError ? <Alert type="error">{dialog.choiceError==='denied'
            ? t('Subnet discovery was denied. The selected identity needs ec2:DescribeSubnets.','サブネットの参照が拒否されました。現在の ID に ec2:DescribeSubnets 権限が必要です。')
            : t('Could not verify the available subnets. No request was sent to change associations.','利用可能なサブネットを確認できません。関連付けを変更するリクエストは送信していません。')}</Alert>
            : <FormField label={attaching?t('Unassociated subnet in this VPC','この VPC 内の未関連付けサブネット')
              :t('Saved explicit association','保存済みの明示的な関連付け')}>
              <Select options={associationOptions} selectedOption={associationOptions.find(choice=>choice.value===dialog.selected)??null}
                disabled={pending||!associationOptions.length} statusType={selections?'finished':'loading'}
                loadingText={t('Reading available subnets','利用可能なサブネットを読み込み中')}
                placeholder={selections&&!selections.length?t('No unassociated subnets are available.','関連付け可能なサブネットはありません。'):t('Choose an entry','項目を選択')}
                onChange={({detail})=>setModal(old=>old===dialog ? {...old,selected:detail.selectedOption.value,confirm:''} : old)}/>
            </FormField>}
          {detaching&&target&&<FormField label={t('Enter the association ID to confirm removal','解除を確認するには関連付け ID を入力')}>
            <Input value={dialog.confirm} disabled={pending} onChange={({detail})=>change('confirm',detail.value)}/>
          </FormField>}
        </>:<>
          <Box>{t('Delete this detached configuration: ','関連付けのない設定を削除します: ')}{identifier(dialog.row.id)}</Box>
          <FormField label={t('Enter the route table ID to confirm deletion','削除を確認するにはルートテーブル ID を入力')}>
            <Input value={dialog.confirm} disabled={pending} onChange={({detail})=>change('confirm',detail.value)}/>
          </FormField>
        </>}
      </SpaceBetween></Form></form>
    </Modal>}
  </SpaceBetween>;
}

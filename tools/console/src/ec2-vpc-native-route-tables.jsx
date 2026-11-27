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
  replaceVpcRouteSubnet,loadVpcNativeRouteTables,loadVpcNativeRouteFallback,loadVpcRouteAssociationTargets,nativeRouteTableFailure,nativeRouteTableTarget,
  validNativeRouteTableName,changeVpcNativeIPv4Route,loadVpcNativeRouteGateway} from './ec2-native-route-tables.mjs';

import {ipv4DefaultRoute,nativeIPv4RouteDestination} from './ec2-ipv4-route-prefix.mjs';

const pageSize=20;
const identifier=value=><span className="resource-identifier">{value}</span>;
const newToken=()=>Array.from(globalThis.crypto.getRandomValues(new Uint8Array(16)),b=>b.toString(16).padStart(2,'0')).join('');

export default function VPCNativeRouteTables({request,region,vpc,acquireWrite,writeBlocked=false,associations=false,replacement=false,routeLimit=0}) {
  const {t}=useLocale();
  const {id,managedBy,cidr}=vpc??{};
  const scope=useMemo(()=>({request,region,vpc:{id,managedBy,cidr},associations,replacement,routeLimit}),
    [request,region,id,managedBy,cidr,associations,replacement,routeLimit]);
  const validScope=nativeRouteTableTarget(scope.vpc);
  const options=signal=>({signal,associations:scope.associations,replacement:scope.replacement,routeLimit:scope.routeLimit});
  const active=useRef(null);
  const [result,setResult]=useState(null),[modal,setModal]=useState(null),[notice,setNotice]=useState(null),[page,setPage]=useState(1);
  const [fallback,setFallback]=useState(null);
  const current=result?.scope===scope ? result : null;
  const dialog=modal?.scope===scope ? modal : null;
  const ready=current?.status==='ready',pending=current?.status==='writing';
  const readyRows=ready?current.rows:null;
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
      const rows=await loadVpcNativeRouteTables(scope.request,scope.region,scope.vpc,options(operation.controller.signal));
      if (finish(operation)) setResult({scope,status:'ready',rows});
    } catch(error) {
      if (finish(operation)) setResult({scope,status:nativeRouteTableFailure(error)});
    }
  }
  useEffect(()=>{void refresh();return retire;},[scope]);

  // A failed subnet inventory leaves the confirmed tables and explicit
  // associations visible, but never turns an unknown fallback into "none".
  useEffect(()=>{
    if (!readyRows?.some(row=>row.mainAssociation)) return;
    const controller=new AbortController();
    const save=(value,error)=>{
      if(!controller.signal.aborted) setFallback({scope,rows:readyRows,value,error});
    };
    loadVpcNativeRouteFallback(scope.request,scope.region,scope.vpc,readyRows,options(controller.signal))
      .then(value=>save(value,null),error=>save(null,nativeRouteTableFailure(error)));
    return ()=>controller.abort();
  },[scope,readyRows]);

  // A separate, current read when opening an explicit association form
  // gives the form a scoped target rather than routing presentation data.
  const draft=dialog?.kind==='associate' ? dialog.draft : null;
  useEffect(()=>{
    if (!draft || !scope.associations) return;
    const controller=new AbortController();
    function save(choices,error) {
      if (!controller.signal.aborted) setModal(old=>old?.draft===draft ? {...old,choices,choiceError:error} : old);
    }
    loadVpcRouteAssociationTargets(scope.request,scope.region,scope.vpc,draft.row,options(controller.signal))
      .then(choices=>save(choices,null),error=>save(null,nativeRouteTableFailure(error)));
    return ()=>controller.abort();
  },[draft,scope]);

  const routeDraft=dialog?.kind==='add-route' ? dialog.draft : null;
  useEffect(()=>{
    if (!routeDraft || !scope.routeLimit) return;
    const controller=new AbortController();
    function save(gateway,error) {
      if (!controller.signal.aborted) setModal(old=>old?.draft===routeDraft ? {...old,gateway,gatewayError:error} : old);
    }
    loadVpcNativeRouteGateway(scope.request,scope.region,scope.vpc,routeDraft.row,options(controller.signal))
      .then(gateway=>save(gateway,null),error=>save(null,nativeRouteTableFailure(error)));
    return ()=>controller.abort();
  },[routeDraft,scope]);

  const addingRoute=dialog?.kind==='add-route', removingRoute=dialog?.kind==='remove-route';
  const savedRoutes=removingRoute?dialog.row.routes:[];
  const selectedRoute=removingRoute?(savedRoutes.length===1?savedRoutes[0]:savedRoutes.find(route=>route.cidr===dialog.routeSelected)):null;
  const routeTarget=addingRoute?dialog.gateway:selectedRoute;
  const destination=addingRoute?nativeIPv4RouteDestination(dialog.destination,scope.vpc.cidr):selectedRoute?.cidr;
  const duplicateRoute=addingRoute&&dialog.row.routes.some(route=>route.cidr===destination);
  const attaching=dialog?.kind==='associate',detaching=dialog?.kind==='disassociate',replacing=dialog?.kind==='replace';
  const selections=attaching ? dialog.choices : detaching||replacing ? dialog.row.associations : null;
  const target=selections?.find(choice=>choice.id===dialog.selected);
  const currentRow=dialog?.row&&current?.rows?.includes(dialog.row);
  const replacementTargets=replacing&&currentRow?current.rows.filter(row=>row.id!==dialog.row.id):[];
  const replacementTarget=replacementTargets.find(row=>row.id===dialog.replacementID);
  const valid=dialog?.kind==='create' ? validNativeRouteTableName(dialog.name) :
    currentRow&&(attaching ? scope.associations&&target :
      detaching ? scope.associations&&target&&dialog.confirm===target.id :
      replacing ? scope.replacement&&target&&replacementTarget :
      addingRoute ? scope.routeLimit>dialog.row.routes.length&&!!destination&&!duplicateRoute&&!!routeTarget :
      removingRoute ? scope.routeLimit&&!!routeTarget&&dialog.confirm===dialog.row.id :
      dialog?.kind==='delete'&&!dialog.row.mainAssociation&&!dialog.row.associations?.length&&!dialog.row.routes?.length&&dialog.confirm===dialog.row.id);
  async function submit(event) {
    event.preventDefault();
    if (!validScope || !ready || !canWrite || !valid) return;
    const operation=begin(true);
    if (!operation) return;
    setResult({...current,status:'writing'});setNotice(null);
    try {
      const args=[scope.request,scope.region,scope.vpc];
      const callOptions=options(operation.controller.signal);
      if (addingRoute||removingRoute) {
        const updated=await changeVpcNativeIPv4Route(...args,dialog.row,routeTarget,destination,addingRoute,callOptions);
        if (!finish(operation)) return;
        setResult({scope,status:'ready',rows:current.rows.map(row=>row===dialog.row?updated:row)});
        setModal(null);setNotice({scope,kind:dialog.kind,id:updated.id});
        return;
      }
      const {created,removed,associated,disassociated,replaced,rows}=dialog.kind==='create'
        ? await createVpcNativeRouteTable(...args,dialog.name,newToken(),callOptions)
        : attaching ? await associateVpcRouteSubnet(...args,dialog.row,target,callOptions)
          : detaching ? await disassociateVpcRouteSubnet(...args,dialog.row,target,callOptions)
            : replacing ? await replaceVpcRouteSubnet(...args,dialog.row,target,replacementTarget,callOptions)
              : await deleteVpcNativeRouteTable(...args,dialog.row,callOptions);
      if (!finish(operation)) return;
      setResult({scope,status:'ready',rows});setModal(null);
      setNotice({scope,kind:dialog.kind,id:(created??removed??associated??disassociated??replaced).id});
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
    'route-exists':t('This destination already has a saved route. No route was replaced.','この宛先には保存済みのルートがあります。ルートの置き換えは行っていません。'),
    'invalid-route':t('A non-default destination must be outside the VPC and reserved IPv4 service range.','デフォルト以外の宛先は、VPC と予約済み IPv4 サービス範囲の外にしてください。'),
    'no-gateway':t('The internet gateway is not attached to this VPC. No route was added.','インターネットゲートウェイはこの VPC に接続されていません。ルートは追加していません。'),
    associated:t('This subnet already has an explicit association. No association was replaced.','このサブネットには既に明示的な関連付けがあります。関連付けの置き換えは行っていません。'),
    dependency:t('A saved dependency prevents deletion. The route table was not removed.','保存済みの依存関係があるため、ルートテーブルを削除できませんでした。'),
    limit:t('The local route table resource or request limit was reached.','ローカルのルートテーブルのリソース数またはリクエスト数が上限に達しました。'),
    unconfirmed:t('Could not confirm the saved route table state. No result was reported.','ルートテーブルの保存状態を確認できませんでした。結果は表示していません。'),
  };
  const rows=current?.rows??[],pages=Math.max(1,Math.ceil(rows.length/pageSize)),shown=Math.min(page,pages);
  const hasMain=rows.some(row=>row.mainAssociation);
  const verifiedFallback=readyRows&&fallback?.scope===scope&&fallback.rows===readyRows?fallback:null;
  function effectiveFallback(item) {
    if (!item.mainAssociation) return '—';
    if (verifiedFallback?.error) return verifiedFallback.error==='denied'
      ? t('Not verified: ec2:DescribeSubnets is denied.','未確認: ec2:DescribeSubnets が拒否されました。')
      : t('Could not verify effective subnets. Refresh route tables.','実際に適用されるサブネットを確認できませんでした。ルートテーブルを更新してください。');
    const known=verifiedFallback?.value;
    if (known?.tableID!==item.id)
      return t('Verifying subnets that use the main table','メインテーブルを使用するサブネットを確認中');
    return known.subnetIDs.length? known.subnetIDs.map(id=><div key={id}>{identifier(id)}</div>) : t('None','なし');
  }
  const loading=!current || current.status==='loading';
  const refreshLabel=t('Refresh route tables','ルートテーブルを更新');
  const createLabel=t('Create route table','ルートテーブルを作成');
  const deleteLabel=t('Delete route table','ルートテーブルを削除');
  const associateLabel=t('Associate subnet','サブネットを関連付ける');
  const disassociateLabel=t('Disassociate subnet','サブネットの関連付けを解除する');
  const replaceLabel=t('Change subnet route table','サブネットのルートテーブルを変更');
  const addRouteLabel=t('Add IPv4 route','IPv4 ルートを追加');
  const removeRouteLabel=t('Remove IPv4 route','IPv4 ルートを削除');
  const modalTitle={create:createLabel,delete:deleteLabel,associate:associateLabel,disassociate:disassociateLabel,replace:replaceLabel,
    'add-route':addRouteLabel,'remove-route':removeRouteLabel}[dialog?.kind];
  const notices={
    create:t('Saved route table: ','ルートテーブルを保存しました: '),
    delete:t('Deleted route table: ','ルートテーブルを削除しました: '),
    associate:t('Saved explicit subnet association: ','明示的なサブネットの関連付けを保存しました: '),
    disassociate:t('Removed explicit subnet association: ','明示的なサブネットの関連付けを解除しました: '),
    replace:t('Changed the saved subnet association: ','サブネットの関連付けを変更しました: '),
    'add-route':t('Saved IPv4 route in: ','IPv4 ルートを保存しました: '),
    'remove-route':t('Removed IPv4 route from: ','IPv4 ルートを削除しました: '),
  };
  const associationOptions=(selections??[]).map(choice=>({value:choice.id,label:attaching?choice.id:choice.subnet,
    description:attaching?choice.cidr:choice.id}));
  const replacementOptions=replacementTargets.map(row=>({value:row.id,label:row.id,description:row.name||undefined}));
  function change(key,value){setModal(old=>old?.scope===scope ? {...old,[key]:value} : old);}
  return <SpaceBetween size="s">
    <Header variant="h3" actions={<SpaceBetween direction="horizontal" size="xs">
      <Button iconName="refresh" disabled={pending} loading={loading} onClick={refresh}>{refreshLabel}</Button>
      <Button disabled={!ready||!canWrite} onClick={()=>setModal({scope,kind:'create',name:''})}>{createLabel}</Button>
    </SpaceBetween>}>{t('Route tables','ルートテーブル')}</Header>
    <Box color="text-body-secondary">{hasMain
      ? t('Saved configuration only. Subnets without an explicit association use the main table. The local route is read-only, and no packets are forwarded.','保存済みの設定のみです。明示的な関連付けのないサブネットにはメインテーブルが適用されます。ローカルルートは読み取り専用で、通信は転送されません。')
      : scope.routeLimit
      ? t('Saved configuration only. A route may target the native internet gateway attached to this VPC. No packets are forwarded.','保存済みの設定のみです。ルートの宛先には、この VPC に接続されたネイティブのインターネットゲートウェイを指定できます。通信は転送されません。')
      : scope.associations
      ? t('Saved configuration only. Explicit subnet associations do not create routes or forward packets.','保存済みの設定のみです。サブネットの明示的な関連付けではルートを作成せず、パケットも転送しません。')
      : t('Detached configuration only. These tables have no routes or subnet associations and do not forward traffic.','関連付けのない設定のみです。ルートやサブネットの関連付けは含まれず、通信は転送しません。')}</Box>
    {notice?.scope===scope&&<Alert type="success">{notices[notice.kind]}{identifier(notice.id)}</Alert>}
    {errors[current?.status]&&<Alert type="error">{errors[current.status]} {current.refreshBeforeWrite&&
      t('Refresh route tables before trying again.','再試行する前にルートテーブルを更新してください。')}</Alert>}
    <Table variant="embedded" trackBy="id" items={rows.slice((shown-1)*pageSize,shown*pageSize)} loading={loading||pending}
      loadingText={t('Reading route tables','ルートテーブルを読み込み中')}
      ariaLabels={{tableLabel:scope.associations||hasMain?t('Saved route tables','保存済みのルートテーブル'):t('Saved detached route tables','保存済みの関連付けのないルートテーブル')}}
      columnDefinitions={[
        {id:'id',header:t('Route table ID','ルートテーブル ID'),cell:item=><>{identifier(item.id)}
          {item.mainAssociation&&<> · {t('Main','メイン')}</>}</>},
        {id:'name',header:t('Name','名前'),cell:item=>item.name||'—'},
        ...(scope.associations?[{id:'associations',header:t('Explicit subnet associations','明示的なサブネットの関連付け'),
          cell:item=>item.associations.length ? item.associations.map(row=><div key={row.id}>{identifier(row.subnet)}</div>)
            : t('None','なし')}]:[]),
        ...(hasMain?[{id:'effective-subnets',header:t('Subnets using the main table implicitly','メインテーブルが暗黙的に適用されるサブネット'),
          cell:effectiveFallback}]:[]),
        ...(scope.routeLimit||hasMain?[{id:'ipv4-routes',header:t('IPv4 routes','IPv4 ルート'),cell:item=>
          item.localRoute||item.routes?.length?<>{item.localRoute&&<div>{identifier(item.localRoute.cidr)} → {identifier('local')} · {t('Read-only','読み取り専用')}</div>}
            {item.routes?.map(route=><div key={route.cidr}>{identifier(route.cidr)} → {identifier(route.gateway)}</div>)}</>:t('None','なし')}]:[]),
        {id:'actions',header:t('Actions','操作'),cell:item=><SpaceBetween size="xxs">
          {scope.associations&&<>
            <Button disabled={!ready||!canWrite} ariaLabel={associateLabel+': '+item.id}
              onClick={()=>setModal({scope,kind:'associate',row:item,draft:{row:item},selected:''})}>{associateLabel}</Button>
            <Button disabled={!ready||!canWrite||!item.associations.length} ariaLabel={disassociateLabel+': '+item.id}
              onClick={()=>setModal({scope,kind:'disassociate',row:item,selected:'',confirm:''})}>{disassociateLabel}</Button>
            {scope.replacement&&item.associations.length>0&&rows.length>1&&
              <Button disabled={!ready||!canWrite} ariaLabel={replaceLabel+': '+item.id}
                onClick={()=>setModal({scope,kind:'replace',row:item,selected:'',replacementID:''})}>{replaceLabel}</Button>}
          </>}
          {!!scope.routeLimit&&<>
            <Button disabled={!ready||!canWrite||item.routes.length>=scope.routeLimit} ariaLabel={addRouteLabel+': '+item.id}
              disabledReason={item.routes.length>=scope.routeLimit?t('The saved route limit was reached.','保存できるルート数の上限に達しました。'):undefined}
              onClick={()=>setModal({scope,kind:'add-route',row:item,draft:{row:item},
                destination:item.routes.some(route=>route.cidr===ipv4DefaultRoute)?'':ipv4DefaultRoute})}>{addRouteLabel}</Button>
            <Button disabled={!ready||!canWrite||!item.routes.length} ariaLabel={removeRouteLabel+': '+item.id}
              disabledReason={!item.routes.length&&item.localRoute?t('The local route is read-only.','ローカルルートは読み取り専用です。'):undefined}
              onClick={()=>setModal({scope,kind:'remove-route',row:item,routeSelected:'',confirm:''})}>{removeRouteLabel}</Button>
          </>}
          <Button disabled={!ready||!canWrite||!!item.mainAssociation||!!item.associations?.length||!!item.routes?.length} ariaLabel={deleteLabel+': '+item.id}
            disabledReason={item.mainAssociation?t('The main route table cannot be deleted.','メインルートテーブルは削除できません。'):item.routes?.length?t('Remove the saved IPv4 routes before deleting this table.','テーブルを削除するには保存済みの IPv4 ルートを先に削除してください。'):item.associations?.length?t('Remove the explicit associations before deleting this table.','このテーブルを削除するには明示的な関連付けを先に解除してください。'):undefined}
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
        </>:addingRoute||removingRoute?<>
          <Box>{t('Selected route table: ','選択済みルートテーブル: ')}{identifier(dialog.row.id)}</Box>
          {addingRoute ? scope.routeLimit===1
            ? <Box>{t('Destination: ','送信先: ')}{identifier(ipv4DefaultRoute)}</Box>
            : <FormField label={t('Destination IPv4 CIDR','宛先の IPv4 CIDR')}
                errorText={duplicateRoute?t('A route for this destination already exists.','この宛先のルートはすでにあります。'):
                  dialog.destination&&!destination?t('Use a valid IPv4 CIDR outside the VPC and reserved range, or 0.0.0.0/0.','VPC と予約範囲外の IPv4 CIDR、または 0.0.0.0/0 を指定してください。'):undefined}>
                <Input disabled={pending} value={dialog.destination} placeholder="198.51.100.0/24"
                  onChange={({detail})=>change('destination',detail.value)}/>
                {destination&&destination!==dialog.destination&&<Box>{t('Saved as: ','保存する CIDR: ')}{identifier(destination)}</Box>}
              </FormField>
            : savedRoutes.length===1?<Box>{t('Destination: ','送信先: ')}{identifier(selectedRoute.cidr)}</Box>
              :<FormField label={t('Saved destination to remove','削除する保存済みの宛先')}>
                <Select options={savedRoutes.map(route=>({value:route.cidr,label:route.cidr}))}
                  selectedOption={selectedRoute?{value:selectedRoute.cidr,label:selectedRoute.cidr}:null}
                  placeholder={t('Choose a saved CIDR','保存済み CIDR を選択')} disabled={pending}
                  onChange={({detail})=>setModal(old=>old===dialog?{...old,routeSelected:detail.selectedOption.value,confirm:''}:old)}/>
              </FormField>}
          {routeTarget?<Box>{t('Target internet gateway: ','ターゲットのインターネットゲートウェイ: ')}{identifier(addingRoute?routeTarget.id:routeTarget.gateway)}</Box>
            : !addingRoute?null:dialog.gatewayError?<Alert type="error">{dialog.gatewayError==='denied'
              ?t('The selected identity needs ec2:DescribeInternetGateways to verify a target.','ターゲットの確認には現在の ID に ec2:DescribeInternetGateways 権限が必要です。')
              :t('Could not confirm an attached gateway. No route request was sent.','接続済みのゲートウェイを確認できません。ルートのリクエストは送信していません。')}</Alert>
            : dialog.gateway===null?<Alert type="info">{t('Attach a native internet gateway to this VPC first.','先にネイティブのインターネットゲートウェイをこの VPC に接続してください。')}</Alert>
            : <Box>{t('Reading attached internet gateway','接続済みのインターネットゲートウェイを読み込み中')}</Box>}
          {removingRoute&&selectedRoute&&<FormField label={t('Enter the route table ID to confirm route removal','ルートの削除を確認するにはルートテーブル ID を入力')}>
            <Input value={dialog.confirm} disabled={pending} onChange={({detail})=>change('confirm',detail.value)}/>
          </FormField>}
        </>:attaching||detaching||replacing?<>
          <Box>{t('Selected route table: ','選択済みルートテーブル: ')}{identifier(dialog.row.id)}</Box>
          {dialog.choiceError ? <Alert type="error">{dialog.choiceError==='denied'
            ? t('Subnet discovery was denied. The selected identity needs ec2:DescribeSubnets.','サブネットの参照が拒否されました。現在の ID に ec2:DescribeSubnets 権限が必要です。')
            : t('Could not verify the available subnets. No request was sent to change associations.','利用可能なサブネットを確認できません。関連付けを変更するリクエストは送信していません。')}</Alert>
            : <FormField label={attaching?(hasMain?t('Subnet without an explicit association','明示的な関連付けのないサブネット')
              :t('Unassociated subnet in this VPC','この VPC 内の未関連付けサブネット'))
              :t('Saved explicit association','保存済みの明示的な関連付け')}>
              <Select options={associationOptions} selectedOption={associationOptions.find(choice=>choice.value===dialog.selected)??null}
                disabled={pending||!associationOptions.length} statusType={selections?'finished':'loading'}
                loadingText={t('Reading available subnets','利用可能なサブネットを読み込み中')}
                placeholder={selections&&!selections.length?t('No subnets are available for explicit association.','明示的に関連付けられるサブネットはありません。'):t('Choose an entry','項目を選択')}
                onChange={({detail})=>setModal(old=>old===dialog ? {...old,selected:detail.selectedOption.value,confirm:''} : old)}/>
            </FormField>}
          {detaching&&target&&<FormField label={t('Enter the association ID to confirm removal','解除を確認するには関連付け ID を入力')}>
            <Input value={dialog.confirm} disabled={pending} onChange={({detail})=>change('confirm',detail.value)}/>
          </FormField>}
          {replacing&&<FormField label={t('New route table in this VPC','この VPC 内の変更先ルートテーブル')}>
            <Select options={replacementOptions} selectedOption={replacementOptions.find(choice=>choice.value===dialog.replacementID)??null}
              disabled={pending||!replacementOptions.length} placeholder={t('Choose a route table','ルートテーブルを選択')}
              onChange={({detail})=>change('replacementID',detail.selectedOption.value)}/>
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

import React, {useEffect, useRef, useState} from 'react';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Header from '@cloudscape-design/components/header';
import Table from '@cloudscape-design/components/table';
import Button from '@cloudscape-design/components/button';
import Alert from '@cloudscape-design/components/alert';
import SpaceBetween from '@cloudscape-design/components/space-between';
import TextFilter from '@cloudscape-design/components/text-filter';
import Pagination from '@cloudscape-design/components/pagination';
import Container from '@cloudscape-design/components/container';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import {useLocale} from './i18n.jsx';

import {parseEC2Document} from './ec2-response.mjs';

const idCell=value=><span className="resource-identifier">{value||'—'}</span>;

// Only documented read operations. Never infer live guest health from stack metadata.
export default function EC2Readonly({kind,region,request}) {
  const {t}=useLocale();
  const instance=kind==='ec2';
  const [items,setItems]=useState([]),[loading,setLoading]=useState(false),[error,setError]=useState('');
  const [selected,setSelected]=useState([]),[filter,setFilter]=useState(''),[page,setPage]=useState(1);
  const epoch=useRef(0),alive=useRef(true);
  useEffect(()=>{alive.current=true;return()=>{alive.current=false;epoch.current++;};},[]);
  async function load(){
    const current=++epoch.current;setLoading(true);setError('');setItems([]);setSelected([]);setPage(1);
    try {
      const action=instance?'DescribeInstances':'DescribeVpcs';
      const response=await request('/',{method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded','X-Simulator-Service':'ec2','X-Simulator-Region':region},body:new URLSearchParams({Action:action,Version:'2016-11-15'})});
      const xml=new DOMParser().parseFromString(await response.text(),'application/xml');
      if(!response.ok)throw Error(t('Could not retrieve resources.','リソースを取得できませんでした。'));
      const rows=parseEC2Document(xml,kind);
      if(alive.current&&current===epoch.current)setItems(rows);
    }catch(e){if(alive.current&&current===epoch.current)setError(e.message);}
    finally{if(alive.current&&current===epoch.current)setLoading(false);}
  }
  useEffect(()=>{load();},[kind,region]);
  const fields=instance?[['id','Instance ID','インスタンスID'],['name','Name','名前'],['state','Recorded runtime state','記録されたランタイム状態'],['type','Instance type','インスタンスタイプ'],['vpc','VPC ID','VPC ID'],['subnet','Subnet ID','サブネットID'],['image','Image ID','イメージID']]:[['id','VPC ID','VPC ID'],['name','Name','名前'],['cidr','IPv4 CIDR','IPv4 CIDR']];
  const filtered=items.filter(item=>Object.values(item).some(v=>String(v).toLocaleLowerCase().includes(filter.toLocaleLowerCase())));
  const detail=selected[0];
  return <ContentLayout header={<Header variant="h1">{instance?t('EC2 instances','EC2 インスタンス'):'VPC'}</Header>}><SpaceBetween size="l">
    <Alert type="info">{t('Read-only resources managed through CloudFormation. Create, start, stop and network edits are not supported here. Reported state is not a health check of the guest.','CloudFormation で管理されるリソースの読み取り専用画面です。この画面から作成・起動・停止・ネットワーク編集はできません。表示された状態はゲストの稼働確認ではありません。')}</Alert>
    {error&&<Alert type="error" header={t('Could not load resources','リソースを読み込めませんでした')}>{error}</Alert>}
    <Table trackBy="id" selectionType="single" selectedItems={selected} onSelectionChange={({detail})=>setSelected(detail.selectedItems)}
      ariaLabels={{selectionGroupLabel:t('Select a resource','リソースを選択'),itemSelectionLabel:(_,item)=>t('Select ','選択: ')+item.id}}
      loading={loading} loadingText={t('Loading resources','リソースを読み込み中')}
      items={filtered.slice((page-1)*20,page*20)} columnDefinitions={fields.slice(0,instance?5:4).map(([id,en,ja])=>({id,header:t(en,ja),cell:item=>idCell(id==='state'&&(!item[id]||item[id]==='unknown')?t('Unconfirmed','未確認'):item[id])}))}
      header={<Header counter={'('+items.length+')'} actions={<Button iconName="refresh" loading={loading} onClick={load}>{t('Refresh','更新')}</Button>}>{instance?t('Instances','インスタンス'):t('VPCs','VPC 一覧')}</Header>}
      filter={<TextFilter filteringText={filter} onChange={({detail})=>{setFilter(detail.filteringText);setPage(1);setSelected([]);}} filteringAriaLabel={t('Filter resources','リソースを絞り込む')} filteringPlaceholder={t('Find resources','リソースを検索')}/>}
      pagination={<Pagination currentPageIndex={page} pagesCount={Math.max(1,Math.ceil(filtered.length/20))} onChange={({detail})=>setPage(detail.currentPageIndex)} ariaLabels={{nextPageLabel:t('Next page','次のページ'),previousPageLabel:t('Previous page','前のページ'),pageLabel:n=>String(n)}}/>}
      empty={<SpaceBetween size="s"><span>{error?t('Resources are unavailable. Retry with Refresh.','リソースを取得できません。「更新」で再試行してください。'):items.length?t('No resources match your filter.','検索条件に一致するリソースがありません。'):t('No resources found in this region.','このリージョンには該当するリソースがありません。')}</span>{items.length>0&&<Button onClick={()=>{setFilter('');setPage(1);}}>{t('Clear filter','絞り込みを解除')}</Button>}</SpaceBetween>}/>
    {detail&&<Container header={<Header variant="h2" actions={<Button onClick={()=>setSelected([])}>{t('Close details','詳細を閉じる')}</Button>}>{t('Resource details','リソースの詳細')}</Header>}><KeyValuePairs columns={2} items={fields.map(([key,en,ja])=>({label:t(en,ja),value:idCell(key==='state'&&(!detail[key]||detail[key]==='unknown')?t('Unconfirmed','未確認'):detail[key])}))}/></Container>}
  </SpaceBetween></ContentLayout>;
}

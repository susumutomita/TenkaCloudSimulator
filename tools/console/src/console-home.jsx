import React from 'react';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Container from '@cloudscape-design/components/container';
import Header from '@cloudscape-design/components/header';
import Link from '@cloudscape-design/components/link';
import SpaceBetween from '@cloudscape-design/components/space-between';
import {useLocale} from './i18n.jsx';
import {hashForService, servicesForProvider} from './console-navigation.mjs';

// Presentation only. These categories and original glyphs confer no API access.
export const SERVICE_PRESENTATION = Object.freeze({
  s3: {category:'storage', color:'green', icon:'storage', en:'Store and manage objects', ja:'オブジェクトを保存・管理'},
  ssm: {category:'management', color:'pink', icon:'settings', en:'Manage systems and configuration', ja:'システムと設定を管理'},
  lambda: {category:'compute', color:'orange', icon:'compute', en:'Run functions', ja:'関数を実行'},
  iam: {category:'security', color:'red', icon:'key', en:'Manage identities and access', ja:'IDとアクセスを管理'},
  apigateway: {category:'networking', color:'purple', icon:'gateway', en:'Create and manage APIs', ja:'APIを作成・管理'},
  cfn: {category:'management', color:'pink', icon:'stack', en:'Provision resources with templates', ja:'テンプレートでリソースを構築'},
  kms: {category:'security', color:'red', icon:'key', en:'Manage encryption keys', ja:'暗号化キーを管理'},
  dynamodb: {category:'database', color:'blue', icon:'database', en:'Manage tables and items', ja:'テーブルと項目を管理'},
  sqs: {category:'integration', color:'pink', icon:'queue', en:'Send and receive queue messages', ja:'キューのメッセージを送受信'},
  logs: {category:'management', color:'pink', icon:'logs', en:'Explore log groups and streams', ja:'ロググループとストリームを確認'},
  secretsmanager: {category:'security', color:'red', icon:'key', en:'Manage secrets and versions', ja:'シークレットとバージョンを管理'},
  catalog: {category:'management', color:'purple', icon:'stack', en:'Explore supported service APIs', ja:'対応するサービスAPIを確認'},
});
const paths = {
  storage:'M5 6h14l-2 14H7L5 6Zm0 0c0-3 14-3 14 0M8 10h8M9 14h6',
  settings:'m12 3 3 3 4 1 1 4-2 3-1 5-4 1-3-3-4-1-1-4 2-3 1-4 4-1Zm0 5a4 4 0 1 0 0 8 4 4 0 0 0 0-8',
  compute:'M7 4h5l6 16h-4L9 7M5 20l4-10M4 20h5',
  key:'M14 4a5 5 0 1 1-3 9l-7 7H2v-3l7-7a5 5 0 0 1 5-6Zm2 3h.01M5 16l3 3',
  gateway:'M4 4h5v16H4V4Zm11 0h5v16h-5V4ZM9 8h6M9 16h6M11 11l2 1-2 1',
  stack:'m12 3 9 5-9 5-9-5 9-5Zm-9 9 9 5 9-5M3 16l9 5 9-5',
  database:'M4 6c0-4 16-4 16 0s-16 4-16 0Zm0 0v12c0 4 16 4 16 0V6M4 12c0 4 16 4 16 0',
  queue:'M3 5h18v14H3V5Zm4 4h2v6H7V9Zm4 0h2v6h-2V9Zm4 0h2v6h-2V9',
  logs:'M4 3h16v18H4V3Zm4 5h8M8 12h8M8 16h5',
};
export function ServiceIcon({id}) {
  const style=SERVICE_PRESENTATION[id] ?? {color:'purple',icon:'stack'};
  return <span className={'console-service-icon console-service-icon--'+style.color} aria-hidden="true"><svg viewBox="0 0 24 24" focusable="false"><path d={paths[style.icon]}/></svg></span>;
}

// Deliberately consumes capability metadata only. Home never retrieves resources.
export default function ConsoleHome({capabilities, onNavigate, region}) {
  const {t}=useLocale();
  const services=servicesForProvider('aws',capabilities);
  const follow=id=>event=>{event.preventDefault();onNavigate(id);};
  return <div className="console-home"><ContentLayout header={<Header variant="h1">{t('Console Home','コンソールのホーム')}</Header>}>
    <div className="console-home-grid">
      <div className="console-home-services"><Container header={<Header variant="h2" counter={'('+services.length+')'}>{t('Available services','利用可能なサービス')}</Header>}>
        {services.length?<div className="console-home-service-list">{services.map(item=><div key={item.id} className="console-home-service"><ServiceIcon id={item.id}/><Link href={'#'+hashForService(item.id)} onFollow={follow(item.id)}>{item.label}</Link></div>)}</div>:<p role="status">{t('No services are available in this session.','このセッションで利用可能なサービスはありません。')}</p>}
      </Container></div>
      <div className="console-home-welcome"><Container header={<Header variant="h2">{t('Welcome to TenkaCloud','TenkaCloud へようこそ')}</Header>}>
        <SpaceBetween size="l"><div className="console-home-guide"><span className="console-home-guide-icon" aria-hidden="true">◇</span><div><h3>{t('Explore the console','コンソールを使う')}</h3><p>{t('Find a service using the search bar or the Services menu.','検索バーやサービスメニューから、使いたいサービスを探せます。')}</p></div></div>
        <div className="console-home-guide"><span className="console-home-guide-icon" aria-hidden="true">⌘</span><div><h3>{t('Practice locally','ローカルで練習する')}</h3><p>{t('Use the console and SDK with the same local resources.','コンソールとSDKから、同じローカルリソースを操作できます。')}</p></div></div></SpaceBetween>
      </Container></div>
      <div className="console-home-scope"><Container header={<Header variant="h2">{t('Local environment','ローカル環境')}</Header>}>
        <dl className="console-home-facts"><div><dt>{t('Region','リージョン')}</dt><dd>{region}</dd></div><div><dt>{t('Connection','接続先')}</dt><dd>{t('Local simulator','ローカルシミュレーター')}</dd></div><div><dt>{t('Cloud resources','実クラウドのリソース')}</dt><dd>{t('Not connected','接続なし')}</dd></div></dl>
      </Container></div>
      <div className="console-home-start"><Container header={<Header variant="h2">{t('Getting started','はじめに')}</Header>}>
        <div className="console-home-start-grid"><div><h3>{t('Choose a service','サービスを選ぶ')}</h3><p>{t('Only services available in this session are listed.','このセッションで利用可能なサービスだけを表示しています。')}</p></div><div><h3>{t('Work with your resources','リソースを操作する')}</h3><p>{t('Open a service to browse its resources and available actions.','サービスを開くと、リソースと利用可能な操作を確認できます。')}</p></div><div><h3>{t('Explore the API','APIを確認する')}</h3><p><Link href="/_simulator/docs/">{t('API reference','APIリファレンス')}</Link></p></div></div>
      </Container></div>
      <div className="console-home-limits"><Container header={<Header variant="h2">{t('Simulator scope','シミュレーターの対応範囲')}</Header>}>
        <p>{t('TenkaCloud Simulator is an independent learning environment, not the AWS console. Only the documented subset is implemented.','TenkaCloud Simulator は独立した学習環境です。AWSコンソールではありません。ドキュメントに記載された機能のみ対応しています。')}</p>
        <p>{t('No real cloud resources are created. Availability here does not grant additional permissions.','実クラウドのリソースは作成されません。ここでの表示によって権限が追加されることはありません。')}</p>
      </Container></div>
    </div>
  </ContentLayout></div>;
}

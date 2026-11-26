import React from 'react';
import Box from '@cloudscape-design/components/box';
import Header from '@cloudscape-design/components/header';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import {useLocale} from './i18n.jsx';

export default function EC2InstanceMetadata({groupIds,tags}) {
  const {t}=useLocale();
  const groupsLabel=t('Security group IDs','セキュリティグループ ID');
  return <SpaceBetween size="m">
    <div><Box variant="h3">{groupsLabel}</Box>{groupIds.length
      ?<ul aria-label={groupsLabel}>{groupIds.map(id=><li key={id}><span className="resource-identifier">{id}</span></li>)}</ul>
      :<Box color="text-body-secondary">{t('No security group IDs reported.','セキュリティグループ ID は報告されていません。')}</Box>}</div>
    <Table variant="embedded" trackBy="key" items={tags}
      header={<Header variant="h3">{t('Tags','タグ')}</Header>}
      columnDefinitions={[
        {id:'key',header:t('Key','キー'),cell:tag=><span style={{overflowWrap:'anywhere'}}>{tag.key}</span>},
        {id:'value',header:t('Value','値'),cell:tag=>tag.value===''?<Box color="text-body-secondary">{t('(empty)','（空）')}</Box>:<span style={{overflowWrap:'anywhere'}}>{tag.value}</span>},
      ]}
      empty={<Box color="text-body-secondary">{t('No tags reported.','タグは報告されていません。')}</Box>}/>
  </SpaceBetween>;
}

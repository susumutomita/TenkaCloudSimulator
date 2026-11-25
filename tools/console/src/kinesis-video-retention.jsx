import React from 'react';
import FormField from '@cloudscape-design/components/form-field';
import Input from '@cloudscape-design/components/input';
import Select from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';

export default function KinesisVideoRetention({edit,onChange,busy,error,t}){
 const choices=[
  {value:'none',label:t('No data retention','データ保持なし')},
  {value:'keep',label:t('Keep data for a period','指定した期間データを保持する')},
 ];
 return <SpaceBetween size="l">
  <FormField label={t('Data retention period','データ保持期間')} controlId="catalog-kinesis-video-retention-mode">
   <Select selectedOption={choices[edit.enabled?1:0]} options={choices} disabled={busy}
    onChange={e=>onChange({enabled:e.detail.selectedOption.value==='keep'})}/>
  </FormField>
  {edit.enabled&&<FormField label={t('Retention (hours)','保持期間 (時間)')} controlId="catalog-kinesis-video-retention-hours"
    description={t('Set a whole number of hours, from 1 to 87,600.','1 ～ 87,600 の整数で時間を指定してください。')} errorText={error}>
   <Input type="number" inputMode="numeric" disabled={busy} value={edit.hours}
    onChange={e=>onChange({hours:e.detail.value})}/>
  </FormField>}
 </SpaceBetween>;
}

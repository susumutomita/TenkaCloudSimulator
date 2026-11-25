// A Kinesis Video console edit selects the desired period. The native API
// remains directional and takes the version from the freshly described stream.
export function isKinesisVideoRetention(service,resource,op){
 return service?.model==='kinesisvideo'&&resource?.kind==='stream'&&
  op?.name==='UpdateDataRetention'&&op?.uri==='/updateDataRetention'&&
  op?.lookup==='StreamName'&&op?.token==='CurrentVersion';
}

const nativeMaxHours=87600;
const hoursAreLocal=v=>Number.isInteger(v)&&v>=0&&v<=nativeMaxHours;

export function kinesisRetentionEdit(value,token){
 const hours=value?.DataRetentionInHours,name=value?.StreamName;
 if(!hoursAreLocal(hours)||typeof name!=='string'||!/^[a-zA-Z0-9_.-]{1,256}$/.test(name)||
    typeof token!=='string'||!/^[a-zA-Z0-9]{1,64}$/.test(token))return null;
 return {name,token,currentHours:hours,enabled:hours!==0,hours:hours===0?'1':String(hours)};
}

export function kinesisRetentionUpdate(edit){
 if(!edit||typeof edit.enabled!=='boolean'||!kinesisRetentionEdit(
   {StreamName:edit.name,DataRetentionInHours:edit.currentHours},edit.token))return {problem:'snapshot'};
 const raw=typeof edit.hours==='string'?edit.hours.trim():'';
 if(edit.enabled&&!/^[0-9]{1,5}$/.test(raw))return {problem:'hours'};
 const desired=edit.enabled?Number(raw):0;
 if(!hoursAreLocal(desired)||(edit.enabled&&desired===0))return {problem:'hours'};
 if(desired===edit.currentHours)return {problem:'unchanged'};
 return {input:{
  StreamName:edit.name,CurrentVersion:edit.token,
  Operation:desired>edit.currentHours?'INCREASE_DATA_RETENTION':'DECREASE_DATA_RETENTION',
  DataRetentionChangeInHours:Math.abs(desired-edit.currentHours),
 }};
}

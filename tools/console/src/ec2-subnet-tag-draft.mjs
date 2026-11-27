import {nativeSubnetTagLimit,validNativeSubnetTagKey,validNativeSubnetTagValue} from './ec2-native-subnets.mjs';

// Empty untouched rows are omitted. A supplied key with an empty value is a
// real tag; the dedicated Name input owns that exact case-sensitive tag key.
export function nativeSubnetTagDraft(name, rows) {
  const tags = name === '' ? [] : [{key:'Name',value:name}];
  const nameError = !validNativeSubnetTagValue(name);
  const counts = new Map();
  for (const {key} of rows) if (key !== '') counts.set(key,(counts.get(key)??0)+1);
  const errors = rows.map(({key,value})=>{
    if (key === '' && value === '') return {};
    tags.push({key,value});
    return {
      key:key === 'Name' ? 'name' : !validNativeSubnetTagKey(key) ? 'invalid' : counts.get(key)>1 ? 'duplicate' : undefined,
      value:!validNativeSubnetTagValue(value),
    };
  });
  const overLimit = tags.length > nativeSubnetTagLimit;
  return {tags,errors,nameError,overLimit,valid:!nameError&&!overLimit&&!errors.some(error=>error.key||error.value)};
}

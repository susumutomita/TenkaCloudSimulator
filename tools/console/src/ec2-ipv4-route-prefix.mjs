export const ipv4DefaultRoute='0.0.0.0/0';
export const nativeIPv4RouteLimit=16;

export function ec2IPv4Prefix(raw) {
  if (typeof raw!=='string'||raw.length>18||!/^(?:(?:0|[1-9][0-9]{0,2})\.){3}(?:0|[1-9][0-9]{0,2})\/(?:0|[1-9][0-9]?)$/.exec(raw)||raw.trim()!==raw) return null;
  const [address,length]=raw.split('/'), parts=address.split('.').map(Number), bits=Number(length);
  if(parts.some(octet=>octet>255)||bits>32)return null;
  const size=2**(32-bits), numeric=parts.reduce((n,x)=>n*256+x,0), start=Math.floor(numeric/size)*size;
  const masked=[24,16,8,0].map(shift=>Math.floor(start/2**shift)%256).join('.')+'/'+bits;
  return {masked,bits,start,end:start+size-1};
}

const reserved=ec2IPv4Prefix('169.254.168.0/22');
const overlaps=(a,b)=>a.start<=b.end&&b.start<=a.end;
export function nativeIPv4RouteDestination(raw, vpcCIDR) {
  const prefix=ec2IPv4Prefix(raw), vpc=ec2IPv4Prefix(vpcCIDR);
  if(!prefix||!vpc||vpc.masked!==vpcCIDR||vpc.bits<16||vpc.bits>28)return null;
  return prefix.bits===0||!overlaps(prefix,vpc)&&!overlaps(prefix,reserved)?prefix.masked:null;
}

export function compareIPv4RoutePrefixes(a,b) {
  return a.start-b.start||a.bits-b.bits;
}

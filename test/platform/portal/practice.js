'use strict';
// Tokens remain in memory, never URL parameters, localStorage, logs or HTML.
let token='';
const byId=id=>document.getElementById(id);
async function api(path,method='GET',body){
 const response=await fetch('/api/portal/me'+path,{method,headers:{Authorization:'Bearer '+token,'Content-Type':'application/json'},...(body?{body:JSON.stringify(body)}:{})});
 const data=await response.json();if(!response.ok)throw Error(data.message||'Request failed: '+response.status);return data;
}
async function refresh(){const data=await api('');const problem=data.problems.find(p=>p.problemId==='hello-world');if(!problem)throw Error('hello-world is unavailable');byId('state').textContent=data.team.teamName+' / hello-world / '+problem.status;byId('score').textContent=String(problem.score);}
byId('login').addEventListener('submit',async event=>{event.preventDefault();byId('error').textContent='';token=byId('team').value;byId('team').value='';try{await refresh();byId('exercise').hidden=false;}catch(error){token='';byId('exercise').hidden=true;byId('error').textContent=error.message;}});
byId('submit').addEventListener('submit',async event=>{event.preventDefault();const flag=byId('flag').value;byId('flag').value='';try{const result=await api('/submit-flag','POST',{problemId:'hello-world',flag});byId('result').textContent=result.kind;await refresh();}catch(error){byId('error').textContent=error.message;}});

export const contentOperations = ['ListBranches','GetBranch','GetCommit','GetFile','PutFile','CreateBranch'];
export const objectId = value => typeof value === 'string' && /^[a-f0-9]{40}$/.test(value);
export const maxFileSize = 65536;
const encoder = new TextEncoder();

export function repositoryPath(value) {
  if (typeof value !== 'string') return false;
  const relative = value.replace(/^\//,'');
  const parts = relative.split('/');
  return encoder.encode(relative).length <= 256 && parts.length <= 8 && !/[\\\p{Cc}]/u.test(value)
    && parts.every(part => part && part !== '.' && part !== '..' && part.toLowerCase() !== '.git');
}

export function encodeBytes(bytes) {
  let raw = '';
  for (const byte of bytes) raw += String.fromCharCode(byte);
  return btoa(raw);
}

export function previewText(bytes) {
  try {
    const text = new TextDecoder('utf-8',{fatal:true,ignoreBOM:true}).decode(bytes);
    return /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/.test(text) ? null : text;
  } catch { return null; }
}

function requireResult(condition) {
  if (!condition) throw Error('Invalid CodeCommit response.');
}

export function contentClient(request, region, repositoryName, repositoryId) {
  async function call(operation, input, signal) {
    const response = await request('/', {method:'POST', signal, headers:{
      'Content-Type':'application/x-amz-json-1.1', 'X-Simulator-Region':region,
      'X-Amz-Target':'CodeCommit_20150413.' + operation,
    }, body:JSON.stringify({repositoryName,...input})});
    if (!response.ok) throw Error(response.status + ': ' + await response.text());
    return response.json();
  }
  async function identity(signal) {
    const {repositoryMetadata} = await call('GetRepository',{},signal);
    if (repositoryMetadata?.repositoryName !== repositoryName || repositoryMetadata.repositoryId !== repositoryId)
      throw Error('The selected repository has changed. Reopen its details.');
    return repositoryMetadata;
  }
  async function branches(nextToken, signal) {
    const page = await call('ListBranches',nextToken ? {nextToken} : {},signal);
    requireResult(Array.isArray(page.branches) && page.branches.length <= 10 &&
      page.branches.every(name=>typeof name==='string' && name) &&
      (page.nextToken === undefined || typeof page.nextToken === 'string' && page.nextToken && page.nextToken !== nextToken));
    return page;
  }
  async function branch(name, signal) {
    const {branch} = await call('GetBranch',{branchName:name},signal);
    requireResult(branch?.branchName===name && objectId(branch.commitId));
    return branch.commitId;
  }
  async function commit(commitId, signal) {
    const {commit} = await call('GetCommit',{commitId},signal);
    requireResult(commit?.commitId===commitId && objectId(commitId) && objectId(commit.treeId) &&
      Array.isArray(commit.parents) && commit.parents.length<=1 && commit.parents.every(objectId) &&
      typeof commit.message==='string' && [commit.author,commit.committer].every(who=>
        who && ['name','email','date'].every(key=>typeof who[key]==='string')));
    return commit;
  }
  async function file(commitId, filePath, signal) {
    const result=await call('GetFile',{commitSpecifier:commitId,filePath},signal);
    requireResult(result.commitId===commitId && objectId(result.blobId) && result.filePath===filePath.replace(/^\//,'') &&
      ['NORMAL','EXECUTABLE'].includes(result.fileMode) && Number.isInteger(result.fileSize) &&
      result.fileSize>=0 && result.fileSize<=maxFileSize && typeof result.fileContent==='string' &&
      result.fileContent.length===4*Math.ceil(result.fileSize/3));
    const bytes=Uint8Array.from(atob(result.fileContent),char=>char.charCodeAt(0));
    requireResult(bytes.length===result.fileSize && encodeBytes(bytes)===result.fileContent);
    return {...result,bytes,text:previewText(bytes)};
  }
  async function change(input, fork, signal, sending) {
    await identity(signal);
    signal.throwIfAborted();
    sending();
    let ack;
    try { ack=await call(fork?'CreateBranch':'PutFile',input,signal); }
    catch (error) {
      if (/^4\d\d:/.test(error.message)) error.writeRejected=true;
      throw error;
    }
    if (fork) {
      requireResult(ack && Object.keys(ack).length===0);
    } else {
      requireResult(objectId(ack.commitId) && objectId(ack.treeId) && objectId(ack.blobId));
      const saved=await file(ack.commitId,input.filePath,signal);
      requireResult(saved.blobId===ack.blobId && saved.fileContent===input.fileContent && saved.fileMode===input.fileMode);
      const savedCommit=await commit(ack.commitId,signal);
      requireResult(savedCommit.treeId===ack.treeId && savedCommit.parents.length===(input.parentCommitId?1:0) &&
        (!input.parentCommitId || savedCommit.parents[0]===input.parentCommitId) && savedCommit.message===(input.commitMessage || ''));
      requireResult(await branch(input.branchName,signal)===ack.commitId);
      await identity(signal);
      return {commit:savedCommit,file:saved};
    }
    requireResult(await branch(input.branchName,signal)===input.commitId);
    const savedCommit=await commit(input.commitId,signal);
    await identity(signal);
    return {commit:savedCommit,file:null};
  }
  return {identity,branches,branch,commit,file,change};
}

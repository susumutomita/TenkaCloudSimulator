import { afterEach, describe, expect, it } from 'bun:test';
import {
  cleanupContexts,
  createContext,
  execute,
  resourceByLogicalId,
} from './support';

afterEach(cleanupContexts);

describe('AWS Lambda payload boundary', () => {
  it('InvokeFunctionの文字列payloadが不正JSONならloudに拒否する', async () => {
    const context = await createContext();
    const functionName = String(
      resourceByLogicalId(context, 'HelloFunction').properties['refValue']
    );

    expect(() =>
      execute(context, 'lambda', 'InvokeFunction', {
        FunctionName: functionName,
        Payload: '{',
      })
    ).toThrow('Payload must contain valid JSON');
  });

  it('CreateFunctionのtag mapを64件へboundedに保つ', async () => {
    const context = await createContext();
    const tags = Object.fromEntries(
      Array.from({ length: 65 }, (_, index) => [`Tag${index}`, 'value'])
    );

    expect(() =>
      execute(context, 'lambda', 'CreateFunction', {
        FunctionName: 'participant-bounded-tags',
        Runtime: 'nodejs22.x',
        Role: 'arn:aws:iam::123456789012:role/tc-fixture-role',
        Handler: 'index.handler',
        Code: { ZipFile: Buffer.from('zip').toString('base64') },
        Tags: tags,
      })
    ).toThrow('Tags has too many entries');
  });
});

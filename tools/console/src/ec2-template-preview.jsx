import React, {useEffect, useRef, useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Modal from '@cloudscape-design/components/modal';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import Textarea from '@cloudscape-design/components/textarea';
import {useLocale} from './i18n.jsx';
import {exampleEC2Template, templatePreviewFailure, templatePreviewInputProblem, validateEC2Template} from './ec2-template-preview.mjs';

export default function EC2TemplatePreview({request, region, onDismiss}) {
  const {t} = useLocale();
  const [draft, setDraft] = useState('');
  const [outcome, setOutcome] = useState(null);
  const active = useRef(null);
  function cancel() {
    active.current?.abort();
    active.current = null;
  }
  // The EC2 page is keyed by route, region and exercise identity. An edit,
  // close or unmount also discards in-flight validation and any old result.
  useEffect(() => () => cancel(), [request, region]);
  const problem = templatePreviewInputProblem(draft);
  function edit(text) {
    cancel();
    setOutcome(null);
    setDraft(text);
  }
  async function submit(event) {
    event.preventDefault();
    if (active.current || problem) return;
    const controller = new AbortController();
    active.current = controller;
    setOutcome({kind: 'loading'});
    try {
      const result = await validateEC2Template(request, region, draft, {signal: controller.signal});
      if (active.current === controller && !controller.signal.aborted) setOutcome({kind: 'accepted', result});
    } catch (error) {
      if (active.current === controller && !controller.signal.aborted) setOutcome({kind: templatePreviewFailure(error)});
    } finally {
      if (active.current === controller) active.current = null;
    }
  }
  const errors = {
    denied: t('The current identity needs cloudformation:ValidateTemplate. No permission was changed.', '現在の ID に cloudformation:ValidateTemplate 権限が必要です。権限は変更していません。'),
    invalid: t('CloudFormation rejected this declaration. Check the YAML or JSON and the simulator’s supported fields.', 'CloudFormation がこの宣言を拒否しました。YAML または JSON とシミュレーターが対応する項目を確認してください。'),
    unconfirmed: t('Could not confirm the preview. No result was accepted.', 'プレビューを確認できませんでした。結果は採用していません。'),
  };
  const result = outcome?.result;
  return <Modal visible size="large" onDismiss={() => {cancel(); onDismiss();}}
    header={t('Preview EC2 stack template', 'EC2 スタックテンプレートのプレビュー')} closeAriaLabel={t('Close preview', 'プレビューを閉じる')}>
    <form onSubmit={submit}>
      <Form errorText={errors[outcome?.kind]}
        actions={<Button variant="primary" formAction="submit" loading={outcome?.kind === 'loading'} disabled={!!problem}>
          {t('Validate template', 'テンプレートを検証')}
        </Button>}>
        <SpaceBetween size="m">
          <Alert type="info">{t('Preview only. This checks the CloudFormation declaration in the selected region. It does not create resources, launch a guest, or verify AMI availability or launch permissions.',
            'プレビュー専用です。選択したリージョンで CloudFormation の宣言を検証します。リソースの作成、ゲストの起動、AMI の可用性や起動権限の確認は行いません。')}</Alert>
          <Button formAction="none" onClick={() => edit(exampleEC2Template)}>{t('Use example template', 'サンプルテンプレートを使用')}</Button>
          <FormField label={t('Template body', 'テンプレート本文')}
            constraintText={t('Paste inline YAML or JSON, up to 51,200 UTF-8 bytes. Do not include secrets.', 'YAML または JSON を貼り付けてください。UTF-8 で 51,200 バイトまでです。秘密情報は含めないでください。')}
            errorText={problem === 'size' ? t('The template exceeds 51,200 UTF-8 bytes.', 'テンプレートが UTF-8 で 51,200 バイトを超えています。') : undefined}>
            <Textarea rows={10} value={draft} onChange={({detail}) => edit(detail.value)}/>
          </FormField>
          {result && <>
            <Alert type="success" header={t('Template declaration accepted', 'テンプレートの宣言を確認しました')}>
              {t('No resources were created.', 'リソースは作成していません。')}
            </Alert>
            <Box>{t('IAM acknowledgement:', 'IAM の確認項目:')} {result.acknowledgement || t('None reported', 'なし')}</Box>
            <Table items={result.parameters} trackBy="name"
              ariaLabels={{tableLabel: t('Declared parameters', '宣言されたパラメータ')}}
              columnDefinitions={[
                {id: 'name', header: t('Parameter', 'パラメータ'), cell: parameter => parameter.name},
                {id: 'noEcho', header: 'NoEcho', cell: parameter => parameter.noEcho ? t('Declared', '宣言あり') : t('Not declared', '宣言なし')},
                {id: 'default', header: t('Default', '既定値'), cell: parameter => parameter.noEcho ? t('Not shown', '非表示') :
                  parameter.defaultDeclared ? t('Declared', '宣言あり') : t('Not declared', '宣言なし')},
              ]}
              empty={t('No parameters declared.', '宣言されたパラメータはありません。')}/>
          </>}
        </SpaceBetween>
      </Form>
    </form>
  </Modal>;
}

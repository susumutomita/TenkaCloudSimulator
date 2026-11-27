import React, {useEffect, useRef, useState} from 'react';
import Alert from '@cloudscape-design/components/alert';
import Button from '@cloudscape-design/components/button';
import Checkbox from '@cloudscape-design/components/checkbox';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Input from '@cloudscape-design/components/input';
import Modal from '@cloudscape-design/components/modal';
import SpaceBetween from '@cloudscape-design/components/space-between';
import {useLocale} from './i18n.jsx';
import {confirmInstanceStack, instanceStackInputProblem, submitInstanceStack} from './ec2-instance-stack-launch.mjs';

export default function EC2InstanceStackLaunch({request, region, onDismiss, onSaving, onConfirmed}) {
  const {t} = useLocale();
  const [form, setForm] = useState({stackName: '', imageId: '', acknowledge: false});
  const [progress, setProgress] = useState('idle');
  const [receipt, setReceipt] = useState(null);
  const [instance, setInstance] = useState(null);
  const operation = useRef(null);
  const dispatched = useRef(false);
  useEffect(() => () => {
    operation.current?.abort();
    operation.current = null;
    onSaving(false);
  }, [request, region, onSaving]);
  const busy = progress === 'submitting' || progress === 'reading';
  const problem = instanceStackInputProblem(form);

  async function run(event, readOnly = false) {
    event.preventDefault();
    if (operation.current || (readOnly ? !receipt : dispatched.current || problem)) return;
    const controller = new AbortController();
    operation.current = controller;
    const current = () => operation.current === controller && !controller.signal.aborted;
    const options = {allowed: true, signal: controller.signal, onDispatch: () => {dispatched.current = true;}};
    let accepted = receipt;
    setProgress(readOnly ? 'reading' : 'submitting');
    onSaving(true);
    try {
      if (!readOnly) {
        accepted = await submitInstanceStack(request, region, form, options);
        if (!current()) return;
        setReceipt(accepted);
      }
      setProgress('reading');
      const confirmed = await confirmInstanceStack(request, region, accepted, options);
      if (current()) {
        setInstance(confirmed.instance);
        setProgress('confirmed');
        onConfirmed(confirmed.instance.id);
      }
    } catch {
      if (current()) setProgress(accepted ? 'unconfirmed' : dispatched.current ? 'unknown' : 'unavailable');
    } finally {
      if (current()) {
        operation.current = null;
        onSaving(false);
      }
    }
  }

  const close = () => { if (!operation.current) onDismiss(); };
  return <Modal visible size="medium" onDismiss={close}
    header={t('Create EC2 instance stack', 'EC2 インスタンスのスタックを作成')}
    closeAriaLabel={t('Close instance stack form', 'インスタンス作成フォームを閉じる')}>
    <form onSubmit={run}>
      <Form actions={<SpaceBetween direction="horizontal" size="xs">
        <Button disabled={busy} formAction="none" onClick={close}>{t('Close', '閉じる')}</Button>
        {receipt ? progress !== 'confirmed' &&
          <Button variant="primary" formAction="none" loading={busy} onClick={event => run(event, true)}>
            {t('Check stack and instance status', 'スタックとインスタンスの状態を確認')}
          </Button> : <Button variant="primary" formAction="submit" loading={busy} disabled={!!problem || dispatched.current}>
          {t('Create local instance stack', 'ローカルインスタンスのスタックを作成')}
        </Button>}
      </SpaceBetween>}>
        <SpaceBetween size="m">
          <Alert type="info">{t(
            'This creates one private t3.micro and its VPC, subnet, security group and IAM profile in CloudFormation. No ingress, public IP or Internet access is configured. To remove it, delete the stack through CloudFormation.',
            'CloudFormation でプライベートな t3.micro と、専用の VPC、サブネット、セキュリティグループ、IAM プロファイルを作成します。受信ルール、パブリック IP、インターネット接続は設定しません。削除は CloudFormation からスタック単位で行います。')}</Alert>
          <FormField label={t('Stack name', 'スタック名')} constraintText={t(
            'Begin with a letter; up to 128 letters, digits or hyphens.', '先頭を英字にし、英数字とハイフンで最大 128 文字。')}>
            <Input value={form.stackName} disabled={busy || dispatched.current}
              onChange={({detail}) => setForm({...form, stackName: detail.value})}/>
          </FormField>
          <FormField label={t('Configured AMI alias', '設定済みの AMI エイリアス')}
            constraintText={t('Use an image ID from the local runtime configuration. No image is downloaded.', 'ローカル実行環境に設定済みのイメージ ID を入力してください。イメージはダウンロードしません。')}>
            <Input value={form.imageId} disabled={busy || dispatched.current}
              onChange={({detail}) => setForm({...form, imageId: detail.value})}/>
          </FormField>
          <Checkbox checked={form.acknowledge} disabled={busy || dispatched.current}
            onChange={({detail}) => setForm({...form, acknowledge: detail.checked})}>
            {t('Acknowledge creation of a scoped IAM role and instance profile (CAPABILITY_IAM).', 'このインスタンス用の IAM ロールとインスタンスプロファイルの作成を承認します（CAPABILITY_IAM）。')}
          </Checkbox>
          {progress === 'submitting' && <Alert type="info">{t(
            'Waiting for CloudFormation. Local startup can take about a minute; do not send another create request.',
            'CloudFormation の応答を待っています。ローカルでの起動に 1 分ほどかかることがあります。作成を再送しないでください。')}</Alert>}
          {receipt && <Alert type="info" header={t('CloudFormation request accepted', 'CloudFormation がリクエストを受け付けました')}>
            <span className="resource-identifier">{receipt.stackId}</span>
          </Alert>}
          {progress === 'confirmed' && <Alert type="success" header={t('Stack complete and instance record confirmed', 'スタック完了とインスタンスの登録を確認しました')}>
            <span className="resource-identifier">{instance.id}</span>{' '}{t(
              'Current guest liveness was not checked by this view.', 'この画面ではゲストが現在稼働しているかは確認していません。')}
          </Alert>}
          {progress === 'unavailable' && <Alert type="error">{t(
            'The session or local instance runtime is unavailable. No create request was sent.',
            'セッションまたはローカルのインスタンス実行環境が利用できません。作成リクエストは送信していません。')}</Alert>}
          {progress === 'unknown' && <Alert type="error">{t(
            'CloudFormation did not confirm the create request. Check the stack by name before another attempt; nothing was retried.',
            '作成リクエストを確認できませんでした。再度作成する前に CloudFormation でスタック名を確認してください。自動で再送していません。')}</Alert>}
          {progress === 'unconfirmed' && <Alert type="error">{t(
            'Could not confirm both stack completion and the corresponding EC2 record. Check status without sending another create request.',
            'スタックの完了と対応する EC2 の登録を確認できませんでした。作成を再送せず、状態を確認してください。')}</Alert>}
        </SpaceBetween>
      </Form>
    </form>
  </Modal>;
}

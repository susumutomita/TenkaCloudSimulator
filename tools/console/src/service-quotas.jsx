import React, { useEffect, useId, useRef, useState } from "react";
import "./service-quotas.css";
import { useLocale } from "./i18n.jsx";
import {
  quotaIdentity,
  quotaCall,
  quotaValue,
  quotaRecord,
  quotaHistory,
  quotaEpoch,
  definitiveQuotaRefusal,
  quotaScopeRecord,
  quotaAcknowledgement,
} from "./service-quotas.mjs";
import Alert from "@cloudscape-design/components/alert";
import Button from "@cloudscape-design/components/button";
import Checkbox from "@cloudscape-design/components/checkbox";
import Container from "@cloudscape-design/components/container";
import FormField from "@cloudscape-design/components/form-field";
import Header from "@cloudscape-design/components/header";
import Input from "@cloudscape-design/components/input";
import KeyValuePairs from "@cloudscape-design/components/key-value-pairs";
import Link from "@cloudscape-design/components/link";
import Select from "@cloudscape-design/components/select";
import SpaceBetween from "@cloudscape-design/components/space-between";
import Table from "@cloudscape-design/components/table";
export default function ServiceQuotas({ request, region, account }) {
  const warningHeadingId = useId();
  const { t } = useLocale(),
    [quota, setQuota] = useState(null),
    [published, setPublished] = useState(null),
    [error, setError] = useState(""),
    [loading, setLoading] = useState(false),
    [rows, setRows] = useState([]),
    [next, setNext] = useState(""),
    [tokens, setTokens] = useState([""]),
    [index, setIndex] = useState(0),
    [status, setStatus] = useState("ALL"),
    [level, setLevel] = useState("ACCOUNT"),
    [historyKind, setHistoryKind] = useState("quota"),
    [desired, setDesired] = useState("8"),
    [support, setSupport] = useState(false),
    [record, setRecord] = useState(null),
    [lookup, setLookup] = useState(""),
    [writing, setWriting] = useState(false),
    [uncertain, setUncertain] = useState(false),
    [acknowledged, setAcknowledged] = useState(false);
  const reads = useRef(quotaEpoch()),
    alive = useRef(true),
    write = useRef(null);
  const call = (op, input, signal) =>
    quotaCall(request, region, op, input, signal);
  useEffect(
    () => () => {
      alive.current = false;
      reads.current.cancel();
      write.current?.abort();
    },
    [],
  );
  function clear() {
    setAcknowledged(false);
    setLookup("");
    setQuota(null);
    setPublished(null);
    setRows([]);
    setNext("");
    setRecord(null);
    setTokens([""]);
    setIndex(0);
  }
  async function refreshQuota() {
    const task = reads.current.begin();
    clear();
    setError("");
    setLoading(true);
    const failures = [];
    async function readSource(operation, input, apply) {
      try {
        const result = await call(operation, input, task.signal);
        if (task.valid()) apply(result);
      } catch (error) {
        if (task.valid()) failures.push(operation + ": " + error.message);
      }
    }
    await Promise.all([
      readSource("GetAWSDefaultServiceQuota", quotaIdentity, (result) =>
        setPublished(quotaValue(result.Quota)),
      ),
      readSource("GetServiceQuota", quotaIdentity, (result) => {
        const value = quotaValue(result.Quota);
        if (
          value.QuotaAppliedAtLevel !== "ACCOUNT" ||
          value.QuotaArn !==
            `arn:aws:servicequotas:${region}:${account}:b2bi/L-05F6A9EF`
        )
          throw Error("Invalid applied account scope.");
        setQuota(value);
      }),
      readSource("ListServices", {}, (result) => {
        if (!Array.isArray(result.Services))
          throw Error("Invalid service discovery.");
      }),
      readSource(
        "ListAWSDefaultServiceQuotas",
        { ServiceCode: quotaIdentity.ServiceCode },
        (result) => {
          if (!Array.isArray(result.Quotas))
            throw Error("Invalid default discovery.");
          result.Quotas.forEach(quotaValue);
        },
      ),
      readSource(
        "ListServiceQuotas",
        {
          ServiceCode: quotaIdentity.ServiceCode,
          QuotaAppliedAtLevel: "ACCOUNT",
        },
        (result) => {
          if (!Array.isArray(result.Quotas))
            throw Error("Invalid applied discovery.");
          result.Quotas.forEach(quotaValue);
        },
      ),
    ]);
    if (task.valid()) {
      setError(failures.join(" — "));
      setLoading(false);
    }
  }
  useEffect(() => {
    refreshQuota();
  }, [region, request]);
  async function history(token = "", page = 0, recovery = false) {
    if (recovery) setAcknowledged(false);
    const task = reads.current.begin();
    setRows([]);
    setRecord(null);
    setNext("");
    setError("");
    setLoading(true);
    try {
      const result = quotaHistory(
        await call(
          recovery || historyKind === "quota"
            ? "ListRequestedServiceQuotaChangeHistoryByQuota"
            : "ListRequestedServiceQuotaChangeHistory",
          {
            ...(recovery || historyKind === "quota" ? quotaIdentity : {}),
            MaxResults: 1,
            QuotaRequestedAtLevel: recovery ? "ACCOUNT" : level,
            ...(!recovery && status !== "ALL" ? { Status: status } : {}),
            ...(token ? { NextToken: token } : {}),
          },
          task.signal,
        ),
      );
      if (task.valid()) {
        setRows(
          result.items.map((item) => quotaScopeRecord(item, region, account)),
        );
        setNext(result.next);
        setIndex(page);
        setTokens((old) =>
          page === 0 ? [""] : old.slice(0, page).concat(token),
        );
        if (recovery && uncertain) {
          setAcknowledged(true);
          setHistoryKind("quota");
          setStatus("ALL");
          setLevel("ACCOUNT");
        }
      }
    } catch (e) {
      if (task.valid()) {
        clear();
        setError(e.message);
      }
    } finally {
      if (task.valid()) setLoading(false);
    }
  }
  async function inspect(id) {
    const task = reads.current.begin();
    setRecord(null);
    setError("");
    setLoading(true);
    try {
      const value = quotaScopeRecord(
        (
          await call(
            "GetRequestedServiceQuotaChange",
            { RequestId: id },
            task.signal,
          )
        ).RequestedQuota,
        region,
        account,
      );
      if (value.Id !== id) throw Error("Request ID mismatch.");
      if (task.valid()) setRecord(value);
    } catch (e) {
      if (task.valid()) {
        clear();
        setError(e.message);
      }
    } finally {
      if (task.valid()) setLoading(false);
    }
  }
  async function submit() {
    if (write.current || uncertain || !quota) return;
    const value = Number(desired);
    if (!Number.isFinite(value) || value <= quota.Value) {
      setError(
        t(
          "Desired value must exceed applied capacity.",
          "希望値は適用済み上限を超える必要があります。",
        ),
      );
      return;
    }
    reads.current.cancel();
    setWriting(true);
    setRecord(null);
    setError("");
    const controller = new AbortController();
    write.current = controller;
    try {
      const saved = quotaAcknowledgement(
        (
          await call(
            "RequestServiceQuotaIncrease",
            {
              ...quotaIdentity,
              DesiredValue: value,
              SupportCaseAllowed: support,
            },
            controller.signal,
          )
        ).RequestedQuota,
        {
          region,
          account,
          desired: value,
          support,
        },
      );
      if (alive.current) {
        setRecord(saved);
        setLookup(saved.Id);
        setRows([]);
        setNext("");
      }
    } catch (e) {
      if (alive.current) {
        if (/^403:|AccessDenied|not authorized/i.test(e.message)) clear();
        if (!definitiveQuotaRefusal(e)) {
          setUncertain(true);
          setAcknowledged(false);
        }
        setError(e.message);
      }
    } finally {
      if (alive.current) setWriting(false);
      write.current = null;
    }
  }
  const disabled = loading || writing;
  return (
    <SpaceBetween size="l">
      <Header variant="h1">Service Quotas</Header>
      <Alert type="info">
        {t(
          "Local account-level B2B Data Interchange Profiles capacity. Requests do not change capacity or open support cases.",
          "ローカルのアカウント単位 B2B Data Interchange Profiles 上限です。リクエストで上限は変更されず、サポートケースも作成されません。",
        )}
      </Alert>
      <KeyValuePairs
        columns={2}
        items={[
          { label: t("Region", "リージョン"), value: region },
          { label: t("Account", "アカウント"), value: account },
          { label: "ServiceCode", value: quotaIdentity.ServiceCode },
          { label: "QuotaCode", value: quotaIdentity.QuotaCode },
          {
            label: t("Published AWS default", "公開 AWS デフォルト"),
            value: published
              ? String(published.Value)
              : t("Not loaded", "未取得"),
          },
          {
            label: t("Applied account capacity", "適用済みアカウント上限"),
            value: quota ? String(quota.Value) : t("Not loaded", "未取得"),
          },
        ]}
      />
      <Button disabled={disabled} loading={loading} onClick={refreshQuota}>
        {t("Refresh quota", "クォータを更新")}
      </Button>
      {error && <Alert type="error">{error}</Alert>}
      {uncertain && (
        <section
          className="service-quotas-unknown"
          role="alert"
          aria-labelledby={warningHeadingId}
        >
          <h2 id={warningHeadingId}>
            {t("Request outcome unknown", "リクエスト結果が不明です")}
          </h2>
          <p>{t(
            "The request may have been saved. It has no idempotency token. Do not retry it. Refresh history in this same account, Region and identity; matching amounts are not proof of the lost request ID. Leaving this view clears this local warning.",
            "リクエストが保存された可能性があります。冪等性トークンはありません。再送しないでください。同じアカウント・リージョン・IDで履歴を更新してください。同じ希望値から失われたIDを特定することはできません。この画面を離れるとローカルの警告は消えます。",
          )}</p>
          <SpaceBetween direction="horizontal" size="xs">
          <Button disabled={disabled} onClick={() => history("", 0, true)}>
            {t("Refresh same-scope history", "同じスコープの履歴を更新")}
          </Button>
          {acknowledged && (
            <Button
              disabled={disabled}
              onClick={() => {
                setUncertain(false);
                setAcknowledged(false);
                setError("");
              }}
            >
              {t("Start a separate new request", "別の新規リクエストを開始")}
            </Button>
          )}
          </SpaceBetween>
        </section>
      )}
      <Container
        header={
          <Header variant="h3">
            {t("Request increase", "上限引き上げをリクエスト")}
          </Header>
        }
      >
        <SpaceBetween size="m">
          <FormField label={t("Desired value", "希望値")}>
            <Input
              type="number"
              value={desired}
              disabled={disabled || uncertain || !quota}
              onChange={({ detail }) => setDesired(detail.value)}
            />
          </FormField>
          <Checkbox
            checked={support}
            disabled={disabled || uncertain || !quota}
            onChange={({ detail }) => setSupport(detail.checked)}
          >
            {t(
              "Allow support case (stored PENDING locally only)",
              "サポートケースを許可（ローカルで PENDING として保存するのみ）",
            )}
          </Checkbox>
          <Button
            variant="primary"
            disabled={disabled || uncertain || !quota}
            loading={writing}
            onClick={submit}
          >
            {t("Submit increase request", "引き上げリクエストを送信")}
          </Button>
        </SpaceBetween>
      </Container>
      <Container
        header={
          <Header variant="h3">{t("Request history", "リクエスト履歴")}</Header>
        }
      >
        <SpaceBetween size="m">
          <FormField label={t("History scope", "履歴の範囲")}>
            <Select
              disabled={disabled}
              selectedOption={{
                value: historyKind,
                label:
                  historyKind === "quota"
                    ? t("Quota", "クォータ")
                    : t("Service", "サービス"),
              }}
              options={[
                { value: "quota", label: t("Quota", "クォータ") },
                { value: "service", label: t("Service", "サービス") },
              ]}
              onChange={({ detail }) => {
                reads.current.cancel();
                setHistoryKind(detail.selectedOption.value);
                setRows([]);
                setNext("");
                setIndex(0);
                setTokens([""]);
                setRecord(null);
              }}
            />
          </FormField>
          <FormField label={t("Status filter", "ステータスフィルター")}>
            <Select
              disabled={disabled}
              selectedOption={{ value: status, label: status }}
              options={["ALL", "PENDING", "NOT_APPROVED"].map((value) => ({
                value,
                label: value,
              }))}
              onChange={({ detail }) => {
                reads.current.cancel();
                setStatus(detail.selectedOption.value);
                setRows([]);
                setNext("");
                setRecord(null);
                setTokens([""]);
                setIndex(0);
              }}
            />
          </FormField>
          <FormField label="QuotaRequestedAtLevel">
            <Select
              disabled={disabled}
              selectedOption={{ value: level, label: level }}
              options={["ACCOUNT", "RESOURCE"].map((value) => ({
                value,
                label: value,
              }))}
              onChange={({ detail }) => {
                reads.current.cancel();
                setLevel(detail.selectedOption.value);
                setRows([]);
                setNext("");
                setRecord(null);
                setTokens([""]);
                setIndex(0);
              }}
            />
          </FormField>
          <SpaceBetween direction="horizontal" size="xs">
            <Button disabled={disabled} onClick={() => history()}>
              {t("Refresh history", "履歴を更新")}
            </Button>
            <Button
              disabled={disabled || index === 0}
              onClick={() => history(tokens[index - 1], index - 1)}
            >
              {t("Previous history page", "前の履歴ページ")}
            </Button>
            <Button
              disabled={disabled || !next}
              onClick={() => history(next, index + 1)}
            >
              {t("Next history page", "次の履歴ページ")}
            </Button>
          </SpaceBetween>
          <Table
            ariaLabels={{
              tableLabel: t(
                "Quota increase requests",
                "上限引き上げリクエスト",
              ),
            }}
            loading={loading}
            items={rows}
            trackBy="Id"
            empty={t(
              "No request records loaded.",
              "リクエストレコードは読み込まれていません。",
            )}
            columnDefinitions={[
              {
                id: "id",
                header: "Request ID",
                cell: (item) => (
                  <Link
                    onFollow={(e) => {
                      e.preventDefault();
                      setLookup(item.Id);
                      inspect(item.Id);
                    }}
                    href="#"
                  >
                    {item.Id}
                  </Link>
                ),
              },
              {
                id: "value",
                header: t("Desired value", "希望値"),
                cell: (item) => String(item.DesiredValue),
              },
              {
                id: "status",
                header: t("Status", "ステータス"),
                cell: (item) => item.Status,
              },
              {
                id: "date",
                header: t("Created", "作成日時"),
                cell: (item) => new Date(item.Created * 1000).toISOString(),
              },
            ]}
          />
          <FormField label="Request ID">
            <Input
              value={lookup}
              disabled={disabled}
              onChange={({ detail }) => setLookup(detail.value)}
            />
          </FormField>
          <Button
            disabled={disabled || !lookup}
            onClick={() => inspect(lookup)}
          >
            {t("Get request", "リクエストを取得")}
          </Button>
          {record && (
            <KeyValuePairs
              columns={1}
              items={[
                "Id",
                "DesiredValue",
                "Status",
                "Created",
                "LastUpdated",
                "QuotaRequestedAtLevel",
              ].map((label) => ({
                label,
                value: ["Created", "LastUpdated"].includes(label)
                  ? new Date(record[label] * 1000).toISOString()
                  : String(record[label]),
              }))}
            />
          )}
          <Link external href="/_simulator/docs/service-quotas.html">
            {t(
              "Service Quotas API reference (us-east-1)",
              "Service Quotas API リファレンス（us-east-1）",
            )}
          </Link>
          <p>
            {t(
              "The reference uses your browser credential in us-east-1. It does not forward the Console's selected exercise identity.",
              "リファレンスは us-east-1 でブラウザーの認証情報を使用します。コンソールで選択した演習 ID は引き継ぎません。",
            )}
          </p>
        </SpaceBetween>
      </Container>
    </SpaceBetween>
  );
}

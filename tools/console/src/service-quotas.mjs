export const quotaIdentity = Object.freeze({
  ServiceCode: "b2bi",
  QuotaCode: "L-05F6A9EF",
});
export const quotaOperations = Object.freeze([
  "ListServices",
  "ListServiceQuotas",
  "ListAWSDefaultServiceQuotas",
  "GetAWSDefaultServiceQuota",
  "GetServiceQuota",
  "RequestServiceQuotaIncrease",
  "GetRequestedServiceQuotaChange",
  "ListRequestedServiceQuotaChangeHistory",
  "ListRequestedServiceQuotaChangeHistoryByQuota",
]);
export function quotaConsoleAvailable(service) {
  return (
    service?.model === "service-quotas" &&
    service.protocol === "json" &&
    service.jsonVersion === "1.1" &&
    service.targetPrefix === "ServiceQuotasV20190624" &&
    Array.isArray(service.operations) &&
    quotaOperations.every((name) =>
      service.operations.some(
        (op) => (typeof op === "string" ? op : op.name) === name,
      ),
    )
  );
}
export async function quotaCall(request, region, operation, input, signal) {
  if (!quotaOperations.includes(operation))
    throw Error("Unsupported Service Quotas operation.");
  const response = await request("/", {
    method: "POST",
    signal,
    redirect: "error",
    headers: {
      "Content-Type": "application/x-amz-json-1.1",
      "X-Amz-Target": "ServiceQuotasV20190624." + operation,
      "X-Simulator-Region": region,
    },
    body: JSON.stringify(input),
  });
  if (!response.ok)
    throw Error(response.status + ": Service Quotas request refused.");
  const value = await response.json();
  if (!value || typeof value !== "object" || Array.isArray(value))
    throw Error("Invalid Service Quotas response.");
  return value;
}
export function quotaValue(value) {
  if (
    !value ||
    value.ServiceCode !== quotaIdentity.ServiceCode ||
    value.QuotaCode !== quotaIdentity.QuotaCode ||
    typeof value.Value !== "number" ||
    !Number.isFinite(value.Value) ||
    value.Value < 0
  )
    throw Error("Invalid quota response.");
  return value;
}
export function quotaRecord(value) {
  if (
    !value ||
    typeof value.Id !== "string" ||
    !value.Id ||
    value.ServiceCode !== quotaIdentity.ServiceCode ||
    value.QuotaCode !== quotaIdentity.QuotaCode ||
    !["PENDING", "NOT_APPROVED"].includes(value.Status) ||
    !Number.isFinite(value.DesiredValue) ||
    !Number.isFinite(value.Created) ||
    !Number.isFinite(value.LastUpdated) ||
    value.Created < 0 ||
    value.LastUpdated < value.Created ||
    !Number.isFinite(new Date(value.Created * 1000).getTime()) ||
    !Number.isFinite(new Date(value.LastUpdated * 1000).getTime()) ||
    value.QuotaRequestedAtLevel !== "ACCOUNT"
  )
    throw Error("Invalid request response.");
  return value;
}
export function quotaHistory(value) {
  if (
    !Array.isArray(value.RequestedQuotas) ||
    (value.NextToken !== undefined &&
      (typeof value.NextToken !== "string" || !value.NextToken))
  )
    throw Error("Invalid request history.");
  return {
    items: value.RequestedQuotas.map(quotaRecord),
    next: value.NextToken || "",
  };
}
// A failed transport or malformed acknowledgement is ambiguous, never retried.
export function definitiveQuotaRefusal(error) {
  return /^(?:400|403|404|409|429):/.test(error?.message || "");
}
export function quotaEpoch() {
  let generation = 0,
    controller;
  return {
    begin() {
      controller?.abort();
      controller = new AbortController();
      const current = ++generation;
      return {
        signal: controller.signal,
        valid: () => current === generation && !controller.signal.aborted,
      };
    },
    cancel() {
      generation++;
      controller?.abort();
    },
  };
}

export function quotaScopeRecord(value, region, account) {
  const record = quotaRecord(value);
  if (
    record.QuotaArn !==
    `arn:aws:servicequotas:${region}:${account}:b2bi/L-05F6A9EF`
  )
    throw Error("Request account or Region mismatch.");
  return record;
}
export function quotaAcknowledgement(
  value,
  { region, account, desired, support, started, finished },
) {
  const record = quotaScopeRecord(value, region, account);
  if (
    record.DesiredValue !== desired ||
    record.Status !== (support ? "PENDING" : "NOT_APPROVED") ||
    record.Created < started / 1000 - 1 ||
    record.Created > finished / 1000 + 1 ||
    record.LastUpdated !== record.Created
  )
    throw Error("Request acknowledgement does not match this submission.");
  return record;
}

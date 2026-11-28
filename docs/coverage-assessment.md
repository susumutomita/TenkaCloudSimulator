# Coverage assessment: services the simulator does not serve

The simulator implements a product only when the pinned official API model offers
configuration resources that it can store honestly as bounded metadata. It never
fakes success for real work: running workloads, sending messages, inference,
purchases, physical provisioning or AWS- and Google-owned data. This page records
the products assessed and not served, with the evidence behind each decision.
Each entry links to its issue. A product moves back into scope when its pinned
model gains an honest configuration surface, or when an engine feature named here
is added.

Pinned sources:
- AWS: botocore 1.41.6 service models.
- Google Cloud: the discovery documents bundled with google-api-python-client 2.199.0.

## End of support

| Issue | Product | Evidence |
| --- | --- | --- |
| #288 | Amazon Lookout for Equipment | AWS ended support on 2026-10-07 (<https://aws.amazon.com/lookout-for-equipment>). It stopped accepting new customers on 2024-10-17. |
| #89 | Amazon Chime (application) | AWS ended support on 2026-02-20 (<https://docs.aws.amazon.com/chime/latest/ag/amazon-chime-transition-features.html>). The Chime SDK (#90) is unaffected and is served. |

## No public API in the pinned official models

| Issue | Product | Finding |
| --- | --- | --- |
| #134 | AWS CloudShell | No botocore model. |
| #114 | Red Hat OpenShift Service on AWS | No botocore model. |
| #99 | AWS Wickr | No botocore model. |
| #93 | Amazon Connect Talent | No botocore model. |
| #92 | Amazon Connect Health | No botocore model. |
| #219 | AWS Elemental Inference | No botocore model. |
| #227 | AWS Data Transfer Terminal | No botocore model. |
| #234 | AWS Transform | No botocore model. AWS Transform MGN (#235) is served through `mgn`. |
| #287 | Amazon Q Developer | No `q`, `qdeveloper` or `codewhisperer` model; `qconnect` and `qapps` are other products. |
| #178 | Amazon Nova Act | No botocore model. |
| #177 | Amazon Monitron | No botocore model. |
| #164 | Claude Platform on AWS | No botocore model; `bedrock-agent` is Agents for Amazon Bedrock. |
| #289 | Amazon PartyRock | No botocore model; it is a consumer web application. |
| #299 | AWS DeepRacer | No botocore model. |
| #87 | AWS App Studio | No botocore model. |
| #197 | AWS DevOps Agent | No botocore model. |
| #191 | AWS Security Agent | No botocore model. |
| #209 | AWS Service Management Connector | No botocore model; it is distributed as an integration package. |
| #192 | AWS Assurance | No botocore model. |
| #120 | AWS Managed Services | No botocore model. |
| #267 | AWS Continuum | No botocore model. |
| #245 | AWS Network Security Manager | No botocore model. |
| #244 | AWS Interconnect | No botocore model. |
| #215 | AWS Well-Architected Agent | No separate API; the Well-Architected Tool (#216) is served. |
| #285 | VMware Cloud on AWS | No AWS API; `evs` is Amazon Elastic VMware Service, a different product. |
| #398 | Earth Engine | No discovery document among the 598 pinned documents. |
| #393 | Data Studio Pro | No discovery document. |
| #330 | Cloud Managed Lustre | No `lustre` discovery document; only references from other APIs. |
| #370 | Telecom Network Automation | No `telcoautomation` discovery document. |
| #380 | Assured Open Source Software | No discovery document. |
| #387 | Google Device Cloud | No discovery document. |
| #352 | Cloud Network Insights | No discovery document names it. The closest, `networkmanagement` network monitoring providers, links an external platform and reports observed data. |

## Google Cloud APIs outside the pinned discovery corpus

The 2026-11-07 assessment found no discovery document for these three APIs in
`google-api-python-client` 2.199.0. Google also publishes the contracts below.
The simulator serves none of these APIs. Their P0 issues under
[#317](https://github.com/susumutomita/TenkaCloudSimulator/issues/317) remain open.

### Data Studio

[#392](https://github.com/susumutomita/TenkaCloudSimulator/issues/392) covers
Data Studio, the name Google gave Looker Studio on 2026-04-16. Data Studio Pro
is the paid edition (#393). Looker (Google Cloud core) instances have a separate
API (#402). [Release notes](https://docs.cloud.google.com/data-studio/release-notes#April_16_2026)
and [Google Cloud services summary](https://cloud.google.com/terms/services).

`GET https://datastudio.googleapis.com/v1/assets:search` searches existing
assets of exactly one requested type. The API also reads and changes asset
permissions. Access requires a Google Workspace or Cloud Identity organization
whose administrator authorizes the app. P0 must identify how local assets and
their owners would be established before defining search or sharing behavior.
[Asset search](https://developers.google.com/data-studio/integrate/api/reference/assets/search),
[API reference](https://developers.google.com/data-studio/integrate/api/reference)
and [authorization guide](https://developers.google.com/data-studio/integrate/api).

### License Manager

[#325](https://github.com/susumutomita/TenkaCloudSimulator/issues/325) covers
Google Cloud's per-user third-party licensing service, generally available since
2025-08-13. The pinned `licensing.v1` document covers Workspace Enterprise
License Manager. [Compute Engine release notes](https://docs.cloud.google.com/compute/docs/release-notes#August_13_2025).

At `licensemanager.googleapis.com`, v1 lists configurations at
`/v1/projects/{project}/locations/{location}/configurations`. Configuration
creation and deletion return long-running operations. Google documents one
configuration per product per region and a default limit of 5,000 licenses per
configuration. P0 must distinguish locally stored counts and configuration
state from VM-agent activation and the billing obligation that creating a
configuration can incur. [v1 service definition](https://github.com/googleapis/googleapis/blob/4fdff74be5e1d12849d1650f6fb58bd3179fb13f/google/cloud/licensemanager/v1/licensemanager.proto)
and [License Manager guide](https://docs.cloud.google.com/compute/docs/instances/windows/license-manager).

### Google Cloud Universal Ledger

[#323](https://github.com/susumutomita/TenkaCloudSimulator/issues/323) covers
Google Cloud Universal Ledger (GCUL). Blockchain Node Engine has a separate
node resource (#320). At `universalledger.googleapis.com`, v1
lists endpoints at `/v1/projects/{project}/locations/{location}/endpoints` and
gets an endpoint by name. The published discovery document (revision
`20261120`) also defines transaction submission. P0 must identify a source for
existing endpoints before serving these reads. Project IAM grants endpoint
reads through `roles/universalledger.viewer`. Submitting signed transactions
requires a ledger account created by a privileged ledger administrator. Ledger
execution falls outside configuration metadata. [REST reference](https://docs.cloud.google.com/universal-ledger/reference/rest),
[v1 discovery document](https://universalledger.googleapis.com/$discovery/rest?version=v1)
and [onboarding guide](https://docs.cloud.google.com/universal-ledger/getting-started).

## Every operation performs real work or reads provider-owned data

| Issue | Product | Finding |
| --- | --- | --- |
| #167 | Amazon Comprehend Medical | Only Detect*, Infer* and analysis-job operations. |
| #176 | Amazon Machine Learning | Data sources read S3, RDS or Redshift; models train; predictions infer; endpoints are provisioned. |
| #230 | Amazon Elastic VMware Service | Every operation provisions VMware Cloud Foundation on bare-metal hosts, VLANs and Elastic IPs. |
| #96 | AWS End User Messaging Social | Every resource links a WhatsApp Business Account through Meta, or sends templates and messages to Meta. |
| #102 | Savings Plans | Plans are purchased commitments; offerings and rates are AWS price data. |
| #213 | AWS Trusted Advisor | Checks and recommendations are AWS-owned data. |
| #198 | AWS Health | Events and entities are AWS-owned data; the rest is organization-level. |
| #217 | AWS Marketplace | Catalog change sets are listing requests reviewed by AWS; agreements and entitlements are purchases. |
| #301 | AWS Support | Cases are real support requests to AWS. |
| #195 | AWS Control Tower | Landing zones, controls and baselines deploy across an AWS Organizations organization; catalogs are AWS-owned. |
| #199 | AWS Launch Wizard | Deployments provision workloads; workloads and patterns are AWS-owned catalogs. |
| #395 | Dataflow | Jobs, templates and snapshots run or derive from running pipelines. |
| #391 | Cloud Life Sciences | The only resource method, `pipelines.run`, runs a workload. |
| #319 | Batch (Google Cloud) | Only jobs (which run work) and tasks exist in `batch.v1`. |
| #386 | Firebase Test Lab | Test matrices run tests on devices; the environment catalog is Google-owned data. |
| #378 | Android Device Streaming | Every device session reserves a physical device. |
| #369 | Spectrum Access System | Alpha only; device registration and signing are regulated real-world actions. |
| #360 | Media CDN | `networkservices` offers only IAM policy methods for edge cache services, origins and keysets. |
| #334 | Cloud Storage for Firebase | `addFirebase` links an existing Cloud Storage bucket from another provider; the default bucket provisions a real bucket. |
| #383 | Cloud Source Repositories | The `sourcerepo.v1` discovery document states end of sales from 2024-06-17: only projects that enabled the API before then can use it. |
| #346 | Dedicated Interconnect | `compute.v1` `interconnects.insert` orders physical links (`requestedLinkCount`) at an interconnect location. Partner Interconnect attachments (#347) are served. |
| #348 | Cross-Cloud Interconnect | The same `interconnects.insert` orders physical links to another cloud provider. |
| #349 | Cross-Site Interconnect | `compute.v1` `wireGroups` group the physical wires between interconnect locations. |

## Service Quotas

[#210](https://github.com/susumutomita/TenkaCloudSimulator/issues/210) now serves
five read operations for the [regional B2BI Profiles limit](aws-catalog.md#b2b-data-interchange-profile-quota),
including its published default and the actual local applied capacity. Four
additional operations submit and read local increase requests. A request does
not change the enforced B2BI limit or open an external Support case.

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
| #392 | Data Studio | No discovery document. |
| #393 | Data Studio Pro | No discovery document. |
| #330 | Cloud Managed Lustre | No `lustre` discovery document; only references from other APIs. |
| #370 | Telecom Network Automation | No `telcoautomation` discovery document. |
| #380 | Assured Open Source Software | No discovery document. |
| #387 | Google Device Cloud | No discovery document. |
| #323 | Google Cloud Universal Ledger | No discovery document. |
| #325 | Google Cloud License Manager | No discovery document; `licensing.v1` is the Workspace Enterprise License Manager. |
| #352 | Cloud Network Insights | No discovery document names it. The closest, `networkmanagement` network monitoring providers, links an external platform and reports observed data. |

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
| #210 | Service Quotas | Quotas, AWS default values and the service list are AWS-owned data. Increase requests and support cases are sent to AWS, request templates need an AWS Organizations management account, and automatic management starts AWS-side utilization monitoring and notifications. |
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

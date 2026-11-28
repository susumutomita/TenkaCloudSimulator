# AWS backlog assessment against the pinned models

This page records a per-product assessment of the AWS products that had an open
Issue and no catalog specification on 2026-11-21, judged against the pinned
botocore 1.41.6 models and the catalog's specification vocabulary
(`internal/aws/catalog/spec.go`). It complements the
[coverage assessment](coverage-assessment.md), which lists products that cannot
be served at all.

Each product has one verdict:

- **A — addable**: the model has resources that are stored configuration, or
  that the catalog can hold as configuration records, and the vocabulary can
  express them. Most class A products were then added; the
  [generated service table](aws-catalog-services.md) is the source of truth for
  what is served. A class A product that is absent from that table was assessed
  as addable and not yet added, or was dropped for the reason its entry gives.
- **B — needs an engine feature**: an honest configuration surface exists but
  the vocabulary could not express it at the time. The entry names the feature.
- **C — not servable**: every operation performs real work, returns AWS-owned
  data, or needs an external system.
- **D — no model**: the pinned botocore release has no model for the product, or
  the product is served under another model.

The entries are the assessors' notes, kept as written. They were a starting
point for implementation, not a specification: where a served service's `Bounds`
disagree with an entry here, the `Bounds` are right.


General: no native handler or catalog claim uses any of these JSON target prefixes or signing names (checked ssm.go dispatch chain, selection.json). Precedent: eks/kafka/mwaa/opensearch clusters and domains are served as records that stay CREATING, so "cluster-like" parents are acceptable when children/config are honest. Tag ops are ARN-only (engine `tagged()` uses findARN), so tagging by id/name is a B feature.

### #60 Amazon AppFlow — A (flows only, limited)
- appflow / rest-json / signing `appflow`.
- flow: CreateFlow, DescribeFlow, ListFlows, UpdateFlow, DeleteFlow, TagResource, UntagResource, ListTagsForResource. Identity flowName (POST /create-flow, /describe-flow bodies), ARN flowArn.
- Check: restrict source and destination to S3 (and no connectorProfileName), because connector profiles hold SaaS credentials and are unbound; triggers stored, never run. StartFlow/StopFlow/CancelFlowExecutions/Describe*Connector*/executions unbound. Flow status stored only (Draft/Active), no execution.
- Collisions: none. Uncertain: UpdateFlow requires full config (needs full replace not merge).

### #62 AWS Clean Rooms — A
- cleanrooms / rest-json / signing `cleanrooms` (second model cleanroomsml, signing `cleanrooms-ml`, no clash).
- configuredTable (Create/Get/List/Update/Delete/Tag; output wrapper `configuredTable`), configuredTableAnalysisRule child (Parent configuredTableIdentifier, Lookup analysisRuleType, Create/Get/Update/Delete), collaboration (CreateCollaboration/GetCollaboration/ListCollaborations/UpdateCollaboration/DeleteCollaboration), membership (Create/Get/List/Update/DeleteMembership; Parent collaboration), analysisTemplate (SQL text stored), privacyBudgetTemplate.
- Check: allowedColumns non-empty, tableReference Glue names stored unchecked; collaboration creator member required. StartProtectedQuery/Job, PopulateIdMappingTable, PreviewPrivacyImpact, schemas unbound.
- cleanroomsml extra A: trainingDataset, configuredModelAlgorithm (stored role/container refs); CreateAudienceModel/TrainedModel/inference/export jobs unbound.

### #63 Amazon CloudSearch — A (domains only, Query)
- cloudsearch / query / signing `cloudsearch` (new Query service; the cloudsearchdomain REST model also signs `cloudsearch` but is data plane and must stay unclaimed: engine refuses Query+REST on one signing name).
- domain: CreateDomain, DescribeDomains (DomainNames filter), DeleteDomain. ListDomainNames returns a map, not a list, so it cannot be bound.
- Field/expression/suggester/analysis-scheme/option ops need B features (OptionStatus envelope Options+Status with RequiresIndexDocuments state, domain-scoped singletons, Define* put semantics); IndexDocuments/BuildSuggesters unbound. No tags in API.
- No endpoints returned (DocService/SearchService omitted).

### #66 AWS Data Pipeline — B
- datapipeline / json / target `DataPipeline`, signing `datapipeline`.
- Missing: tagging by pipelineId, not ARN (AddTags/RemoveTags, no ListTags; tags come back in DescribePipelines); PutPipelineDefinition/GetPipelineDefinition is a second whole-document (pipelineObjects, parameterObjects, parameterValues) with a validation-error result not stored on the PipelineDescription fields list; DescribePipelines/ListPipelines keyed by id lists; Activate/Deactivate state is run-time. Unlocks: pipeline + definition. docs/aws-catalog.md already lists it as unimplemented.

### #67 Amazon DataZone — A
- datazone / rest-json / signing `datazone`.
- domain (CreateDomain, GetDomain, ListDomains, UpdateDomain, DeleteDomain, tags by ARN; status AVAILABLE immediately, no portalUrl), project (Parent domainIdentifier; Create/Get/List/Update/Delete), glossary (Parent domain, ref owningProjectIdentifier), glossaryTerm (Parent domain, glossaryIdentifier member), later formType/assetType/domainUnit/projectProfile.
- Check: refuse singleSignOn/IdC, KMS; skipDeletionCheck accepted. Environments, data sources, assets, subscriptions, connections (provision/run/external) unbound.
- Uncertain: revisioned form/asset types need version semantics (leave out).

### #68 Amazon EMR — A (three models)
- emr / json 1.1 / target `ElasticMapReduce`, signing `elasticmapreduce`: securityConfiguration (Create/Describe/List/Delete, no update, no tags API by ARN), blockPublicAccessConfiguration (Singleton: Put/GetBlockPublicAccessConfiguration), studio (CreateStudio, DescribeStudio, ListStudios, UpdateStudio, DeleteStudio; tags only via Create since AddTags/RemoveTags use ResourceId = B), studio session mappings (composite identity, B).
- emr-serverless / rest-json / `emr-serverless`: application (Create/Get/List/Update/Delete/Tag; stays CREATED, Start/Stop/StartJobRun unbound).
- emr-containers / rest-json / `emr-containers`: virtualCluster (container provider EKS name stored unchecked), jobTemplate, securityConfiguration (Create/Describe/List/Delete + tags).
- RunJobFlow, clusters, steps, instance groups, notebooks unbound.

### #72 AWS Lake Formation — A (partial)
- lakeformation / rest-json (all POST /<Op>) / signing `lakeformation`; AccountMembers CatalogId.
- LF-tag (TagKey identity; CreateLFTag/GetLFTag/UpdateLFTag via TagValuesToAdd/ToDelete with a Merged hook/DeleteLFTag/ListLFTags), LF-tag expression (Name identity), data lake settings (Singleton Put/Get), registered resource (RegisterResource/DescribeResource/ListResources/UpdateResource/DeregisterResource keyed by S3 ResourceArn via Kind Put; unverified that Put accepts the empty register output).
- B-features, left unbound: DataCellsFilter and opt-ins (composite Catalog+Database+Table+Name identity), Grant/Revoke/ListPermissions (principal x resource grants, no identity), LF-tags on resources (AddLFTagsToResource, search), transactions, query planning, temporary credentials.

### #73 Amazon Managed Service for Apache Flink — B
- kinesisanalyticsv2 / json / target `KinesisAnalytics_20180523`, signing `kinesisanalytics` (v1 `kinesisanalytics` is SQL, legacy, same signing name).
- Missing: optimistic version token (CurrentApplicationVersionId, bumping ApplicationVersionId, ConditionalToken), update shape (ApplicationConfigurationUpdate) differing from stored ApplicationConfigurationDescription, and nested child Add*/Delete* operations (outputs, logging, VPC, reference data) that mutate the parent and bump the version. CreateApplication itself stays READY, so the resource is honest once those exist. Start/Stop/Snapshot/Rollback unbound. Matches docs/aws-catalog.md.

### #76 Amazon Quick (Quick Sight) — A (narrow)
- quicksight / rest-json / signing `quicksight`; AccountMembers AwsAccountId.
- folder (client-chosen FolderId; Create/Describe/Update/Delete/ListFolders; wrapper `Folder`; ParentFolderArn Ref folder; Tags by ARN), namespace (Create/Describe/List/Delete, name identity). Theme is versioned (VersionNumber, Configuration nested under Version) so it needs a Check to reshape; treat as optional.
- Unbound/needs more: users/groups (AwsAccountId+Namespace+name composite), data sources and data sets (connect to external data), analyses/dashboards (definition validation), CreateAccountSubscription, VPC connections, ingestions.
- Does not clash with other signing names; 225 ops, only a handful extracted.

### #78 AWS B2B Data Interchange — A
- b2bi / json / target `B2BI`, signing `b2bi`.
- profile (Create/Get/List/Update/Delete/Tag; logging stored, logGroupName generated), capability (Create/Get/List/Update/Delete; instructionsDocuments S3 refs stored unchecked), transformer (Create/Get/List/Update/Delete, status inactive), partnership (profileId and capability ids are ids, not ARNs, so cross-checks need a Check or id Refs).
- Unbound: StartTransformerJob, GetTransformerJob, TestMapping/Parsing/Conversion, GenerateMapping, CreateStarterMappingTemplate.

### #82 Amazon Simple Workflow Service — B
- swf / json / target `SimpleWorkflowService`, signing `swf`.
- Missing: Register*/Deprecate/Undeprecate lifecycle (RegisterDomain returns nothing, name is the identity, deprecation is a status transition with different delete rules), composite type identity (domain+name+version) for workflow and activity types, DescribeDomain split into domainInfo plus configuration members. Unlocks: domains (tags by domain ARN) and workflow/activity types. Executions and tasks are workload and stay unbound. Listed unimplemented in docs/aws-catalog.md.

### #84 Amazon Q — A (Q Business; Q in Connect optional)
- qbusiness / rest-json / signing `qbusiness`: application (CreateApplication/GetApplication/ListApplications/UpdateApplication/DeleteApplication, Tag), index (Parent applicationId), retriever (Parent applicationId), chatResponseConfiguration, chat controls (singleton per application), dataAccessor.
- Check: refuse IAM Identity Center/OIDC auth that implies external IdP; plugins (OAuth), data sources and sync jobs, web experiences (URL), Chat, subscriptions unbound.
- qconnect / rest-json / signing `wisdom` (collides with legacy `wisdom` model; claim only one): assistant, aiGuardrail, aiPrompt, aiAgent, quickResponse, messageTemplate are stored config; knowledge base content and sessions are not. qapps: libraries hold generated content, skip.
- "Q Developer" is already a no-model item (#287).

### #86 Amazon Connect Customer — A
- connect / rest-json / signing `connect` (separate models: connectcampaigns and connectcampaignsv2 both sign `connect-campaigns`, cases signs `cases`, customer-profiles `profile`, voiceid, qconnect `wisdom`).
- instance (CreateInstance/DescribeInstance/ListInstances/DeleteInstance, tags by ARN), children with Parent instanceId (alnum ids): hoursOfOperation, queue (hoursOfOperationId ref), quickConnect, agentStatus, securityProfile, routingProfile, userHierarchyGroup, contactFlow and flow module (JSON content stored, never executed), taskTemplate, predefinedAttribute, prompt (S3 audio unbound), rule. Many Update* ops are partial per attribute (UpdateQueueName, UpdateQueueMaxContacts...), so each is an Update op with its own Fields.
- Users need passwords/identity: unbound. Contacts, outbound calls, phone numbers (claim/purchase), data tables, vocabularies (training) unbound. 341 ops, extract a subset.

### #90 Amazon Chime SDK — A with a tagging collision
- chime-sdk-identity, -messaging, -voice, -media-pipelines, -meetings / rest-json; every one signs `chime` (legacy `chime` too, EOL #89).
- identity: appInstance, appInstanceUser, appInstanceBot (Create/Describe/List/Update/Delete). messaging: channel and channelFlow (ChimeBearer header carries the caller), channel membership, streaming configuration. voice: voiceConnector (+ Put*Termination/Origination/Logging singleton-per-parent configs), voiceConnectorGroup, sipMediaApplication, sipRule, voiceProfileDomain. media-pipelines: mediaInsightsPipelineConfiguration. Unbound: meetings and attendees (media placement), pipelines, phone number orders/proxy sessions (purchase/run).
- Collision: all five models share route `POST /tags?operation=tag-resource`, `.../untag-resource` and `GET /tags`; the engine resolves a shared signing name by route literals only, so tagging is ambiguous. Bind tag ops in at most one model or add disambiguation (B feature).
- Note docs/coverage-assessment.md says #90 "is served" but selection.json has no chime model.

### #91 Amazon Connect Decisions — D
- No botocore model exists (`connectdecisions`, no `decision` service in the 1.41.6 list). Add to the "no public API" table.

### #100 Amazon WorkMail — A (organization-scoped configuration)
- workmail / json / target `WorkMailService`, signing `workmail` (workmailmessageflow is a data plane).
- organization (CreateOrganization/DescribeOrganization/ListOrganizations/DeleteOrganization, tags by ARN), children with Parent OrganizationId: impersonationRole (Create/Get/List/Update/Delete), mobileDeviceAccessRule, availabilityConfiguration (Create/Update/Delete/List; Test unbound), accessControlRule (PutAccessControlRule keyed by Name), group and resource (Create/Describe/Update/Delete, no mailboxes).
- Unbound: users (passwords, mailboxes), RegisterToWorkMail/DeregisterFromWorkMail (register naming), mailbox permissions/export, ResetPassword, Assume*.
- Judgement call: an organization is a mail service record; no SMTP/IMAP exists.

### #101 AWS Billing and Cost Management — A (several models), billing views B
- ce / json / target `AWSInsightsIndexService`, signing `ce`: costCategory (CreateCostCategoryDefinition/DescribeCostCategoryDefinition/ListCostCategoryDefinitions/UpdateCostCategoryDefinition/DeleteCostCategoryDefinition, ARN lookup), anomalyMonitor (Create/GetAnomalyMonitors with MonitorArnList filter/Update/Delete), anomalySubscription (same; MonitorArnList refs), TagResource/UntagResource/ListTagsForResource. Get*Cost/Usage/Forecast/Recommendation/Anomalies read AWS-owned data and stay unbound. Check: monitor spec/type pairing.
- bcm-data-exports / json / `bcm-data-exports`: export (CreateExport/GetExport/ListExports/UpdateExport/DeleteExport + tags; query text stored, never run); GetTable/ListTables are AWS-owned schema.
- billing / json / `billing`: views need the AWS-owned primary billing view as source (sourceViews required); no seeded AWS view exists, so B (seeded or ref-free parent). bcm-pricing-calculator estimates compute from AWS prices: unbound. cost-optimization-hub, freetier: AWS-owned data (C).
- Collisions: budgets, cur, billingconductor already served under their own prefixes.

### #104 Amazon Lightsail — B
- lightsail / json / target `Lightsail_20161128`, signing `lightsail`.
- Missing: every mutation returns an `operations` list of generated Operation records (id, status, resourceName, createdAt, isTerminal) that the projector cannot synthesize; names are unique across resource types (GetActiveNames). Unlocks: domains and domain entries, contact methods, alarms (PutAlarm), imported key pairs, static IP allocation, snapshots. Instances, databases, load balancers, container services, distributions, buckets, certificates provision or validate externally and stay unbound. Not determined: whether a bounded subset (e.g. Alarm) is worth that engine feature.

### #105 AWS Batch — A (partial; job definitions B)
- batch / rest-json (POST /v1/...) / signing `batch`.
- schedulingPolicy (Create/Describe by arns list (Names)/List/Update/Delete), consumableResource, jobQueue (computeEnvironmentOrder is a list of structures containing ARNs; Refs may not reach nested members, so check or skip refs), computeEnvironment (record that stays CREATING per eks/kafka precedent; managed vs unmanaged stored), tags by ARN.
- B feature for job definitions: RegisterJobDefinition/DeregisterJobDefinition (no create, automatic name:revision numbering, INACTIVE instead of delete). SubmitJob, ListJobs, Terminate/Cancel, service jobs, snapshots unbound.
- docs/aws-catalog.md lists Batch as unimplemented; revisit once job definitions are scoped out.

### #108 AWS Outposts — A
- outposts / rest-json / signing `outposts`.
- site (Create/Get/List/Update/Delete + Tag; GetSiteAddress/UpdateSiteAddress as Update on a stored member), outpost (Parent SiteId; Create/Get/List/Update/Delete + Tag; wrappers `Site`, `Outpost`; AZ stored unchecked).
- Unbound: CreateOrder/CancelOrder (purchase), catalog items, assets, capacity tasks, StartConnection, decommission, billing information (AWS-owned).

### #109 AWS Parallel Computing Service — A
- pcs / json / target `AWSParallelComputingService`, signing `pcs`.
- cluster (record that stays CREATING, like eks/kafka; Create/Get/List/Update/Delete + tags), computeNodeGroup and queue (Parent clusterIdentifier; Create/Get/List/Update/Delete). clusterIdentifier accepts name or id (LookupAlt). Responses wrap in `cluster`, `queue`, `computeNodeGroup`.
- RegisterComputeNodeGroupInstance unbound. Launch template, AMI, subnets stored unchecked; no Slurm endpoint or controller.
- Uncertain: whether Lookup resolves name-or-id in one member.

### #110 AWS Serverless Application Repository — A (with caveat)
- serverlessrepo / rest-json / signing `serverlessrepo`.
- application (CreateApplication/GetApplication/ListApplications/UpdateApplication/DeleteApplication; ApplicationId is the ARN, name unique; ARN contains `/` in the path label), version (Parent ApplicationId, CreateApplicationVersion as Put on SemanticVersion, ListApplicationVersions), policy (PutApplicationPolicy/GetApplicationPolicy/UnshareApplication as application member). No tag API.
- Check must derive Version members (ParameterDefinitions, ResourcesSupported, RequiredCapabilities) from TemplateBody or omit; CreateCloudFormationChangeSet and CreateCloudFormationTemplate (generate artifacts) unbound; ListApplicationDependencies unbound.

### #116 AWS CloudHSM — B
- cloudhsmv2 / json / target `BaldrApiService`, signing `cloudhsm`; classic cloudhsm / json / target `CloudHsmFrontendService`, signing `cloudhsm` (different JSON prefixes, no clash).
- Missing: tagging by ResourceId (TagResource/UntagResource/ListTags take a cluster or backup id, not an ARN), DescribeClusters/DescribeBackups use Filters maps. Honest config exists (cluster records that stay UNINITIALIZED, backup attributes, backup resource policy), but backups only arise from clusters.
- Classic: hapg and Luna client (Create/Describe/Modify/Delete/List, ARN-based tags) are configuration only, but classic CloudHSM is a retired product; I could not confirm its lifecycle from the pinned model. CreateHsm (both) provisions an appliance and stays unbound.

### #121 AWS re:Post Private — A
- repostspace / rest-json / signing `repostspace`.
- space (CreateSpace returns only spaceId; Get/List/Update/Delete + tags by ARN; status CREATE_SUCCEEDED stored; no vanity domain or site), channel (Parent spaceId; Create/Get/List/Update; no delete op, removed by cascade with the space).
- Unbound: RegisterAdmin/DeregisterAdmin (register naming), BatchAddRole/BatchRemoveRole and channel-role batches (accessor assignments), SendInvites (sends email).


General notes: REST-JSON services sharing a signing name are supported (bySigning holds a slice), so ivs/ivs-realtime and personalize/personalize-runtime/-events are not collisions. Query services cannot share a signing name (none here). Where "A" lists a resource that needs a stored "generated" AWS-owned value (endpoint URL, ingest credential, stream key, token), the plan is to omit it (output members are optional) rather than invent it.

### #126 Amazon Keyspaces — A
- keyspaces / JSON 1.0 / signing `cassandra`, target `KeyspacesService`.
- keyspace (id name): CreateKeyspace, GetKeyspace, ListKeyspaces, UpdateKeyspace (replicationSpecification), DeleteKeyspace.
- table (Parent keyspace via keyspaceName, name tableName): CreateTable, GetTable, ListTables, DeleteTable; UpdateTable only for plain members (capacitySpecification, pointInTimeRecovery, ttl, defaultTimeToLive, ...); `addColumns` needs Check/Merged to append to schemaDefinition.allColumns (or leave refused).
- type (parent keyspace): CreateType, GetType, ListTypes, DeleteType. Tag/Untag/ListTags on resourceArn (tags are lowercase key/value lists).
- ARN literal path: arn:{partition}:cassandra:{region}:{account}:/keyspace/{parentName}/table/{name}. Unbound: RestoreTable (PITR data), GetTableAutoScalingSettings (derived). System keyspaces (system, system_schema) are AWS-owned: do not list. docs/aws-catalog.md lists Keyspaces as unimplemented but gives no evidence; coverage-assessment has no entry.
- Check hooks: keyspace/table name pattern; table needs at least one partition key in schemaDefinition; CDC/replica specs refused if unsupported; deletion with tables refused (parent rule).

### #132 AWS Cloud Control API — C
- cloudcontrol / JSON 1.0 / signing `cloudcontrolapi`. Every operation (Create/Update/DeleteResource, GetResource, ListResources) creates, mutates or reads real resources of any other AWS service by type name through its handler; no configuration of its own besides request-status records that exist only as a result of that work.

### #137 Amazon CodeCatalyst — B
- codecatalyst / REST-JSON / signing `codecatalyst`, auth `bearer` (personal access token), not SigV4.
- Missing: (1) bearer/PAT authentication instead of SigV4+IAM; (2) spaces cannot be created through the API (no CreateSpace), so projects need a pre-existing parent; (3) identity is a composite of spaceName + name (path labels), no ARNs, no tags.
- Would unlock: project (Create/Get/List/Update/Delete) and source repository (empty repo record) once a space can be seeded. Dev environments, workflows, access tokens and sessions run compute or mint credentials: unbound.

### #165 Amazon CodeGuru — A (Profiler only)
- codeguruprofiler / REST-JSON / signing `codeguru-profiler`.
- profilingGroup (id profilingGroupName, ARN arn:{partition}:codeguru-profiler:{region}:{account}:profilingGroup/{name}): CreateProfilingGroup (clientToken required, tags map), DescribeProfilingGroup, ListProfilingGroups (two outputs: profilingGroupNames via ListValue, profilingGroups when includeDescription; verify engine handles both), UpdateProfilingGroup (agentOrchestrationConfig), DeleteProfilingGroup, Tag/Untag/ListTags (/tags/{resourceArn}).
- Possible extras needing check: notification channels (AddNotificationChannels, GetNotificationConfiguration, RemoveNotificationChannel: child collection of the group, likely B) and PutPermission/GetPolicy/RemovePermission (revisionId tokens, B). Profiles, recommendations, ConfigureAgent, PostAgentProfile: AWS-owned or data plane, unbound.
- codeguru-reviewer: C (AssociateRepository links a real CodeCommit/GitHub/Bitbucket repo and CreateCodeReview analyses code; recommendations are AWS-owned). codeguru-security: C/marginal (CreateScan runs scans; only a singleton encryption account configuration is configuration).

### #168 Amazon DevOps Guru — B
- devops-guru / REST-JSON / signing `devops-guru`.
- Honest config exists but is awkward: notification channels (AddNotificationChannel -> generated Id, RemoveNotificationChannel by Id, ListNotificationChannels; no read, no ARN, no tags), resource collection (UpdateResourceCollection uses Action ADD/REMOVE over CloudFormation/tag collections), service integration and event-source config (Describe/Update singletons nested in structures).
- Missing: list-member add/remove actions on an update, singleton Update whose input wraps the config in a structure, resources with no ARN/read. Insights, anomalies, events, recommendations, cost estimation, org health: AWS-owned data.

### #169 Amazon Forecast — A
- forecast / JSON 1.1 / signing `forecast`, target `AmazonForecast`. (forecastquery shares the signing name; QueryForecast is inference, C.) Note: closed to new customers; not in coverage-assessment end-of-support table.
- dataset: CreateDataset, DescribeDataset, ListDatasets, DeleteDataset (no update op). datasetGroup: CreateDatasetGroup, DescribeDatasetGroup, ListDatasetGroups, UpdateDatasetGroup (DatasetArns, Refs to dataset), DeleteDatasetGroup. Tag/Untag/ListTags (tags list Key/Value) on ResourceArn.
- ARNs arn:{partition}:forecast:{region}:{account}:dataset/{name} and dataset-group/{name}; status ACTIVE. Check: Schema attributes vs Domain/DatasetType minimum, group Domain equals dataset domain.
- Unbound: import jobs, predictors, forecasts, explainability, monitors, what-if, exports, Stop/ResumeResource, GetAccuracyMetrics, DeleteResourceTree (training/inference work).

### #170 Amazon Fraud Detector — A
- frauddetector / JSON 1.1 / no signingName (endpointPrefix `frauddetector`), target `AWSHawksNestServiceFacade`. Closed to new customers since 2025; still a model.
- Kind Put + Filters-by-name List + Delete (no read op; same pattern as Glacier CreateVault Put and Events PutRule): entityType (PutEntityType/GetEntityTypes/DeleteEntityType), label (PutLabel/GetLabels/DeleteLabel), outcome (PutOutcome/GetOutcomes/DeleteOutcome), variable (CreateVariable/UpdateVariable/GetVariables/DeleteVariable), list (CreateList/UpdateList/GetListsMetadata/DeleteList; elements via UpdateList updateMode, GetListElements is a read of members), eventType (PutEventType/GetEventTypes/DeleteEventType; references variables, labels, entity types by NAME, not ARN, so Refs cannot enforce -> Check cannot see store), detector (PutDetector/GetDetectors/DeleteDetector; eventTypeName by name). Tag/Untag/ListTags on resourceARN.
- Unbound: models and model versions (training), batch import/prediction jobs, GetEventPrediction, SendEvent/GetEvent/DeleteEvent (data plane), external models (SageMaker link), rules/detector versions (expression evaluation; stored-only possible but versioned/immutable semantics), KMS key.
- Engine gap for full value: name-based references between kinds.

### #171 AWS HealthImaging — A (thin, datastore container only)
- medical-imaging / REST-JSON / signing `medical-imaging`.
- datastore: CreateDatastore (clientToken, kmsKeyArn, tags, lambdaAuthorizerArn), GetDatastore, ListDatastores, DeleteDatastore, Tag/Untag/ListTags. No update op. Same precedent as dsql cluster / timestream database (record stays CREATING/ACTIVE, no data plane). The data-plane endpoint is on runtime-medical-imaging and is not served.
- Unbound: DICOM import jobs, image sets, frames, metadata, search (data plane/real processing).

### #172 AWS HealthLake — A (thin, datastore container only)
- healthlake / JSON 1.0 / signing `healthlake`, target `HealthLake`.
- datastore: CreateFHIRDatastore, DescribeFHIRDatastore, ListFHIRDatastores, DeleteFHIRDatastore, Tag/Untag/ListTags. No update. Refuse PreloadDataConfig (Synthea data is AWS-owned), do not report DatastoreEndpoint. Skeptic note: a FHIR store is a provisioned service; justified only by the dsql/timestream precedent.
- Unbound: Start/Describe/List FHIR import and export jobs (real data movement).

### #173 AWS HealthOmics — A
- omics / REST-JSON / signing `omics` (several hostPrefix values; irrelevant locally). Requests carry required requestId.
- workflow (id numeric alnum): CreateWorkflow, GetWorkflow, ListWorkflows, UpdateWorkflow, DeleteWorkflow; workflowVersion (Parent workflow, name versionName): CreateWorkflowVersion, GetWorkflowVersion, ListWorkflowVersions, UpdateWorkflowVersion, DeleteWorkflowVersion. Store definitionUri/definitionZip without parsing; do not return the presigned `definition`/`digest`/`readme` or run engine validation.
- runGroup: CreateRunGroup, GetRunGroup, ListRunGroups, UpdateRunGroup, DeleteRunGroup. runCache: CreateRunCache/GetRunCache/ListRunCaches/UpdateRunCache/DeleteRunCache. sequenceStore: Create/Get/List/Update/DeleteSequenceStore. referenceStore: Create/Get/List/DeleteReferenceStore. annotationStore and variantStore (CreateAnnotationStore/GetAnnotationStore/ListAnnotationStores/UpdateAnnotationStore/DeleteAnnotationStore, same for variant; delete route uses name). Tag/Untag/ListTags on /tags/{resourceArn} (tags map).
- Unbound: all runs, import/export/activation jobs, read sets, references (data), multipart uploads, shares (cross-account), s3 access policy (maybe Put per store, B).

### #179 Amazon Personalize — A
- personalize / JSON 1.1 / signing `personalize`, target `AmazonPersonalize`. personalize-runtime and personalize-events share the signing name but are REST and unbound (inference / event ingestion).
- schema: CreateSchema, DescribeSchema, ListSchemas, DeleteSchema. datasetGroup: CreateDatasetGroup, DescribeDatasetGroup, ListDatasetGroups, DeleteDatasetGroup. dataset (Refs schema, datasetGroup): CreateDataset, DescribeDataset, ListDatasets, UpdateDataset (schemaArn), DeleteDataset. filter: CreateFilter, DescribeFilter, ListFilters, DeleteFilter (expression stored, not evaluated). eventTracker (trackingId generated): Create/Describe/List/DeleteEventTracker. solution (training config only): CreateSolution/DescribeSolution/ListSolutions/UpdateSolution/DeleteSolution. Tags are lists with tagKey/tagValue; verify tagLike handles them.
- Unbound: solution versions, campaigns, recommenders, import/export/batch jobs, metric attribution (starts delivery), DescribeAlgorithm/Recipe/FeatureTransformation (AWS-owned catalogs).

### #193 AWS Compute Optimizer — B
- compute-optimizer / JSON 1.0 / signing `compute-optimizer`, target `ComputeOptimizerService`.
- Only configuration: recommendation preferences (Put/Get/DeleteRecommendationPreferences) identified by composite (resourceType + scope.name + scope.value), listed through a Get op returning a list, and the enrollment status singleton (UpdateEnrollmentStatus/GetEnrollmentStatus). Everything else (Get*Recommendations, summaries, projected metrics, exports) is AWS-owned analysis.
- Missing: composite identity without a single Lookup member (dotted path covers one member only) and Put without a generated id. Enrollment status alone would be an A singleton but is not worth a service.

### #201 Amazon Managed Grafana — A (workspace only)
- grafana / REST-JSON / signing `grafana`.
- workspace (id alnum:10:g-, ARN arn:{partition}:grafana:{region}:{account}:/workspaces/{id}): CreateWorkspace (output wraps `workspace`), DescribeWorkspace, ListWorkspaces, UpdateWorkspace, DeleteWorkspace, Tag/Untag/ListTags (/tags/{resourceArn}). Same bounded-record precedent as amp CreateWorkspace; status stays CREATING, endpoint not reported, vpcConfiguration/IAM ids stored unchecked. Check: authenticationProviders vs permissionType, grafanaVersion.
- Needs engine feature (B) for the rest: UpdateWorkspaceConfiguration/Authentication/DescribeWorkspaceConfiguration (per-parent singleton child; engine Singleton is per region), UpdatePermissions (batch grants), license association. API keys, service-account tokens: mint credentials, unbound.
- docs/aws-catalog.md lists Grafana as unimplemented without evidence.

### #203 AWS Organizations — B
- organizations / JSON 1.1 / no signingName (endpointPrefix `organizations`), target `AWSOrganizationsV20161128`; Global service (supported by engine).
- Missing: (1) organization singleton whose id (o-xxx) and implicit root (r-xxx) and management account are parents/ARN parts of everything else; (2) polymorphic parent (OU parent is root or OU; accounts move between parents); (3) many-to-many attach/detach of policies to targets (AttachPolicy/DetachPolicy/ListPoliciesForTarget/ListTargetsForPolicy, no resource of its own); (4) TagResource by ResourceId across accounts/OUs/roots/policies (not ARN); (5) policy types enabled per root; (6) handshakes and account creation involving other accounts.
- Would unlock: OUs, policies (SCP, tag policy etc. stored, never evaluated by the simulator), delegated administrators. CreateAccount/InviteAccount/CloseAccount involve real accounts.

### #218 AWS Deadline Cloud — A (B for associations)
- deadline / REST-JSON / signing `deadline` (hostPrefix management./scheduling.); clientToken header.
- farm: CreateFarm, GetFarm, ListFarms, UpdateFarm, DeleteFarm. Under farm (Parent farmId): queue (Create/Get/List/Update/DeleteQueue), fleet (Create/Get/List/Update/DeleteFleet; stays CREATE_IN_PROGRESS/ACTIVE with no workers), storageProfile (Create/Get/List/Update/Delete), limit (Create/Get/List/Update/DeleteLimit), budget (Create/Get/List/Update/DeleteBudget), queueEnvironment (Create/Get/List/Update/Delete). monitor (global, Identity Center URL): Create/Get/List/Update/DeleteMonitor. Tag/Untag/ListTags (resourceArn).
- B: queue-fleet and queue-limit associations (composite farmId+queueId+fleetId, no id, Create returns nothing, Update by composite), membership ops (AssociateMemberToFarm, etc.). 
- Unbound: jobs, tasks, sessions, workers, steps, license endpoints (provision VPC endpoint), metered products, role assumption (credentials).

### #220 AWS Elemental MediaConnect — A (flows/gateways/bridges/router); B for Add*/Remove* children
- mediaconnect / REST-JSON / signing `mediaconnect`.
- flow (ARN contains generated id and name; lookup FlowArn): CreateFlow (output wraps `Flow`, status STANDBY), DescribeFlow, ListFlows, UpdateFlow, DeleteFlow; gateway: CreateGateway, DescribeGateway, ListGateways, DeleteGateway; bridge: CreateBridge, DescribeBridge, ListBridges, UpdateBridge, DeleteBridge; routerInput/routerOutput/routerNetworkInterface: Create/Get/List/Update/Delete (+BatchGet*). Tags: TagResource/ListTagsForResource on /tags/{ResourceArn}, and global variants TagGlobalResource/UntagGlobalResource/ListTagsForGlobalResource for router resources (two tag routes).
- B: AddFlowSources/Outputs/VpcInterfaces/MediaStreams and Remove*, GrantFlowEntitlements/RevokeFlowEntitlement, AddBridgeOutputs/Sources (child creation as parent-verb operations with lists, not Create on a child kind).
- Unbound: Start/StopFlow, router Start/Stop/Take/Restart, thumbnails/source metadata, offerings and reservations (purchases), gateway instances (registered by on-prem software).

### #221 AWS Elemental MediaConvert — A
- mediaconvert / REST-JSON / signing `mediaconvert`, paths /2017-08-29/...
- queue (name): CreateQueue (output wraps `Queue`; Check refuses PricingPlan RESERVED / ReservationPlanSettings; the Default queue exists in AWS, decide to seed or not), GetQueue, ListQueues, UpdateQueue, DeleteQueue. jobTemplate: Create/Get/List/Update/DeleteJobTemplate. preset: Create/Get/List/Update/DeletePreset (system presets are AWS-owned, omit). policy singleton: PutPolicy/GetPolicy/DeletePolicy. Tags: TagResource (Arn in body), UntagResource, ListTagsForResource returns ResourceTags{Arn,Tags} (verify tag shape).
- Unbound: CreateJob/CancelJob/GetJob/ListJobs/SearchJobs/StartJobsQuery (transcoding), DescribeEndpoints (AWS-owned), Probe (reads media), AssociateCertificate (ACM), CreateResourceShare, ListVersions.

### #222 AWS Elemental MediaLive — A (selected kinds)
- medialive / REST-JSON / signing `medialive`, paths /prod/...
- inputSecurityGroup (generated id): CreateInputSecurityGroup, DescribeInputSecurityGroup, ListInputSecurityGroups, UpdateInputSecurityGroup, DeleteInputSecurityGroup. cloudWatchAlarmTemplateGroup and template (Create/Get/List/Update/Delete each), eventBridgeRuleTemplateGroup and template (same), network (Create/Describe/List/Update/DeleteNetwork), sdiSource, channelPlacementGroup (cluster ref), input (CreateInput/DescribeInput/ListInputs/UpdateInput/DeleteInput; refuse devices/ingest endpoints or not report them), channel (Create/Describe/List/Update/DeleteChannel; stays IDLE, encoderSettings stored, references inputs by id), multiplex and multiplexProgram (parent). Tags via CreateTags/DeleteTags(tagKeys)/ListTagsForResource on /prod/tags/{resource-arn} (operation names, not TagResource).
- Unbound: Start/Stop/Batch*, schedule actions, devices/claim/transfer, clusters/nodes (on-prem registration), signal maps (deployments), offerings/reservations (purchases), thumbnails.

### #223 AWS Elemental MediaPackage — A (three models)
- mediapackage / REST-JSON / signing `mediapackage`: channel (CreateChannel, DescribeChannel, ListChannels, UpdateChannel, DeleteChannel; id is client-chosen), originEndpoint (parent channel via ChannelId): Create/Describe/List/Update/DeleteOriginEndpoint; tags /tags/{resource-arn}. Do not report ingest endpoint URLs/credentials; RotateChannelCredentials/RotateIngestEndpointCredentials, ConfigureLogs, harvest jobs (copy content to S3) unbound.
- mediapackagev2 / signing `mediapackagev2`: channelGroup (Create/Get/List/Update/Delete), channel (parent group, composite lookup by group+channel names), originEndpoint (parent channel; composite). B for Put/Get/DeleteChannelPolicy and OriginEndpointPolicy (per-parent singleton) and ResetChannelState. Harvest jobs unbound. Parent chain has three levels with name identities (verify engine supports 3-deep Parent).
- mediapackage-vod / signing `mediapackage-vod`: packagingGroup (Create/Describe/List/Update/Delete), packagingConfiguration (parent group member PackagingGroupId; Create/Describe/List/Delete). Assets (ingest source from S3) unbound.

### #224 AWS Elemental MediaTailor — A
- mediatailor / REST-JSON / signing `mediatailor` (endpointPrefix api.mediatailor).
- playbackConfiguration (Kind Put by Name): PutPlaybackConfiguration, GetPlaybackConfiguration, ListPlaybackConfigurations, DeletePlaybackConfiguration; do not report HlsConfiguration/PlaybackEndpointPrefix/SessionInitializationEndpointPrefix (AWS-owned) unless generated. sourceLocation: Create/Describe/List/Update/DeleteSourceLocation; vodSource and liveSource (Parent SourceLocationName): Create/Describe/List/Update/Delete; channel: CreateChannel, DescribeChannel, ListChannels, UpdateChannel, DeleteChannel (ChannelState stays STOPPED); program (parent channel+source location refs); prefetchSchedule (parent playbackConfigurationName): Create/Get/List/Delete (stored, never evaluated). Tags: TagResource/UntagResource/ListTagsForResource on /tags/{ResourceArn}.
- B for Put/Get/DeleteChannelPolicy (per-channel singleton), Configure*Logs (per-resource singleton update). Unbound: StartChannel/StopChannel, GetChannelSchedule, ListAlerts (runtime).

### #225 Amazon Interactive Video Service — A (three models)
- ivs / REST-JSON / signing `ivs`: playbackRestrictionPolicy (Create/Get/List/Update/Delete), recordingConfiguration (Create/Get/List/Delete; destination S3 bucket stored, ARN ref only), playbackKeyPair (ImportPlaybackKeyPair/GetPlaybackKeyPair/ListPlaybackKeyPairs/DeletePlaybackKeyPair; Check public key is a P-256 ECDSA PEM, fingerprint derived), channel (CreateChannel output has {channel, streamKey}: stream key is a generated secret, return only if a generator exists, else omit; UpdateChannel/Get/List/Delete/BatchGetChannel), streamKey (parent channel; Create/Get/List/Delete). Tags /tags/{resourceArn}. Unbound: streams, stream sessions, PutMetadata, StopStream, viewer-session revocation (live data).
- ivschat / signing `ivschat`: room (Create/Get/List/Update/Delete), loggingConfiguration (Create/Get/List/Update/Delete). Unbound: CreateChatToken (mints credentials), SendEvent, DeleteMessage, DisconnectUser.
- ivs-realtime (endpointPrefix ivsrealtime, signing `ivs`, shares the scope with ivs; engine allows REST sharing): encoderConfiguration, storageConfiguration, publicKey (Import/Get/List/Delete), stage (CreateStage returns participantTokens: omit), ingestConfiguration (parent stage ref; streamKey secret). Unbound: compositions, participants, replication, CreateParticipantToken.

### #226 AWS Application Discovery Service — B
- discovery / JSON 1.1 / no signingName (endpointPrefix `discovery`), target `AWSPoseidonService_V2015_11_01`.
- Only user-creatable config is the application (CreateApplication -> configurationId, UpdateApplication, DeleteApplications batch by id list); it is read only through ListConfigurations/DescribeConfigurations, which return lists of string-to-string attribute maps of discovered items, and tagging is CreateTags/DeleteTags/DescribeTags by configurationIds. Missing: read/list through a generic attribute-map list output, batch delete by id list, ARN-less ids, tags keyed by configuration id.
- Agents, connectors, import tasks (parse S3 CSV), exports, continuous export, data collection and neighbor graphs are discovered data or real work.

### #228 AWS Database Migration Service — A (config kinds only)
- dms / JSON 1.1 / no signingName (endpointPrefix `dms`), target `AmazonDMSv20160101`. Describe* take `Filters` (list of {Name, Values}) and Marker: refuse Filters in Check or map to equality; no separate Read op, so details come from the list.
- replicationSubnetGroup (id ReplicationSubnetGroupIdentifier): Create/Describe/Modify/Delete (mirrors RDS subnet group); endpoint (CreateEndpoint/DescribeEndpoints/ModifyEndpoint/DeleteEndpoint; refuse Password/secret members, no connection; TestConnection unbound); certificate (ImportCertificate/DescribeCertificates/DeleteCertificate); eventSubscription (Create/Describe/Modify/DeleteEventSubscription); dataProvider, instanceProfile (Create/Describe/Modify/Delete each); migrationProject (Refs to dataProvider and instanceProfile ARNs); replicationConfig (serverless, Refs to endpoints). Tags: AddTagsToResource, RemoveTagsFromResource, ListTagsForResource (ResourceArnList input, TagList output; verify).
- Not servable: replication instances (provision capacity; the catalog does not serve instance-like kinds in RDS), replicationTask (requires a live instance), Start*/Stop*/Reload*/Refresh*/assessments/fleet advisor/metadata-model/recommendations (work or AWS data).

### #229 AWS DataSync — B
- datasync / JSON 1.1 / signing `datasync`, target `FmrsService`.
- Location is one kind with eleven typed Create*/Describe*/Update* operations (S3, EFS, NFS, SMB, FSx variants, HDFS, Azure Blob, object storage) but a shared ListLocations/DeleteLocation and tags by ARN. Missing: a resource kind whose create/read/update ops come in several per-type shapes while list and delete are shared (or per-type kinds sharing one List/Delete). Tasks (CreateTask/DescribeTask/ListTasks/UpdateTask/DeleteTask, Refs to locations, tags) are honest config and become servable once locations are. Secrets (SMB password, Azure SAS token) must be refused.
- Unbound: agents (activation key from a deployed appliance), StartTaskExecution/CancelTaskExecution/UpdateTaskExecution (data transfer).


### #231 AWS Mainframe Modernization — A
- Model m2, rest-json, signing name m2, IAM prefix m2. No collision in selection.json.
- Resource application: CreateApplication, GetApplication, ListApplications, DeleteApplication, TagResource, UntagResource, ListTagsForResource. ARN arn:{partition}:m2:{region}:{account}:app/{id}.
- UpdateApplication creates a new application version and needs currentApplicationVersion: leave unbound, or bind it with a Merged hook that rotates the version.
- Environment (CreateEnvironment, UpdateEnvironment) provisions a managed runtime environment with instances and storage, so it is excluded. StartApplication, StartBatchJob, data set import/export tasks, deployments and GetSignedBluinsightsUrl stay unbound (real work).
- Check hooks: engineType enum and definition (s3Location or content, exactly one). Delete should be refused while the application is deployed or running (never true here).

### #232 AWS Migration Hub — A
- Model mgh, json 1.1, target prefix AWSMigrationHub, signing name mgh. No collision.
- Resource progressUpdateStream (Shape ProgressUpdateStreamSummary): CreateProgressUpdateStream, ListProgressUpdateStreams, DeleteProgressUpdateStream. There is no Describe and no tagging.
- Optional child migrationTask under the stream: ImportMigrationTask (create), DescribeMigrationTask, ListMigrationTasks. The stream is the Parent and the task name the unique name, so no composite identity is needed. NotifyMigrationTaskState and the Associate*/Disassociate* operations are tracker writes and could stay unbound.
- No ARN appears in any official output. The IAM resource ARN template must be chosen by hand (progressUpdateStream/{name}). I could not verify the documented resource type.
- The service needs a home region (migrationhub-config), which is a separate model and not part of this issue. migrationhuborchestrator, migrationhubstrategy and migration-hub-refactor-spaces are separate models I did not assess.

### #233 AWS Transfer Family — A
- Model transfer, json 1.1, target prefix TransferService, signing name transfer. No collision.
- Resources and official operations:
  - workflow (CreateWorkflow, DescribeWorkflow, ListWorkflows, DeleteWorkflow). No update.
  - profile (CreateProfile, DescribeProfile, ListProfiles, UpdateProfile, DeleteProfile).
  - certificate (ImportCertificate, DescribeCertificate, ListCertificates, UpdateCertificate, DeleteCertificate). Needs a PEM Check.
  - connector (CreateConnector and the matching Describe, List, Update, Delete).
  - server (CreateServer and the matching Describe, List, Update, Delete). Serve it as a record only, with StartServer and StopServer as Set transitions. No endpoint exists, so no endpoint address is returned.
  - Children of server: user (CreateUser, DescribeUser, ListUsers, UpdateUser, DeleteUser, keyed by ServerId plus UserName), agreement, access, hostKey. They use Parent. ImportSshPublicKey and ImportHostKey are child lists.
  - tags: TagResource, UntagResource, ListTagsForResource.
- Check hooks: As2 profile needs an AS2 id and certificate ids; Agreement refs server, profile and certificate; identity provider type must have its matching details.
- Unbound (real work): StartFileTransfer, StartDirectoryListing, StartRemoteDelete, StartRemoteMove, TestConnection, TestIdentityProvider, SendWorkflowStepState, ListExecutions, DescribeExecution, ListFileTransferResults.
- Judgment call: a Server is an endpoint in AWS. I would accept it only as a documented bounded record, as the OpenSearch domain precedent does.

### #235 AWS Transform MGN — A
- Model mgn, rest-json, signing name mgn. No collision. The issue text says "served through mgn", but selection.json has no mgn entry.
- Resources:
  - launchConfigurationTemplate (CreateLaunchConfigurationTemplate, DescribeLaunchConfigurationTemplates with the top-level list launchConfigurationTemplateIDs as Names, UpdateLaunchConfigurationTemplate, DeleteLaunchConfigurationTemplate).
  - replicationConfigurationTemplate (the same four operations, with replicationConfigurationTemplateIDs).
  - application, wave and connector (Create, List, Update, Delete, Archive/Unarchive as Set).
  - tags: TagResource, UntagResource, ListTagsForResource.
- application, wave and connector have no Get. Their list filters are nested in a `filters` structure, so read-by-id is not expressible. List-all works and the filter would be refused (partial gap).
- Unbound (real work): StartReplication, StartTest, StartCutover, FinalizeCutover, TerminateTargetInstances, imports and exports, all source-server operations (agent-created, with replication state). InitializeService creates service-linked roles. Server-side actions (PutSourceServerAction, PutTemplateAction) could be added later.

### #238 Amazon CloudFront — B
- Model cloudfront, protocol rest-xml, signing name cloudfront, API 2020-05-31, 167 operations. Native handler cloudfront_vpc_origin*.go owns the /2020-05-31/vpcorigin paths.
- Missing features: REST-XML protocol (the catalog serves JSON 1.0/1.1, REST-JSON and Query) and ETag/If-Match handling. Every update and delete needs the If-Match ETag from the previous read, and responses return ETag and Location headers.
- Honest configuration resources it would unlock: CachePolicy, OriginRequestPolicy, ResponseHeadersPolicy, OriginAccessControl, CloudFrontOriginAccessIdentity, PublicKey, KeyGroup, FieldLevelEncryption config and profile, RealtimeLogConfig, ContinuousDeploymentPolicy, KeyValueStore (the store itself), tags, and Distribution and DistributionTenant as stored config with no edge traffic.
- Stay unbound: CreateFunction/TestFunction (runs code), invalidations, monitoring subscription data, and anything that produces a live domain.
- Collision risk: any generic REST-XML dispatcher must coexist with the native VPC-origin handler. The cloudfront-keyvaluestore model (GetKey, PutKey, ListKeys) is data plane and not servable.

### #239 AWS Direct Connect — A
- Model directconnect, json 1.1, target prefix OvertureService, signing name directconnect. No collision.
- Resource directConnectGateway (Shape DirectConnectGateway; Create/Update/Delete take directConnectGatewayId): CreateDirectConnectGateway, DescribeDirectConnectGateways (list filtered by directConnectGatewayId), UpdateDirectConnectGateway, DeleteDirectConnectGateway. Pure config: name and amazonSideAsn.
- Optional children: directConnectGatewayAssociation (Create/Describe/Update/Delete) and its proposals. These store virtual-gateway or transit-gateway ids without checking another service.
- Tags: TagResource and UntagResource bind. DescribeTags takes resourceArns and returns resourceTags[], which does not fit the single-resource list-tags role, so leave it unbound or mark a partial gap.
- Not servable: Connection, Interconnect, Lag, hosted connections (order physical ports) and virtual interfaces (need a physical connection, BGP peers, tests). LOAs, locations and router configuration are AWS-owned data.

### #247 AWS Verified Access — B
- Model ec2, protocol ec2 (Query variant with Item-wrapped lists), signing name ec2, 749 operations, API 2016-11-15.
- Configuration surface: VerifiedAccessInstance, TrustProvider, Group and Endpoint, with Create*/Describe*/Modify*/Delete* operations plus Attach/DetachVerifiedAccessTrustProvider. Group and endpoint policies use Get/ModifyVerifiedAccess*Policy. Logging uses DescribeVerifiedAccessInstanceLoggingConfigurations and ModifyVerifiedAccessInstanceLoggingConfiguration. Tags go through CreateTags.
- Serving it needs the EC2 protocol, which is missing: Filter.N, TagSpecification.N, DryRun, ClientToken, Item list shapes, XML error format. It also needs a claim of individual ec2 Actions without disturbing the native handlers, which own ec2 DescribeInstances and DescribeVpcs plus CloudFormation EC2 resources.
- It also needs cross-resource references: endpoints reference security groups, subnets, network interfaces or load balancer ARNs that live in native handlers.
- Keep unbound: ExportVerifiedAccessInstanceClientConfiguration and GetVerifiedAccessEndpointTargets (live data); no traffic is ever evaluated.

### #248 AWS Virtual Private Network — B
- Model ec2 (same as above); the configuration surface is the VPN portion of the ec2 model.
- Site-to-Site resources: CustomerGateway (BgpAsn, IpAddress, Type), VpnGateway, VpnConnection, VpnConnectionRoute, ModifyVpnConnection and ModifyVpnConnectionOptions. AttachVpnGateway and DetachVpnGateway need the VPC owned by the native handler.
- VpnConnection returns an AWS-generated CustomerGatewayConfiguration XML with tunnel addresses and pre-shared keys. Tunnels never come up, and Replace/ModifyVpnTunnel* and GetActiveVpnTunnelStatus would have to be bounded or unbound.
- Client VPN: CreateClientVpnEndpoint and its routes, authorization rules and target networks. It needs ACM certificate refs and provisions an endpoint.
- Missing features: the EC2 protocol, claiming individual ec2 Actions next to the native handlers, and references to VPC and Transit Gateway objects in other handlers. See #247.

### #250 Amazon Braket — A (thin)
- Model braket, rest-json, signing name braket. No collision.
- Resource spendingLimit: CreateSpendingLimit, UpdateSpendingLimit, DeleteSpendingLimit, SearchSpendingLimits (list; its nested `filters` would be refused), plus tags (TagResource, UntagResource, ListTagsForResource). There is no Get.
- Everything else is real work or AWS-owned data: Jobs and QuantumTasks execute, devices (GetDevice, SearchDevices) are AWS-owned. They stay unbound.
- It is only one resource, so value is low. Spending limit is stored without enforcement.

### #251 AWS Ground Station — A
- Model groundstation, rest-json, signing name groundstation. No collision.
- Resources:
  - config (CreateConfig, GetConfig, ListConfigs, UpdateConfig, DeleteConfig). The ARN includes the config type, arn:...:config/{configType}/{id}, which the one-placeholder ARN template cannot express; this needs a hand-picked template or a Generate $fmt of the ARN member.
  - missionProfile (the matching Create, Get, List, Update, Delete).
  - dataflowEndpointGroup (CreateDataflowEndpointGroup and V2, Get, List, Delete; no update).
  - ephemeris (CreateEphemeris, DescribeEphemeris, ListEphemerides, UpdateEphemeris, DeleteEphemeris) stores element metadata only.
  - tags: TagResource, UntagResource, ListTagsForResource.
- Not servable: ReserveContact, CancelContact, DescribeContact (reserve real satellite time), satellites, ground stations and GetMinuteUsage (AWS-owned), RegisterAgent, UpdateAgentStatus, GetAgentConfiguration.
- Check hooks: mission profile dataflowEdges must name existing configs; minimum viable contact duration range.

### #252 AWS Artifact — A (singleton only)
- Model artifact, rest-json, signing name artifact, 7 operations. No collision. The service is global (us-east-1).
- Only honest resource: singleton accountSettings (GetAccountSettings, PutAccountSettings: notificationSubscriptionStatus). The engine has Singleton resources.
- Reports, report metadata, terms and customer agreements (GetReport, GetReportMetadata, GetTermForReport, ListReports, ListCustomerAgreements) are AWS-owned documents and stay unbound. It is a thin, low-value spec.

### #253 Amazon Cognito — B
- Models: cognito-idp (json 1.1, prefix AWSCognitoIdentityProviderService, signing cognito-idp, 119 operations), cognito-identity (json 1.1, prefix AWSCognitoIdentityService, signing cognito-identity, 23 operations), cognito-sync (rest-json, 17 operations, mostly data plane).
- Honest config: user pool, user pool client, identity provider, group, resource server, terms, managed login branding, user pool domain, identity pool plus SetIdentityPoolRoles.
- Missing features:
  1. Region-prefixed identifiers (<region>_<9 alnum> and <region>:<uuid>): newID supports only uuid, name and alnum/hex with a fixed prefix.
  2. Replace-semantics Updates (UpdateUserPool, UpdateUserPoolClient and UpdateIdentityPool reset omitted members to defaults), while the engine's Update merges. The existing docs/aws-catalog.md already lists Cognito as inexpressible.
  3. Server-generated secrets (client secret, signing certificate).
- Users, devices, auth flows (SignUp, InitiateAuth, Admin*), tokens and import jobs are data plane and stay unbound.
- Mitigation: a create/describe/list/delete/tag-only subset is possible if updates stay unbound, but the ID format remains the blocker.

### #254 AWS Directory Service — A (low confidence)
- Models: ds (json 1.1, prefix DirectoryService_20150416, signing ds, 80 operations); ds-data (rest-json, signing ds-data) is data plane (users and groups inside a running directory): C.
- Only a bounded Directory record is possible: CreateDirectory, CreateMicrosoftAD and ConnectDirectory (three creates), DescribeDirectories (DirectoryIds as Names), DeleteDirectory. Tags use ResourceId, not an ARN. In AWS the creates provision domain controllers, so this is the OpenSearch-domain precedent: no DnsIpAddrs, no domain.
- Children of the directory: conditionalForwarder (Create, Describe, Update, Delete), logSubscription (Create, List, Delete), ipRoute (Add, List, Remove), event topics. Trusts need a reachable external domain (VerifyTrust), snapshots copy data, and Enable*/Disable* switches are per-directory settings.
- If the reviewer rejects a record that provisions nothing, this becomes C.

### #263 AWS Network Firewall — B
- Model network-firewall, json 1.0, target prefix NetworkFirewall_20201112, signing name network-firewall. No collision. Already listed as inexpressible in docs/aws-catalog.md.
- Honest config: FirewallPolicy, RuleGroup, TLSInspectionConfiguration, ResourcePolicy (Put/Describe/DeleteResourcePolicy), ProxyConfiguration, ProxyRuleGroup and proxy rules. Firewall, VpcEndpointAssociation and Proxy create endpoints in VPC subnets (real infrastructure) and stay unbound.
- Missing features:
  1. Required UpdateToken on every update and delete, returned by Create/Describe and rotated on each change. The engine only has a per-row generation (a Merged hook could compare and rotate it, but the design does not give it one).
  2. Describe output splits the resource across several members (UpdateToken, FirewallPolicy body and FirewallPolicyResponse metadata), which the single Shape-per-resource projection does not express.
  3. Name-or-ARN lookup is fine (LookupAlt).
- Unlocks: policies, rule groups, TLS inspection configurations, resource policies, proxy configuration and rule groups.

### #275 Amazon Elastic Block Store — B
- Two models. ebs (rest-json, signing ebs, 6 operations: StartSnapshot, PutSnapshotBlock, GetSnapshotBlock, ListSnapshotBlocks, ListChangedBlocks, CompleteSnapshot) is purely snapshot data plane: C.
- The ec2 model (protocol ec2, signing ec2) holds the configuration: CreateVolume, DescribeVolumes, ModifyVolume, DeleteVolume, AttachVolume and DetachVolume, CreateSnapshot(s), CopySnapshot, DescribeSnapshots, ModifySnapshotAttribute, EbsEncryptionByDefault and its default KMS key, SnapshotBlockPublicAccess and LockSnapshot.
- Missing features: the EC2 protocol, claiming individual ec2 Actions beside the native ec2 handlers, and a reference to instances and AZs owned by native code. Native ec2 handlers currently cover DescribeInstances, DescribeVpcs and CloudFormation EC2 resources only.
- A volume would have to be a metadata record: it holds no data. A snapshot copies data, so serve it only as a record with no blocks. Attaching to a native guest VM needs the guest attachment code owned by others.

### #276 AWS Elastic Disaster Recovery — A
- Model drs, rest-json, signing name drs, 50 operations. No collision.
- Resources: launchConfigurationTemplate and replicationConfigurationTemplate (Create, Describe with ID lists, Update, Delete), tags, launchAction (PutLaunchAction, ListLaunchActions, DeleteLaunchAction; keyed by resourceId plus actionId, which fits Parent if modeled as a child of the template).
- Source networks (CreateSourceNetwork) reference a VPC and drive replication, and are excluded.
- Unbound (real work): all source-server, recovery-instance, job and failback operations, StartRecovery, StartReplication, ReverseReplication, InitializeService.
- Same engine shape as mgn (#235).

### #280 Amazon Kinesis Video Streams — A
- Model kinesisvideo, rest-json, no signingName in metadata so endpointPrefix kinesisvideo is the signing name. No collision with "kinesis" (different credential scope). Related models kinesis-video-media, -archived-media, -signaling and -webrtc-storage are data plane: C.
- Resources:
  - stream: CreateStream, DescribeStream (StreamName or StreamARN via LookupAlt), ListStreams (nested StreamNameCondition would be refused), UpdateStream, UpdateDataRetention, DeleteStream, TagStream/UntagStream/ListTagsForStream or TagResource/UntagResource/ListTagsForResource.
  - signalingChannel: CreateSignalingChannel, DescribeSignalingChannel, ListSignalingChannels, UpdateSignalingChannel, DeleteSignalingChannel.
- Updates require CurrentVersion; use a Merged hook that compares and rotates the stored Version, or leave updates unbound. The ARN carries a creation timestamp (stream/{name}/{ts}), so the template must differ (stream/{name}/{id}) and be documented.
- Unbound: GetDataEndpoint, GetSignalingChannelEndpoint (endpoints), edge agent and media storage, image generation, notification and stream storage configuration (per-stream singleton children).

### #282 AWS Cost Explorer — A
- Model ce, json 1.1, target prefix AWSInsightsIndexService, signing name ce. Global service (Global: true, ARNs have no region). No collision.
- Resources:
  - costCategory: CreateCostCategoryDefinition, DescribeCostCategoryDefinition, ListCostCategoryDefinitions, UpdateCostCategoryDefinition, DeleteCostCategoryDefinition.
  - anomalyMonitor: CreateAnomalyMonitor, GetAnomalyMonitors (MonitorArnList as Names), UpdateAnomalyMonitor, DeleteAnomalyMonitor.
  - anomalySubscription: Create, Get (SubscriptionArnList as Names), Update, Delete. It refs anomalyMonitor ARNs.
  - tags: TagResource, UntagResource, ListTagsForResource.
- Check hooks: cost category rules JSON (rule version CostCategoryExpression/1.0) and subscription threshold, frequency and subscribers (EMAIL or SNS).
- AWS-owned or real work, unbound: Get* cost and usage, forecasts, recommendations, anomalies, cost allocation tags, commitment and Savings Plans analyses.

### #291 AWS Transit Gateway — B
- Model ec2 (protocol ec2, signing ec2). Native ec2 handlers do not serve any TGW action.
- Config surface: TransitGateway, TransitGatewayRouteTable, TransitGatewayVpcAttachment, PeeringAttachment, PrefixListReference, static routes (CreateTransitGatewayRoute, ReplaceTransitGatewayRoute), association/propagation, multicast domains, policy tables and metering policies. No packet forwarding is implied.
- Missing features: the EC2 protocol, per-Action claiming next to native handlers, references to VPCs and subnets owned by the native network actor, and attachment and propagation as edges between resources (Associate/Enable/Disable...). AcceptTransitGateway*/Reject* depend on cross-account state. SearchTransitGatewayRoutes and ExportTransitGatewayRoutes need a route evaluation.
- networkmanager is a separate model that is already served.

### #292 AWS PrivateLink — B
- Model ec2 (protocol ec2, signing ec2).
- Config surface: VpcEndpoint (Gateway, Interface, GatewayLoadBalancer), VpcEndpointServiceConfiguration, service permissions, connection notifications, ModifyVpcEndpoint*, Accept/RejectVpcEndpointConnections and private DNS verification (StartVpcEndpointServicePrivateDnsVerification).
- Missing features: the EC2 protocol, per-Action claiming next to native handlers, references to VPCs, subnets, route tables, security groups and load balancer ARNs (elbv2 is owned by native elb_dispatch.go), and Interface endpoints creating ENIs and DNS names (provisioned in AWS).
- DescribeVpcEndpointServices returns an AWS-owned service catalog and stays unbound.

### #294 Amazon AI Operations — A
- Model aiops, rest-json, signing name aiops (CloudWatch investigations), 11 operations. No collision. I did not verify that the issue means this model (the product name is not an official botocore title).
- Resource investigationGroup: CreateInvestigationGroup, GetInvestigationGroup, ListInvestigationGroups, UpdateInvestigationGroup, DeleteInvestigationGroup, tags (TagResource, UntagResource, ListTagsForResource).
- Resource policy: PutInvestigationGroupPolicy, GetInvestigationGroupPolicy, DeleteInvestigationGroupPolicy, as a Put-kind or child singleton keyed by the group identifier.
- Check hooks: retention range, role ARN, chatbot notification channel ARN. Nothing is investigated.

### #295 AWS Application Auto Scaling — B
- Model application-autoscaling, json 1.1, target prefix AnyScaleFrontendService, signing name application-autoscaling. No collision. Already listed as unimplemented in docs/aws-catalog.md.
- Config resources: scalable target (RegisterScalableTarget, DeregisterScalableTarget, DescribeScalableTargets), scaling policy (PutScalingPolicy, DeleteScalingPolicy, DescribeScalingPolicies), scheduled action (PutScheduledAction, DeleteScheduledAction, DescribeScheduledActions), tags on the target.
- Missing features:
  1. Composite identity with no id or name: (ServiceNamespace, ResourceId, ScalableDimension) plus a PolicyName or ScheduledActionName; spec.Lookup is a single member.
  2. Register/Deregister naming instead of Create/Delete, with upsert semantics.
  3. Describe-only reads filtered by a namespace, a ResourceIds list and a dimension.
  4. A required reference to a resource in another service (ECS service, DynamoDB table, ...).
- Not servable: scaling activities and the predictive forecast (GetPredictiveScalingForecast) are evaluation outputs.

### #298 AWS CodeStar Notifications

Five native rule operations now link local CodeCommit branch creation and updates to configured Standard SNS topics and SQS subscriptions. The standalone service owns rule identity, filter traversal, pending delivery and retry. Event catalogs for additional providers, tags, independent target management, FULL detail and external notification endpoints remain open. [Local behavior and limits](aws-codestar-notifications.md)

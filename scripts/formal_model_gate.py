"""Fail-closed finite TLC validation; no implementation/refinement proof."""
import hashlib
import json
import os
import re
import shutil
import stat
import subprocess
import tempfile
import time
from pathlib import Path

BASE_MODELS = {'BedrockRuntimeAdmission', 'ELBTargetLifecycle', 'AppEngineTraffic', 'Route53AtomicBatch', 'S3MultipartState', 'AzureBlobState',
 'CatalogConsoleEpoch',
 'CatalogResourceLifecycle',
 'AzurePropertiesEpoch',
 'AzurePropertiesTarget',
 'BlobRange',
 'BrowserSessions',
 'CFNConditionAdmission',
 'CFNConditionMemoDepth',
 'CFNFinalCleanupACK',
 'CFNPhysicalResourceACK',
 'CloudWatchLogsLifecycle',
 'CloudWatchLogsViewEpoch',
 'DynamoDBAdmission',
 'DynamoDBConsoleEpoch',
 'DynamoDBLifecycle',
 'GCPStorageState',
 'IAMManagedPolicies',
 'KMSAliasCatalog',
 'KMSAliasCrypto',
 'KMSAliasesViewEpoch',
 'KMSBootstrapTransaction',
 'KMSKeyState',
 'KMSProtocolAdmission',
 'NativeIAMOwnership',
 'NativeWireProjection',
 'ObjectStore',
 'ProjectAuthority',
 'ProviderActor',
 'ProviderPath',
 'RESTDeploymentSnapshot',
 'RESTProxyEventBoundary',
 'RESTRouteMatching',
 'S3Versions',
 'SQSConsoleEpoch',
 'SQSLifecycle',
 'SQSStorageBudget',
 'SSMParameterState',
 'SakuraDiskAdmission',
 'SakuraDiskMetadata',
 'SakuraDisksViewEpoch',
 'SakuraStartupAdmission',
 'SecretMetadataViewEpoch',
 'SecretVersionListing',
 'SecretsDisclosureChannels',
 'SecretsMutationRefresh',
 'SecretsRevealEpoch',
 'SecretsVersionLifecycle',
 'StackOwnership',
 'StartupTokenVisibility',
 'LandingTopologyState', 'LandingAccessAdmission', 'LoginInputIntent'}
BASE_INVARIANTS = {'BedrockRuntimeAdmission': {'AuthorizationFirst', 'TypeOK', 'SuccessOnlyFromBackend', 'TruthfulFailure', 'BackendOnlyAfterAdmission', 'NoFallback', 'NoCallAfterClose', 'NoPersistence', 'ConcurrencyBound'}, 'ELBTargetLifecycle': {'TypeOK', 'ListenerReferencesExist', 'TargetNeedsGroup', 'HealthCurrent', 'ReportedHealthInUse', 'OnlyAuthorizedChanges', 'OtherTeamUnchanged'}, 'AppEngineTraffic': {'SplitComplete', 'ApplicationParent', 'TypeOK', 'DefaultFirst', 'ServedThroughRouteAndFirewall', 'DenialAtomic', 'OtherTeamUnchanged', 'ApplicationPermanent', 'AllocationsReferenceVersions'}, 'Route53AtomicBatch': {'PendingInvisible', 'TypeOK', 'PreserveSentinel', 'AtomicPair', 'OnlyAuthorizedValid', 'OtherTeamUnchanged'}, 'S3MultipartState': {'TypeOK', 'Budget', 'NoOrphans', 'Cleanup', 'RefusalAtomic'}, 'CatalogConsoleEpoch': {'TypeOK', 'NoUnmountedAcceptance', 'ReloadOnlyAfterAck', 'OnlyCurrentReplies', 'FailureVisible', 'SingleWriter'},
 'CatalogResourceLifecycle': {'PeerFrame', 'QuotaBound', 'FailureFrame', 'FreshIDs', 'TokenOnce', 'GenerationAdvances', 'ChildGuard', 'StaleWriteRejected', 'AuthorizationFirst', 'TokenBinding', 'UniqueNames', 'TypeOK', 'ReplayNoDuplicate'},
 'AzureBlobState': {'BudgetBounded',
                    'ConditionEnforced',
                    'FailureAtomic',
                    'FreshWriteIdentity',
                    'NoDeniedRead',
                    'NoOrphans',
                    'NoUnauthorizedMutation',
                    'PeerStatePreserved',
                    'ReadExact',
                    'TypeOK'},
 'AzurePropertiesEpoch': {'AcceptedOnlyCurrent',
                          'ClearedWhenClosed',
                          'CurrentProperties',
                          'CurrentRequestLoading',
                          'FailureNoDisclosure',
                          'LoadingClearsPrevious',
                          'LocaleStable',
                          'SuccessfulOnly',
                          'TypeOK'},
 'AzurePropertiesTarget': {'DetailCapturedIdentity',
                           'DistinctTargetCarriers',
                           'FieldShapeExact',
                           'NativeHeadOnly',
                           'RequestIdentityExact',
                           'RouteMatchesTarget',
                           'TypeOK'},
 'BlobRange': {'ChecksumBound',
               'ConditionBeforeRange',
               'DeniedNoDisclosure',
               'ExactNormalRead',
               'ExactSelectedBytes',
               'Frame',
               'InvalidRefused',
               'MissingRangeChecksumRefused',
               'NoFullMD5OnPartial',
               'OutOfBoundsRefused',
               'TypeOK'},
 'BrowserSessions': {'AcceptedOnlyLiveParent',
                     'NoBrowserSDKAuthority',
                     'NoRawSDKCookie',
                     'ParentLifetimeBound',
                     'RevokedCookiesDenied',
                     'SDKAuthorityPreserved',
                     'TypeOK',
                     'UnrelatedSessionsPreserved'},
 'CFNConditionAdmission': {'AllConditionsValidated',
                           'BothBranchesValidated',
                           'ConditionalOutputsAbsent',
                           'DisabledDeclarationsValidated',
                           'DisabledResourcesAbsent',
                           'ListNoValuePruned',
                           'MapNoValuePruned',
                           'NoEarlyPublication',
                           'NoResultOnInvalid',
                           'ParameterAdmission',
                           'PlanExact',
                           'SelectedBranchExact',
                           'SelectedLinkValidated',
                           'TypeOK'},
 'CFNConditionMemoDepth': {'AdmissionExact',
                           'AllDefinitionsValidated',
                           'MemoHeightsExact',
                           'MemoValuesExact',
                           'NoCycleMemoized',
                           'NoInvalidGraphAccepted',
                           'NoMalformedMemoized',
                           'NoOverDepthAccepted',
                           'NoUnknownMemoized',
                           'PublishedValuesExact',
                           'RejectedPublishesNoValues',
                           'TypeOK'},
 'CFNFinalCleanupACK': {'FailureAtomic',
                        'NoFalseFinalRecordACK',
                        'PeerFramePreserved',
                        'PendingFrameUnchanged',
                        'TypeOK'},
 'CFNPhysicalResourceACK': {'FailureAtomic',
                            'PeerFramePreserved',
                            'PendingFrameUnchanged',
                            'SuccessPhysicalAbsent',
                            'TypeOK'},
 'CloudWatchLogsLifecycle': {'TypeOK', 'TokenAuthority', 'NoOrphans', 'OnlyAcceptedWrites'},
 'CloudWatchLogsViewEpoch': {'NoUnmountedResults', 'TypeOK', 'FreshResults'},
 'DynamoDBAdmission': {'AcknowledgedRemoval',
                       'ActiveTableRequired',
                       'AdmissionExact',
                       'CanonicalKeys',
                       'CommittedBytes',
                       'CreateCommitted',
                       'DeniedFrame',
                       'InitializedBeforeMutation',
                       'ItemBudget',
                       'ItemCountBudget',
                       'NoOrphans',
                       'PeerFrame',
                       'RefusalFrame',
                       'RemovalAdmissionExact',
                       'SemanticBytes',
                       'StartupAdmission',
                       'StartupRefusedFrame',
                       'StatisticsExact',
                       'TableAdmissionExact',
                       'TableBudget',
                       'TypeOK',
                       'ValidKeysOnly'},
 'DynamoDBConsoleEpoch': {'AckBelongsToCurrentView',
                          'AckClosesDraft',
                          'CancelNoOrphanLoading',
                          'DisplayedKeyBound',
                          'LocalePreservesDraft',
                          'NoStalePublication',
                          'ReadGenerationVerified',
                          'ReadRetryNeverWrites',
                          'SingleSendBeforeACK',
                          'TypeOK'},
 'DynamoDBLifecycle': {'ConditionalAtomicity',
                       'FailedUnchanged',
                       'GenerationChecked',
                       'NoForeignMutation',
                       'NoOrphans',
                       'NoStaleItems',
                       'NoUnauthorizedMutation',
                       'TypeOK'},
 'GCPStorageState': {'Budget',
                     'DeniedFrame',
                     'GenerationFreshness',
                     'GenerationRefusal',
                     'NoOrphans',
                     'NonemptyDeleteRefused',
                     'OtherEnvironmentUnchanged',
                     'OtherProjectUnchanged',
                     'ReadIsolation',
                     'TypeOK'},
 'IAMManagedPolicies': {'AggregateBudget',
                        'AttachmentAuthority',
                        'DefaultExists',
                        'DeleteDependencies',
                        'ExplicitDenyWins',
                        'FailureFrame',
                        'FreshIncarnations',
                        'ImmutableVersions',
                        'PeerFrame',
                        'RoleViewsExact',
                        'TypeOK',
                        'VersionCap',
                        'VersionIDsMonotonic'},
 'KMSAliasCatalog': {'AcknowledgedSuccess',
                     'AdmissionBudgets',
                     'ConcreteTarget',
                     'ContinuationAfterLast',
                     'CreationIdentity',
                     'CursorBound',
                     'FailureFrame',
                     'ListAuthorized',
                     'ListMoreExact',
                     'ListPageExact',
                     'MutationAuthority',
                     'MutationEffect',
                     'NoPartialRead',
                     'PeerRegionFrame',
                     'ReadFrame',
                     'RevisionFresh',
                     'TypeOK'},
 'KMSAliasCrypto': {'AliasResolutionCurrent',
                    'AuthorizationBeforeMismatch',
                    'AuthorizedCryptoStages',
                    'ConcreteKeyAuthorization',
                    'DecryptCounterFrame',
                    'EncryptCounterExact',
                    'FailureCounterFrame',
                    'FailureNoPublication',
                    'NoPublicationBeforeCommit',
                    'OmittedDecryptUsesBlob',
                    'PublishedKeyBound',
                    'TransactionSnapshotStable',
                    'TypeOK',
                    'WriteFailureRefused'},
 'KMSAliasesViewEpoch': {'AckClearsDraft',
                         'AckClosesForm',
                         'ClosedFormHasNoTarget',
                         'CurrentListOutcome',
                         'CurrentTargetMetadata',
                         'DeleteRequiresExactConfirmation',
                         'LocalePreservesIntent',
                         'NoCachedRowsOnFailure',
                         'OnlyVerifiedTargetSubmitted',
                         'ReadRetryNeverWrites',
                         'TypeOK'},
 'KMSBootstrapTransaction': {'AdmissionRequired',
                             'CommitAuthorized',
                             'OperationAuthorized',
                             'PeerFrame',
                             'PreCommitFrame',
                             'PublishedAfterCommit',
                             'RejectedDurableFrame',
                             'SuccessComplete',
                             'SuccessfulScope',
                             'TypeOK'},
 'KMSKeyState': {'EnabledCrypto', 'TypeOK', 'AuthorizedEvents', 'BoundContextAndKey'},
 'KMSProtocolAdmission': {'InfraNotDeny',
                          'MissingIdentityDenied',
                          'NoAuthorityFromWireMetadata',
                          'NoCrossServiceFallback',
                          'NoSuccessWithoutKMSTarget',
                          'TypeOK'},
 'NativeIAMOwnership': {'TypeOK', 'OwnershipExclusive'},
 'NativeWireProjection': {'BadValueRefused',
                          'CompleteProjection',
                          'FailureNoPartial',
                          'OmissionExact',
                          'OrderExact',
                          'SkippedBadDoesNotFail',
                          'TypeOK',
                          'ValueKindsPreserved'},
 'ObjectStore': {'TypeOK', 'NoUnauthorizedMutation', 'NoOrphans', 'DenyWins'},
 'ProjectAuthority': {'ActorUsesPersistedProject',
                      'ConstructorRefusalFrame',
                      'DeniedFrame',
                      'FinitePermissions',
                      'FixedHostConfiguration',
                      'InitializationBindsConfigured',
                      'NoUnboundAdoption',
                      'OnlyActualOwnerAuthorized',
                      'OwnerBindingFrozen',
                      'TypeOK'},
 'ProviderActor': {'AuthenticatedRequestsAttach',
                   'CredentialIdentityPreserved',
                   'CredentialRoleNotWireRole',
                   'NoActorOnRejected',
                   'OperatorExerciseSelection',
                   'ParticipantBound',
                   'TypeOK'},
 'ProviderPath': {'ActorPreserved',
                  'ContextPreserved',
                  'DistinctCarriersRemainDistinct',
                  'EncodedProviderRejected',
                  'EscapedTailPreserved',
                  'ProviderSelectedCorrectly',
                  'SourcePathImmutable',
                  'TailPreserved',
                  'TypeOK',
                  'UnregisteredRejected'},
 'RESTDeploymentSnapshot': {'SnapshotsFrozen', 'NoStageOrphans', 'TypeOK'},
 'RESTProxyEventBoundary': {'ActualRoute',
                            'Admission',
                            'ApplicationData',
                            'BodyPreserved',
                            'CredentialSealed',
                            'FailureNoEvent',
                            'QueryProjection',
                            'TypeOK'},
 'RESTRouteMatching': {'CaptureFidelity',
                       'InvalidTemplateRejected',
                       'MatchOracle',
                       'RejectedShape',
                       'Specificity',
                       'TypeOK'},
 'S3Versions': {'CurrentMatchesLatest', 'TypeOK', 'NoHistoryOrphans'},
 'SQSConsoleEpoch': {'NoUnmountedAcceptance',
                     'OnlyCurrentReplies',
                     'RawReceiptsPrivate',
                     'ReceiptCustodyBound',
                     'TypeOK'},
 'SQSLifecycle': {'AuthorizedMutations',
                  'ChangeOnlyInflight',
                  'FailedMutationAtomic',
                  'FreshReceipt',
                  'IdempotentCreateUnchanged',
                  'NoOrphans',
                  'ReceiptMutationCurrent',
                  'ReceiveOnlyEligible',
                  'ScopedMutation',
                  'TypeOK'},
 'SQSStorageBudget': {'BudgetAccountsPhysical',
                      'DeleteScopeExact',
                      'FailedSendPreservesRows',
                      'ForeignRowsPreserved',
                      'LiveRowsPreserved',
                      'MaintenanceNotBudgetBlocked',
                      'PayloadByteCap',
                      'PhysicalRowCap',
                      'ReadRemovesOwnExpired',
                      'TypeOK'},
 'SSMParameterState': {'DeleteEffect',
                       'DeniedPreservesState',
                       'OtherKeysUnchanged',
                       'OverwriteRefusal',
                       'PutVersion',
                       'ReadIsolation',
                       'TypeOK'},
 'SakuraDiskAdmission': {'ActorBeforeDispatch',
                         'ActorFailureStatus',
                         'AdmissionBeforeLookup',
                         'ConcreteResult',
                         'DeniedFrame',
                         'FailureNoDisclosure',
                         'MappedRealmUsesCopiedHostConfig',
                         'MappedSubjectUsesExerciseIdentity',
                         'MarkerBeforeDispatch',
                         'MarkerFailureStatus',
                         'NoDefaultActorOnExtractionFailure',
                         'NoUnauthorizedLookup',
                         'TypeOK',
                         'UnsupportedNoMutation',
                         'WireHeadersDoNotMapAuthority'},
 'SakuraDiskMetadata': {'ActiveQuota',
                        'AllocatorOnlyCreate',
                        'BindingFrozen',
                        'CreateExact',
                        'DeleteExact',
                        'FailureFrame',
                        'FreshCommittedID',
                        'PeerFrame',
                        'QuotaRefusal',
                        'ReadSnapshot',
                        'StoredIDsBound',
                        'TypeOK',
                        'UpdateExact'},
 'SakuraDisksViewEpoch': {'AcceptedReadScope',
                          'AckClearsDraft',
                          'AckClosesForm',
                          'CancelDraftClear',
                          'LocaleFrame',
                          'NavigationACKFrame',
                          'ReadRetryOnly',
                          'RenderedResourceBound',
                          'RenderedScopeBound',
                          'SingleWriteSend',
                          'StaleReadFrame',
                          'StaleWriteFrame',
                          'SuccessfulReadOnly',
                          'TypeOK'},
 'SakuraStartupAdmission': {'AcceptedConfigExact',
                            'DisabledNoSakura',
                            'EnabledOnlyAuthenticated',
                            'NoEffectsOnRefusal',
                            'RegistrationRequiresSchema',
                            'SchemaRequiresDatabase',
                            'StartupComplete',
                            'TokenOnlyAuthenticated',
                            'TypeOK',
                            'ValidAdmissionAvailable',
                            'ValidationPrecedesEffects'},
 'SecretMetadataViewEpoch': {'CurrentPageOnly',
                             'CursorRequestBound',
                             'LoadingTracksLatest',
                             'LocalePreservesQuery',
                             'MetadataRequestsOnly',
                             'NoRowsOnError',
                             'NoStaleCallbackAcceptance',
                             'NoStaleRenderedError',
                             'NoStaleRenderedMetadata',
                             'ParentRevealClearedForMetadata',
                             'PendingClearsMetadata',
                             'TypeOK',
                             'ValidatedMetadataOnly'},
 'SecretVersionListing': {'ContinuationPosition',
                          'FilterBeforePageLimit',
                          'NoDeniedEmission',
                          'ReadAfterAuthorization',
                          'ReadFramePreserved',
                          'StrictForwardKeyset',
                          'TerminalConverges',
                          'TokenScopeBound',
                          'TypeOK'},
 'SecretsDisclosureChannels': {'DeniedNoRaw',
                               'MetadataLogErrorRedacted',
                               'RawEmissionRequiresGet',
                               'RawRequiresAuthorizedGet',
                               'TypeOK'},
 'SecretsMutationRefresh': {'AckClearsDraft',
                            'AckClosesForm',
                            'FreshFormHasToken',
                            'NoWriteOnReadRetry',
                            'TypeOK'},
 'SecretsRevealEpoch': {'FreshEmissionHistory',
                        'NoUnmountedDisplay',
                        'ReturnedARNMatches',
                        'TypeOK',
                        'VisibleContextMatches'},
 'SecretsVersionLifecycle': {'ActiveHasCurrent',
                             'AuthorizedDisclosure',
                             'AuthorizedVersions',
                             'ImmutableVersions',
                             'NoOrphans',
                             'SeparateCurrentPrevious',
                             'TypeOK',
                             'UniqueLabels'},
 'StackOwnership': {'TypeOK', 'OwnershipExclusive'},
 'StartupTokenVisibility': {'CreatedFilePrivate',
                            'FileIdentityFrozen',
                            'HistoryVisibility',
                            'InvalidFileNeverEmits',
                            'TypeOK'},
 'LandingAccessAdmission': {'ExternalBoundary',
                            'PublicResponse',
                            'TokenBoundary',
                            'TypeOK',
                            'UnknownNeverStatic'},
 'LandingTopologyState': {'CanonicalHTMLAvailable',
                          'DOMSelectionAgrees',
                          'InitialFragmentSelection',
                          'InvalidInputPreservesSelection',
                          'MountUsesLatestSelection',
                          'NodeClickSelectsExact',
                          'PaintUsesLatestSelection',
                          'ProviderSelectsDefault',
                          'RetiredCallbackNoEffect',
                          'RetiredCanvasHidden',
                          'TypeOK',
                          'UnsupportedCanvasHidden'},
 'LoginInputIntent': {'LocaleIntentFrame', 'NoEmptyPOST', 'IntentInvalidatesAsync', 'TypeOK', 'SubmissionSelectionExact', 'NoPOSTWhileBlocked', 'StaleFileCompletionFrame', 'AdoptedValueExact'}}
# Established controls require an explicit gate-policy change before retirement.
REQUIRED_MUTANTS = {'BedrockRuntimeAdmission': {'FallbackBackend', 'OmitMapping', 'ShutdownClaimsBackendFailure', 'OmitEnablement', 'AdmitAfterClose', 'PersistPrompt', 'MisreportMissingModel', 'OmitAuthorization', 'SyntheticDisabledSuccess', 'OmitValidation', 'CallUnsupportedOperation', 'LeakSlot', 'MapBeforeAuthorization', 'IgnoreConcurrency'}, 'ELBTargetLifecycle': {'OmitInUseGuard', 'OmitListenerCascade', 'OmitTargetCascade', 'OmitEpochGuard', 'OmitUnuseReset', 'CheckUnusedGroup', 'OmitAuthorization', 'ChangeOtherTeam'}, 'AppEngineTraffic': {'PartialSplit', 'DeleteDefaultService', 'DeployWithoutApplication', 'OmitOwnerCheck', 'KeepSplitOfDeletedService', 'DeleteVersionWithTraffic', 'OmitDefaultFirst', 'DeleteApplication', 'SplitNamesMissingVersion', 'ChangeOtherTeam', 'ServeUnroutedVersion', 'ServePastFirewall', 'FirstVersionWithoutTraffic'}, 'Route53AtomicBatch': {'ChangeSentinel', 'ExposePendingState', 'HalfBatch', 'OmitAuthorization', 'ChangeOtherTeam', 'OmitValidation'}, 'S3MultipartState': {'OmitPartBudget', 'OmitUploadParent', 'RetainCompletedParts', 'PartialFailure', 'OmitUploadBinding'}, 'CatalogConsoleEpoch': {'OmitReplyEpochGuard', 'DropFailureError', 'ReloadAfterFailedWrite', 'AcceptAfterUnmount', 'AllowConcurrentWrites'},
 'CatalogResourceLifecycle': {'AllowDuplicateNames', 'ReplayCommits', 'IgnoreGeneration', 'TokenToFutureID', 'UpdateKeepsGeneration', 'ReplayCreatesRow', 'WritePeerScope', 'ReuseDeletedID', 'IgnoreQuota', 'DeleteParentWithChildren', 'FaultCommits', 'DeniedCommits'},
 'AzureBlobState': {'ContainerDeleteErasesPeer',
                    'ContainerDeleteLeavesBlobs',
                    'FailedPutCommits',
                    'IgnoreAccountOwner',
                    'IgnoreCreateOnly',
                    'IgnoreStaleETag',
                    'MissingTrustedActor',
                    'OmitByteBudget',
                    'OmitRowBudget',
                    'ReadNeighbor',
                    'ReuseETag'},
 'AzurePropertiesEpoch': {'AcceptFailedHEAD',
                          'BypassCurrentPublication',
                          'CloseKeepsPrevious',
                          'OpenKeepsPrevious',
                          'StaleFinallyClearsBusy'},
 'AzurePropertiesTarget': {'BlobOmitsFields',
                           'ContainerGetsBlobFields',
                           'DropBlobParent',
                           'RequestGET',
                           'WrongKindRoute',
                           'WrongRenderedName'},
 'BlobRange': {'BypassAuthorization',
               'DiscloseFullMD5OnPartial',
               'IgnoreConditionDecision',
               'IgnoreEndClamp',
               'PermitExcessChecksum',
               'ReadMutatesStoredBody',
               'UncheckedBounds',
               'WrongSliceStart'},
 'BrowserSessions': {'IgnoreParentValidity',
                     'LogoutClearsOthers',
                     'LogoutKeepsSession',
                     'LogoutRevokesSDK',
                     'RawOperatorCookie'},
 'CFNConditionAdmission': {'CycleAccepted',
                           'DefaultOverrideBypass',
                           'DisabledDeclarationBypass',
                           'DisabledOutputIncluded',
                           'DisabledResourceIncluded',
                           'EarlyResultPublished',
                           'FailedResultPublished',
                           'InvalidSuppliedAccepted',
                           'ListNoValueRetained',
                           'MapNoValueRetained',
                           'SelectedLinkBypass',
                           'UnknownParameterAccepted',
                           'UnusedElseSyntaxBypass',
                           'WrongSelectedArm'},
 'CFNConditionMemoDepth': {'CacheAliasHeightAsLeaf',
                           'CachedDepthOffByOne',
                           'CachedReturnIgnoresStructuralHeight',
                           'InvertMemoizedValue',
                           'PublishPartialValuesOnFailure',
                           'SkipUnusedDefinitions',
                           'TreatMalformedAsFalse',
                           'TreatUnknownAsFalse',
                           'TreatVisitingAsCompleted'},
 'CFNFinalCleanupACK': {'CommitPartialAfterRefusal',
                        'DeletePeerResource',
                        'IgnoreFinalCleanupACK',
                        'PublishBeforeCleanup'},
 'CFNPhysicalResourceACK': {'CommitFailedPostcondition',
                            'CommitSQLExecFailure',
                            'DeletePeerPhysicalResource',
                            'OmitFinalPhysicalCheck',
                            'PublishBeforeFinalRead',
                            'ReadBeforeLateCleanup',
                            'SkipSecondResource'},
 'CloudWatchLogsLifecycle': {'AcceptInvalidBatch',
                             'OmitDeleteStreamEventCascade',
                             'OmitDeleteEventCascade',
                             'OmitPutAuthority',
                             'OmitTokenGeneration',
                             'OmitTokenTeam'},
 'CloudWatchLogsViewEpoch': {'OmitResponseEpoch', 'AcceptUnmountedReply'},
 'DynamoDBAdmission': {'AcknowledgeIgnoredChildDelete',
                       'AcknowledgeIgnoredCreate',
                       'AcknowledgeIgnoredItemDelete',
                       'AcknowledgeIgnoredTableDelete',
                       'AcknowledgeIgnoredWrite',
                       'AdmitInvalidKey',
                       'BypassStartupValidation',
                       'ChargeWireEncoding',
                       'DirtyOversizeRefusal',
                       'EraseKeyType',
                       'IgnoreAllow',
                       'IgnoreDeny',
                       'IgnoreItemByteCap',
                       'IgnoreItemCountCap',
                       'IgnoreTableCountCap',
                       'OmitNestedOverhead',
                       'RestartLosesStatistics',
                       'SplitNumericAlias',
                       'WriteBeforeInitialization',
                       'WritePeerScope',
                       'WriteWithoutTable',
                       'WrongTableStatistics'},
 'DynamoDBConsoleEpoch': {'ACKKeepsDraft',
                          'ACKKeepsForm',
                          'CancelKeepsLoading',
                          'IgnorePostReadGeneration',
                          'IgnoreReadViewGuard',
                          'IgnoreWriteViewGuard',
                          'KeepDisplayedItemOnKeyEdit',
                          'LocaleClearsDirtyDraft',
                          'RemoveSynchronousOwnerLock',
                          'RetryRepeatsWrite'},
 'DynamoDBLifecycle': {'AcceptRecreatedGeneration',
                       'CachedCondition',
                       'DirtyRollback',
                       'ForeignPut',
                       'IgnoreAllow',
                       'IgnoreDeny',
                       'MissingDropChildCleanup',
                       'MissingPutGenerationGuard'},
 'GCPStorageState': {'DeleteNonemptyBucket',
                     'DeniedBucketCreate',
                     'IgnoreActualOwner',
                     'IgnoreGenerationMatch',
                     'IgnoreStateBudget',
                     'ReadWrongEnvironment',
                     'ResetSequenceAfterDelete',
                     'ReuseGeneration'},
 'IAMManagedPolicies': {'AttachWithoutViewRefresh',
                        'CommitFailedCandidate',
                        'DeleteDefaultVersion',
                        'DetachWithoutViewRefresh',
                        'DropManagedDeny',
                        'IgnoreAggregateLimit',
                        'IgnorePolicyDeleteDependencies',
                        'IgnoreRoleDeleteAttachments',
                        'PartialDefaultRoleRefresh',
                        'ReuseDeletedVersionID',
                        'SkipVersionCap'},
 'KMSAliasCatalog': {'AliasAsTarget',
                     'BypassMACIntegrity',
                     'CrossRegionMutation',
                     'DirtyFailedCommit',
                     'FilterAfterLimit',
                     'IgnoreCursorAuthority',
                     'IgnoreCursorFilter',
                     'IgnoreCursorLimit',
                     'IgnoreCursorRevision',
                     'IgnoreGlobalCap',
                     'IgnorePerKeyCap',
                     'IgnoreWriteAcknowledgement',
                     'InclusiveResume',
                     'MissingContinuation',
                     'OmitAliasCreatePermission',
                     'OmitDeleteTargetPermission',
                     'OmitListPermission',
                     'OmitNewKeyCreatePermission',
                     'OmitNewKeyUpdatePermission',
                     'OmitOldKeyUpdatePermission',
                     'OmitRevisionAdvance',
                     'PublishPartialReadFailure',
                     'ReplaceCreationOnUpdate',
                     'StaleContinuationPosition'},
 'KMSAliasCrypto': {'AllowMidTransactionRetarget',
                    'AuthorizeAliasResource',
                    'BypassConcreteKeyDenial',
                    'ChargeNeighborCounter',
                    'DecryptChangesCounter',
                    'IgnoreCiphertextMismatch',
                    'IgnoreCounterAcknowledgement',
                    'MismatchBeforeAuthorization',
                    'OmittedDecryptUsesLiveAlias',
                    'PersistOnFailedCommit',
                    'PublishBeforeCommit',
                    'PublishOnFailedCommit',
                    'UseStaleAliasTarget'},
 'KMSAliasesViewEpoch': {'BypassRetiredListLife',
                         'DeleteWithoutExactConfirmation',
                         'IgnoreSameLifeListRevision',
                         'IgnoreSelectedTargetRevision',
                         'KeepDraftAfterAcknowledgedWrite',
                         'KeepFormAfterAcknowledgedWrite',
                         'LocaleClearsUnsavedDraft',
                         'ReplayWriteOnReadRetry',
                         'RetainRowsOnCurrentReadFailure',
                         'RetainTargetMetadataOnCancel',
                         'SubmitWithoutVerifiedTarget'},
 'KMSBootstrapTransaction': {'BypassActionPermission',
                             'BypassAliasPermission',
                             'BypassInvalidAdmission',
                             'BypassKeyPermission',
                             'BypassMissingPolicy',
                             'CommitAfterFailure',
                             'CommitWrongRegion',
                             'DiscardPeerCursor',
                             'OperationNoEffect',
                             'PersistCursorAtBootstrap',
                             'PersistDeniedBootstrap',
                             'PersistSchemaAtBootstrap',
                             'PublishBeforeCommit'},
 'KMSKeyState': {'MissingContextGuard',
                 'MissingEnabledDecryptGuard',
                 'MissingEnabledEncryptGuard',
                 'MissingEncryptionCeiling',
                 'MissingKeyBinding',
                 'MissingPermissionGuard',
                 'MissingRegionGuard',
                 'MissingTeamGuard'},
 'KMSProtocolAdmission': {'DropSigningIntent',
                          'IgnoreRequiredTarget',
                          'MaskIdentityOutage',
                          'ReadLivePolicyInsteadOfSnapshot'},
 'NativeIAMOwnership': {'LegacyAdoptsNativeRole'},
 'NativeWireProjection': {'AcceptBadValue',
                          'CollapseNullToZero',
                          'EncodeOmittedBad',
                          'IgnoreOmission',
                          'PublishErrorPrefix',
                          'ReverseFieldOrder'},
 'ObjectStore': {'MissingDeletionGuard'},
 'ProjectAuthority': {'AdoptUnboundResource',
                      'AllowOwnerRebinding',
                      'DefaultAllowUnknownPermission',
                      'PartialCommitOnRefusal',
                      'TrustClaimedProject',
                      'WriteDespiteDenial'},
 'ProviderActor': {'OperatorSelectionIgnored',
                   'ParticipantCapabilitiesSelection',
                   'ParticipantSelectionBypass',
                   'SelectedCredentialConfusion',
                   'UnauthenticatedAttachment',
                   'WireRoleSpoof'},
 'ProviderPath': {'CleanTail',
                  'DecodeAgain',
                  'DropActor',
                  'DropRawTail',
                  'EncodedProviderAllowed',
                  'MutateOriginalPath',
                  'UnknownFallsBack'},
 'RESTDeploymentSnapshot': {'MutableSnapshot'},
 'RESTProxyEventBoundary': {'AcceptMalformedQuery',
                            'DropBearerApplication',
                            'EmitRejectedEvent',
                            'FirstHeaderValueWins',
                            'FirstQueryValueWins',
                            'ForwardAWSSignature',
                            'ForwardLocalCredentials',
                            'IgnoreEncodedBudget',
                            'ReplaceBody',
                            'TemplateAsActualPath'},
 'RESTRouteMatching': {'AllowEmptyGreedy',
                       'BadRejectedScore',
                       'FirstDuplicateCaptureWins',
                       'IgnoreLiteral',
                       'LoseSpecificity',
                       'TruncateGreedy'},
 'S3Versions': {'CurrentOnlyDeletion'},
 'SQSConsoleEpoch': {'OmitReplyEpochGuard',
                     'OmitUnmountGuardAndEpochAdvance',
                     'RenderRawReceiptInRows',
                     'RetainReceiptsAcrossContextSwitch'},
 'SQSLifecycle': {'AcceptStaleReceipt',
                  'ChangeWithoutInflight',
                  'CrossScopeQueueDelete',
                  'DeleteWithoutCascade',
                  'FailedWriteChangesState',
                  'IdempotentCreateChangesState',
                  'ReceiptNotRotated',
                  'ReceiveAfterRetention',
                  'ReceiveBeforeDelay',
                  'SendWithoutParentCheck',
                  'UnauthorizedSend'},
 'SQSStorageBudget': {'BlockLegacyMaintenance',
                      'CommitPurgeOnFailedSend',
                      'DiscardOwnedLiveRows',
                      'IgnoreExpiredForeignCharge',
                      'OmitPayloadByteCap',
                      'OmitPhysicalRowCap',
                      'PurgeExpiredForeignRows'},
 'SSMParameterState': {'DoNotAdvanceVersion',
                       'IgnoreExplicitDeny',
                       'IgnoreOverwriteGuard',
                       'ReadWrongName',
                       'ReadWrongRegion'},
 'SakuraDiskAdmission': {'AcceptWrongMarker',
                         'CredentialInsteadOfExercise',
                         'DefaultOwnerMissing',
                         'DeniedMetadataDisclosure',
                         'DummyMarkerAsAuthority',
                         'EarlyLookupFault',
                         'FaultCommitsWrite',
                         'IgnoreFixedRealm',
                         'OperatorForcesOwner',
                         'OperatorRoleBypass',
                         'RealmFromHeader',
                         'UnsupportedReachesStore'},
 'SakuraDiskMetadata': {'AdoptReopenedOwner',
                        'CommitFailedCandidate',
                        'DeleteWrongID',
                        'IgnoreQuotaBoundary',
                        'MissingCreatedRow',
                        'ModifyPeerRows',
                        'ReturnPeerList',
                        'ReuseDeletedID',
                        'UpdateAdvancesAllocator',
                        'UpdateNeighbor'},
 'SakuraDisksViewEpoch': {'ACKStealsNavigation',
                          'CancelRetainsDraft',
                          'DropPublicationContextGuard',
                          'DropPublicationSequenceGuard',
                          'DropReadContextGuard',
                          'DropReadSequenceGuard',
                          'DropSynchronousTicket',
                          'DropWriteContextGuard',
                          'FailedReadPublishesMetadata',
                          'KeepDisplayOnNavigation',
                          'KeepDisplayOnScopeChange',
                          'KeepDraftOnACK',
                          'KeepFormOnACK',
                          'LocaleClearsDraft',
                          'ReadRetryResendsWrite',
                          'StaleFinallyClearsLoading',
                          'WrongDisplayedDisk'},
 'SakuraStartupAdmission': {'ConstructBeforeDatabase',
                            'CreateTokenBeforeCheck',
                            'CreateTokenWhenUnsafe',
                            'DefaultMissingScope',
                            'IgnoreInvalidScopeValue',
                            'IgnoreOrphanScope',
                            'OpenDatabaseBeforeCheck',
                            'OpenListenerBeforeCheck',
                            'OptInWithNonLoopback',
                            'OptInWithNonNumeric',
                            'OptInWithRemote',
                            'OptInWithoutScope',
                            'PermitUnsafeEnabled',
                            'RegisterDisabled',
                            'RejectValidEnabled',
                            'RejectValidOptIn',
                            'SkipEnabledRegistration',
                            'SkipEnabledSchema'},
 'SecretMetadataViewEpoch': {'AppendPreviousPage',
                             'ChildRequestsValue',
                             'ErrorFabricatesRows',
                             'LateCallbackAccepted',
                             'LocaleResetsQuery',
                             'NewReadKeepsOldRows',
                             'NextFilterContext',
                             'OldFinallyClearsNewLoading',
                             'ParentKeepsPriorReveal',
                             'RenderOldMetadata',
                             'RenderOldNativeError',
                             'ScopeCommitKeepsLoading',
                             'SkipPayloadValidation',
                             'UnmountWithoutCleanup'},
 'SecretVersionListing': {'AuthorityKeyBypass',
                          'AuthorizationBypass',
                          'DomainBypass',
                          'ExpiryBypass',
                          'FilterAfterLimit',
                          'FilterBindingBypass',
                          'GenerationBindingBypass',
                          'InclusiveResume',
                          'MissingNextToken',
                          'ReadWritesVersionState',
                          'RegionBindingBypass',
                          'SignatureBypass',
                          'StaleNextPosition'},
 'SecretsDisclosureChannels': {'DirectDeniedGetWriter',
                               'PrivateSinkGetsRaw',
                               'RawAccessorDenied',
                               'RawAccessorForMetadata',
                               'RetainRawAcrossContext'},
 'SecretsMutationRefresh': {'OmitAckFormExit',
                            'OmitFreshIntentToken',
                            'ReplayWriteOnReadRetry',
                            'RetainDraftOnAck'},
 'SecretsRevealEpoch': {'RetainVisibleValueOnUnmount', 'OmitReplyEpoch', 'OmitResponseArnBinding'},
 'SecretsVersionLifecycle': {'OmitCreatePermission',
                             'OmitReadGenerationBinding',
                             'OmitReadTeamBinding',
                             'OverwriteConflictingVersion',
                             'RetainDeletedVersions',
                             'RetainPreviousLabelOnPromote'},
 'StackOwnership': {'ReclaimDriftedResource'},
 'StartupTokenVisibility': {'OmittedCIGuard', 'OverwriteExistingToken', 'OmittedTTYGuard'},
 'LandingAccessAdmission': {'IgnoreHostBoundary',
                            'IgnoreOriginBoundary',
                            'MakeEveryPathPublic',
                            'RequireTokenForProductInformation',
                            'ServeNonReadMethod',
                            'ServeUnknownResource'},
 'LandingTopologyState': {'AcceptInvalidSelection',
                          'ClickSelectsDifferentNode',
                          'DrawInitialSelection',
                          'HideCanonicalHTMLOnRetire',
                          'LatePaintMutatesSurface',
                          'LeaveRetiredCanvasVisible',
                          'MountCapturesInitialSelection',
                          'ResetInitialFragmentSelectionOnMount',
                          'RevealUnsupportedCanvas',
                          'WrongDetailProjection',
                          'WrongProviderDefault'},
 'LoginInputIntent': {'FileCompletionIgnoresRevision', 'NonemptyDOMUsesControlledState', 'UnmountedFileCompletionPublishes', 'LocaleClearsIntent', 'LocaleInvalidatesCurrentFile', 'SubmitIgnoresBusy', 'CurrentFileErrorDiscarded', 'FlushDoesNotAdoptNativeValue', 'NativeAdoptionSkipsNewIntent', 'EmptyDOMReusesOldCredential', 'OldPasteSurvivesNewIntent', 'SubmitIgnoresFileGate', 'FocusSkipsNativeAdoption', 'KeyDownSkipsNativeAdoption'}}
REQUIRED_SOURCE_SCOPES = {'BedrockRuntimeAdmission': {'native/apple/main.swift', 'cmd/simulator/bedrock_flags.go', 'cmd/simulator/main.go', 'internal/aws/bedrock_runtime_messages.go', 'internal/inference/apple.go', 'internal/aws/ssm.go', 'internal/aws/bedrock_runtime.go', 'internal/aws/s3.go', 'internal/inference/ollama.go', 'internal/server/server.go', 'internal/server/bedrock_runtime.go', 'internal/server/sakura_provider.go', 'internal/inference/inference.go'}, 'ELBTargetLifecycle': {'internal/aws/elb_store.go', 'internal/aws/elb_dispatch.go', 'internal/aws/elb_query.go', 'internal/aws/elb_target.go', 'internal/aws/apirouting/alb_headers.go', 'internal/aws/lambda_permissions.go', 'internal/aws/s3.go', 'internal/aws/ssm.go', 'internal/server/server.go'}, 'AppEngineTraffic': {'internal/gcp/appengine/deploy.go', 'internal/gcp/appengine/wsgi.py', 'internal/server/cloud_storage.go', 'tools/gcpmodels/appengine.py', 'internal/gcp/appengine/store.go', 'internal/gcp/appengine/apps.go', 'internal/gcp/storage/source.go', 'internal/gcp/appengine/services.go', 'internal/gcp/appengine/model.go', 'internal/gcp/appengine/openapi.go', 'internal/server/server.go', 'internal/gcp/appengine/engine.go', 'internal/gcp/appengine/firewall.go', 'internal/gcp/appengine/serve.go', 'internal/gcp/appengine/model.json'}, 'Route53AtomicBatch': {'internal/aws/s3.go', 'internal/aws/route53_http.go', 'internal/server/server.go', 'internal/aws/ssm.go', 'internal/aws/route53_store.go', 'internal/aws/route53_dispatch.go'}, 'CFNConditionAdmission': {'internal/aws/cloudformation.go',
                           'internal/aws/cloudformation_conditions.go',
                           'internal/aws/cloudformation_conditions_link.go',
                           'internal/aws/cloudformation_intrinsics.go'},
 'CFNConditionMemoDepth': {'internal/aws/cloudformation_conditions.go'},
 'CFNFinalCleanupACK': {'internal/aws/cloudformation.go',
                        'internal/aws/cloudformation_cleanup_ack_test.go'},
 'CFNPhysicalResourceACK': {'internal/aws/cloudformation.go',
                            'internal/aws/cloudformation_physical_delete_ack_test.go'},
 'DynamoDBConsoleEpoch': {'internal/server/web/app.js',
                          'tools/console/src/dynamodb.jsx',
                          'tools/console/src/main.jsx'},
 'KMSAliasCatalog': {'internal/aws/kms_aliases.go'},
 'KMSBootstrapTransaction': {'internal/aws/kms_aliases.go', 'internal/aws/kms_http.go'},
 'NativeWireProjection': {'internal/aws/wire_json_projection.go'},
 'RESTProxyEventBoundary': {'internal/aws/apirouting/event.go'},
 'RESTRouteMatching': {'internal/aws/apirouting/path.go'},
 'SakuraDiskAdmission': {'internal/sakura/provider.go', 'internal/sakura/iaas/disk_http.go'},
 'SakuraDiskMetadata': {'internal/sakura/iaas/disk_store.go'},
 'SakuraDisksViewEpoch': {'internal/server/web/app.js',
                          'tools/console/src/main.jsx',
                          'tools/console/src/sakura-disks.jsx'},
 'SakuraStartupAdmission': {'cmd/simulator/main.go',
                            'cmd/simulator/sakura_flags.go',
                            'internal/server/sakura_provider.go',
                            'internal/server/server.go'},
 'SecretMetadataViewEpoch': {'internal/server/web/app.js',
                             'tools/console/src/secret_versions.jsx',
                             'tools/console/src/secrets-manager.jsx'},
 'SecretVersionListing': {'internal/aws/secret_http.go',
                          'internal/aws/secrets_manager_versions.go',
                          'internal/aws/secrets_manager_versions_http_test.go',
                          'internal/aws/secrets_manager_versions_test.go'},
 'LandingAccessAdmission': {'internal/server/access.go',
                            'internal/server/console_assets.go',
                            'internal/server/landing.go',
                            'internal/server/server.go'},
 'LandingTopologyState': {'internal/server/landing/landing.mjs',
                          'internal/server/landing/topology-canvas.mjs',
                          'internal/server/landing/topology-data.mjs'},
 'LoginInputIntent': {'tools/console/src/login.jsx', 'test/e2e/login_paste.py', 'test/console/login-native-input.test.mjs', 'internal/server/web/app.js', 'test/e2e/login_token_file.py'}}
# SNS obligations are fixed policy, not inferred from a mutable registry.
BASE_MODELS.update({'SNSLifecycle', 'SNSAdmission'})
BASE_INVARIANTS.update({'SNSAdmission': {'NoFIFOPublish',
                  'NoRegionMismatch',
                  'NoRejectedMutation',
                  'NoSuccessOnRefusal',
                  'NoUnauthorizedEffect',
                  'TypeOK'},
 'SNSLifecycle': {'DeleteRetryAcknowledged', 'ByteBudgetBounded',
                  'DeleteAckEmpty',
                  'DeliveryBudgetBounded',
                  'DurableAck',
                  'MessageCountBounded',
                  'NoDanglingDeliveredMessage',
                  'NoOrphans',
                  'PeerFrame',
                  'PublishRecipientsExact',
                  'TypeOK'}})
REQUIRED_MUTANTS.update({'SNSAdmission': {'AcceptUnknownOperation',
                  'BypassAuthorization',
                  'IgnoreAttributeAdmission',
                  'IgnoreDuplicateFields',
                  'IgnoreInitialization',
                  'IgnoreRegionBinding',
                  'IgnoreSigningScope',
                  'IgnoreValueAdmission',
                  'MutateAfterRejection',
                  'PermitFIFOPublication'},
 'SNSLifecycle': {'RejectAbsentDelete', 'AcknowledgeWithoutCleanup',
                  'DeletePeerSubscriptions',
                  'IgnoreByteBudget',
                  'IgnoreDeliveryBudget',
                  'IgnoreMessageCount',
                  'IgnoreWriteAcknowledgement',
                  'KeepDeletedDeliveries',
                  'KeepDeletedMessages',
                  'MissingTopicGuard',
                  'OmitDeliveryRows'}})
REQUIRED_SOURCE_SCOPES.update({'SNSAdmission': {'internal/aws/s3.go',
                  'internal/aws/sns.go',
                  'internal/aws/sns_admission_regression_test.go',
                  'internal/aws/ssm.go',
                  'internal/server/server.go'},
 'SNSLifecycle': {'internal/aws/sns_storage_regression_test.go',
                  'internal/aws/sns_store.go',
                  'internal/aws/sns_test.go',
                  'internal/server/server.go',
                  'internal/server/sns_test.go'}})

# STS guard lifecycle requirements. Existing service requirements stay intact.
BASE_MODELS.add('STSSessionLifecycle')
BASE_INVARIANTS['STSSessionLifecycle'] = {'UseRequiresAllGuards', 'IssueRequiresAllGuards', 'TypeOK'}
REQUIRED_MUTANTS['STSSessionLifecycle'] = {'SkipChain',
 'SkipExpiry',
 'SkipExternal',
 'SkipGeneration',
 'SkipPermission',
 'SkipSignature',
 'SkipTenant',
 'SkipTrust'}
REQUIRED_SOURCE_SCOPES['STSSessionLifecycle'] = {'internal/aws/cloudformation.go',
 'internal/aws/iam_managed_query.go',
 'internal/aws/iam_query.go',
 'internal/aws/iam_roles.go',
 'internal/aws/ssm.go',
 'internal/aws/sts.go',
 'internal/aws/sts_credentials.go',
 'internal/aws/sts_roles.go',
 'internal/aws/sts_signature.go',
 'internal/server/access.go',
 'internal/server/access_sdk.go'}


# Synchronous local CloudFormation event/history requirements.
BASE_MODELS.add('CFNEventHistory')
BASE_INVARIANTS['CFNEventHistory'] = {'CleanupAfterAllWrites',
 'CursorScoped',
 'EventParentExists',
 'FailureAtomic',
 'HistoryTracksLatest',
 'LatestDeletedRetained',
 'LivePeerPreserved',
 'NextCursorExact',
 'NoUnauthorizedDisclosure',
 'PageExact',
 'PendingFrameUnchanged',
 'ReadSnapshotUnchanged',
 'ResolvedGenerationExact',
 'RetentionBound',
 'SuccessfulLifecycleExact',
 'TypeOK'}
REQUIRED_MUTANTS['CFNEventHistory'] = {'AcknowledgeBeforeFinalWrites',
 'IgnoreCursorBound',
 'LeaveOrphanEvents',
 'PruneLiveHistory',
 'PublishFailedTransaction',
 'PublishPending',
 'ResolveDeletedByName',
 'ReturnFirstRowCursor',
 'ReversePageOrder',
 'SkipAuthorization',
 'SkipCursorGeneration',
 'SkipDeletedCountACK',
 'SkipEventWriteACK',
 'SkipHistoryWriteACK',
 'SkipRegion',
 'SkipSelectedHistoryACK'}
REQUIRED_SOURCE_SCOPES['CFNEventHistory'] = {'internal/aws/cloudformation.go',
 'internal/aws/cloudformation_events.go',
 'internal/aws/cloudformation_events_cleanup_order_test.go',
 'internal/aws/cloudformation_events_test.go'}


# WAF ordered evaluation and association authorization are mandatory.
BASE_MODELS.add('WAFRequestEvaluation')
BASE_INVARIANTS['WAFRequestEvaluation'] = {'CountContinues',
 'FirstTerminatingRuleWins',
 'NoAssociationOrphans',
 'TypeOK',
 'UnassociatedAllows'}
REQUIRED_MUTANTS['WAFRequestEvaluation'] = {'CountTerminates',
 'DeleteAssociatedACL',
 'DeleteAssociatedStage',
 'EarlyDefault',
 'ReversedOrder'}
REQUIRED_SOURCE_SCOPES['WAFRequestEvaluation'] = {'formal/WAFRequestEvaluation.md',
 'go.mod',
 'go.sum',
 'internal/aws/apigateway_deployments.go',
 'internal/aws/apigateway_invoke.go',
 'internal/aws/apigateway_store.go',
 'internal/aws/s3.go',
 'internal/aws/ssm.go',
 'internal/aws/wafv2_dispatch.go',
 'internal/aws/wafv2_http.go',
 'internal/aws/wafv2_match.go',
 'internal/aws/wafv2_store.go',
 'internal/aws/wafv2_types.go',
 'internal/server/server.go'}
BASE_MODELS.add('WAFAssociationAuthorization')
BASE_INVARIANTS['WAFAssociationAuthorization'] = {'CheckedAssociationStable',
 'DeniedLeavesState',
 'NoUnauthorizedDelete',
 'NoUnauthorizedRead',
 'StagePermissionRequired',
 'TypeOK'}
REQUIRED_MUTANTS['WAFAssociationAuthorization'] = {'AllowPrivateACL',
 'AllowPrivateDelete',
 'ReleaseBeforeUse',
 'SkipStagePermission',
 'WriterIgnoresTransaction'}
REQUIRED_SOURCE_SCOPES['WAFAssociationAuthorization'] = {'internal/aws/wafv2_http.go', 'internal/aws/wafv2_store.go'}

# Bounded local CloudFormation SSM snapshot requirements.
BASE_MODELS.add('CFNParameterSnapshot')
BASE_INVARIANTS['CFNParameterSnapshot'] = {'AdmissionExact',
 'AuthorizeEveryKeyBeforeRead',
 'CompletePublication',
 'ConditionsUseResolvedValues',
 'KeysPreserved',
 'NoEarlyWrites',
 'NoFailedWrites',
 'OneAtomicSnapshot',
 'RefsUseResolvedValues',
 'ReplayIdentityExact',
 'ReplayNoSourceRead',
 'ReplayPreservesStack',
 'SnapshotFrozen',
 'TypeOK',
 'ValidInputsPublish',
 'ValidateBeforeRead'}
REQUIRED_MUTANTS['CFNParameterSnapshot'] = {'AcceptMismatchedToken',
 'AcceptNonStringSource',
 'AuthorizeOnlyFirstKey',
 'ConditionsUseKeys',
 'LeakWritesOnReadFailure',
 'OmitEvents',
 'RefreshAtCommit',
 'RefsUseKeys',
 'ReplayReadsAgain',
 'SkipInputValidation',
 'SkipPlanValidation',
 'SkipReplayAuthorization',
 'StoreValuesAsKeys',
 'TearSnapshot',
 'ValidateResolvedValueAsKey'}
REQUIRED_SOURCE_SCOPES['CFNParameterSnapshot'] = {'internal/aws/cloudformation.go',
 'internal/aws/cloudformation_conditions.go',
 'internal/aws/cloudformation_ssm_parameters.go'}

BASE_MODELS.add('ConsoleHandoff')
BASE_INVARIANTS['ConsoleHandoff'] = {'LogoutRejectsReplay', 'TypeOK', 'RedeemRequiresFreshParent', 'OperatorRefused', 'ExactDestination', 'ParentLifetimeBound', 'AttemptConsumesCode', 'UseRequiresFreshAuthority', 'SDKAuthorityPreserved', 'UnrelatedSessionsPreserved', 'OneTimeRedemption', 'TerminalRequiresRelay'}
REQUIRED_MUTANTS['ConsoleHandoff'] = {'SkipParameter', 'RevokeSDK', 'SkipRedeemExpiry', 'SkipUseGeneration', 'CachePolicy', 'ExtendHandoff', 'SkipRedeemGeneration', 'SkipCookie', 'SkipRegion', 'SkipBrowserExpiry', 'ReuseCode', 'SkipAction', 'SkipRedeemParent', 'RevokePeer', 'KeepLoggedOutCookie', 'ExtendBrowser', 'IgnoreConsumedCode', 'SkipUseParent', 'GrantBrowserSDK', 'AllowOperator', 'CrossKindAction', 'SkipRelay'}
REQUIRED_SOURCE_SCOPES['ConsoleHandoff'] = {'internal/aws/sts_terminal_binding.go', 'internal/aws/ec2_read.go', 'internal/aws/ssm_sessions.go', 'internal/server/terminal_channel.go', 'internal/server/terminal_handoff.go', 'internal/server/console_instance_handoff_test.go', 'internal/server/console_handoff.go', 'internal/aws/sts_credentials_test.go', 'internal/server/console_handoff_test.go', 'internal/server/access.go', 'internal/aws/sts_roles.go', 'internal/aws/sts_credentials.go', 'internal/server/server.go', 'internal/aws/ssm.go'}


# Bounded opt-in Lambda response and UI request-lifetime disclosure contract.
BASE_MODELS.add('LambdaLogTailDisclosure')
BASE_INVARIANTS['LambdaLogTailDisclosure'] = {'TypeOK', 'DisplayCurrent', 'DisplayHasResponse', 'TailBound', 'ResponseAuthorized', 'ResponseSync', 'ResponseOptIn'}
REQUIRED_MUTANTS['LambdaLogTailDisclosure'] = {'AcceptOldEpoch', 'SkipOptIn', 'KeepOnEpoch', 'KeepOnUnmount', 'AcceptUnmounted', 'KeepOnContext', 'SkipSync', 'SkipInvokePermission', 'SkipTailCap', 'SkipContextInvalidation', 'DisplayWithoutResponse'}
REQUIRED_SOURCE_SCOPES['LambdaLogTailDisclosure'] = {'internal/workload/python/runtime_log_tail_test.go', 'internal/aws/lambda_log_tail_test.go', 'tools/console/src/lambda-invoke.mjs', 'internal/server/server.go', 'test/e2e/lambda_log_tail_console.py', 'test/console/lambda-invoke.test.mjs', 'internal/server/lambda_test.go', 'internal/server/web/app.js', 'internal/aws/lambda.go', 'internal/workload/python/runtime.go', 'tools/console/src/lambda.jsx'}

IDENTIFIER = re.compile(r'^[A-Za-z][A-Za-z0-9_]*$')

# TLC1.7.4 ModelConfig.ALL_KEYWORDS. A directive cannot masquerade as a
# user invariant even when placed on the same physical CFG line.
CFG_KEYWORDS = {'CONSTANT', 'CONSTANTS', 'CONSTRAINT', 'CONSTRAINTS',
                'ACTION_CONSTRAINT', 'ACTION_CONSTRAINTS', 'INVARIANT', 'INVARIANTS',
                'INIT', 'NEXT', 'VIEW', 'SYMMETRY', 'SPECIFICATION', 'PROPERTY',
                'PROPERTIES', 'TYPE', 'TYPE_CONSTRAINT', 'CHECK_DEADLOCK'}


# Native instance profile membership, admission and retained-generation contract.
BASE_MODELS.add('IAMInstanceProfiles')
BASE_INVARIANTS['IAMInstanceProfiles'] = {'AttachmentAuthority', 'SingleRole', 'ProfileGenerationGuard', 'TypeOK', 'FailureFrame', 'PassRoleRequired', 'FreshProfileIdentity', 'PeerFrame', 'RemovalExact', 'RoleGenerationGuard', 'AuthorizationRequired'}
REQUIRED_MUTANTS['IAMInstanceProfiles'] = {'IgnorePassRole', 'RemovePeerMembership', 'DeletePopulatedProfile', 'BindWrongProfileGeneration', 'ReuseProfileIdentity', 'IgnoreProfileGeneration', 'DeleteAttachedRole', 'IgnoreRoleGeneration', 'AllowMultipleRoles', 'BindWrongRoleGeneration', 'IgnoreActionAuthorization', 'CommitStorageFailure'}
REQUIRED_SOURCE_SCOPES['IAMInstanceProfiles'] = {'internal/aws/iam_query.go', 'internal/aws/iam_roles.go', 'internal/server/server.go', 'internal/aws/iam_instance_profiles_query.go', 'internal/aws/iam_instance_profiles.go'}

# Bounded guest-command observation/ownership policy; no runtime refinement claim.
BASE_MODELS.add('GuestCommandLifecycle')
BASE_INVARIANTS['GuestCommandLifecycle'] = {'TerminalPublicationEvidence', 'TypeOK', 'AbandonDoesNotStop', 'RejectedCannotRun', 'StaleAbandonFrame', 'StartingAbortDefersCleanup', 'PollBound', 'UnsentCloses', 'CorrelationGuard', 'ExitEvidence', 'QuarantineSticky', 'NoClosedAdmission', 'TerminalObservationsSticky', 'CleanupOnlyPossible', 'StartEvidence', 'ExclusiveAdmission', 'CleanupRequired'}
REQUIRED_MUTANTS['GuestCommandLifecycle'] = {'PublishPendingAsExit', 'PretendAbandonStopsGuest', 'IgnorePollCorrelation', 'ResetClosedOnResolution', 'DiscardExitEvidence', 'IgnoreStartCorrelation', 'InventCleanupForRejected', 'CleanupBeforeClassification', 'IgnorePollBudget', 'ReuseUnsentTransport', 'RetargetStaleAbandon', 'IgnoreStartingAbort', 'OmitCleanup', 'RejectMayRun', 'AllowConcurrentStart', 'AcceptLatePoll', 'AdmitAfterClose'}
REQUIRED_SOURCE_SCOPES['GuestCommandLifecycle'] = {'internal/workload/qga/protocol.go', 'internal/workload/guestcommand/driver.go', 'internal/workload/qga/client.go'}


BASE_MODELS.add('CFNNativeIAMTransaction')
BASE_INVARIANTS['CFNNativeIAMTransaction'] = {'FailureAtomic', 'SingleRole', 'PendingFrame', 'PeerFrame', 'FinalCleanupACK', 'TypeOK', 'ZeroPolicyImplicitDeny', 'RequestedStateACK', 'AdmissionSound'}
REQUIRED_MUTANTS['CFNNativeIAMTransaction'] = {'BypassSupported', 'PublishPendingWrite', 'CommitPeerMutation', 'BypassRoleGeneration', 'BypassActionAllowed', 'SkipCapturedIdentity', 'BypassCanonicalOwner', 'SkipRequestedMetadata', 'SkipRequestedPolicies', 'SkipFinalPhysicalACK', 'TrustObservedFingerprint', 'BypassPassAllowed', 'BypassNoExternalDependency', 'CommitRefusal', 'SkipRequestedMembership', 'BypassProfileGeneration', 'BypassNative'}
REQUIRED_SOURCE_SCOPES['CFNNativeIAMTransaction'] = {'internal/aws/cloudformation_conditions_link.go', 'internal/aws/s3_cloudformation.go', 'internal/aws/iam_instance_profiles.go', 'internal/aws/iam_roles.go', 'internal/aws/cloudformation.go', 'internal/aws/iam_cloudformation.go'}

BASE_MODELS.add('IAMBuiltinPinTransaction')
BASE_INVARIANTS['IAMBuiltinPinTransaction'] = {'NormalRequestCompletes', 'AdmissionSound', 'RetiredRoleFinalACK', 'PinPreserved', 'CustomerFrame', 'PendingFrame', 'AggregateExact', 'RequestedStateACK', 'FailureAtomic', 'TypeOK', 'RequestedAttachmentACK'}
REQUIRED_MUTANTS['IAMBuiltinPinTransaction'] = {'AdoptObservedAttachmentAndAggregate', 'TrustObservedPin', 'SkipFinalRecords', 'BypassInitialPinValid', 'ChangeSameNameCustomer', 'PinBeforeEventsOnly', 'SkipPinValidation', 'BypassReadOnly', 'CommitRefusal', 'RejectNormalDetach', 'PublishPendingWrite', 'PinOnlyWhileRoleLive', 'SkipRequestedEdges', 'BypassFreshRole', 'SkipRoleFrame', 'SkipExactAggregate', 'SkipRetiredCFNRequestedPin', 'PinOnlyWhileAttached', 'BypassAuthorized', 'ForgetNativeDetachedTarget', 'TrustObservedEdges'}
REQUIRED_SOURCE_SCOPES['IAMBuiltinPinTransaction'] = {'internal/aws/iam_cloudformation.go', 'internal/aws/s3_cloudformation.go', 'internal/aws/cloudformation.go', 'internal/aws/iam_managed.go', 'internal/aws/iam_builtin.go', 'internal/aws/iam_managed_query.go', 'internal/aws/iam_roles.go'}


BASE_MODELS.add('DurableGuestLease')
BASE_INVARIANTS['DurableGuestLease'] = {'ObservationEvidence', 'AuthoritativeObservation', 'ReadFence', 'ExternalOutsideTransaction', 'BackendDuplicateSuppression', 'BackendImmutableMapping', 'CrashDurability', 'RetainedBudget', 'FailedACKRetainsPending', 'BackendMonotone', 'IntentBeforeDispatch', 'CASFence', 'PendingInvisible', 'NoReplacement', 'TombstoneRetained', 'NoRedispatch', 'ObservationEnvelopeExact', 'BackendPayloadIdentity', 'CleanupSurvivesProfile', 'TypeOK', 'NoFalseStopped', 'UnknownNeverRetires', 'RetainedLeaseUnique', 'AdmissionCurrentRows'}
REQUIRED_MUTANTS['DurableGuestLease'] = {'SkipReadFence', 'IgnorePrimaryQuota', 'SkipObservedAction', 'EchoRequestedEnvelopeState', 'SkipObservedLease', 'SkipReadLease', 'AcceptConflictingPayload', 'SkipObservedLeaseBinding', 'RequireRecreatedProfileForCleanup', 'AliasBackendMapping', 'OmitDurableIntentWrite', 'IgnoreRetainedQuota', 'SkipReadOp', 'DispatchUncommitted', 'RepeatBackendEffect', 'ReplaceLiveRecord', 'ReplaceRetiredRecord', 'ReplayAfterReopen', 'LoseDBOnReopen', 'SkipObservedOp', 'SkipObservedState', 'ResumeStaleBackendEffect', 'SkipNonemptyObservedLease', 'SkipObservedFence', 'StaleAdmissionSnapshot', 'AliasRetiredLease', 'SkipReadOwner', 'EchoRequestAsObservation', 'ObserveInsideTransaction', 'PublishRollback', 'IgnoreCAS', 'AcknowledgeFailedWrite'}
REQUIRED_SOURCE_SCOPES['DurableGuestLease'] = {'internal/aws/ec2_guest_lease.go', 'internal/workload/guestlease/lease.go'}


BASE_MODELS.add('SSMDurableCommands')
BASE_INVARIANTS['SSMDurableCommands'] = {'AllTargets', 'BackendDeduplication', 'BackendLeaseFence', 'CASFence', 'CompleteEvidence', 'CrashDurability', 'ExactObservation', 'ImmutableIdentity', 'IntentBeforeDispatch', 'NoRedispatch', 'RestartNoDispatch', 'ScopedPagination', 'SnapshotImmutable', 'TerminalImmutable', 'TypeOK'}
REQUIRED_MUTANTS['SSMDurableCommands'] = {'AnyTargetSuccess', 'BackendDuplicateEffect', 'BackendIgnoresLease', 'CrashLosesIntent', 'DispatchBeforeCommit', 'LivePagination', 'MutableAuthority', 'ObservationAuthority', 'ObservationLease', 'ObservationRequest', 'ObservationStarted', 'ObservationToken', 'PageExpiry', 'PageFilter', 'PageScope', 'PageSize', 'RepeatDispatch', 'RestartDispatch', 'StaleCAS', 'SuccessCleanup', 'SuccessExit', 'SuccessTruncated', 'TerminalRewrite'}
REQUIRED_SOURCE_SCOPES['SSMDurableCommands'] = {'internal/aws/ssm_commands.go', 'internal/aws/ssm_commands_list.go', 'internal/workload/guestcommand/durable.go'}

BASE_MODELS.add('SNSQueueFanout')
BASE_INVARIANTS['SNSQueueFanout'] = {'Authorized', 'TypeOK', 'ParentPresent', 'BytesBounded', 'DurableAck', 'AuditExact', 'RecipientsExact', 'DestinationsExist', 'CountBounded', 'RefusalUnchanged'}
REQUIRED_MUTANTS['SNSQueueFanout'] = {'SkipQueueParent', 'SkipPayloadBytes', 'DropAuditRow', 'SkipTopicParent', 'SkipQueueAuthorization', 'DropQueueDelivery', 'SkipMessageCount', 'PartialDeliveryOnFailure', 'IgnoreCommitFailure'}
REQUIRED_SOURCE_SCOPES['SNSQueueFanout'] = {'internal/server/server.go', 'internal/aws/sqs_store.go', 'internal/server/sns_test.go', 'internal/aws/sns_store.go', 'internal/aws/sns_sqs_fanout_test.go', 'internal/aws/sns.go', 'internal/aws/sns_sqs_fanout.go'}

BASE_MODELS.add('SSMDocumentState')
BASE_INVARIANTS['SSMDocumentState'] = {'AddressIsolation', 'DeleteCleanup', 'CreateOnlyAbsent', 'HistoryImmutable', 'LatestPrecondition', 'TypeOK', 'DefaultPreserved', 'DeniedPreserves', 'RefusalPreserves'}
REQUIRED_MUTANTS['SSMDocumentState'] = {'SkipLatestVersion', 'IgnoreExplicitDeny', 'SkipGlobalCount', 'KeepDeletedVersions', 'ReplaceHistory'}
REQUIRED_SOURCE_SCOPES['SSMDocumentState'] = {'internal/server/server.go', 'internal/aws/s3.go', 'internal/aws/ssm.go', 'internal/aws/ssm_documents_validation.go', 'internal/aws/ssm_documents.go', 'internal/aws/ssm_documents_api.go'}

# Normalized read-only SSM document version pages.
BASE_MODELS.add('SSMDocumentVersionPage')
BASE_INVARIANTS['SSMDocumentVersionPage'] = set(['TypeOK', 'DeniedHasNoHistory', 'MissingHasNoHistory', 'ContinuationScope', 'AdmittedHasPage', 'MetadataOnly', 'NumericWindow', 'ContinuationEnd'])
REQUIRED_MUTANTS['SSMDocumentVersionPage'] = set(['IgnorePermission', 'IgnoreMissingDefinition', 'IgnoreCursorAddress', 'IgnoreCursorSubject', 'IgnoreCursorStamp', 'IgnoreCursorSize', 'IgnoreCursorPosition', 'IncludeDefinitionBody', 'WindowStartsAtOne', 'ContinuePastEnd'])
REQUIRED_SOURCE_SCOPES['SSMDocumentVersionPage'] = set(['internal/aws/ssm_document_versions.go', 'internal/aws/ssm_documents_api.go'])

BASE_MODELS.add('SecretMetadataUpdate')
BASE_INVARIANTS['SecretMetadataUpdate'] = {'ValidPresent', 'Authorized', 'VersionPreserved', 'StagePreserved', 'ValuePreserved', 'NoPartialVisibility', 'TypeOK', 'ExactMetadata', 'IdentityPreserved', 'PeerPreserved', 'RefusalUnchanged', 'CreatedPreserved', 'DurableAcknowledgement'}
REQUIRED_MUTANTS['SecretMetadataUpdate'] = {'FailedRollback', 'CorruptStagePreserved', 'SkipCommitAcknowledgement', 'SkipValidity', 'SkipExistence', 'SkipFinalStateCheck', 'CorruptIdentityPreserved', 'CorruptVersionPreserved', 'ExposeTentativeWrite', 'CorruptValuePreserved', 'SkipAuthorization', 'WrongDescription', 'CorruptPeerPreserved', 'CorruptCreatedPreserved'}
REQUIRED_SOURCE_SCOPES['SecretMetadataUpdate'] = {'internal/aws/secret_http.go', 'internal/aws/secret_metadata_update.go'}

BASE_MODELS.add('SecretDescriptionWorkflow')
BASE_INVARIANTS['SecretDescriptionWorkflow'] = {'MetadataIdentity', 'ReadRetriesStayReadOnly', 'AcknowledgmentIdentity', 'TypeOK', 'NoStaleFollowupRead', 'LiveWorkKeepsLatch', 'NoValueRetrieval', 'NoWriteWhileUncertain', 'FreshEmissions', 'AcknowledgedStatusPreserved', 'OnePendingSubmission', 'ServerCommitPreserved', 'UnmountHidesDisplay', 'UncertaintyFence'}
REQUIRED_MUTANTS['SecretDescriptionWorkflow'] = {'AbortRollsBackServerCommit', 'PublishStaleRead', 'FailedReconciliationClearsFence', 'StaleACKStartsRefresh', 'RetrieveValueOnRefresh', 'ReadRetryResubmitsMutation', 'AcceptMalformedACK', 'StaleWriteUnlocksNewRequest', 'PublishStaleWrite', 'SkipSynchronousLatch', 'SkipEpochCheck', 'AcceptMalformedMetadata', 'AbortSuppressesServerCommit', 'ACKRefreshResubmitsMutation', 'UnmountKeepsDisplay', 'FailedRefreshLosesACK', 'StaleReadUnlocksNewRequest', 'SkipUncertaintyFence'}
REQUIRED_SOURCE_SCOPES['SecretDescriptionWorkflow'] = {'tools/console/src/secret_description.jsx'}

# Independent bounded native SSM batch transaction contract.
BASE_MODELS.add('SSMBatchDeletion')
BASE_INVARIANTS['SSMBatchDeletion'] = {'TypeOK', 'AdmissionBeforeMutation', 'PendingInvisible', 'NoEarlyResponse', 'FailureAtomic', 'DurableAck', 'PeerFrame', 'ExactDeletion', 'SuccessfulResultsExact', 'MissingIsNotFailure', 'CanceledPreservesState'}
REQUIRED_MUTANTS['SSMBatchDeletion'] = {'CountAfterDedup', 'AllowEmpty', 'IgnoreShape', 'ValidateFirstOnly', 'IgnoreExplicitDeny', 'AuthorizePresentOnly', 'AuthorizeFirstOnly', 'WriteBeforeCommit', 'RespondBeforeCommit', 'PersistFailedCommit', 'AcknowledgeFailedCommit', 'PersistCanceledWork', 'PersistStorageFailure', 'DeleteForeignRegion', 'DeleteUnrelatedName', 'RetainTarget', 'MutatePeerValue', 'KeepDuplicates', 'ReverseResults', 'ReportMissingAsDeleted', 'HideMissingResults', 'RejectMissing', 'MutatePeerVersion'}
REQUIRED_SOURCE_SCOPES['SSMBatchDeletion'] = {'internal/aws/parameters.go', 'internal/aws/ssm.go'}

BASE_MODELS.add('SQSBatchDeletion')
BASE_INVARIANTS['SQSBatchDeletion'] = {'DurableAck', 'ParentBeforeMutation', 'PeerFrame', 'NoEarlyResponse', 'PurgeBeforeResolution', 'PreflightBeforeEffects', 'IAMBinding', 'CommittedStateExact', 'TypeOK', 'FailureAtomic', 'VerifiedBeforeCommit', 'OrderedPartition', 'BothArraysPresent', 'SemanticFailuresCommit', 'AuthorizationBeforeTransaction', 'PendingInvisible', 'ResolutionExact', 'SanitizedFailure', 'DeleteAcknowledged'}
REQUIRED_MUTANTS['SQSBatchDeletion'] = {'ReuseSelectedReceipt', 'LeakInternalFailure', 'IgnoreDeny', 'ReplyBeforeCommit', 'AcceptRetiredQueueReceipt', 'ReverseFailed', 'ReverseSuccessful', 'OmitSuccessfulArray', 'SkipShape', 'SkipFinalQueueCheck', 'SkipQueueURL', 'PurgePeerRows', 'SkipExpiryCleanup', 'UseBatchIAMAction', 'IgnorePersistedDuplicate', 'AcknowledgeUncommittedDeletion', 'DropResolvedFailure', 'ValidateFirstIDOnly', 'WrongSenderFault', 'IgnorePurgeFailure', 'ResolvePeerReceipts', 'DropResolvedSuccess', 'RejectAllInvalidHandles', 'SkipMaximumCount', 'OmitFailedArray', 'AcceptPreviousReceipt', 'DiscardCommittedExpiry', 'IgnoreCommitFailure', 'SkipMinimumCount', 'SkipFinalIDCheck', 'SkipQueueRecheck', 'SkipFinalReceiptCheck', 'PublishFailedTransaction', 'AuthorizePeerQueue', 'IgnoreAffectedCount', 'ExposeStagedCleanup', 'ValidateFirstReceiptOnly', 'SkipFinalExpiryCheck', 'SkipDistinctIDs'}
REQUIRED_SOURCE_SCOPES['SQSBatchDeletion'] = {'internal/aws/sqs_store.go', 'internal/aws/sqs_batch_delete.go', 'internal/aws/sqs.go'}


# Bounded, reachable-seed NIC bookkeeping model; not physical packet authority.
BASE_MODELS.add('NICSessionBookkeeping')
BASE_INVARIANTS['NICSessionBookkeeping'] = {'TupleTombstones', 'RevocationRetainsDebt', 'IncomingIsolation', 'ClosedQueuesEmpty', 'ImmutableTuples', 'EffectNotCleanupProof', 'LiveAccounting', 'RewrapPreservesState', 'ChargedAttempts', 'TupleDirectionGuard', 'BudgetMonotone', 'NoClosedAdmission', 'LateSuccessRejected', 'DrainRequiresJoins', 'SingleDelivery', 'TypeOK'}
REQUIRED_SOURCE_SCOPES['NICSessionBookkeeping'] = {'internal/workload/guestnet/tcp_sessions.go'}
REQUIRED_MUTANTS['NICSessionBookkeeping'] = {'DeliverStaleServiceFrame', 'AdmitSecondDelivery', 'ReadAdmittedAfterClose', 'ErasePossibleEffect', 'ReleaseWithPendingCallback', 'RewrapDropsPendingQueue', 'KeepRevokedQueue', 'ReuseBeforeQuarantine', 'ReuseLiveTuple', 'PublishLateWriteSuccess', 'CrossSessionQueue', 'SkipByteCharge', 'ReleaseWithoutWatcher', 'NICDrainWithoutChildren', 'ReuseFirstTuple', 'ReleaseWithPendingRead', 'ResetIncarnationBudget', 'AcceptWrongIncomingTuple', 'SkipFrameCharge', 'MutateAllocatedService', 'ReleaseLiveOnRevoke', 'RevokeErasesReadDebt'}


# Reachable-seed operation protocol abstraction, not transport or implementation proof.
BASE_MODELS.add('ProbeOperationLifecycle')
BASE_INVARIANTS['ProbeOperationLifecycle'] = {'EvictedForgotten', 'RouteRetained', 'PoisonSticky', 'WireUnknown', 'TypeOK', 'NoLateHostAcceptance', 'AtMostOnce', 'WireResult', 'WorkerBound', 'PoisonExclusion', 'SettlementEvidence', 'ScopedIdentity', 'ScopeImmutable', 'ResultSticky', 'ValidatedAdmission', 'RouteBeforeDispatch', 'LookupIsolation', 'ImmutableBinding', 'WireSettlement', 'DuplicateIsolation', 'TimelySuccess', 'WireRoute', 'RetainedExact', 'WireIdentity', 'RetainedBound', 'WorkerDebt'}
REQUIRED_SOURCE_SCOPES['ProbeOperationLifecycle'] = {'docs/private-http-probe-operations.md', 'internal/workload/guesttcp/http_probe_scope.go', 'internal/workload/guesttcp/http_probe_operation.go', 'internal/workload/guesttcp/http_probe_scope_test.go', 'internal/workload/guesttcp/http_probe_observation_test.go', 'internal/workload/guesttcp/http_probe_wire_test.go', 'internal/workload/guesttcp/http_probe_wire.go', 'internal/workload/guesttcp/http_probe_operation_test.go', 'internal/workload/guesttcp/http_probe_context_test.go', 'internal/workload/guesttcp/http_probe_observation.go'}
REQUIRED_MUTANTS['ProbeOperationLifecycle'] = {'EvictBeforeRetention', 'EvictRunningHandle', 'EvictRawHandle', 'ReplayForgottenKey', 'AcceptAfterHostDeadline', 'WireReadyMeansSettled', 'OverwriteReadyResult', 'EvictSettledHandle', 'UnknownMeansSettled', 'WireDropsAllocatedRoute', 'WireRehabilitatesFailure', 'RenewDuplicateDeadline', 'RetainInvalidContext', 'RepeatDuplicateDispatch', 'ScorePoisonedResult', 'AdmitZeroKey', 'WireWrongIdentity', 'IgnoreLookupOwnership', 'ForgetFailedRoute', 'DispatchBeforeRoute', 'AdoptForeignDuplicate', 'OmitWorkerBudget', 'PublishLateSuccess', 'ReleaseTimeoutWorker', 'ClearPoisonOnValidRead', 'SettleRunningWorker', 'OmitHandleBudget', 'UnsealedScopedStart', 'RebindScope'}


# Bounded source-correspondence model; explicit limits remain in registry.
BASE_MODELS.add('TCPRouteClassification')
BASE_INVARIANTS['TCPRouteClassification'] = {'CompleteSelection', 'ExactDirectionalTuple', 'CopiedConfiguration', 'ConstructorGuard', 'NonzeroPorts', 'SoleIndex', 'FailClosed', 'TypeOK', 'NoAmbiguousRoutes'}
REQUIRED_MUTANTS['TCPRouteClassification'] = {'SkipSyntax', 'DiscardCopiedRouteList', 'AcceptOverlappingConstructor', 'Ignore_lip', 'AlwaysFirstIndex', 'SkipDirection', 'ZeroPeerPort', 'IgnoreFixedPeerPort', 'LoseWildcard', 'Ignore_pip', 'ReverseDirection', 'Ignore_pm', 'Ignore_lm', 'Ignore_lp'}
REQUIRED_SOURCE_SCOPES['TCPRouteClassification'] = {'internal/workload/guestnet/tcp_routes.go'}


# Bounded source-correspondence model; explicit limits remain in registry.
BASE_MODELS.add('FrameCodecLifecycle')
BASE_INVARIANTS['FrameCodecLifecycle'] = {'EncoderNoRetry', 'OneWrite', 'EncoderSuccess', 'EncoderSerialization', 'TypeOK', 'DecoderPoison', 'UncertainSticky', 'DecoderSerialization', 'HeaderBeforeBody', 'LocalRejectNoIO', 'NoBodyForInvalidHeader', 'BoundedReads', 'DecoderNoRetry', 'DecoderSuccess', 'EncoderPoison'}
REQUIRED_MUTANTS['FrameCodecLifecycle'] = {'DecoderForgetsPoison', 'EncoderRetriesPoison', 'InvalidLocalPoison', 'EncoderForgetsPoison', 'FalseWriteSuccess', 'PartialBodySuccess', 'DecoderRetriesPoison', 'IgnoreWriteError', 'ConcurrentDecoderStart', 'ConcurrentEncoderStart', 'InvalidLocalWrites', 'ForgetPartialEffect', 'IgnorePartialHeader', 'IgnoreShortWrite', 'IgnoreInvalidLength', 'HeaderFailureEntersBody', 'DuplicateHeaderRead', 'DuplicateWrite'}
REQUIRED_SOURCE_SCOPES['FrameCodecLifecycle'] = {'internal/workload/guestnet/frame.go'}


# Bounded source-correspondence model; explicit limits remain in registry.
BASE_MODELS.add('PersistentTransportLifecycle')
BASE_INVARIANTS['PersistentTransportLifecycle'] = {'SelectedDebt', 'CompletionJoin', 'ProbePumpDebt', 'QueueSealed', 'NoLateSuccess', 'CapacityRelease', 'ChildJoin', 'TypeOK', 'NoInvalidAdmission', 'ReadJoin', 'QueueOwnership', 'EffectRetained'}
REQUIRED_MUTANTS['PersistentTransportLifecycle'] = {'ReleaseUncertainResources', 'ForgetPendingSend', 'ExitWithSelectedDebt', 'EnqueueAfterSendJoined', 'EnqueueAfterClosure', 'ForgetPendingDial', 'FinishBeforeWriter', 'PublishLateWriteSuccess', 'ExitWithoutQueueSeal', 'EraseUncertainWriteEffect', 'SelectAfterProbeClose', 'FinishBeforeOwnedClose', 'LoseQueuedAcknowledgement', 'ForgetPendingRead', 'AdmitAfterRevocation'}
REQUIRED_SOURCE_SCOPES['PersistentTransportLifecycle'] = {'internal/workload/guesttcp/transport_persistent_test.go', 'internal/workload/guesttcp/transport_persistent.go', 'internal/workload/guesttcp/transport.go'}


# Bounded classification model for dropping unowned persistent-NIC guest frames.
BASE_MODELS.add('PersistentIngressPolicy')
BASE_INVARIANTS['PersistentIngressPolicy'] = {'AcceptOnlyOwnedValid', 'TypeOK', 'OwnedProbeAbuseRevokes', 'DroppedNotChargedOrAccepted', 'OwnedNeverDropped', 'UnownedNeverRevokes'}
REQUIRED_MUTANTS['PersistentIngressPolicy'] = {'HandleUntrustedARP', 'ChargeDropped', 'RevokeUnhandled', 'DropOwnedProbe', 'SkipTCPParse', 'AcceptSpoofedProbe'}
REQUIRED_SOURCE_SCOPES['PersistentIngressPolicy'] = {'internal/workload/guesttcp/transport_persistent_test.go', 'internal/workload/guesttcp/transport_persistent.go', 'internal/workload/guesttcp/transport.go'}


# Bounded source-correspondence model; explicit limits remain in registry.
BASE_MODELS.add('PacketAdmission')
BASE_INVARIANTS['PacketAdmission'] = {'HeaderSound', 'BoundedCursor', 'OptionComplete', 'Progress', 'OptionSound'}
REQUIRED_MUTANTS['PacketAdmission'] = {'SkipTTL', 'AllowUnknown', 'SkipTCPHeaderMin', 'IgnoreEOLTail', 'RejectMSS', 'SACKPermWrongLength', 'LargeScale', 'SACKInvalidBlockWidth', 'SkipTCPChecksum', 'AllowDuplicate', 'AllowTruncatedTimestamp', 'SkipVersionIHL', 'SACKTooShort', 'SkipProtocol', 'RejectNOP', 'MSSWrongLength', 'SkipTotalMax', 'MSSWithoutSYN', 'ScaleWithoutSYN', 'SkipFragment', 'SACKPermWithoutSYN', 'TimestampWrongLength', 'SACKWithoutACK', 'SkipTCPHeaderMax', 'SkipDestinationPort', 'RejectAllOptions', 'SkipEtherType', 'SACKWithSYN', 'ZeroMSS', 'SkipReserved', 'SkipFrameMax', 'SkipSourcePort', 'SkipIPChecksum'}
REQUIRED_SOURCE_SCOPES['PacketAdmission'] = {'internal/workload/guestnet/packet.go'}


# Bounded source-correspondence model; explicit limits remain in registry.
BASE_MODELS.add('EngineLifecycle')
BASE_INVARIANTS['EngineLifecycle'] = {'ReleaseAfterDestroy', 'WorkerBound', 'NoTimerAfterReset', 'RegistrationBeforeDone', 'CloseWaitsMutation', 'ConnectionJoin', 'ConnectionOwnership', 'TimerJoin', 'NoInvalidAdmission', 'DialJoin', 'SingleDial', 'RejectedRollback', 'EndpointJoin', 'LateConnectionJoin', 'WorkerReservation', 'DeadlineBound', 'TypeOK', 'CaptureRetained', 'WorkerDebt'}
REQUIRED_MUTANTS['EngineLifecycle'] = {'ReadmitDial', 'DestroyBeforeDialDone', 'ExceedWorkerBudget', 'ReleaseSlotEarly', 'LeakRejectedSlot', 'ReleaseWorkersEarly', 'CloseBeforeTimerReset', 'RetainDeadlineTimer', 'DialAfterCancellation', 'ForgetRegistration', 'CloseOverDeadlineMutation', 'RepeatCloseCapture', 'ExtendAbsoluteLifetime', 'MutationAfterCancellation', 'DoneBeforeEndpointReturn', 'MutationAfterAbsoluteExpiry', 'ForgetLateConnectionClose', 'DestroyBeforeConnectionClosed', 'ForgetConnectionPointer', 'LoseReservedSlot'}
REQUIRED_SOURCE_SCOPES['EngineLifecycle'] = {'internal/workload/guesttcp/engine.go', 'go.mod', 'go.sum'}


# Bounded source-correspondence model; explicit limits remain in registry.
BASE_MODELS.add('ARPTrustedReply')
BASE_INVARIANTS['ARPTrustedReply'] = {'AdmissionSound', 'ConstructorExact', 'TrustedReply', 'ReplyFormat', 'AdmissionComplete', 'ImmutableBinding'}
REQUIRED_MUTANTS['ARPTrustedReply'] = {'WrongReplyTargetMAC', 'WrongReplyLength', 'WrongReplySenderIP', 'SkipEtherSource', 'WrongReplySenderMAC', 'SkipEtherType', 'SkipDestination', 'RejectBroadcast', 'SkipHardwareLength', 'WrongReplySource', 'WrongReplyPadding', 'SkipTargetIP', 'WrongReplyOpcode', 'AcceptBadBinding', 'SkipMinimumLength', 'SkipProtocolLength', 'SkipMaximumLength', 'WrongReplyTargetIP', 'SkipSenderIP', 'WrongReplyDestination', 'SkipProtocol', 'SkipOpcode', 'SkipPadding', 'RetainCallerAlias', 'SkipHardware', 'RejectDirectedRefresh', 'SkipTargetMAC', 'SkipSenderMAC'}
REQUIRED_SOURCE_SCOPES['ARPTrustedReply'] = {'internal/workload/guestnet/arp.go'}


# Bounded source-correspondence model; explicit limits remain in registry.
BASE_MODELS.add('PassiveServiceBoundary')
BASE_INVARIANTS['PassiveServiceBoundary'] = {'NoOwnershipTransfer', 'PreserveFailure', 'DeadlineExact', 'OutputExact', 'ErrorPrecedence', 'ReceiveExact', 'ExpiryExact', 'ConstructorExact', 'AccountRejected', 'DelegationExact'}
REQUIRED_MUTANTS['PassiveServiceBoundary'] = {'WrongServiceReceive', 'IgnoreElapsedParent', 'WrongReceiveEngine', 'EraseWrappedFailure', 'ClampBeforeWindowValidation', 'IgnoreOutputBudget', 'AcceptExpiredRequest', 'BypassPersistentQueue', 'SkipRouteValidation', 'IgnoreParentCancellation', 'IgnoreParentPresence', 'SkipMixedBinding', 'AllowOverlappingRoute', 'PrematureOwnership', 'IgnoreServiceRevocation', 'MissingService', 'IgnoreEarlierParent', 'RequireSameLocalIP', 'IgnoreContextCause', 'UnaccountedOutput', 'CrossRouteOutput'}
REQUIRED_SOURCE_SCOPES['PassiveServiceBoundary'] = {'internal/workload/guesttcp/passive_lifetime.go', 'internal/workload/guesttcp/transport_service.go', 'internal/workload/guesttcp/transport_passive.go'}


# Bounded source-correspondence model; explicit limits remain in registry.
BASE_MODELS.add('HTTPObservationLifecycle')
BASE_INVARIANTS['HTTPObservationLifecycle'] = {'RawAccounting', 'DeadlineImmutable', 'NoBodyConsumption', 'ReceivedAtObserved', 'CleanupRequested', 'InformationalBound', 'NoPostTimeoutPublication', 'RetainedWorkerDebt', 'PublicationEvidence', 'StatusPreserved', 'RawHeaderBound', 'InfoAccounting', 'TypeOK', 'RequestBound', 'TimeoutRequestsClose'}
REQUIRED_MUTANTS['HTTPObservationLifecycle'] = {'TimeoutOmitsClose', 'ResetInformationalHistory', 'AcceptBareOrFoldedLines', 'IgnoreCumulativeFields', 'ConsumeResponseBody', 'AcceptOutOfRangeStatus', 'IgnoreRawByteLimit', 'AcceptUnsupportedVersion', 'ForgeHeaderTimestamp', 'AcceptSwitchingProtocols', 'AcceptOwnerFailure', 'AcceptUnsupportedMinor', 'IgnoreInformationalLimit', 'ResetSharedByteBudget', 'ResetCumulativeFieldCount', 'AcceptArbitraryPath', 'ReleaseOnCallerTimeout', 'IgnoreFinalDeadline', 'PublishUncertainCleanup', 'FailureOmitsCleanup', 'RetryShortWrite', 'AcceptJoinedCancellationError', 'PromoteErrorStatus', 'LateWorkerPublishes', 'RenewOriginalDeadline'}
REQUIRED_SOURCE_SCOPES['HTTPObservationLifecycle'] = {'internal/workload/guesttcp/http_observation.go'}


# Bounded source-correspondence model; explicit limits remain in registry.
BASE_MODELS.add('ListenerLifecycle')
BASE_INVARIANTS['ListenerLifecycle'] = {'JoinBeforeDestroy', 'SharedCapacity', 'OnePendingAccept', 'OwnerRetention', 'NoEarlyRelease', 'BudgetAccounting', 'TimerResetAttempted', 'SequenceRetention', 'OneAcceptAdmission', 'CapacityAfterJoin', 'NoLateAdmission', 'BoundedDeadline', 'AttemptRetention'}
REQUIRED_MUTANTS['ListenerLifecycle'] = {'ForgetAttempt', 'AllowChangedSYN', 'SkipConnectionTimer', 'ClosedFlagIsNotReturn', 'UnrecordedData', 'ReleaseBeforeDestroy', 'SkipAggregateByteCharge', 'RewriteFirstSequence', 'ForgetPortOnConnectionClose', 'AcceptBadTuple', 'ForgetOwnedWrapper', 'DestroyBeforeShutdown', 'ForgetWrapperOnCloseError', 'DestroyWithAcceptDebt', 'AcceptSYNPayload', 'AcceptUnrecordedPort', 'AcceptBadSYNFlags', 'SkipCleanupTimer', 'UnclampedDeadline', 'SkipAggregateFrameCharge', 'IgnoreRegisteredAcceptJoin', 'ParallelAccept', 'ResetBudgetOnClose', 'EraseExternalWorkers', 'DestroyWithConnectionDebt', 'LateAcceptPublish', 'LateAcceptStart', 'AllowSealedSYN'}
REQUIRED_SOURCE_SCOPES['ListenerLifecycle'] = {'internal/workload/guesttcp/listener.go', 'go.mod', 'go.sum'}


# Bounded selected-capability command forwarding, not physical authority.
BASE_MODELS.add('CommandDispatchLifecycle')
BASE_INVARIANTS['CommandDispatchLifecycle'] = {'Admission', 'ExactDomain', 'ReadOnly', 'UnknownNotSettled', 'AtMostOnce', 'TypeOK', 'ReadAfterExpiry', 'ReadAdmission', 'OriginalDeadline', 'DuplicatePreserved'}
REQUIRED_MUTANTS['CommandDispatchLifecycle'] = {'RepeatDuplicateExecution', 'IgnoreService', 'RenewFromReceipt', 'UnknownMeansSettled', 'ReadIgnoresShape', 'IgnoreDomain', 'IgnoreShape', 'ReadIgnoresBinding', 'FreshCheckBeforeDuplicate', 'StartRefusalMeansSettled', 'ReadEntersStart', 'ReadIgnoresService', 'IgnoreExpiry', 'ReadRequiresLiveClock', 'IgnoreQualification', 'IgnoreBinding'}
REQUIRED_SOURCE_SCOPES['CommandDispatchLifecycle'] = {'internal/workload/guesttcp/http_probe_command.go'}

# Exact bounded owned-session composition obligations, additive to accepted models.
BASE_MODELS.add('OwnedProbeSessionLifecycle')
BASE_INVARIANTS['OwnedProbeSessionLifecycle'] = {'NoUnsafeException', 'ImmutableBinding', 'FailedPacketNotPublished', 'NoTokenReplay', 'NoNeedlessSeal', 'NoInvalidPublication', 'CloseBeforeJoin', 'OneApply', 'TentativeHidden', 'TerminalSeal', 'CloseOnce', 'EvidenceHasSuccess', 'NoInvalidAdmission', 'TypeOK', 'DebtUntilDrain'}
REQUIRED_MUTANTS['OwnedProbeSessionLifecycle'] = {'ReleaseBeforeGateDrain', 'ObservationException', 'IgnoreOwnerSeal', 'SealWithoutClose', 'IOAfterSeal', 'FailedInjectionCreatesProof', 'AdmitAfterSeal', 'DuplicateApply', 'PublishFailedApply', 'PublishAfterRevoke', 'DiscardCompletedInboundException', 'IgnoreClaimedToken', 'RebindOnRevoke', 'IgnoreOriginalCancellation', 'ReleaseBeforeIODrain', 'RepeatClose', 'AcceptSwallowedCallback', 'ReuseSuccessfulContext', 'ResetTokenAfterSuccessfulCall', 'ExposeFailedTentative', 'IgnoreMixedTransportFailure', 'AcceptForeignMarker', 'AcceptIndependentProviderError', 'AcceptProviderWrap', 'ReadTentative', 'PublishProviderError', 'JoinBeforeCloseReturn', 'ReleaseCloseError', 'OutboundException'}
REQUIRED_SOURCE_SCOPES['OwnedProbeSessionLifecycle'] = {'internal/workload/guesttcp/local_injection_outcome.go', 'internal/workload/guesttcp/owned_probe_session.go', 'internal/workload/guesttcp/transport.go', 'internal/workload/guesttcp/transport_persistent.go'}
BASE_MODELS.add('OwnedProbeAttachmentLifecycle')
BASE_INVARIANTS['OwnedProbeAttachmentLifecycle'] = {'TypeOK', 'OwnerDebt', 'TransferredOnlySuccess', 'TransportDebt', 'NoInvalidAdmission', 'IndependentJoins', 'AdmissionOnlyContext', 'RetainedFailureHandle', 'PublishedOnlyBound', 'SingleAttach', 'NoLatePublish', 'ReturnedTransferExact', 'ImmutableDescriptor'}
REQUIRED_MUTANTS['OwnedProbeAttachmentLifecycle'] = {'ForgetTransferredService', 'ReleaseFailedOwner', 'DropFailureHandle', 'PublishFailedBinding', 'IgnoreExpiredAdmission', 'InferOwnerFromTransport', 'ReleaseFailedTransport', 'UseCallerLifetime', 'IgnoreRevokedAdmission', 'RebindAtConstruction', 'PublishAfterRevoke', 'PublishAfterCallerExpiry', 'InferTransportFromOwner', 'TransferOnFailedConstruction', 'RepeatConstructor', 'ReuseReservedOwner'}
REQUIRED_SOURCE_SCOPES['OwnedProbeAttachmentLifecycle'] = {'internal/workload/guesttcp/owned_probe_attachment.go'}
BASE_MODELS.add('OwnedProbeDispatchPolicy')
BASE_INVARIANTS['OwnedProbeDispatchPolicy'] = {'DispatchComplete', 'DispatchSound', 'ExactReplyMatch', 'ImmutableCanonical'}
REQUIRED_MUTANTS['OwnedProbeDispatchPolicy'] = {'CaptureWrongCanonical', 'IgnoreReply_length', 'IgnoreProbe_localIP', 'DropARPEarlyBranch', 'IgnorePassive_peerIP', 'IgnoreReply_hlen', 'IgnoreReply_tip', 'UseMutatedCallerBinding', 'IgnorePassive_localMAC', 'IgnoreReply_eth', 'IgnoreProbe_peerIP', 'IgnoreReply_dst', 'IgnoreReply_smac', 'IgnorePassive_peerMAC', 'IgnorePassive_localPort', 'RemoveReplyDirection', 'IgnoreProbe_localMAC', 'IgnoreTCPDirection', 'IgnoreARPOptIn', 'IgnoreReply_htype', 'IgnoreReply_padding', 'IgnoreProbe_peerPort', 'IgnoreReply_tmac', 'IgnoreProbeLowerPort', 'AllowMalformedTCP', 'IgnorePassiveOptIn', 'IgnoreReply_sip', 'ReverseARPDirection', 'IgnoreResponderPresence', 'IgnoreResponderReady', 'IgnoreProbe_peerMAC', 'IgnoreReply_src', 'IgnoreProbeUpperPort', 'IgnoreReply_op', 'IgnoreReply_plen', 'IgnoreReply_ptype', 'IgnorePassive_localIP'}
REQUIRED_SOURCE_SCOPES['OwnedProbeDispatchPolicy'] = {'internal/workload/guesttcp/owned_probe_session.go', 'internal/workload/guestnet/arp.go'}
OWNED_COMPOSITION_BOUNDS = {'OwnedProbeSessionLifecycle': 'Original one-session/transaction and two facade calls retained, plus finite private-token provenance, three cancellation windows and exact provider propagation. No liveness.', 'OwnedProbeAttachmentLifecycle': 'One preissued owner and immutable descriptor, two attachment attempts, one constructor, independent pending/ok/error owner and transport joins. Exhaustive finite safety abstraction.', 'OwnedProbeDispatchPolicy': 'Canonical reply plus14single-field corruptions, immutable original/caller mutation; explicit normalized probe/passive tuples including source port boundaries; five frame classes, three directions and independent opt-in/presence/readiness booleans.'}
OWNED_COMPOSITION_CONFIG = {'OwnedProbeSessionLifecycle': 'SPECIFICATION Spec\nCHECK_DEADLOCK FALSE\nINVARIANTS TypeOK NoInvalidAdmission NoInvalidPublication TentativeHidden CloseOnce CloseBeforeJoin DebtUntilDrain ImmutableBinding OneApply TerminalSeal EvidenceHasSuccess NoUnsafeException NoTokenReplay FailedPacketNotPublished NoNeedlessSeal\n', 'OwnedProbeAttachmentLifecycle': 'SPECIFICATION Spec\nCHECK_DEADLOCK FALSE\nINVARIANTS TypeOK NoInvalidAdmission SingleAttach TransferredOnlySuccess ReturnedTransferExact PublishedOnlyBound NoLatePublish RetainedFailureHandle IndependentJoins OwnerDebt TransportDebt ImmutableDescriptor AdmissionOnlyContext\n', 'OwnedProbeDispatchPolicy': 'SPECIFICATION Spec\nCHECK_DEADLOCK FALSE\nINVARIANTS ImmutableCanonical ExactReplyMatch DispatchSound DispatchComplete\n'}


# Exact bounded EC2 attachment coordinator guard obligation.
BASE_MODELS.add('EC2GuestAttachmentLifecycle')
BASE_INVARIANTS['EC2GuestAttachmentLifecycle'] = {'RetainedScope', 'RegisteredBeforeIO', 'BackendIndependent', 'AdvanceGuard', 'NoReconstruction', 'DurableBeforeConsumption', 'ImmutableAssociation', 'IndependentDrain', 'TypeOK', 'DurableSealAck', 'PreparationExact', 'SealBeforeAck', 'ProfileGuard', 'PublicationAdmission'}
REQUIRED_MUTANTS['EC2GuestAttachmentLifecycle'] = {'TransportBeforeRegistration', 'PrepareDeletedProfile', 'PublishAfterSeal', 'OrdinaryAdvanceBypass', 'DrainWithoutTransportJoin', 'PublishAfterCancellation', 'DrainWithoutOwnerJoin', 'AdoptUnknownPrepare', 'AcknowledgeUnwrittenSeal', 'ConsumeBeforeJournal', 'SealWithoutOwnerRevoke', 'AdvanceWrongIdentity', 'LocalDrainClaimsBackend', 'TransportAfterSeal', 'DrainDuringConstruction', 'AdvanceBeforeSeal', 'IgnoreAssociation', 'RenewAttachmentDeadline', 'ProfileGuardRemoved', 'PrepareAfterLeaseAdvance', 'RestartRestoresReceipt', 'PublishUnknownCommit'}
REQUIRED_SOURCE_SCOPES['EC2GuestAttachmentLifecycle'] = {'internal/aws/ec2_guest_lease.go', 'internal/aws/ec2_guest_attachment.go', 'internal/aws/iam_instance_profiles.go'}
EC2_ATTACHMENT_BOUNDS = 'One held lease, one retained association/reservation, one owner and transport construction, one seal, one profile mutation, one held-to-contain advance and one restart. Symbolic fixed deadlines and exact identity; all finite schedules from empty journal, no seeds or depth cutoff.'
EC2_ATTACHMENT_CONFIG = 'SPECIFICATION Spec\nCHECK_DEADLOCK FALSE\nINVARIANTS TypeOK PreparationExact DurableBeforeConsumption RegisteredBeforeIO PublicationAdmission SealBeforeAck DurableSealAck BackendIndependent IndependentDrain AdvanceGuard ProfileGuard ImmutableAssociation RetainedScope NoReconstruction\n'

COMMAND_DISPATCH_BOUNDS = 'One selected capability/key, two sequential commands, clock0..3 and original requested deadlines1..4; existing key classes none/exact/foreign, independent lower-layer admission/refusal. Forwarded deadline is not effective scope-capped deadline.'
COMMAND_DISPATCH_CONFIG = 'INIT Init\nNEXT Next\nCHECK_DEADLOCK FALSE\nINVARIANT TypeOK\nINVARIANT Admission\nINVARIANT OriginalDeadline\nINVARIANT ReadOnly\nINVARIANT UnknownNotSettled\nINVARIANT DuplicatePreserved\nINVARIANT ExactDomain\nINVARIANT ReadAfterExpiry\nINVARIANT AtMostOnce\nINVARIANT ReadAdmission\n'


# Independent bounded Auto Scaling plan capacity-target contract.
BASE_MODELS.add('AutoScalingPlanTargets')
BASE_INVARIANTS['AutoScalingPlanTargets'] = {'TypeOK', 'BoundedWhileOwned', 'OnlyAuthorizedValid', 'NoOwnershipSteal', 'DeleteKeepsCapacity', 'RejectionFrame', 'OtherTeamUnchanged', 'PendingInvisible'}
REQUIRED_MUTANTS['AutoScalingPlanTargets'] = {'OmitAuthorization', 'OmitValidation', 'SkipCapacityBound', 'ClampLowerOnly', 'StealOwnership', 'RevertOnDelete', 'WriteOtherTeam', 'ExposePendingState', 'ReuseTableGeneration', 'RejectWritesCapacity', 'RejectClaimsOwner'}
REQUIRED_SOURCE_SCOPES['AutoScalingPlanTargets'] = {'internal/aws/autoscalingplans_store.go', 'internal/aws/autoscalingplans_http.go', 'internal/aws/autoscalingplans_dispatch.go', 'internal/aws/s3.go', 'internal/aws/ssm.go', 'internal/server/server.go'}

# Console request ownership is independent of the native transaction model.
BASE_MODELS.add('ScalingPlanConsole')
BASE_INVARIANTS['ScalingPlanConsole'] = {
    'TypeOK', 'OnlyCurrentReplyPublishes', 'SuccessRequiresAck', 'RetryRequiresNamedRefresh',
}
REQUIRED_MUTANTS['ScalingPlanConsole'] = {
    'PublishRetiredRead', 'UnknownAsAcknowledged', 'ResendUncertainWrite', 'ListUnlocksWrite',
}
REQUIRED_SOURCE_SCOPES['ScalingPlanConsole'] = {
    'internal/server/server.go', 'internal/server/web/app.js',
    'tools/console/src/autoscaling-plans.jsx', 'tools/console/src/autoscaling-plans.mjs',
    'tools/console/src/console-navigation.mjs', 'tools/console/src/main.jsx',
}

# Independent EC2 read-only discovery admission contract. These fixed sets must
# not be derived from the mutable registry, which this gate validates.
BASE_MODELS.add('EC2SecurityGroupConsoleEpoch')
BASE_INVARIANTS['EC2SecurityGroupConsoleEpoch'] = {'TypeOK', 'EligibleRequest', 'ReadOnlyOperation', 'OnlyCurrentScope', 'OnlySelectedVPC', 'FailuresNeverEmpty'}
REQUIRED_MUTANTS['EC2SecurityGroupConsoleEpoch'] = {'UnsignedRequest', 'UnadvertisedRequest', 'HandoffRequest', 'NativeVPCRequest', 'WrongOperation', 'LateReply', 'OldRowsPersist', 'WrongVPC', 'RefusalAsEmpty'}
REQUIRED_SOURCE_SCOPES['EC2SecurityGroupConsoleEpoch'] = {'internal/server/server.go', 'internal/server/web/app.js', 'tools/console/src/ec2-readonly.jsx', 'tools/console/src/ec2-response.mjs', 'tools/console/src/ec2-security-groups.mjs', 'tools/console/src/ec2-vpc-security-groups.jsx', 'tools/console/src/main.jsx'}
# These models were already registered on main. Pin their reviewed inventory
# explicitly so deleting a pre-existing property, scope or control cannot evade the gate.
BASE_MODELS.add('SimpleDBConsoleEpoch')
BASE_INVARIANTS['SimpleDBConsoleEpoch'] = {
    'TypeOK', 'VisibleRequiresCapability', 'OnlyCurrentScopeVisible',
    'SingleCurrentChain', 'ReadbackOnlyAfterAck', 'WriteNoticeRequiresAck',
}
REQUIRED_MUTANTS['SimpleDBConsoleEpoch'] = {'ReadBackAfterRejectedPut'}
REQUIRED_SOURCE_SCOPES['SimpleDBConsoleEpoch'] = {
    'internal/server/server.go', 'internal/server/web/app.js',
    'tools/console/src/console-navigation.mjs', 'tools/console/src/main.jsx',
    'tools/console/src/simpledb-client.mjs', 'tools/console/src/simpledb.jsx',
}

BASE_MODELS.add('RDSPrivatePostgresTopology')
BASE_INVARIANTS['RDSPrivatePostgresTopology'] = {
    'TypeOK', 'LocalStartupRequired', 'AuthorityBeforeNative', 'LegacyConfigurationPreserved',
    'OwnedTopologyBeforeWrite', 'AtomicRefusal', 'ConfigurationOnly',
}
REQUIRED_MUTANTS['RDSPrivatePostgresTopology'] = {
    'AllowUntrustedStartup', 'ReadBeforeAuthority', 'RequireTopologyByDefault',
    'IgnoreTopology', 'PersistRefusedUpdate', 'AdvertiseEndpoint',
}
REQUIRED_SOURCE_SCOPES['RDSPrivatePostgresTopology'] = {
    'cmd/simulator/main.go', 'internal/aws/catalog/engine.go',
    'internal/aws/catalog/rds_private_postgres_topology.go', 'internal/aws/catalog/semantics.go',
    'internal/aws/rds_private_postgres_topology.go', 'internal/server/sakura_provider.go',
    'internal/server/server.go',
}

BASE_MODELS.add('RDSOwnedPostgresRuntime')
BASE_INVARIANTS['RDSOwnedPostgresRuntime'] = {
    'TypeOK', 'NoWorkWithoutPin', 'ReadyNeedsOwnedSCRAM', 'NoProofAfterRevocation',
    'CleanupAfterDrain', 'UncertaintyRetainsWorkspace',
}
REQUIRED_MUTANTS['RDSOwnedPostgresRuntime'] = {
    'UnpinnedWorkspace', 'ReadyBeforeProof', 'ReplyAfterRevocation',
    'CleanBeforeDrain', 'DiscardUnknownAcquisition',
}
REQUIRED_SOURCE_SCOPES['RDSOwnedPostgresRuntime'] = {
    'internal/rdsruntime/bundle_linux_amd64.go', 'internal/rdsruntime/runtime_linux_amd64.go',
}

BASE_MODELS.add('RDSOwnedOperatorComposition')
BASE_INVARIANTS['RDSOwnedOperatorComposition'] = {
    'TypeOK',
    'MixedRefusesBeforeEffects',
    'OwnedAdmissionRequiresPreflight',
    'DefaultRemainsMetadata',
    'IndependentClosersAttempted',
    'ObservedJoinBeforeReturn',
    'DebtKeepsSQLite',
    'CleanAttemptsSQLiteClose',
    'ReportsCloseCauses',
}
REQUIRED_MUTANTS['RDSOwnedOperatorComposition'] = {
    'AllowMixedMode',
    'IgnoreLocalScope',
    'IgnoreBundlePin',
    'SelectOwnerByDefault',
    'SkipLambdaAfterDebt',
    'SkipAppEngineAfterDebt',
    'SkipHealthWait',
    'CloseDespiteDebt',
    'RetainCleanStore',
    'SuppressOwnerError',
    'SuppressIndependentErrorsOnDebt',
}
REQUIRED_SOURCE_SCOPES['RDSOwnedOperatorComposition'] = {
    'cmd/simulator/main.go',
    'cmd/simulator/rds_owned_config.go',
    'internal/server/battle_runtime_unix.go',
    'internal/server/rds_owned_operator.go',
    'internal/server/rds_owned_operator_linux_amd64.go',
    'internal/server/rds_owned_operator_unsupported.go',
    'internal/server/sakura_provider.go',
    'internal/server/server.go',
}

BASE_MODELS.add('RDSOwnedGenerationFence')
BASE_INVARIANTS['RDSOwnedGenerationFence'] = {
    'TypeOK', 'LaunchNeedsCommittedAuthority', 'AttachNeedsCurrent',
    'FencedGenerationCannotChange', 'UncertainCleanupRemainsFenced',
    'NoPublishedEndpoint', 'InvalidModifyKeepsOwner',
}
REQUIRED_MUTANTS['RDSOwnedGenerationFence'] = {
    'ReserveWithoutAuthority', 'AttachStale', 'MutateThroughFence',
    'ReleaseOnUncertainCleanup', 'PublishNetwork', 'RetireInvalidModify',
}
REQUIRED_SOURCE_SCOPES['RDSOwnedGenerationFence'] = {
    'internal/aws/catalog/engine.go', 'internal/aws/catalog/rds_owned_runtime_linux_amd64.go',
    'internal/aws/catalog/rds_private_postgres_topology.go', 'internal/aws/catalog/semantics.go',
    'internal/rdsruntime/runtime_linux_amd64.go',
}

# Pin the isolated Entra fixture's admission, output and sensitivity checks
# independently of the mutable registry; this is not a refinement proof.
BASE_MODELS.add('EntraVerificationFixture')
BASE_INVARIANTS['EntraVerificationFixture'] = {
    'FixtureIsolated', 'NumericLoopback', 'ExplicitValidToken',
    'SyntheticTokenOnly', 'ReadOnlyAdmission', 'PublicVerificationOnly',
}
REQUIRED_MUTANTS['EntraVerificationFixture'] = {
    'IgnoreCommand', 'AllowRemote', 'IgnoreTokenOptIn', 'AllowArbitraryIdentity',
    'AllowHTTPWrite', 'AllowForeignHost', 'AllowRemotePeer', 'AllowUnexpectedInput', 'PublishPrivateKey',
}
REQUIRED_SOURCE_SCOPES['EntraVerificationFixture'] = {
    'cmd/simulator/main.go', 'cmd/simulator/azure_entra_fixture.go',
    'internal/azure/entra/verification_fixture.go',
}

BASE_MODELS.add('EC2DetachedNetworkConsole')
BASE_INVARIANTS['EC2DetachedNetworkConsole'] = {'TypeOK', 'AdmittedNativeOnly', 'OnlyCurrentScope', 'VerifiedWriteOnly', 'ScopeChangeFenced', 'SiblingLeasePreserved', 'LeaseUntilReadback'}
REQUIRED_MUTANTS['EC2DetachedNetworkConsole'] = {'AdmitUnverifiedOwner', 'AcceptLateRead', 'ReportUnverified', 'ReportBeforeAck', 'NavigateWhileSaving', 'ReleaseSiblingLease', 'ReleaseOnReceipt', 'SkipRouteTableLease'}
REQUIRED_SOURCE_SCOPES['EC2DetachedNetworkConsole'] = {'internal/server/web/app.js', 'tools/console/src/ec2-readonly.jsx', 'tools/console/src/main.jsx', 'tools/console/src/ec2-native-security-groups.mjs', 'tools/console/src/ec2-vpc-security-groups.jsx', 'tools/console/src/ec2-network-acls.mjs', 'tools/console/src/ec2-vpc-network-acls.jsx', 'tools/console/src/ec2-native-route-tables.mjs', 'tools/console/src/ec2-vpc-native-route-tables.jsx', 'internal/server/server.go'}

BASE_MODELS.add('EC2NativeRouteAssociationLifecycle')
BASE_INVARIANTS['EC2NativeRouteAssociationLifecycle'] = {
    'TypeOK',
    'OneBinding',
    'LegacyAndPeerFrame',
    'RegionalBindings',
    'SameVPC',
    'ExactPairAuthority',
    'CommitFrame',
    'CreateOrReplay',
    'ExactRemoval',
    'MissingIsOpaque',
    'ParentSafety',
    'LiveEndpoints',
    'ScopedExplicitRead',
}
REQUIRED_MUTANTS['EC2NativeRouteAssociationLifecycle'] = {
    'MissingDecision',
    'SkipTableAuthority',
    'SkipSubnetAuthority',
    'IgnoreStoredRemovalPair',
    'AdoptCloudFormationPair',
    'RemoveCloudFormationBinding',
    'AllowCrossVPC',
    'AllowCrossRegion',
    'DenyAllEligibleCreates',
    'DenyAllEligibleRemovals',
    'SecondBinding',
    'LoseRetryIdentity',
    'RemoveSiblingOnSameTable',
    'AllowAttachedTableDelete',
    'CascadeAttachedSubnetDelete',
    'TreatAbortAsCommit',
    'RevealUnknownToOrdinaryCaller',
    'InventMain',
    'ProjectPeerRegion',
}
REQUIRED_SOURCE_SCOPES['EC2NativeRouteAssociationLifecycle'] = {
    'internal/aws/ec2_read.go',
    'internal/aws/ec2_route_association_native_query.go',
    'internal/aws/ec2_route_association_native_store.go',
    'internal/aws/ec2_route_tables_native.go',
    'internal/aws/ec2_route_tables_read.go',
    'internal/aws/ec2_subnet_native.go',
    'internal/aws/ec2_vpc.go',
}

BASE_MODELS.add('EC2NativeRouteTableLifecycle')
BASE_INVARIANTS['EC2NativeRouteTableLifecycle'] = {'TypeOK', 'GrantAtomic', 'NativeParentOnly', 'ParentDependency', 'LegacyPreserved', 'RetiredNotRecreated', 'LiveReceiptMatches', 'ReplayBindsIntent', 'ReadScoped'}
REQUIRED_MUTANTS['EC2NativeRouteTableLifecycle'] = {'IgnoreAllocatedGrant', 'AdoptCFNParent', 'RetireParentWithChild', 'DeleteCFNTable', 'ReplayResurrectsTombstone', 'LoseReceipt', 'ReplayIgnoresIntent', 'ReadOtherRegion'}
REQUIRED_SOURCE_SCOPES['EC2NativeRouteTableLifecycle'] = {'internal/aws/cloudformation.go', 'internal/aws/ec2_read.go', 'internal/aws/ec2_route_tables_native.go', 'internal/aws/ec2_route_tables_native_query.go', 'internal/aws/ec2_route_tables_read.go', 'internal/aws/ec2_vpc.go'}

BASE_MODELS.add('EC2DetachedSecurityGroupRules')
BASE_INVARIANTS['EC2DetachedSecurityGroupRules'] = {'OwnershipFrame', 'TypeOK', 'DirectionBounds', 'ExactTransition'}
REQUIRED_SOURCE_SCOPES['EC2DetachedSecurityGroupRules'] = {
    'internal/aws/ec2_read.go',
    'internal/aws/ec2_security_group_rules_native.go',
    'internal/aws/ec2_security_groups_native_query.go',
    'internal/aws/ec2_security_groups_native_store.go',
    'internal/aws/ec2_security_groups_read.go',
    'internal/aws/ec2_vpc.go',
    'internal/server/server.go',
    'internal/server/web/app.js',
    'tools/console/src/ec2-native-security-groups.mjs',
    'tools/console/src/ec2-readonly.jsx',
    'tools/console/src/ec2-vpc-security-groups.jsx',
    'tools/console/src/main.jsx',
}
REQUIRED_MUTANTS['EC2DetachedSecurityGroupRules'] = {
    'IgnoreRegion',
    'AdoptStack',
    'IgnoreActionGrant',
    'AllowOutsideParent',
    'AcknowledgeFailedCommit',
    'AllowDuplicate',
    'AllowMissingRevoke',
    'PermitOverflow',
    'WrongDirection',
    'RetainDeletedRules',
    'InventDefaultEgress',
    'HideSavedEgress',
}

BASE_MODELS.add('EC2NativeVPCChildren')
BASE_INVARIANTS['EC2NativeVPCChildren'] = {'TypeOK', 'GrantAtomic', 'RejectOtherOwner', 'ParentDependency', 'SiblingFrame', 'ReadScoped', 'ReplayNoRecreate', 'ReceiptTracksLiveACL'}
REQUIRED_MUTANTS['EC2NativeVPCChildren'] = {'IgnoreParentGrant', 'IgnoreAllocatedGrant', 'AdoptStackParent', 'DeleteParentWithChildren', 'DeletePeerKinds', 'ReadForeignScope', 'RecreateRetiredACL', 'LoseACLReceipt'}
REQUIRED_SOURCE_SCOPES['EC2NativeVPCChildren'] = {'internal/aws/cloudformation.go', 'internal/aws/ec2_read.go', 'internal/aws/ec2_vpc.go', 'internal/aws/ec2_subnet_native.go', 'internal/aws/ec2_network_acl.go', 'internal/aws/ec2_network_acl_store.go', 'internal/aws/ec2_security_groups_read.go', 'internal/aws/ec2_security_groups_native_query.go', 'internal/aws/ec2_security_groups_native_store.go', 'internal/server/server.go'}

BASE_MODELS.add('EC2NativeSubnetLifecycle')
BASE_INVARIANTS['EC2NativeSubnetLifecycle'] = {'AuthorizedWrites', 'ExactRead', 'Nonoverlap', 'Bounded', 'ExactDelete', 'NativeParent', 'StackUnchanged'}
REQUIRED_MUTANTS['EC2NativeSubnetLifecycle'] = {'AdoptStackParent', 'IgnoreLimit', 'AllowOverlap', 'UseForeignRegion', 'DeleteDependent', 'ReadAllRegions', 'AllowOutsideCIDR', 'DeleteStackOwned', 'SkipParentGrant', 'SkipChildGrant'}
REQUIRED_SOURCE_SCOPES['EC2NativeSubnetLifecycle'] = {'tools/console/src/ec2-native-subnets.mjs', 'internal/aws/ec2_network.go', 'internal/aws/ec2_read.go', 'tools/console/src/ec2-vpc-native-subnets.jsx', 'internal/aws/ec2_subnet_native.go', 'internal/aws/ec2_vpc.go', 'tools/console/src/ec2-readonly.jsx', 'internal/server/web/app.js', 'tools/console/src/main.jsx', 'internal/server/server.go'}
BASE_MODELS.add('EC2TemplatePreviewEpoch')
BASE_INVARIANTS['EC2TemplatePreviewEpoch'] = {'TypeOK', 'OrdinaryPreview', 'BoundedDraft', 'FixedDeclarationRead', 'OnlyCurrentIdentity', 'OnlyCurrentRegion', 'OnlyCurrentDraft', 'NoClosedOrHandoffResult', 'EvidencePreserved', 'NoReturnedValues'}
REQUIRED_MUTANTS['EC2TemplatePreviewEpoch'] = {'AdmitClosed', 'AdmitHandoff', 'OversizeDraft', 'AllowMutation', 'AllowTemplateURL', 'AcceptStaleIdentity', 'AcceptStaleRegion', 'AcceptStaleDraft', 'AcceptAfterClose', 'AcceptAfterHandoff', 'RetainOldDraft', 'RefusalAsAccepted', 'InvalidAsAccepted', 'MalformedAsAccepted', 'ShowReturnedValue'}
REQUIRED_SOURCE_SCOPES['EC2TemplatePreviewEpoch'] = {'internal/server/web/app.js', 'tools/console/src/ec2-readonly.jsx', 'tools/console/src/ec2-template-preview.jsx', 'tools/console/src/ec2-template-preview.mjs', 'tools/console/src/main.jsx'}
BASE_MODELS.add('EC2RouteTableConsoleEpoch')
BASE_INVARIANTS['EC2RouteTableConsoleEpoch'] = {'TypeOK', 'EligibleRead', 'FixedRead', 'CurrentIdentity', 'CurrentRegion', 'CurrentTarget', 'CurrentRevision', 'CurrentGate', 'StatusIsEvidence', 'DefaultIsRecorded', 'AssociationIsExplicit', 'NoImplicitMain', 'CurrentSubnet', 'ValidSubnetRequest'}
REQUIRED_MUTANTS['EC2RouteTableConsoleEpoch'] = {'UnsignedRead', 'UnadvertisedRead', 'HandoffRead', 'NativeRead', 'UnverifiedRead', 'OfferWrongOperation', 'AcceptOldPrincipal', 'AcceptOldRegion', 'AcceptOldVPC', 'AcceptOldRevision', 'AcceptOldGate', 'KeepPreviousPrincipal', 'IncludeForeignRows', 'MaskRefusalAsEmpty', 'InventDefault', 'InventAssociation', 'InventMain', 'AcceptOldSubnet', 'QueryIncompleteSubnet', 'MaskNonmatchingSubnetResponse'}
REQUIRED_SOURCE_SCOPES['EC2RouteTableConsoleEpoch'] = {'internal/server/server.go', 'internal/server/web/app.js', 'tools/console/src/ec2-readonly.jsx', 'tools/console/src/ec2-response.mjs', 'tools/console/src/ec2-route-tables.mjs', 'tools/console/src/ec2-vpc-route-tables.jsx', 'tools/console/src/main.jsx'}
BASE_MODELS.add('EC2RouteTableRead')
BASE_INVARIANTS['EC2RouteTableRead'] = {'TypeOK', 'Admission', 'BoundSnapshot', 'SameVPC', 'TypedDefault', 'ExplicitOnly', 'CurrentDefault', 'FilterMatchesExplicit'}
REQUIRED_MUTANTS['EC2RouteTableRead'] = {'AllowUnknownAction', 'SkipIAM', 'AllowInstanceScope', 'SkipRegion', 'DropDeclaration', 'DropOwnership', 'DropStoredRows', 'AllowLossyXML', 'CrossVPCSubnet', 'CrossVPCGateway', 'InventAssociation', 'InventMain', 'ShowRemovedRoute', 'IgnoreMalformedFlag', 'AdmitMalformedSelector', 'ReturnUnmatchedTable', 'ReturnImplicitSubnetTable'}
REQUIRED_SOURCE_SCOPES['EC2RouteTableRead'] = {'internal/aws/ec2_read.go', 'internal/aws/ec2_route_tables_read.go', 'internal/aws/ec2_vpc.go'}

BASE_MODELS.add('EC2SecurityGroupRead')
BASE_INVARIANTS['EC2SecurityGroupRead'] = {'OperationAdmission', 'CanonicalGroup', 'TypeOK', 'AuthorizedRegionalRead', 'CurrentIngress', 'FaithfulXML', 'CanonicalParent'}
REQUIRED_MUTANTS['EC2SecurityGroupRead'] = {'AllowInstanceHandoff', 'MissingParentDeclaration', 'AllowUnknownOperation', 'MissingAuthorization', 'MissingParentRow', 'MissingRegion', 'MissingGroupBinding', 'MissingParentClaim', 'AllowLossyXML', 'UseStaleTemplateIngress'}
REQUIRED_SOURCE_SCOPES['EC2SecurityGroupRead'] = {'internal/aws/ec2_read.go', 'internal/aws/ec2_security_groups_read.go', 'internal/aws/ec2_vpc.go'}
BASE_MODELS.add('EC2ReadDiscovery')
BASE_INVARIANTS['EC2ReadDiscovery'] = {'TypeOK', 'AuthorizedRead', 'RegionBound', 'OwnedRead'}
REQUIRED_MUTANTS['EC2ReadDiscovery'] = {'MissingAllow', 'MissingDeny', 'MissingRegion', 'MissingOwner'}
REQUIRED_SOURCE_SCOPES['EC2ReadDiscovery'] = {'internal/aws/ec2_read.go', 'internal/aws/ssm.go', 'internal/server/server.go'}

# Independent Lambda execution-role bridge decision source. These fixed sets
# must not be derived from the mutable registry, which this gate validates.
BASE_MODELS.add('LambdaSourceEdit')
BASE_INVARIANTS['LambdaSourceEdit'] = {'PreservedPackage', 'AuthorizedWrite', 'TypeOK', 'FreshWrite', 'ValidWrite', 'AuthorizedRead'}
REQUIRED_MUTANTS['LambdaSourceEdit'] = {'ReadWithoutGrant', 'InvalidWrite', 'StaleWrite', 'LoseOtherEntries', 'WriteWithoutGrant'}
REQUIRED_SOURCE_SCOPES['LambdaSourceEdit'] = {'internal/aws/lambda_source.go'}

BASE_MODELS.add('LambdaExecutionRoleBridge')
BASE_INVARIANTS['LambdaExecutionRoleBridge'] = {'TypeOK', 'NoCallerEscalation', 'NoCallerNarrowing', 'CanceledNotDispatched'}
REQUIRED_MUTANTS['LambdaExecutionRoleBridge'] = {'InheritCallerSession', 'IntersectCallerSession', 'DispatchAfterCancel'}
REQUIRED_SOURCE_SCOPES['LambdaExecutionRoleBridge'] = {'internal/aws/lambda_bridge.go', 'internal/aws/lambda_bridge_test.go', 'internal/aws/lambda_bridge_cancel_test.go'}

class GateError(ValueError):
    pass



def digest(data):
    return hashlib.sha256(data).hexdigest()


def require(condition, message):
    if not condition:
        raise GateError(message)


def load_json(data):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            require(key not in result, 'duplicate JSON field')
            result[key] = value
        return result
    def reject_constant(value):
        raise GateError('non-finite JSON constant: ' + value)
    return json.loads(data, object_pairs_hook=unique, parse_constant=reject_constant)


def asset(root, name):
    require(isinstance(name, str) and name and not Path(name).is_absolute(), 'asset path must be relative')
    path = root / name
    require(path.resolve().is_relative_to(root.resolve()) and not path.is_symlink(), 'asset escapes repository')
    require(path.is_file() and path.stat().st_size > 0, 'missing or empty asset: ' + name)
    return path


def invariants(config, model_name=None):
    # Deliberately small CFG grammar: named invariants, init/next/specification,
    # Boolean deadlock setting and finite literal constant assignments only.
    # Views/constraints/substitutions cannot silently become fake invariants or
    # prune checked behavior. The one explicit Azure binding below supplies a
    # trusted finite owner function, never an invariant/operator constraint.
    # Registry metadata cannot grant additional substitution permissions.
    names, constants = [], False
    owner_binding_seen = False
    literal = r'(?:[A-Za-z][A-Za-z0-9_]*|[0-9]+|"(?:[^"\\]|\\.)*")'
    value = rf'(?:{literal}|\{{(?:\s*{literal}(?:\s*,\s*{literal})*\s*)?\}})'
    assignment = re.compile(rf'^[A-Za-z][A-Za-z0-9_]*\s*=\s*{value}$')
    def constant_assignment(text):
        nonlocal owner_binding_seen
        if assignment.fullmatch(text):
            return True
        if model_name == 'AzureBlobState' and re.fullmatch(r'Owners\s*<-\s*AccountOwners', text) and not owner_binding_seen:
            owner_binding_seen = True
            return True
        return False
    for raw in config.splitlines():
        line = raw.split('\\*', 1)[0].strip()
        if not line:
            continue
        words = line.split()
        if words[0] in ('INVARIANT', 'INVARIANTS'):
            require(len(words) > 1 and all(IDENTIFIER.fullmatch(n) and n not in CFG_KEYWORDS for n in words[1:]), 'invalid named invariants')
            names.extend(words[1:])
            constants = False
        elif words[0] in ('INIT', 'NEXT', 'SPECIFICATION'):
            require(len(words) == 2 and IDENTIFIER.fullmatch(words[1]), 'invalid model behavior declaration')
            constants = False
        elif words[0] == 'CHECK_DEADLOCK':
            require(len(words) == 2 and words[1] in ('TRUE', 'FALSE'), 'invalid deadlock setting')
            constants = False
        elif words[0] in ('CONSTANT', 'CONSTANTS'):
            constants = True
            remainder = line[len(words[0]):].strip()
            require(not remainder or constant_assignment(remainder), 'unsupported constant declaration')
        else:
            require(constants and constant_assignment(line), 'unsupported CFG directive or continuation: ' + words[0])
    require(model_name != 'AzureBlobState' or owner_binding_seen, 'Azure finite owner binding is required')
    require(names and len(names) == len(set(names)), 'invalid or absent config invariants')
    return names



# Exact reviewed optional adapter pins, not dependency safety or refinement proof.
GUESTTCP_MODULE_PINS = {'gvisor.dev/gvisor': 'v0.0.0-20260909235104-1db01ca840f4', 'github.com/google/btree': 'v1.1.2', 'golang.org/x/exp': 'v0.0.0-20250711185948-6ae5c78190dc', 'golang.org/x/time': 'v0.15.0'}
GUESTTCP_MODULE_SUMS = {'gvisor.dev/gvisor': ['gvisor.dev/gvisor v0.0.0-20260909235104-1db01ca840f4 h1:IyKvejmukmnokiyneuLVIMiFYlnRJEy2cceShDCA/3Y=', 'gvisor.dev/gvisor v0.0.0-20260909235104-1db01ca840f4/go.mod h1:8aLQqUBHDH8fY5y60lzmwDpMMbQCcT3EBfoSwhfaGCY='], 'github.com/google/btree': ['github.com/google/btree v1.1.2 h1:xf4v41cLI2Z6FxbKm+8Bu+m8ifhj15JuZ9sa0jZCMUU=', 'github.com/google/btree v1.1.2/go.mod h1:qOPhT0dTNdNzV6Z/lhRX0YXUafgPLFUh+gZMl761Gm4='], 'golang.org/x/exp': ['golang.org/x/exp v0.0.0-20250711185948-6ae5c78190dc h1:TS73t7x3KarrNd5qAipmspBDS1rkMcgVG/fS1aRb4Rc=', 'golang.org/x/exp v0.0.0-20250711185948-6ae5c78190dc/go.mod h1:A+z0yzpGtvnG90cToK5n2tu8UJVP2XUATh+r+sfOOOc='], 'golang.org/x/time': ['golang.org/x/time v0.15.0 h1:bbrp8t3bGUeFOx08pvsMYRTCVSMk89u4tKbNOZbp88U=', 'golang.org/x/time v0.15.0/go.mod h1:Y4YMaQmXwGQZoFaVFk4YpCt4FLQMYKZe9oeV/f4MSno=']}

def guesttcp_requirements(source):
    # Deliberately conservative subset of go.mod. Quoted tokens and directives
    # outside this reviewed subset require explicit parser/boundary review.
    clean, index = [], 0
    while index < len(source):
        if source.startswith('//', index):
            end = source.find('\n', index)
            index = len(source) if end < 0 else end
        elif source.startswith('/*', index):
            raise GateError('malformed guesttcp module comment: block comments are unsupported')
        else:
            require(source[index] not in '\"`', 'unsupported quoted guesttcp module token')
            clean.append(source[index])
            index += 1
    declarations, block = {}, False
    seen_directives = set()
    for line in ''.join(clean).splitlines():
        words = line.split()
        if not words:
            continue
        if block and words == [')']:
            block = False
            continue
        if not block:
            directive = words.pop(0)
            require(directive not in ('replace', 'exclude'), 'guesttcp replacement or exclusion requires review')
            if directive in ('module', 'go', 'toolchain'):
                require(len(words) == 1 and directive not in seen_directives and not any(c in words[0] for c in '();'), 'malformed guesttcp module directive')
                seen_directives.add(directive)
                continue
            require(directive == 'require', 'unsupported guesttcp module directive')
            if words == ['(']:
                block = True
                continue
        require(len(words) == 2 and re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._~+/-]*', words[0]) and re.fullmatch(r'v[0-9][A-Za-z0-9.+-]*', words[1]), 'malformed guesttcp module requirement')
        module, version = words
        require(module not in declarations, 'duplicate guesttcp module requirement')
        declarations[module] = version
    require(not block, 'unterminated guesttcp require block')
    require({'module', 'go'} <= seen_directives, 'missing guesttcp module directive')
    return declarations


def validate_guesttcp_module_pins(root, content=None):
    module_text = content['go.mod'].decode() if content is not None else asset(root, 'go.mod').read_text()
    sum_text = content['go.sum'].decode() if content is not None else asset(root, 'go.sum').read_text()
    requirements = guesttcp_requirements(module_text)
    sums = sum_text.splitlines()
    for module, version in GUESTTCP_MODULE_PINS.items():
        require(requirements.get(module) == version, 'guesttcp reviewed module pin changed: ' + module)
        observed = [line for line in sums if line.startswith(module + ' ')]
        require(sorted(observed) == sorted(GUESTTCP_MODULE_SUMS[module]), 'guesttcp reviewed module checksum changed: ' + module)

# Additive constants from independently reviewed, hash-pinned candidate snapshots.
BATTLE_INTEGRATION_EXPECTED = {'CanonicalVMComposition': {'bounds': 'Five disjoint initial modes: one canonical launch, one '
                                      'network ownership operation, one asynchronous creator, or '
                                      'one explicit runtime configuration, or one saved typed '
                                      'provider/Host association and operation. One immutable '
                                      'identity compared with a foreign identity; one '
                                      'cancellation, restart, child, pending stack generation and '
                                      'cleanup. All finite schedules, no depth cutoff.',
                            'mutants': ['UnpinnedPlan',
                                        'ForeignStackPlan',
                                        'UnknownJournalReadback',
                                        'UnwrittenReservation',
                                        'WrongPreparedIdentity',
                                        'ForeignControl',
                                        'RunningBeforeAttachment',
                                        'ForeignAttachment',
                                        'UnknownAttachmentCommit',
                                        'ActivateAfterSeal',
                                        'ForeignAgent',
                                        'UnreadyAgent',
                                        'ForeignPublication',
                                        'LatePublication',
                                        'SkipOwnerJoin',
                                        'SkipTransportJoin',
                                        'AssumeContainment',
                                        'ReleaseBeforeContainment',
                                        'ForeignNetwork',
                                        'MutateLeasedNetwork',
                                        'UnauthorizedSeal',
                                        'ForeignDelete',
                                        'UnqualifiedChildFD',
                                        'UnboundedCreators',
                                        'LateChildPublication',
                                        'RestartRestoresAuthority',
                                        'LoseLateChildDebt',
                                        'UnsupportedPlatform',
                                        'InvalidOperatorFile',
                                        'UnpinnedRuntime',
                                        'ImplicitRuntime',
                                        'ConfiguredAdvertisesReady',
                                        'UntypedProvider',
                                        'UnreadyProvider',
                                        'ForeignProviderService',
                                        'ForeignHostOwner',
                                        'ForeignHostBinding',
                                        'ForeignHostService',
                                        'ReuseSourceNewKey',
                                        'RebindSavedService',
                                        'StartAfterRegistrySeal',
                                        'StartForeignTarget',
                                        'ReadForeignOperation',
                                        'ReadWithoutOperator',
                                        'UnknownPreparedReadback',
                                        'UnwrittenPreparedRecord'],
                            'properties': ['TypeOK',
                                           'PinnedPlan',
                                           'JournalBeforeLaunch',
                                           'PreparedIdentity',
                                           'PausedExactControl',
                                           'ExactAttachment',
                                           'AttachBeforeActivation',
                                           'PrivateAgentReadiness',
                                           'PublicationAdmission',
                                           'IndependentCleanup',
                                           'ObservedContainment',
                                           'ReleaseAfterContainment',
                                           'NetworkOwnership',
                                           'FrozenNetwork',
                                           'AuthorizedDelete',
                                           'CreatorAdmission',
                                           'NoReconstruction',
                                           'UncertainDebtRetained',
                                           'ConfigurationAdmission',
                                           'ConfiguredIsNotReady',
                                           'TypedProvider',
                                           'SavedAssociation',
                                           'NoRebinding',
                                           'SavedOperationAuthority'],
                            'source_scope': ['internal/aws/ec2_cloudformation_adapter_unix.go',
                                             'internal/aws/ec2_cloudformation_runtime_unix.go',
                                             'internal/aws/ec2_completed_guest_unix.go',
                                             'internal/aws/ec2_vm_backend_unix.go',
                                             'internal/aws/ec2_vm_nic_guard.go',
                                             'internal/aws/ec2_vm_nic_unix.go',
                                             'internal/aws/ec2_vm_qga_readiness_unix.go',
                                             'internal/workload/guestbundle/bundle.go',
                                             'internal/workload/guestbundle/embedded.go',
                                             'internal/workload/guestprocess/attempt_unix.go',
                                             'internal/workload/guestprocess/attempt_other.go',
                                             'internal/workload/guestprocess/nic_unix.go',
                                             'internal/workload/guestprocess/process.go',
                                             'internal/workload/guestprocess/process_linux.go',
                                             'internal/workload/guestprocess/process_other.go',
                                             'internal/workload/guestprocess/supervisor_linux.go',
                                             'internal/workload/guestseed/network.go',
                                             'internal/workload/guestseed/seed.go',
                                             'internal/workload/guestvm/control.go',
                                             'internal/workload/guestvm/launch.go',
                                             'internal/workload/guestvm/network.go',
                                             'internal/workload/guestvm/nic_unix.go',
                                             'internal/workload/guestvm/prepare.go',
                                             'internal/workload/guestvm/recover.go',
                                             'internal/workload/qmp/client.go',
                                             'internal/workload/qmp/protocol.go',
                                             'internal/aws/cloudformation_instance.go',
                                             'internal/aws/ec2_instance_cloudformation.go',
                                             'internal/aws/ec2_network.go',
                                             'internal/aws/ec2_network_actor.go',
                                             'internal/aws/ec2_network_cloudformation.go',
                                             'cmd/simulator/battle_runtime_unix.go',
                                             'cmd/simulator/battle_runtime_config.go',
                                             'cmd/simulator/battle_runtime_other.go',
                                             'internal/server/battle_runtime_unix.go',
                                             'internal/server/ssm_runtime_capabilities.go',
                                             'internal/server/battle_capabilities_unix.go',
                                             'cmd/simulator/main.go',
                                             'internal/aws/cloudformation.go',
                                             'internal/server/server.go',
                                             'internal/workload/guestprocess/budget_unix.go',
                                             'internal/workload/guestprocess/platform_darwin.go',
                                             'internal/workload/guestprocess/platform_linux.go',
                                             'internal/workload/guestprocess/process_darwin.go',
                                             'internal/workload/guestprocess/supervisor_darwin.go',
                                             'internal/workload/guestprocess/supervisor_entry_other.go',
                                             'internal/workload/guestvm/platform_darwin.go',
                                             'internal/workload/guestvm/platform_linux.go',
                                             'internal/workload/guestvm/platform_other.go',
                                             'cmd/simulator/battle_platform.go',
                                             'internal/server/sakura_provider.go']},
 'IAMConditionEvaluation': {'bounds': '15 immutable finite input witnesses, '
                                      'parse/context/match/resolve phases, two context keys, '
                                      'literal/variable/glob representatives, matching explicit '
                                      'Deny and legacy nil-context Deny case. No request loop.',
                            'mutants': ['AdmitOperator',
                                        'AdmitNull',
                                        'AdmitDuplicate',
                                        'AdmitVariable',
                                        'MissingContextAccepted',
                                        'MissingContextDropsDeny',
                                        'RequestContextOverride',
                                        'VariableExpandedAsWildcard',
                                        'EqualsBecomesGlob',
                                        'DenyLosesPrecedence',
                                        'LikeDisabled',
                                        'MissingClauseDropped'],
                            'properties': ['TypeOK',
                                           'StrictAdmission',
                                           'TrustedContextRequired',
                                           'AbsentContextDenyNotDiscarded',
                                           'MissingOrDuplicateKeyRefuses',
                                           'AllTermsRequired',
                                           'ProviderContextOnly',
                                           'VariableIsLiteral',
                                           'EqualityNotGlob',
                                           'ExplicitDenyPrecedence',
                                           'AllowedWitness'],
                            'source_scope': ['internal/core/policy_conditions.go',
                                             'internal/core/policy.go']},
 'InstanceMetadataTokens': {'bounds': 'One instance-owned token slot (abstracts capacity eight), '
                                      'one request, one foreign token, time 0..2, lifetime2, '
                                      'TTL1or2. Every finite interleaving; no depth cutoff. '
                                      'Issue/read checks linearize at request admission, not '
                                      'response emission.',
                            'mutants': ['ForeignToken',
                                        'ExpiredToken',
                                        'CredentialEndpoint',
                                        'WrongReadMethod',
                                        'WrongTokenMethod',
                                        'InvalidTTL',
                                        'ForwardedToken',
                                        'RandomFailure',
                                        'IssueAfterClose',
                                        'ReadAfterClose',
                                        'UnboundedExpiry'],
                            'properties': ['TypeOK',
                                           'TokenAdmission',
                                           'InstanceBoundRead',
                                           'NoCredentials',
                                           'BoundedExpiry'],
                            'source_scope': ['internal/aws/ec2_metadata_service_unix.go',
                                             'internal/aws/ec2_instance_alias.go']},
 'NativeCommandIssuer': {'bounds': 'Three disjoint finite scenarios: one parent '
                                   'intent/token/worker with two delivery opportunities and a '
                                   'single crash/restart, two lease incarnations and three reply '
                                   'classes; three immutable policy revisions for document+target; '
                                   'one two-item page with one append or binding/expiry/record '
                                   'change.',
                         'mutants': ['IgnoreOwnerSeal',
                                     'IgnoreLeaseIncarnation',
                                     'OrphanWorkerRestart',
                                     'InventSuccessFromLostReply',
                                     'InventSuccessFromPending',
                                     'MixedPolicyRevisions',
                                     'ReserveAfterEffect',
                                     'DuplicateGuestEffect',
                                     'PageRole',
                                     'PageRegion',
                                     'PageFilter',
                                     'PageSize',
                                     'PageExpiry',
                                     'PageRetainedRequest',
                                     'PageUsesLiveItems'],
                         'properties': ['TypeOK',
                                        'ReservationBeforeEffect',
                                        'AtMostOneEffect',
                                        'CurrentOwnerFence',
                                        'RestartNoWorker',
                                        'WorkerRequiresReservation',
                                        'NoReplayAfterRestart',
                                        'KnownExitOnly',
                                        'NoCleanupFromLostReply',
                                        'JoinAfterPublication',
                                        'SinglePolicySnapshot',
                                        'PageScope',
                                        'PageRecordRevalidated',
                                        'PageSnapshotImmutable'],
                         'source_scope': ['internal/aws/ec2_command_backend_unix.go',
                                          'internal/aws/ssm_commands_native.go',
                                          'internal/aws/ssm_commands_native_state.go',
                                          'internal/aws/ssm_commands_native_wire.go']},
 'OwnedClockQualification': {'bounds': 'One owned process and pending sample, two process '
                                       'incarnations, logical time0..5, expiry4 and original '
                                       'deadline3; Boolean namespace/FD/platform/cryptographic '
                                       'witnesses; one retained command.',
                             'mutants': ['ArbitraryHTTPProof',
                                         'ForeignNamespace',
                                         'UncheckedInheritedFD',
                                         'UnsupportedPlatform',
                                         'SampleWrongIncarnation',
                                         'WrongSampleMAC',
                                         'ReplayedSampleNonce',
                                         'ForeignSampleDomain',
                                         'UnboundedSample',
                                         'ExpiredSample',
                                         'LateSampleAfterStop',
                                         'WrongResponseMAC',
                                         'ReplayedResponseNonce',
                                         'ForeignResponseDomain',
                                         'ChangedResponseRequest',
                                         'StopKeepsQualification',
                                         'V1WallAccepted',
                                         'StartAfterOwnerRotation',
                                         'UnqualifiedStart',
                                         'RenewAtReceipt',
                                         'RoundDeadlineLater',
                                         'ExpiredReadBlocked',
                                         'ReadAfterStop',
                                         'ReadAfterRotation'],
                             'properties': ['TypeOK',
                                            'OwnedProof',
                                            'QualifiedOnlyCurrent',
                                            'AuthenticatedResponses',
                                            'StopInvalidates',
                                            'V2Admission',
                                            'ConservativeOriginalDeadline',
                                            'RetainedReadAfterExpiry',
                                            'ReadRequiresCurrentOwner'],
                             'source_scope': ['internal/workload/probeclock/bootstrap_linux.go',
                             'internal/workload/probeclock/bootstrap_unix.go',
                             'internal/workload/probeclock/bootstrap_darwin.go',
                             'internal/workload/probeclock/clock_darwin.go',
                                              'internal/workload/probeclock/bootstrap_other.go',
                                              'internal/workload/probeclock/clock.go',
                                              'internal/workload/probeclock/clock_linux.go',
                                              'internal/workload/probeclock/clock_other.go',
                                              'internal/workload/probeclock/http.go',
                                              'internal/workload/guesttcp/http_probe_command_v2.go',
                                              'cmd/simulator/main.go']},
 'ParticipantSessionLifecycle': {'bounds': 'One session with two driver generations, one Resume '
                                           'per generation, two-valued identity/role/target/origin '
                                           'witnesses, one-use expiring tokens, three '
                                           'independently draining guest tasks, Boolean reviewed '
                                           'image pin/private channel/explicit opt-in and '
                                           'independent exclusive overlay packaging verdicts. No '
                                           'pruning constraints or fairness.',
                                 'mutants': ['MissingImagePin',
                                             'QGAFallback',
                                             'ImplicitSessionOptIn',
                                             'UnverifiedOverlay',
                                             'OverwriteOverlay',
                                             'StaleChannelToken',
                                             'ExpiredHandoff',
                                             'OperatorAdmission',
                                             'CredentialRebind',
                                             'RoleGenerationRebind',
                                             'TargetRebind',
                                             'CrossOriginChannel',
                                             'RevokedAdmission',
                                             'ChannelReplay',
                                             'ExpiredChannel',
                                             'HandoffReplay',
                                             'WrongHello',
                                             'ReadyAfterClose',
                                             'PublishAfterRevoke',
                                             'ResumeNewDriver',
                                             'ResumeAfterShutdown',
                                             'ExitBeforeDrain',
                                             'ExitOnUncertainClose',
                                             'TerminalBeforeJoin',
                                             'FabricatedExit',
                                             'FreshBeforeJoin'],
                                 'properties': ['TypeOK',
                                                'PreparedEvidence',
                                                'PreparationOptIn',
                                                'PackageEvidence',
                                                'BoundAdmission',
                                                'CurrentAdmission',
                                                'ChannelTokenBinding',
                                                'HandoffExpiry',
                                                'SingleUse',
                                                'HelloEvidence',
                                                'ReadyEvidence',
                                                'NoPostRevokeOutput',
                                                'ResumeKeepsDriver',
                                                'NoResurrection',
                                                'GuestDrainEvidence',
                                                'TerminalEvidence',
                                                'FreshEvidence',
                                                'RevokeIsNotExit'],
                                 'source_scope': ['guest/ssm-relay/io_linux.go',
                                                  'guest/ssm-relay/main_linux.go',
                                                  'guest/ssm-relay/tenkacloud-session-relay.service',
                                                  'internal/aws/ec2_session_backend_unix.go',
                                                  'internal/aws/ssm_session_auth.go',
                                                  'internal/aws/ssm_sessions.go',
                                                  'internal/aws/ssm_sessions_native.go',
                                                  'internal/aws/ssm_sessions_shutdown.go',
                                                  'internal/aws/ssm_sessions_state.go',
                                                  'internal/aws/sts_terminal_binding.go',
                                                  'internal/server/terminal_channel.go',
                                                  'internal/server/terminal_handoff.go',
                                                  'internal/server/terminal_json.go',
                                                  'internal/workload/guestsession/driver.go',
                                                  'internal/workload/guestsession/protocol.go',
                                                  'internal/workload/guestvm/session.go',
                                                  'internal/workload/guestvm/platform_darwin.go',
                                                  'internal/workload/guestvm/platform_linux.go',
                                                  'internal/workload/guestvm/platform_other.go',
                                                  'scripts/guest-image/session-overlay.py',
                                                  'internal/aws/ssm.go',
                                                  'internal/aws/sts_signature.go',
                                                  'internal/server/access.go',
                                                  'internal/server/server.go']},
 'TerminalClientEpoch': {'bounds': 'Two sequential mounted components, at most two explicitly '
                                   'invoked native operations, one retained pending response and '
                                   'one current channel with stale-callback input. No fairness or '
                                   'state pruning. Remote result and local disposition '
                                   'independent.',
                         'mutants': ['LateNativeReceipt',
                                     'ReplacementReceipt',
                                     'StaleSocketCallback',
                                     'OpenTokenReplay',
                                     'UnmountedUI',
                                     'ReplacementUI',
                                     'DisposeFabricatesTermination',
                                     'AmbiguousStartRetry',
                                     'DiscardClearsKnownSession'],
                         'properties': ['TypeOK',
                                        'ReceiptEvidence',
                                        'SocketEvidence',
                                        'SingleOpen',
                                        'UIEvidence',
                                        'DisposeNotRemoteTermination',
                                        'NoAutomaticStartRetry',
                                        'DiscardPreservesIdentity'],
                         'source_scope': ['tools/console/src/ssm-terminal-client.mjs',
                                          'tools/console/src/ssm-terminal.jsx',
                                          'tools/console/src/session-manager.jsx',
                                          'tools/console/src/main.jsx',
                                          'internal/server/web/app.js']}}
BATTLE_INTEGRATION_CONFIGS = {'CanonicalVMComposition': 'SPECIFICATION Spec\n'
                           'CHECK_DEADLOCK FALSE\n'
                           'INVARIANTS TypeOK PinnedPlan JournalBeforeLaunch PreparedIdentity '
                           'PausedExactControl ExactAttachment AttachBeforeActivation '
                           'PrivateAgentReadiness PublicationAdmission IndependentCleanup '
                           'ObservedContainment ReleaseAfterContainment NetworkOwnership '
                           'FrozenNetwork AuthorizedDelete CreatorAdmission NoReconstruction '
                           'UncertainDebtRetained ConfigurationAdmission ConfiguredIsNotReady '
                           'TypedProvider SavedAssociation NoRebinding SavedOperationAuthority\n',
 'IAMConditionEvaluation': 'SPECIFICATION Spec\n'
                           'CHECK_DEADLOCK FALSE\n'
                           'INVARIANT TypeOK\n'
                           'INVARIANT StrictAdmission\n'
                           'INVARIANT TrustedContextRequired\n'
                           'INVARIANT AbsentContextDenyNotDiscarded\n'
                           'INVARIANT MissingOrDuplicateKeyRefuses\n'
                           'INVARIANT AllTermsRequired\n'
                           'INVARIANT ProviderContextOnly\n'
                           'INVARIANT VariableIsLiteral\n'
                           'INVARIANT EqualityNotGlob\n'
                           'INVARIANT ExplicitDenyPrecedence\n'
                           'INVARIANT AllowedWitness\n',
 'InstanceMetadataTokens': 'SPECIFICATION Spec\n'
                           'CHECK_DEADLOCK FALSE\n'
                           'INVARIANTS TypeOK TokenAdmission InstanceBoundRead NoCredentials '
                           'BoundedExpiry\n',
 'NativeCommandIssuer': 'SPECIFICATION Spec\n'
                        'CHECK_DEADLOCK FALSE\n'
                        'INVARIANT TypeOK\n'
                        'INVARIANT ReservationBeforeEffect\n'
                        'INVARIANT AtMostOneEffect\n'
                        'INVARIANT CurrentOwnerFence\n'
                        'INVARIANT RestartNoWorker\n'
                        'INVARIANT WorkerRequiresReservation\n'
                        'INVARIANT NoReplayAfterRestart\n'
                        'INVARIANT KnownExitOnly\n'
                        'INVARIANT NoCleanupFromLostReply\n'
                        'INVARIANT JoinAfterPublication\n'
                        'INVARIANT SinglePolicySnapshot\n'
                        'INVARIANT PageScope\n'
                        'INVARIANT PageRecordRevalidated\n'
                        'INVARIANT PageSnapshotImmutable\n',
 'OwnedClockQualification': 'INIT Init\n'
                            'NEXT Next\n'
                            'CHECK_DEADLOCK FALSE\n'
                            'INVARIANT TypeOK\n'
                            'INVARIANT OwnedProof\n'
                            'INVARIANT QualifiedOnlyCurrent\n'
                            'INVARIANT AuthenticatedResponses\n'
                            'INVARIANT StopInvalidates\n'
                            'INVARIANT V2Admission\n'
                            'INVARIANT ConservativeOriginalDeadline\n'
                            'INVARIANT RetainedReadAfterExpiry\n'
                            'INVARIANT ReadRequiresCurrentOwner\n',
 'ParticipantSessionLifecycle': 'INIT Init\n'
                                'NEXT Next\n'
                                'CHECK_DEADLOCK FALSE\n'
                                'INVARIANT TypeOK\n'
                                'INVARIANT PreparedEvidence\n'
                                'INVARIANT PreparationOptIn\n'
                                'INVARIANT PackageEvidence\n'
                                'INVARIANT BoundAdmission\n'
                                'INVARIANT CurrentAdmission\n'
                                'INVARIANT ChannelTokenBinding\n'
                                'INVARIANT HandoffExpiry\n'
                                'INVARIANT SingleUse\n'
                                'INVARIANT HelloEvidence\n'
                                'INVARIANT ReadyEvidence\n'
                                'INVARIANT NoPostRevokeOutput\n'
                                'INVARIANT ResumeKeepsDriver\n'
                                'INVARIANT NoResurrection\n'
                                'INVARIANT GuestDrainEvidence\n'
                                'INVARIANT TerminalEvidence\n'
                                'INVARIANT FreshEvidence\n'
                                'INVARIANT RevokeIsNotExit\n',
 'TerminalClientEpoch': 'INIT Init\n'
                        'NEXT Next\n'
                        'CHECK_DEADLOCK FALSE\n'
                        'INVARIANT TypeOK\n'
                        'INVARIANT ReceiptEvidence\n'
                        'INVARIANT SocketEvidence\n'
                        'INVARIANT SingleOpen\n'
                        'INVARIANT UIEvidence\n'
                        'INVARIANT DisposeNotRemoteTermination\n'
                        'INVARIANT NoAutomaticStartRetry\n'
                        'INVARIANT DiscardPreservesIdentity\n'}
for _name, _pins in BATTLE_INTEGRATION_EXPECTED.items():
    BASE_MODELS.add(_name)
    BASE_INVARIANTS[_name] = set(_pins['properties'])
    REQUIRED_MUTANTS[_name] = set(_pins['mutants'])
    REQUIRED_SOURCE_SCOPES[_name] = set(_pins['source_scope'])

BATTLE_INTEGRATION_MUTATIONS = {'CanonicalVMComposition': [{'edits': [{'count': 1,
                                        'new': '/\\ sameStack /\\ Valid',
                                        'old': '/\\ pinned /\\ sameStack /\\ Valid'}],
                             'expect_invariant': 'PinnedPlan',
                             'id': 'UnpinnedPlan'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ pinned /\\ Valid',
                                        'old': '/\\ pinned /\\ sameStack /\\ Valid'}],
                             'expect_invariant': 'PinnedPlan',
                             'id': 'ForeignStackPlan'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ Valid /\\ intentWrite',
                                        'old': '/\\ Valid /\\ intentWrite /\\ intentReadback'}],
                             'expect_invariant': 'JournalBeforeLaunch',
                             'id': 'UnknownJournalReadback'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ Valid /\\ intentReadback',
                                        'old': '/\\ Valid /\\ intentWrite /\\ intentReadback'}],
                             'expect_invariant': 'JournalBeforeLaunch',
                             'id': 'UnwrittenReservation'},
                            {'edits': [{'count': 1,
                                        'new': 's.phase="reserved" /\\ Valid',
                                        'old': 's.phase="reserved" /\\ exact /\\ Valid'}],
                             'expect_invariant': 'PreparedIdentity',
                             'id': 'WrongPreparedIdentity'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ stopped /\\ Valid',
                                        'old': '/\\ exact /\\ stopped /\\ Valid'}],
                             'expect_invariant': 'PausedExactControl',
                             'id': 'ForeignControl'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ exact /\\ Valid',
                                        'old': '/\\ exact /\\ stopped /\\ Valid'}],
                             'expect_invariant': 'PausedExactControl',
                             'id': 'RunningBeforeAttachment'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ ack /\\ Valid',
                                        'old': '/\\ exact /\\ ack /\\ Valid'}],
                             'expect_invariant': 'ExactAttachment',
                             'id': 'ForeignAttachment'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ exact /\\ Valid',
                                        'old': '/\\ exact /\\ ack /\\ Valid'}],
                             'expect_invariant': 'ExactAttachment',
                             'id': 'UnknownAttachmentCommit'},
                            {'edits': [{'count': 1,
                                        'new': 's.phase="attached" /\\ s.live',
                                        'old': 's.phase="attached" /\\ Valid'}],
                             'expect_invariant': 'AttachBeforeActivation',
                             'id': 'ActivateAfterSeal'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ ready /\\ Valid',
                                        'old': '/\\ exact /\\ ready /\\ Valid'}],
                             'expect_invariant': 'PrivateAgentReadiness',
                             'id': 'ForeignAgent'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ exact /\\ Valid',
                                        'old': '/\\ exact /\\ ready /\\ Valid'}],
                             'expect_invariant': 'PrivateAgentReadiness',
                             'id': 'UnreadyAgent'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ s.pending /\\ s.qga /\\ Valid',
                                        'old': '/\\ s.pending /\\ s.qga /\\ sameGeneration /\\ '
                                               'Valid'}],
                             'expect_invariant': 'PublicationAdmission',
                             'id': 'ForeignPublication'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ s.pending /\\ s.qga /\\ sameGeneration /\\ '
                                               's.live',
                                        'old': '/\\ s.pending /\\ s.qga /\\ sameGeneration /\\ '
                                               'Valid'}],
                             'expect_invariant': 'PublicationAdmission',
                             'id': 'LatePublication'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ s.sealed /\\ s.captured /\\ s.transportJoin',
                                        'old': '/\\ s.sealed /\\ s.captured /\\ s.ownerJoin /\\ '
                                               's.transportJoin'}],
                             'expect_invariant': 'IndependentCleanup',
                             'id': 'SkipOwnerJoin'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ s.sealed /\\ s.captured /\\ s.ownerJoin',
                                        'old': '/\\ s.sealed /\\ s.captured /\\ s.ownerJoin /\\ '
                                               's.transportJoin'}],
                             'expect_invariant': 'IndependentCleanup',
                             'id': 'SkipTransportJoin'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ s.live /\\ exact /\\ ~s.contained',
                                        'old': '/\\ s.live /\\ exact /\\ observed /\\ '
                                               '~s.contained'}],
                             'expect_invariant': 'ObservedContainment',
                             'id': 'AssumeContainment'},
                            {'edits': [{'count': 1,
                                        'new': 's.mode="launch" /\\ s.localDrained /\\ s.live /\\ '
                                               '~s.released',
                                        'old': 's.mode="launch" /\\ s.contained /\\ s.live /\\ '
                                               '~s.released'}],
                             'expect_invariant': 'ReleaseAfterContainment',
                             'id': 'ReleaseBeforeContainment'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ valid',
                                        'old': '/\\ same /\\ valid'}],
                             'expect_invariant': 'NetworkOwnership',
                             'id': 'ForeignNetwork'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ ~s.mutated',
                                        'old': '/\\ ~s.mutated /\\ (~same \\/ ~s.held)'}],
                             'expect_invariant': 'FrozenNetwork',
                             'id': 'MutateLeasedNetwork'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ same /\\ ~s.sealed',
                                        'old': '/\\ authorized /\\ same /\\ ~s.sealed'}],
                             'expect_invariant': 'AuthorizedDelete',
                             'id': 'UnauthorizedSeal'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ authorized /\\ ~s.sealed',
                                        'old': '/\\ authorized /\\ same /\\ ~s.sealed'}],
                             'expect_invariant': 'AuthorizedDelete',
                             'id': 'ForeignDelete'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ Valid /\\ budget',
                                        'old': '/\\ Valid /\\ validFD /\\ budget'}],
                             'expect_invariant': 'CreatorAdmission',
                             'id': 'UnqualifiedChildFD'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ Valid /\\ validFD',
                                        'old': '/\\ Valid /\\ validFD /\\ budget'}],
                             'expect_invariant': 'CreatorAdmission',
                             'id': 'UnboundedCreators'},
                            {'edits': [{'count': 1,
                                        'new': 's.phase="returned" /\\ s.live',
                                        'old': 's.phase="returned" /\\ Valid'}],
                             'expect_invariant': 'PublicationAdmission',
                             'id': 'LateChildPublication'},
                            {'edits': [{'count': 1,
                                        'new': '!.restarted=TRUE, !.live=TRUE',
                                        'old': '!.restarted=TRUE, !.live=FALSE'}],
                             'expect_invariant': 'NoReconstruction',
                             'id': 'RestartRestoresAuthority'},
                            {'edits': [{'count': 1,
                                        'new': '!.phase="returned", !.debt=FALSE, !.captured=TRUE',
                                        'old': '!.phase="returned", !.captured=TRUE'}],
                             'expect_invariant': 'UncertainDebtRetained',
                             'id': 'LoseLateChildDebt'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ TRUE /\\ strictFile /\\ pin /\\ explicit',
                                        'old': '/\\ platform /\\ strictFile /\\ pin /\\ explicit'}],
                             'expect_invariant': 'ConfigurationAdmission',
                             'id': 'UnsupportedPlatform'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ platform /\\ TRUE /\\ pin /\\ explicit',
                                        'old': '/\\ platform /\\ strictFile /\\ pin /\\ explicit'}],
                             'expect_invariant': 'ConfigurationAdmission',
                             'id': 'InvalidOperatorFile'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ platform /\\ strictFile /\\ TRUE /\\ explicit',
                                        'old': '/\\ platform /\\ strictFile /\\ pin /\\ explicit'}],
                             'expect_invariant': 'ConfigurationAdmission',
                             'id': 'UnpinnedRuntime'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ platform /\\ strictFile /\\ pin /\\ TRUE',
                                        'old': '/\\ platform /\\ strictFile /\\ pin /\\ explicit'}],
                             'expect_invariant': 'ConfigurationAdmission',
                             'id': 'ImplicitRuntime'},
                            {'edits': [{'count': 1,
                                        'new': '!.advertised=TRUE, !.published=TRUE]',
                                        'old': '!.advertised=TRUE]'}],
                             'expect_invariant': 'ConfiguredIsNotReady',
                             'id': 'ConfiguredAdvertisesReady'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ TRUE /\\ ready /\\ services /\\ Valid',
                                        'old': '/\\ typed /\\ ready /\\ services /\\ Valid'}],
                             'expect_invariant': 'TypedProvider',
                             'id': 'UntypedProvider'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ typed /\\ TRUE /\\ services /\\ Valid',
                                        'old': '/\\ typed /\\ ready /\\ services /\\ Valid'}],
                             'expect_invariant': 'TypedProvider',
                             'id': 'UnreadyProvider'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ typed /\\ ready /\\ TRUE /\\ Valid',
                                        'old': '/\\ typed /\\ ready /\\ services /\\ Valid'}],
                             'expect_invariant': 'TypedProvider',
                             'id': 'ForeignProviderService'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ TRUE /\\ exactBinding /\\ exactService',
                                        'old': '/\\ exactOwner /\\ exactBinding /\\ exactService'}],
                             'expect_invariant': 'SavedAssociation',
                             'id': 'ForeignHostOwner'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ exactOwner /\\ TRUE /\\ exactService',
                                        'old': '/\\ exactOwner /\\ exactBinding /\\ exactService'}],
                             'expect_invariant': 'SavedAssociation',
                             'id': 'ForeignHostBinding'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ exactOwner /\\ exactBinding /\\ TRUE',
                                        'old': '/\\ exactOwner /\\ exactBinding /\\ exactService'}],
                             'expect_invariant': 'SavedAssociation',
                             'id': 'ForeignHostService'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ sameService /\\ s.phase="new"',
                                        'old': '/\\ sameKey /\\ sameService /\\ s.phase="new"'}],
                             'expect_invariant': 'NoRebinding',
                             'id': 'ReuseSourceNewKey'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ sameKey /\\ s.phase="new"',
                                        'old': '/\\ sameKey /\\ sameService /\\ s.phase="new"'}],
                             'expect_invariant': 'NoRebinding',
                             'id': 'RebindSavedService'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ ~s.operation /\\ operator /\\ savedBinding /\\ '
                                               's.live',
                                        'old': '/\\ ~s.operation /\\ operator /\\ savedBinding /\\ '
                                               'Valid'}],
                             'expect_invariant': 'SavedOperationAuthority',
                             'id': 'StartAfterRegistrySeal'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ ~s.operation /\\ operator /\\ Valid',
                                        'old': '/\\ ~s.operation /\\ operator /\\ savedBinding /\\ '
                                               'Valid'}],
                             'expect_invariant': 'SavedOperationAuthority',
                             'id': 'StartForeignTarget'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ ~s.observed /\\ operator',
                                        'old': '/\\ ~s.observed /\\ operator /\\ savedBinding'}],
                             'expect_invariant': 'SavedOperationAuthority',
                             'id': 'ReadForeignOperation'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ ~s.observed /\\ savedBinding',
                                        'old': '/\\ ~s.observed /\\ operator /\\ savedBinding'}],
                             'expect_invariant': 'SavedOperationAuthority',
                             'id': 'ReadWithoutOperator'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ Valid /\\ write',
                                        'old': '/\\ Valid /\\ write /\\ readback'}],
                             'expect_invariant': 'JournalBeforeLaunch',
                             'id': 'UnknownPreparedReadback'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ Valid /\\ readback',
                                        'old': '/\\ Valid /\\ write /\\ readback'}],
                             'expect_invariant': 'JournalBeforeLaunch',
                             'id': 'UnwrittenPreparedRecord'}],
 'IAMConditionEvaluation': [{'edits': [{'count': 1,
                                        'new': 'ParseOK(i) == i.defect \\in {"none","operator"}',
                                        'old': 'ParseOK(i) == i.defect="none"'}],
                             'expect_invariant': 'StrictAdmission',
                             'id': 'AdmitOperator'},
                            {'edits': [{'count': 1,
                                        'new': 'ParseOK(i) == i.defect \\in {"none","null"}',
                                        'old': 'ParseOK(i) == i.defect="none"'}],
                             'expect_invariant': 'StrictAdmission',
                             'id': 'AdmitNull'},
                            {'edits': [{'count': 1,
                                        'new': 'ParseOK(i) == i.defect \\in {"none","duplicate"}',
                                        'old': 'ParseOK(i) == i.defect="none"'}],
                             'expect_invariant': 'StrictAdmission',
                             'id': 'AdmitDuplicate'},
                            {'edits': [{'count': 1,
                                        'new': 'ParseOK(i) == i.defect \\in {"none","variable"}',
                                        'old': 'ParseOK(i) == i.defect="none"'}],
                             'expect_invariant': 'StrictAdmission',
                             'id': 'AdmitVariable'},
                            {'edits': [{'count': 1,
                                        'new': 'ContextOK(i) == i.trusted \\/ ~i.deny',
                                        'old': 'ContextOK(i) == i.trusted'}],
                             'expect_invariant': 'TrustedContextRequired',
                             'id': 'MissingContextAccepted'},
                            {'edits': [{'count': 1,
                                        'new': 'ContextOK(i) == i.trusted \\/ i.deny',
                                        'old': 'ContextOK(i) == i.trusted'}],
                             'expect_invariant': 'AbsentContextDenyNotDiscarded',
                             'id': 'MissingContextDropsDeny'},
                            {'edits': [{'count': 1,
                                        'new': 'BoundTeam(i) == IF i.team="b" THEN "a" ELSE i.team',
                                        'old': 'BoundTeam(i) == i.team'}],
                             'expect_invariant': 'AllTermsRequired',
                             'id': 'RequestContextOverride'},
                            {'edits': [{'count': 1,
                                        'new': 'THEN (i.resource=i.user \\/ i.user="a*")',
                                        'old': 'THEN i.resource=i.user'}],
                             'expect_invariant': 'VariableIsLiteral',
                             'id': 'VariableExpandedAsWildcard'},
                            {'edits': [{'count': 1,
                                        'new': 'ELSE (i.resource=i.pattern \\/ i.pattern="a*")',
                                        'old': 'ELSE i.resource=i.pattern'}],
                             'expect_invariant': 'EqualityNotGlob',
                             'id': 'EqualsBecomesGlob'},
                            {'edits': [{'count': 1,
                                        'new': 'IF s.input.deny THEN "allowed"',
                                        'old': 'IF s.input.deny THEN "explicitDeny"'}],
                             'expect_invariant': 'ExplicitDenyPrecedence',
                             'id': 'DenyLosesPrecedence'},
                            {'edits': [{'count': 1,
                                        'new': 'FALSE',
                                        'old': 'i.resource \\in {"a","abc"}'}],
                             'expect_invariant': 'AllowedWitness',
                             'id': 'LikeDisabled'},
                            {'edits': [{'count': 1,
                                        'new': '(KeyOK(i) /\\ BoundTeam(i) \\in {"a","c"} /\\ '
                                               'Match(i)) \\/ i.team="missing"',
                                        'old': 'KeyOK(i) /\\ BoundTeam(i) \\in {"a","c"} /\\ '
                                               'Match(i)'}],
                             'expect_invariant': 'MissingOrDuplicateKeyRefuses',
                             'id': 'MissingClauseDropped'}],
 'InstanceMetadataTokens': [{'edits': [{'count': 1,
                                        'new': 'TRUE /\\ s.now<s.expires',
                                        'old': 'token \\in s.tokens /\\ s.now<s.expires'}],
                             'expect_invariant': 'InstanceBoundRead',
                             'id': 'ForeignToken'},
                            {'edits': [{'count': 1,
                                        'new': 'token \\in s.tokens',
                                        'old': 'token \\in s.tokens /\\ s.now<s.expires'}],
                             'expect_invariant': 'InstanceBoundRead',
                             'id': 'ExpiredToken'},
                            {'edits': [{'count': 1,
                                        'new': 'path \\in '
                                               '{"instance","alias","ip","region","credentials"}',
                                        'old': 'path \\in {"instance","alias","ip","region"}'}],
                             'expect_invariant': 'InstanceBoundRead',
                             'id': 'CredentialEndpoint'},
                            {'edits': [{'count': 1,
                                        'new': 's.now<s.expires',
                                        'old': 's.now<s.expires /\\ methodOK'}],
                             'expect_invariant': 'InstanceBoundRead',
                             'id': 'WrongReadMethod'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ s.request="none" /\\ TRUE /\\ ttlOK /\\ '
                                               '~forwarded /\\ randomOK',
                                        'old': '/\\ s.request="none" /\\ methodOK /\\ ttlOK /\\ '
                                               '~forwarded /\\ randomOK'}],
                             'expect_invariant': 'TokenAdmission',
                             'id': 'WrongTokenMethod'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ s.request="none" /\\ methodOK /\\ TRUE /\\ '
                                               '~forwarded /\\ randomOK',
                                        'old': '/\\ s.request="none" /\\ methodOK /\\ ttlOK /\\ '
                                               '~forwarded /\\ randomOK'}],
                             'expect_invariant': 'TokenAdmission',
                             'id': 'InvalidTTL'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ s.request="none" /\\ methodOK /\\ ttlOK /\\ '
                                               'TRUE /\\ randomOK',
                                        'old': '/\\ s.request="none" /\\ methodOK /\\ ttlOK /\\ '
                                               '~forwarded /\\ randomOK'}],
                             'expect_invariant': 'TokenAdmission',
                             'id': 'ForwardedToken'},
                            {'edits': [{'count': 1,
                                        'new': '/\\ s.request="none" /\\ methodOK /\\ ttlOK /\\ '
                                               '~forwarded /\\ TRUE',
                                        'old': '/\\ s.request="none" /\\ methodOK /\\ ttlOK /\\ '
                                               '~forwarded /\\ randomOK'}],
                             'expect_invariant': 'TokenAdmission',
                             'id': 'RandomFailure'},
                            {'edits': [{'count': 1,
                                        'new': 'Issue(methodOK,ttlOK,forwarded,randomOK,ttl) == '
                                               '/\\ TRUE',
                                        'old': 'Issue(methodOK,ttlOK,forwarded,randomOK,ttl) == '
                                               '/\\ ~s.closed'}],
                             'expect_invariant': 'TokenAdmission',
                             'id': 'IssueAfterClose'},
                            {'edits': [{'count': 1,
                                        'new': 'Read(token,methodOK,path) == /\\ TRUE',
                                        'old': 'Read(token,methodOK,path) == /\\ ~s.closed'}],
                             'expect_invariant': 'InstanceBoundRead',
                             'id': 'ReadAfterClose'},
                            {'edits': [{'count': 1,
                                        'new': '!.expires=s.now+ttl',
                                        'old': '!.expires=IF s.now+ttl<s.deadline THEN s.now+ttl '
                                               'ELSE s.deadline'}],
                             'expect_invariant': 'BoundedExpiry',
                             'id': 'UnboundedExpiry'}],
 'NativeCommandIssuer': [{'edits': [{'count': 1,
                                     'new': '/\\ s.lease=1 /\\ s.effects=0',
                                     'old': '/\\ ~s.sealed /\\ s.lease=1 /\\ s.effects=0'}],
                          'expect_invariant': 'CurrentOwnerFence',
                          'id': 'IgnoreOwnerSeal'},
                         {'edits': [{'count': 1,
                                     'new': '/\\ ~s.sealed /\\ s.effects=0',
                                     'old': '/\\ ~s.sealed /\\ s.lease=1 /\\ s.effects=0'}],
                          'expect_invariant': 'CurrentOwnerFence',
                          'id': 'IgnoreLeaseIncarnation'},
                         {'edits': [{'count': 1,
                                     'new': 'StartWorker == /\\ s.phase="worker" /\\ s.live',
                                     'old': 'StartWorker == /\\ s.phase="worker" /\\ s.live /\\ '
                                            '~s.restarted'}],
                          'expect_invariant': 'RestartNoWorker',
                          'id': 'OrphanWorkerRestart'},
                         {'edits': [{'count': 1,
                                     'new': 'IF s.reply \\in {"exit0","lost"} THEN "Success" ELSE '
                                            '"Unknown"',
                                     'old': 'IF s.reply="exit0" THEN "Success" ELSE "Unknown"'}],
                          'expect_invariant': 'KnownExitOnly',
                          'id': 'InventSuccessFromLostReply'},
                         {'edits': [{'count': 1,
                                     'new': 'IF s.reply \\in {"exit0","nonterminal"} THEN '
                                            '"Success" ELSE "Unknown"',
                                     'old': 'IF s.reply="exit0" THEN "Success" ELSE "Unknown"'}],
                          'expect_invariant': 'KnownExitOnly',
                          'id': 'InventSuccessFromPending'},
                         {'edits': [{'count': 1,
                                     'new': '!.doc=s.doc',
                                     'old': '!.doc=s.revision \\in {1,3}'}],
                          'expect_invariant': 'SinglePolicySnapshot',
                          'id': 'MixedPolicyRevisions'},
                         {'edits': [{'count': 1,
                                     'new': '!.reserved=FALSE, !.readback=FALSE',
                                     'old': '!.reserved=ok, !.readback=ok'},
                                    {'count': 1,
                                     'new': '/\\ ~s.worker',
                                     'old': '/\\ s.reserved /\\ s.readback /\\ ~s.worker'}],
                          'expect_invariant': 'WorkerRequiresReservation',
                          'id': 'ReserveAfterEffect'},
                         {'edits': [{'count': 1,
                                     'new': '/\\ s.phase \\in {"submit","reply"} /\\ s.live /\\ '
                                            '~s.restarted\n'
                                            ' /\\ s.parentIntent',
                                     'old': '/\\ s.phase="submit" /\\ s.live /\\ ~s.restarted\n'
                                            ' /\\ s.parentIntent'},
                                    {'count': 1,
                                     'new': '/\\ ~s.sealed /\\ s.lease=1 /\\ s.effects<2',
                                     'old': '/\\ ~s.sealed /\\ s.lease=1 /\\ s.effects=0'}],
                          'expect_invariant': 'AtMostOneEffect',
                          'id': 'DuplicateGuestEffect'},
                         {'edits': [{'count': 1, 'new': 'TRUE', 'old': 's.role=s.pageRole'}],
                          'expect_invariant': 'PageScope',
                          'id': 'PageRole'},
                         {'edits': [{'count': 1, 'new': 'TRUE', 'old': 's.region=s.pageRegion'}],
                          'expect_invariant': 'PageScope',
                          'id': 'PageRegion'},
                         {'edits': [{'count': 1, 'new': 'TRUE', 'old': 's.filter=s.pageFilter'}],
                          'expect_invariant': 'PageScope',
                          'id': 'PageFilter'},
                         {'edits': [{'count': 1, 'new': 'TRUE', 'old': 's.size=s.pageSize'}],
                          'expect_invariant': 'PageScope',
                          'id': 'PageSize'},
                         {'edits': [{'count': 1,
                                     'new': 's.unchangedRequest',
                                     'old': '~s.expired /\\ s.unchangedRequest'}],
                          'expect_invariant': 'PageScope',
                          'id': 'PageExpiry'},
                         {'edits': [{'count': 1,
                                     'new': '/\\ TRUE\n IN',
                                     'old': '/\\ s.unchangedRequest\n    IN'}],
                          'expect_invariant': 'PageRecordRevalidated',
                          'id': 'PageRetainedRequest'},
                         {'edits': [{'count': 1,
                                     'new': 'IF ok THEN s.items ELSE {}',
                                     'old': 'IF ok THEN s.frozen ELSE {}'}],
                          'expect_invariant': 'PageSnapshotImmutable',
                          'id': 'PageUsesLiveItems'}],
 'OwnedClockQualification': [{'edits': [{'count': 1,
                                         'new': 'admitted == TRUE /\\ ns /\\ fd /\\ platform',
                                         'old': 'admitted == owned /\\ ns /\\ fd /\\ platform'}],
                              'expect_invariant': 'OwnedProof',
                              'id': 'ArbitraryHTTPProof'},
                             {'edits': [{'count': 1,
                                         'new': 'admitted == owned /\\ TRUE /\\ fd /\\ platform',
                                         'old': 'admitted == owned /\\ ns /\\ fd /\\ platform'}],
                              'expect_invariant': 'OwnedProof',
                              'id': 'ForeignNamespace'},
                             {'edits': [{'count': 1,
                                         'new': 'admitted == owned /\\ ns /\\ TRUE /\\ platform',
                                         'old': 'admitted == owned /\\ ns /\\ fd /\\ platform'}],
                              'expect_invariant': 'OwnedProof',
                              'id': 'UncheckedInheritedFD'},
                             {'edits': [{'count': 1,
                                         'new': 'admitted == owned /\\ ns /\\ fd /\\ TRUE',
                                         'old': 'admitted == owned /\\ ns /\\ fd /\\ platform'}],
                              'expect_invariant': 'OwnedProof',
                              'id': 'UnsupportedPlatform'},
                             {'edits': [{'count': 1,
                                         'new': 'current == proof /\\ ~stopped',
                                         'old': 'current == proof /\\ ~stopped /\\ '
                                                'owner=proofOwner /\\ pendingOwner=owner'}],
                              'expect_invariant': 'QualifiedOnlyCurrent',
                              'id': 'SampleWrongIncarnation'},
                             {'edits': [{'count': 1,
                                         'new': 'authenticated == TRUE /\\ nonce /\\ domain',
                                         'old': 'authenticated == mac /\\ nonce /\\ domain'}],
                              'expect_invariant': 'QualifiedOnlyCurrent',
                              'id': 'WrongSampleMAC'},
                             {'edits': [{'count': 1,
                                         'new': 'authenticated == mac /\\ TRUE /\\ domain',
                                         'old': 'authenticated == mac /\\ nonce /\\ domain'}],
                              'expect_invariant': 'QualifiedOnlyCurrent',
                              'id': 'ReplayedSampleNonce'},
                             {'edits': [{'count': 1,
                                         'new': 'authenticated == mac /\\ nonce /\\ TRUE',
                                         'old': 'authenticated == mac /\\ nonce /\\ domain'}],
                              'expect_invariant': 'QualifiedOnlyCurrent',
                              'id': 'ForeignSampleDomain'},
                             {'edits': [{'count': 1,
                                         'new': 'accepted == current /\\ authenticated /\\ now<4',
                                         'old': 'accepted == current /\\ authenticated /\\ bounds '
                                                '/\\ now<4'}],
                              'expect_invariant': 'QualifiedOnlyCurrent',
                              'id': 'UnboundedSample'},
                             {'edits': [{'count': 1,
                                         'new': 'accepted == current /\\ authenticated /\\ bounds',
                                         'old': 'accepted == current /\\ authenticated /\\ bounds '
                                                '/\\ now<4'}],
                              'expect_invariant': 'QualifiedOnlyCurrent',
                              'id': 'ExpiredSample'},
                             {'edits': [{'count': 1,
                                         'new': 'current == proof /\\ owner=proofOwner /\\ '
                                                'pendingOwner=owner',
                                         'old': 'current == proof /\\ ~stopped /\\ '
                                                'owner=proofOwner /\\ pendingOwner=owner'}],
                              'expect_invariant': 'QualifiedOnlyCurrent',
                              'id': 'LateSampleAfterStop'},
                             {'edits': [{'count': 1,
                                         'new': 'accepted == proof /\\ ~stopped /\\ '
                                                'owner=proofOwner /\\ TRUE /\\ nonce /\\ domain '
                                                '/\\ pathBodyStatus',
                                         'old': 'accepted == proof /\\ ~stopped /\\ '
                                                'owner=proofOwner /\\ mac /\\ nonce /\\ domain /\\ '
                                                'pathBodyStatus'}],
                              'expect_invariant': 'AuthenticatedResponses',
                              'id': 'WrongResponseMAC'},
                             {'edits': [{'count': 1,
                                         'new': 'accepted == proof /\\ ~stopped /\\ '
                                                'owner=proofOwner /\\ mac /\\ TRUE /\\ domain /\\ '
                                                'pathBodyStatus',
                                         'old': 'accepted == proof /\\ ~stopped /\\ '
                                                'owner=proofOwner /\\ mac /\\ nonce /\\ domain /\\ '
                                                'pathBodyStatus'}],
                              'expect_invariant': 'AuthenticatedResponses',
                              'id': 'ReplayedResponseNonce'},
                             {'edits': [{'count': 1,
                                         'new': 'accepted == proof /\\ ~stopped /\\ '
                                                'owner=proofOwner /\\ mac /\\ nonce /\\ TRUE /\\ '
                                                'pathBodyStatus',
                                         'old': 'accepted == proof /\\ ~stopped /\\ '
                                                'owner=proofOwner /\\ mac /\\ nonce /\\ domain /\\ '
                                                'pathBodyStatus'}],
                              'expect_invariant': 'AuthenticatedResponses',
                              'id': 'ForeignResponseDomain'},
                             {'edits': [{'count': 1,
                                         'new': 'accepted == proof /\\ ~stopped /\\ '
                                                'owner=proofOwner /\\ mac /\\ nonce /\\ domain /\\ '
                                                'TRUE',
                                         'old': 'accepted == proof /\\ ~stopped /\\ '
                                                'owner=proofOwner /\\ mac /\\ nonce /\\ domain /\\ '
                                                'pathBodyStatus'}],
                              'expect_invariant': 'AuthenticatedResponses',
                              'id': 'ChangedResponseRequest'},
                             {'edits': [{'count': 1,
                                         'new': "stopped'=TRUE /\\ qualified'=qualified",
                                         'old': "stopped'=TRUE /\\ qualified'=FALSE"}],
                              'expect_invariant': 'StopInvalidates',
                              'id': 'StopKeepsQualification'},
                             {'edits': [{'count': 1,
                                         'new': 'format == domain',
                                         'old': 'format == version=2 /\\ ticks /\\ domain'}],
                              'expect_invariant': 'V2Admission',
                              'id': 'V1WallAccepted'},
                             {'edits': [{'count': 1,
                                         'new': 'admitted == proof /\\ ~stopped /\\ live /\\ '
                                                'format /\\ now<3 /\\ converted>now',
                                         'old': 'admitted == current /\\ live /\\ format /\\ now<3 '
                                                '/\\ converted>now'}],
                              'expect_invariant': 'V2Admission',
                              'id': 'StartAfterOwnerRotation'},
                             {'edits': [{'count': 1,
                                         'new': 'live == TRUE',
                                         'old': 'live == qualified /\\ now<4'}],
                              'expect_invariant': 'V2Admission',
                              'id': 'UnqualifiedStart'},
                             {'edits': [{'count': 1,
                                         'new': 'absolute == now+3',
                                         'old': 'absolute == 3'}],
                              'expect_invariant': 'ConservativeOriginalDeadline',
                              'id': 'RenewAtReceipt'},
                             {'edits': [{'count': 1,
                                         'new': 'converted == absolute+1',
                                         'old': 'converted == absolute-1'}],
                              'expect_invariant': 'ConservativeOriginalDeadline',
                              'id': 'RoundDeadlineLater'},
                             {'edits': [{'count': 1,
                                         'new': 'readable == current /\\ retained /\\ now<4',
                                         'old': 'readable == current /\\ retained'}],
                              'expect_invariant': 'RetainedReadAfterExpiry',
                              'id': 'ExpiredReadBlocked'},
                             {'edits': [{'count': 1,
                                         'new': 'readable == proof /\\ owner=proofOwner /\\ '
                                                'retained',
                                         'old': 'readable == current /\\ retained'}],
                              'expect_invariant': 'ReadRequiresCurrentOwner',
                              'id': 'ReadAfterStop'},
                             {'edits': [{'count': 1,
                                         'new': 'readable == proof /\\ ~stopped /\\ retained',
                                         'old': 'readable == current /\\ retained'}],
                              'expect_invariant': 'ReadRequiresCurrentOwner',
                              'id': 'ReadAfterRotation'}],
 'ParticipantSessionLifecycle': [{'edits': [{'count': 1,
                                             'new': '~d.prepared /\\ private /\\ optin',
                                             'old': '~d.prepared /\\ pin /\\ private /\\ optin'}],
                                  'expect_invariant': 'PreparedEvidence',
                                  'id': 'MissingImagePin'},
                                 {'edits': [{'count': 1,
                                             'new': '~d.prepared /\\ pin /\\ optin',
                                             'old': '~d.prepared /\\ pin /\\ private /\\ optin'}],
                                  'expect_invariant': 'PreparedEvidence',
                                  'id': 'QGAFallback'},
                                 {'edits': [{'count': 1,
                                             'new': '~d.prepared /\\ pin /\\ private',
                                             'old': '~d.prepared /\\ pin /\\ private /\\ optin'}],
                                  'expect_invariant': 'PreparationOptIn',
                                  'id': 'ImplicitSessionOptIn'},
                                 {'edits': [{'count': 1,
                                             'new': 'd.artifact = "absent" /\\ absent',
                                             'old': 'd.artifact = "absent" /\\ valid /\\ absent'}],
                                  'expect_invariant': 'PackageEvidence',
                                  'id': 'UnverifiedOverlay'},
                                 {'edits': [{'count': 1,
                                             'new': 'd.artifact = "absent" /\\ valid',
                                             'old': 'd.artifact = "absent" /\\ valid /\\ absent'}],
                                  'expect_invariant': 'PackageEvidence',
                                  'id': 'OverwriteOverlay'},
                                 {'edits': [{'count': 1,
                                             'new': 'ClaimGuard(i) == IdentityOK(i) /\\ Current',
                                             'old': 'ClaimGuard(i) == IdentityOK(i) /\\ i.token '
                                                    '/\\ Current'}],
                                  'expect_invariant': 'ChannelTokenBinding',
                                  'id': 'StaleChannelToken'},
                                 {'edits': [{'count': 1,
                                             'new': 'd.handoff \\in {"fresh","expired"} /\\ '
                                                    'AdmitGuard(i)',
                                             'old': 'd.handoff = "fresh" /\\ AdmitGuard(i)'}],
                                  'expect_invariant': 'HandoffExpiry',
                                  'id': 'ExpiredHandoff'},
                                 {'edits': [{'count': 1,
                                             'new': 'IdentityOK(i) == i.credential',
                                             'old': 'IdentityOK(i) == i.participant /\\ '
                                                    'i.credential'}],
                                  'expect_invariant': 'BoundAdmission',
                                  'id': 'OperatorAdmission'},
                                 {'edits': [{'count': 1,
                                             'new': 'ClaimGuard(i) == i.participant /\\ i.role /\\ '
                                                    'i.target /\\ i.origin /\\ i.token /\\ Current',
                                             'old': 'ClaimGuard(i) == IdentityOK(i) /\\ i.token '
                                                    '/\\ Current'}],
                                  'expect_invariant': 'BoundAdmission',
                                  'id': 'CredentialRebind'},
                                 {'edits': [{'count': 1,
                                             'new': 'ClaimGuard(i) == i.participant /\\ '
                                                    'i.credential /\\ i.target /\\ i.origin /\\ '
                                                    'i.token /\\ Current',
                                             'old': 'ClaimGuard(i) == IdentityOK(i) /\\ i.token '
                                                    '/\\ Current'}],
                                  'expect_invariant': 'BoundAdmission',
                                  'id': 'RoleGenerationRebind'},
                                 {'edits': [{'count': 1,
                                             'new': 'ClaimGuard(i) == i.participant /\\ '
                                                    'i.credential /\\ i.role /\\ i.origin /\\ '
                                                    'i.token /\\ Current',
                                             'old': 'ClaimGuard(i) == IdentityOK(i) /\\ i.token '
                                                    '/\\ Current'}],
                                  'expect_invariant': 'BoundAdmission',
                                  'id': 'TargetRebind'},
                                 {'edits': [{'count': 1,
                                             'new': 'ClaimGuard(i) == i.participant /\\ '
                                                    'i.credential /\\ i.role /\\ i.target /\\ '
                                                    'i.token /\\ Current',
                                             'old': 'ClaimGuard(i) == IdentityOK(i) /\\ i.token '
                                                    '/\\ Current'}],
                                  'expect_invariant': 'BoundAdmission',
                                  'id': 'CrossOriginChannel'},
                                 {'edits': [{'count': 1,
                                             'new': 'AdmitGuard(i) == IdentityOK(i) /\\ ~d.closed',
                                             'old': 'AdmitGuard(i) == IdentityOK(i) /\\ Current '
                                                    '/\\ ~d.closed'}],
                                  'expect_invariant': 'CurrentAdmission',
                                  'id': 'RevokedAdmission'},
                                 {'edits': [{'count': 1,
                                             'new': 'ClaimGuard(i) == IdentityOK(i) /\\ i.token '
                                                    '/\\ Current /\\ d.token \\in {"fresh","used"}',
                                             'old': 'ClaimGuard(i) == IdentityOK(i) /\\ i.token '
                                                    '/\\ Current /\\ d.token = "fresh"'}],
                                  'expect_invariant': 'SingleUse',
                                  'id': 'ChannelReplay'},
                                 {'edits': [{'count': 1,
                                             'new': 'ClaimGuard(i) == IdentityOK(i) /\\ i.token '
                                                    '/\\ Current /\\ d.token \\in '
                                                    '{"fresh","expired"}',
                                             'old': 'ClaimGuard(i) == IdentityOK(i) /\\ i.token '
                                                    '/\\ Current /\\ d.token = "fresh"'}],
                                  'expect_invariant': 'SingleUse',
                                  'id': 'ExpiredChannel'},
                                 {'edits': [{'count': 1,
                                             'new': 'd.handoff = "used" /\\ TRUE',
                                             'old': 'd.handoff = "used" /\\ FALSE'}],
                                  'expect_invariant': 'SingleUse',
                                  'id': 'HandoffReplay'},
                                 {'edits': [{'count': 1,
                                             'new': '~d.hello /\\ TRUE',
                                             'old': '~d.hello /\\ matches'}],
                                  'expect_invariant': 'HelloEvidence',
                                  'id': 'WrongHello'},
                                 {'edits': [{'count': 1,
                                             'new': 'ReadyGuard == d.hello /\\ ~d.closed /\\ '
                                                    'Current',
                                             'old': 'ReadyGuard == d.hello /\\ d.live /\\ '
                                                    '~d.closed /\\ Current'}],
                                  'expect_invariant': 'ReadyEvidence',
                                  'id': 'ReadyAfterClose'},
                                 {'edits': [{'count': 1,
                                             'new': 'ReadGuard == ~d.closed /\\ d.live /\\ '
                                                    'd.channel',
                                             'old': 'ReadGuard == Current /\\ ~d.closed /\\ d.live '
                                                    '/\\ d.channel'}],
                                  'expect_invariant': 'NoPostRevokeOutput',
                                  'id': 'PublishAfterRevoke'},
                                 {'edits': [{'count': 1,
                                             'new': '!.driver = IF d.driver = 1 THEN 2 ELSE 1',
                                             'old': '!.driver = d.driver'}],
                                  'expect_invariant': 'ResumeKeepsDriver',
                                  'id': 'ResumeNewDriver'},
                                 {'edits': [{'count': 1,
                                             'new': 'ResumeGuard == d.phase \\in '
                                                    '{"resuming","closing"} /\\ d.live /\\ Current',
                                             'old': 'ResumeGuard == d.phase = "resuming" /\\ '
                                                    '~d.closed /\\ d.live /\\ Current'}],
                                  'expect_invariant': 'NoResurrection',
                                  'id': 'ResumeAfterShutdown'},
                                 {'edits': [{'count': 1,
                                             'new': 'ExitGuard == d.clean',
                                             'old': 'ExitGuard == d.drains = {"process", "input", '
                                                    '"output"} /\\ d.clean'}],
                                  'expect_invariant': 'GuestDrainEvidence',
                                  'id': 'ExitBeforeDrain'},
                                 {'edits': [{'count': 1,
                                             'new': 'ExitGuard == d.drains = {"process", "input", '
                                                    '"output"}',
                                             'old': 'ExitGuard == d.drains = {"process", "input", '
                                                    '"output"} /\\ d.clean'}],
                                  'expect_invariant': 'GuestDrainEvidence',
                                  'id': 'ExitOnUncertainClose'},
                                 {'edits': [{'count': 1,
                                             'new': 'TerminalGuard == d.exit /\\ d.clean',
                                             'old': 'TerminalGuard == d.exit /\\ d.joins /\\ '
                                                    'd.clean'}],
                                  'expect_invariant': 'TerminalEvidence',
                                  'id': 'TerminalBeforeJoin'},
                                 {'edits': [{'count': 1,
                                             'new': 'TerminalGuard == d.joins /\\ d.clean',
                                             'old': 'TerminalGuard == d.exit /\\ d.joins /\\ '
                                                    'd.clean'}],
                                  'expect_invariant': 'TerminalEvidence',
                                  'id': 'FabricatedExit'},
                                 {'edits': [{'count': 1,
                                             'new': 'FreshGuard == d.phase = "closing" /\\ d.exit '
                                                    '/\\ d.clean /\\ ~d.closed',
                                             'old': 'FreshGuard == d.phase = "terminated" /\\ '
                                                    'd.exit /\\ d.joins /\\ d.clean /\\ '
                                                    '~d.closed'}],
                                  'expect_invariant': 'FreshEvidence',
                                  'id': 'FreshBeforeJoin'}],
 'TerminalClientEpoch': [{'edits': [{'count': 1,
                                     'new': 'd.pending="receipt" /\\ TRUE',
                                     'old': 'd.pending="receipt" /\\ ReceiptCurrent'}],
                          'expect_invariant': 'ReceiptEvidence',
                          'id': 'LateNativeReceipt'},
                         {'edits': [{'count': 1,
                                     'new': '/\\ TRUE',
                                     'old': '/\\ d.opComponent=d.component /\\ d.opEpoch=d.epoch'}],
                          'expect_invariant': 'ReceiptEvidence',
                          'id': 'ReplacementReceipt'},
                         {'edits': [{'count': 1,
                                     'new': '/\\ d.socketComponent=d.component',
                                     'old': '/\\ d.socketComponent=d.component /\\ c'}],
                          'expect_invariant': 'SocketEvidence',
                          'id': 'StaleSocketCallback'},
                         {'edits': [{'count': 1,
                                     'new': '/\\ d.phase="connecting"',
                                     'old': '/\\ d.phase="connecting" /\\ d.opens=0'}],
                          'expect_invariant': 'SingleOpen',
                          'id': 'OpenTokenReplay'},
                         {'edits': [{'count': 1,
                                     'new': 'UICurrent == d.uiComponent=d.component',
                                     'old': 'UICurrent == d.mounted /\\ '
                                            'd.uiComponent=d.component'}],
                          'expect_invariant': 'UIEvidence',
                          'id': 'UnmountedUI'},
                         {'edits': [{'count': 1,
                                     'new': 'UICurrent == d.mounted',
                                     'old': 'UICurrent == d.mounted /\\ '
                                            'd.uiComponent=d.component'}],
                          'expect_invariant': 'UIEvidence',
                          'id': 'ReplacementUI'},
                         {'edits': [{'count': 1,
                                     'new': '!.channel=FALSE,!.phase="disconnected",!.remote="terminated"]\n'
                                            ' /\\ Record("dispose",TRUE)',
                                     'old': '!.channel=FALSE,!.phase="disconnected"]\n'
                                            ' /\\ Record("dispose",TRUE)'}],
                          'expect_invariant': 'DisposeNotRemoteTermination',
                          'id': 'DisposeFabricatesTermination'},
                         {'edits': [{'count': 1,
                                     'new': 'StartAllowed == ~d.disposed /\\ d.mounted',
                                     'old': 'StartAllowed == ~d.disposed /\\ d.mounted /\\ '
                                            '~d.started'}],
                          'expect_invariant': 'NoAutomaticStartRetry',
                          'id': 'AmbiguousStartRetry'},
                         {'edits': [{'count': 1,
                                     'new': '!.uiComponent=d.opComponent,!.known=FALSE,\n'
                                            '   !.phase=IF ReceiptCurrent',
                                     'old': '!.uiComponent=d.opComponent,\n'
                                            '   !.phase=IF ReceiptCurrent'}],
                          'expect_invariant': 'DiscardPreservesIdentity',
                          'id': 'DiscardClearsKnownSession'}]}

# Original Mac preparation/diagnostic projections; exact required scopes and controls.
BASE_MODELS.add('MacPreparationAdmission')
BASE_INVARIANTS['MacPreparationAdmission'] = {
    'TypeOK',
    'PlanEvidence',
    'ImageEvidence',
    'ImageHandle',
    'PlistEvidence',
    'OriginalBeforeCopy',
    'CopyEvidence',
    'CopySuccess',
    'PreparedEvidence',
    'InstalledEvidence',
    'FinalizedEvidence',
    'NoExecutionClaim',
}
REQUIRED_MUTANTS['MacPreparationAdmission'] = {
    'UnsupportedPlan',
    'ForeignGeneration',
    'UnsafePlanPaths',
    'AliasImage',
    'UnpinnedImage',
    'ExternalImage',
    'ChangedImage',
    'LostImageHandle',
    'UncheckedPlist',
    'UnpinnedOriginal',
    'UnobservedRemoval',
    'ReuseBuildDirectory',
    'UncheckedInstalledBinary',
    'UncleanBuild',
    'PlanStartsProcess',
    'CopyBeforeOriginalAdmission',
}
REQUIRED_SOURCE_SCOPES['MacPreparationAdmission'] = {
    'internal/workload/guesthvf/plan.go',
    'internal/workload/guesthvf/image_unix.go',
    'internal/workload/guesthvf/supervision.go',
    'tools/guesthvf-preflight/main.go',
    'tools/mac-hvf-probe/builder_plan.py',
}
BASE_MODELS.add('MacProbeLifecycle')
BASE_INVARIANTS['MacProbeLifecycle'] = {
    'TypeOK',
    'BuilderAttemptBeforeLaunch',
    'OwnedSuccessEvidence',
    'FailureCannotPass',
    'BuildReceiptAfterLog',
    'LaunchdSuccessEvidence',
    'AbsenceIsNotReap',
    'ReapClaimEvidence',
    'NoBattleQualification',
}
REQUIRED_MUTANTS['MacProbeLifecycle'] = {
    'LoseBuilderAttemptMarker',
    'PublishWithoutOwnedEvidence',
    'LaunchdFailureAsSuccess',
    'AbsenceAsReap',
    'PromoteDiagnosticToBattle',
    'IgnoreLogFailure',
    'ReportUnobservedServiceAbsence',
}
REQUIRED_SOURCE_SCOPES['MacProbeLifecycle'] = {
    'tools/mac-hvf-probe/probe.py',
    'tools/mac-hvf-probe/boot_probe.py',
    'tools/mac-hvf-probe/image_probe.py',
    'tools/mac-hvf-probe/builder_launch.py',
    'tools/mac-supervisor-test/launchd_owned_child.py',
    'tools/mac-hvf-probe/debian-seed/meta-data',
    'tools/mac-hvf-probe/debian-seed/network-config',
    'tools/mac-hvf-probe/debian-seed/user-data',
}


REQUIRED_SOURCE_SCOPES['CFNConditionAdmission'].add('internal/aws/cloudformation_validate.go')

BASE_MODELS.add('ECSTaskRevisionLifecycle')
BASE_INVARIANTS['ECSTaskRevisionLifecycle'] = {
    'CommitRequiresAdmission',
    'HistoryImmutable',
    'RevisionIdentity',
    'CleanupExact',
    'TypeOK',
}
REQUIRED_MUTANTS['ECSTaskRevisionLifecycle'] = {
    'UseStaleStackPlan',
    'PublishFailedStack',
    'OverwriteEarlierImage',
    'RetireRetainedRevision',
}
REQUIRED_SOURCE_SCOPES['ECSTaskRevisionLifecycle'] = {
    'internal/aws/catalog/ecs_revisions.go',
    'internal/aws/catalog/ecs_stack.go',
    'internal/aws/catalog/services_compute.go',
    'internal/aws/catalog/semantics.go',
    'internal/aws/ecs_task_definition_cloudformation.go',
    'internal/aws/cloudformation.go',
    'internal/aws/s3_cloudformation.go',
}

BASE_MODELS.add('VpcOriginReferenceLifecycle')
BASE_INVARIANTS['VpcOriginReferenceLifecycle'] = {
    'PrivateALBRequired',
    'RejectStaleMutation',
    'FailureRollsBack',
    'GenerationAdvances',
    'DeleteExact',
    'TypeOK',
}
REQUIRED_MUTANTS['VpcOriginReferenceLifecycle'] = {
    'AcceptPublicALB',
    'DeleteReferencedALB',
    'AcceptStaleUpdate',
    'DeleteDriftedStackOrigin',
    'CommitPartialOrigin',
}
REQUIRED_SOURCE_SCOPES['VpcOriginReferenceLifecycle'] = {
    'internal/aws/cloudfront_vpc_origin.go',
    'internal/aws/cloudfront_vpc_origin_http.go',
    'internal/aws/cloudformation_vpc_origin.go',
    'internal/aws/elb_store.go',
    'internal/aws/cloudformation.go',
    'internal/aws/s3.go',
    'internal/aws/s3_cloudformation.go',
    'internal/aws/ssm.go',
    'internal/server/server.go',
}

BASE_MODELS.add('CFNParameterRuleAdmission')
BASE_INVARIANTS['CFNParameterRuleAdmission'] = {
    'EveryOperandChecked',
    'ValidationIgnoresValues',
    'AllActiveAssertionsRequired',
    'PublicationFollowsAdmission',
    'ValidatedCreatesNothing',
    'TypeOK',
}
REQUIRED_MUTANTS['CFNParameterRuleAdmission'] = {
    'SkipInactiveShape',
    'IgnoreLateOperand',
    'ValidateWithValues',
    'AcceptEitherComparison',
    'IgnoreAdditionalAssertion',
    'EnforceInactiveRule',
    'PublishBeforeAdmission',
}
REQUIRED_SOURCE_SCOPES['CFNParameterRuleAdmission'] = {
    'internal/aws/cloudformation.go',
    'internal/aws/cloudformation_conditions.go',
    'internal/aws/cloudformation_rules.go',
    'internal/aws/cloudformation_validate.go',
}


BASE_MODELS.add('EC2VPCOwnership')
BASE_INVARIANTS['EC2VPCOwnership'] = {
    'NoAdoption',
    'FailureRollsBack',
    'NativeNeverDeletesStackVPC',
    'NativeDeleteExact',
    'NativeCreateOwned',
    'StackDeleteSparesNative',
    'QuotaBound',
    'DescribeExact',
    'TypeOK',
}
REQUIRED_MUTANTS['EC2VPCOwnership'] = {
    'SkipCreateAuthorization',
    'SkipDeleteAuthorization',
    'DeleteStackVPC',
    'DeleteOtherRegion',
    'OverwriteExistingVPC',
    'AdoptOtherOwnerVPC',
    'StackDeleteRemovesNative',
    'IgnoreNativeQuota',
    'MislabelStackVPC',
    'DescribeOtherRegion',
}
REQUIRED_SOURCE_SCOPES['EC2VPCOwnership'] = {
    'internal/aws/ec2_vpc.go',
    'internal/aws/ec2_read.go',
    'internal/aws/ec2_network.go',
    'internal/aws/ec2_network_cloudformation.go',
    'internal/aws/ssm.go',
    'internal/server/server.go',
}


BASE_MODELS.add('CloudDNSAtomicChange')
BASE_INVARIANTS['CloudDNSAtomicChange'] = {
    'TypeOK',
    'NoOrphanRecords',
    'AtomicPair',
    'ApexPreserved',
    'PendingInvisible',
    'DeniedStateUnchanged',
    'OnlyOwnerCommits',
    'ExactDeletionRequired',
    'CommittedStateMatchesIntent',
    'OtherProjectUnchanged',
    'HistoryPrefixPreserved',
    'ReceiptSequenceExact',
    'NonemptyDeletionRefused',
    'CleanupAdmissibleAtQuota',
}
REQUIRED_MUTANTS['CloudDNSAtomicChange'] = {
    'AcceptPreviousPairedState',
    'AcceptMissingNewestReceipt',
}
REQUIRED_SOURCE_SCOPES['CloudDNSAtomicChange'] = {
    'internal/gcp/dns/engine.go',
    'internal/gcp/dns/model.go',
    'internal/gcp/dns/operations.go',
    'internal/gcp/dns/pagination.go',
    'internal/gcp/dns/records.go',
    'internal/gcp/dns/store.go',
}

BASE_MODELS.add('CloudDNSChildToken')
BASE_INVARIANTS['CloudDNSChildToken'] = {
    'TypeOK',
    'NoOldGenerationReuse',
}
REQUIRED_MUTANTS['CloudDNSChildToken'] = {
    'AcceptOldGeneration',
}
REQUIRED_SOURCE_SCOPES['CloudDNSChildToken'] = {
    'internal/gcp/dns/engine.go',
    'internal/gcp/dns/model.go',
    'internal/gcp/dns/operations.go',
    'internal/gcp/dns/pagination.go',
}


BASE_MODELS.add('CloudDNSConsoleScope')
BASE_INVARIANTS['CloudDNSConsoleScope'] = {
    'TypeOK',
    'CapabilityAuthorized',
    'OnlyCurrentDiscovery',
    'OnlyCurrentReadVisible',
    'NoStaleContinuation',
}
REQUIRED_MUTANTS['CloudDNSConsoleScope'] = {
    'AllowUntrusted',
    'AllowHandoff',
    'AllowNonOwner',
    'ReuseSamePrincipal',
    'ExposeStaleRead',
    'ContinueStaleRead',
}
REQUIRED_SOURCE_SCOPES['CloudDNSConsoleScope'] = {
    'internal/server/server.go',
    'internal/server/web/app.js',
    'tools/console/src/console-navigation.mjs',
    'tools/console/src/gcp-console-shell.jsx',
    'tools/console/src/gcp-dns-requests.mjs',
    'tools/console/src/gcp-dns.jsx',
    'tools/console/src/main.jsx',
}
BASE_MODELS.add('AzureConditionalCRCRead')
BASE_INVARIANTS['AzureConditionalCRCRead'] = {
    'TypeOK',
    'DeniedBeforeOptions',
    'OptionsBeforeLookup',
    'MissingBeforeDate',
    'CacheHitHasNoPartialOutput',
    'EligibleSelectedCRC',
    'CRCRejectsOversize',
    'CRCEmitsOnlyWhenRequested',
}
REQUIRED_MUTANTS['AzureConditionalCRCRead'] = {
    'OptionsBeforeOwner',
    'LookupBeforeOptions',
    'CacheBeforeExistence',
    'EmitCRCOnCacheHit',
    'OmitCap',
}
REQUIRED_SOURCE_SCOPES['AzureConditionalCRCRead'] = {
    'internal/azure/blob/http.go',
    'internal/azure/blob/range.go',
    'internal/azure/blob/store.go',
    'internal/azure/blob/types.go',
    'internal/azure/blob/range_crc64_test.go',
    'internal/azure/blob/read_time_condition_test.go',
    'internal/azure/blob/read_time_range_checksum_test.go',
}


BASE_MODELS.add('AzureStructuredReadAdmission')
BASE_INVARIANTS['AzureStructuredReadAdmission'] = {'FullHasNoRangeMetadata', 'LargeReadsAreQuiet', 'RangeHasRangeMetadata', 'LegacyIsUnchanged', 'DeniedOrConditionalIsQuiet', 'TypeOK', 'ExactNegotiation'}
REQUIRED_MUTANTS['AzureStructuredReadAdmission'] = {'IgnoreMarker', 'AllowNonGet', 'FrameOnNotModified', 'SkipSizeCap', 'WholeHasStoredMD5', 'LegacyIntoXSM', 'RangeLosesStoredMD5', 'AllowConflict', 'WidenVersion'}
REQUIRED_SOURCE_SCOPES['AzureStructuredReadAdmission'] = {'internal/azure/blob/structured_crc64.go', 'internal/azure/blob/http.go'}
AZURE_STRUCTURED_READ_MUTATIONS = [{'id': 'WidenVersion', 'edits': [{'old': 'VersionOK(q) == q.version="XSM"', 'new': 'VersionOK(q) == q.version#"Legacy"', 'count': 1}], 'expect_invariant': 'ExactNegotiation'}, {'id': 'IgnoreMarker', 'edits': [{'old': 'MarkerOK(q) == q.marker="CRC"', 'new': 'MarkerOK(q) == TRUE', 'count': 1}], 'expect_invariant': 'ExactNegotiation'}, {'id': 'AllowNonGet', 'edits': [{'old': 'OperationOK(q) == q.operation="GetBlob"', 'new': 'OperationOK(q) == TRUE', 'count': 1}], 'expect_invariant': 'ExactNegotiation'}, {'id': 'AllowConflict', 'edits': [{'old': 'TransportOK(q) == ~q.plainChecksum', 'new': 'TransportOK(q) == TRUE', 'count': 1}], 'expect_invariant': 'ExactNegotiation'}, {'id': 'LegacyIntoXSM', 'edits': [{'old': 'THEN IF q.marker="None" THEN Reply("Legacy","Legacy",FALSE,FALSE,FALSE) ELSE Quiet("400")', 'new': 'THEN IF q.marker="None" THEN Structured(q) ELSE Quiet("400")', 'count': 1}], 'expect_invariant': 'LegacyIsUnchanged'}, {'id': 'FrameOnNotModified', 'edits': [{'old': '[] OTHER -> Quiet(status)', 'new': '[] OTHER -> IF status="304" THEN Reply(status,"FullXSM",TRUE,FALSE,FALSE) ELSE Quiet(status)', 'count': 1}], 'expect_invariant': 'DeniedOrConditionalIsQuiet'}, {'id': 'SkipSizeCap', 'edits': [{'old': 'q.nativeRead \\in {"LargeWhole","LargeRange"} -> "400"', 'new': 'q.nativeRead \\in {"LargeWhole","LargeRange"} -> "200"', 'count': 1}], 'expect_invariant': 'LargeReadsAreQuiet'}, {'id': 'WholeHasStoredMD5', 'edits': [{'old': 'status="200" -> Reply(status,"FullXSM",TRUE,FALSE,FALSE)', 'new': 'status="200" -> Reply(status,"FullXSM",TRUE,FALSE,TRUE)', 'count': 1}], 'expect_invariant': 'FullHasNoRangeMetadata'}, {'id': 'RangeLosesStoredMD5', 'edits': [{'old': 'status="206" -> Reply(status,"RangedXSM",TRUE,TRUE,TRUE)', 'new': 'status="206" -> Reply(status,"RangedXSM",TRUE,TRUE,FALSE)', 'count': 1}], 'expect_invariant': 'RangeHasRangeMetadata'}]


BASE_MODELS.add('AzureBlobRowDownload')
BASE_INVARIANTS['AzureBlobRowDownload'] = set(['TypeOK', 'RowIsRequestTarget', 'CurrentDownloadOnly', 'SuccessfulBytesOnly'])
REQUIRED_SOURCE_SCOPES['AzureBlobRowDownload'] = set(['internal/server/web/app.js', 'tools/console/src/azure-blob.jsx'])
AZURE_BLOB_ROW_DOWNLOAD_CONTROLS = [{'id': 'UseToolbarSelection',
  'edits': [{'old': 'RequestTarget(row) == row',
             'new': 'RequestTarget(row) == selected',
             'count': 1}],
  'expect_invariant': 'RowIsRequestTarget'},
 {'id': 'AllowRetiredCompletion',
  'edits': [{'old': 'StillCurrent == active /\\ pending.epoch = epoch',
             'new': 'StillCurrent == TRUE',
             'count': 1}],
  'expect_invariant': 'CurrentDownloadOnly'},
 {'id': 'AllowBadPayload',
  'edits': [{'old': 'HasBytes(result) == result = "ok"',
             'new': 'HasBytes(result) == TRUE',
             'count': 1}],
  'expect_invariant': 'SuccessfulBytesOnly'}]
REQUIRED_MUTANTS['AzureBlobRowDownload'] = {m['id'] for m in AZURE_BLOB_ROW_DOWNLOAD_CONTROLS}

BASE_MODELS.add('SakuraServerMetadata')
BASE_INVARIANTS['SakuraServerMetadata'] = {
    'TypeOK',
    'FrozenScope',
    'OnlyOwnerRealmLookup',
    'ReopenBinding',
    'FailureFrame',
    'PeerFrame',
    'ReadAndReopenSnapshot',
    'FreshCommittedID',
    'AllocatorOnlyCreate',
    'CommitExact',
    'StoredIDsAndQuota',
}
REQUIRED_MUTANTS['SakuraServerMetadata'] = {
    'AcceptWrongServerRealm',
    'ReuseDeletedServerID',
}
REQUIRED_SOURCE_SCOPES['SakuraServerMetadata'] = {
    'internal/sakura/iaas/server_http.go',
    'internal/sakura/iaas/server_store.go',
}

BASE_MODELS.add('SakuraServerReadEpoch')
BASE_INVARIANTS['SakuraServerReadEpoch'] = {
    'TypeOK',
    'NoUnadvertisedMount',
    'OnlyCurrentResult',
    'NoDifferentServer',
}
REQUIRED_MUTANTS['SakuraServerReadEpoch'] = {
    'IgnoreDisabled',
    'IgnoreMalformed',
    'IgnoreHandoff',
    'IgnoreContext',
    'IgnoreRevision',
    'AcceptWrongServer',
}
REQUIRED_SOURCE_SCOPES['SakuraServerReadEpoch'] = {
    'internal/server/sakura_provider.go',
    'internal/server/web/app.js',
    'tools/console/src/console-navigation.mjs',
    'tools/console/src/main.jsx',
    'tools/console/src/sakura-console.jsx',
    'tools/console/src/sakura-server-read.mjs',
    'tools/console/src/sakura-servers.jsx',
}
SAKURA_SERVER_READ_EPOCH_MUTATIONS = [{'id': 'IgnoreDisabled', 'edits': [{'old': 'config.enabled /\\ config.valid /\\ ~config.handoff', 'new': 'config.valid /\\ ~config.handoff', 'count': 1}], 'expect_invariant': 'NoUnadvertisedMount'}, {'id': 'IgnoreMalformed', 'edits': [{'old': 'config.enabled /\\ config.valid /\\ ~config.handoff', 'new': 'config.enabled /\\ ~config.handoff', 'count': 1}], 'expect_invariant': 'NoUnadvertisedMount'}, {'id': 'IgnoreHandoff', 'edits': [{'old': 'config.enabled /\\ config.valid /\\ ~config.handoff', 'new': 'config.enabled /\\ config.valid', 'count': 1}], 'expect_invariant': 'NoUnadvertisedMount'}, {'id': 'IgnoreContext', 'edits': [{'old': 'BoundContext(r) == r.context = context.sequence', 'new': 'BoundContext(r) == TRUE', 'count': 1}], 'expect_invariant': 'OnlyCurrentResult'}, {'id': 'IgnoreRevision', 'edits': [{'old': 'BoundRevision(r) == r.revision = revision', 'new': 'BoundRevision(r) == TRUE', 'count': 1}], 'expect_invariant': 'OnlyCurrentResult'}, {'id': 'AcceptWrongServer', 'edits': [{'old': 'ProjectionOK(r, actual) == r.target = actual', 'new': 'ProjectionOK(r, actual) == TRUE', 'count': 1}], 'expect_invariant': 'NoDifferentServer'}]

BASE_MODELS.add('SimpleDBLifecycle')
BASE_INVARIANTS['SimpleDBLifecycle'] = {
    'TypeOK',
    'NoOrphans',
    'PendingInvisible',
    'RefusalUnchanged',
    'AuthorizedCommit',
    'PeerFrame',
    'CommittedExact',
    'DurableAck',
    'RestartExact',
    'DeleteEmpty',
    'CreateIdempotent',
    'PutSetExact',
}
REQUIRED_MUTANTS['SimpleDBLifecycle'] = {
    'PublishStagedSimpleDB',
    'LoseExistingSimpleDBValuesOnAppend',
}
REQUIRED_SOURCE_SCOPES['SimpleDBLifecycle'] = {
    'internal/aws/simpledb/store.go',
    'internal/aws/simpledb/domain.go',
    'internal/aws/simpledb/item.go',
}

BASE_MODELS.add('SimpleDBPageCursor')
BASE_INVARIANTS['SimpleDBPageCursor'] = {
    'TypeOK',
    'CursorBinding',
    'ExactPage',
    'ReadOnly',
}
REQUIRED_MUTANTS['SimpleDBPageCursor'] = {
    'IncludeAnchor',
    'AcceptPreviousEpoch',
}
REQUIRED_SOURCE_SCOPES['SimpleDBPageCursor'] = {
    'internal/aws/simpledb/pagination.go',
    'internal/aws/simpledb/store.go',
}

BASE_MODELS.add('SimpleDBQueryAdmission')
BASE_INVARIANTS['SimpleDBQueryAdmission'] = {
    'TypeOK',
    'OneDispatch',
    'MalformedNeverDispatches',
    'TrustedAdmissionBeforeDispatch',
    'ConfiguredScopeBinding',
    'EnvelopeMatchesOutcome',
}
REQUIRED_MUTANTS['SimpleDBQueryAdmission'] = {
    'SkipTypedDomainGuard',
    'SelectPeerScope',
}
REQUIRED_SOURCE_SCOPES['SimpleDBQueryAdmission'] = {
    'internal/aws/simpledb/validation.go',
    'internal/aws/simpledb/http.go',
    'internal/aws/simpledb_gateway.go',
}


# Cognito local pool metadata only: immutable bounds and independent controls.
COGNITO_METADATA_BOUNDS = 'Four consecutive actions including at most one restart; two signed-region labels, one runtime account and a peer-store fixture, three ordered ID suffixes, local capacity two, MaxResults 1/2/60 and invalid boundary values, one retained cursor, independent credential/exercise/region/account/integrity/page-size/restart challenges.'
COGNITO_METADATA_CONFIG = 'INIT Init\nNEXT Next\nCONSTANTS\n  StepLimit = 4\n  Capacity = 2\n  None = noValue\nINVARIANTS TypeOK AdmissionBeforeStorage ScopedCRUD ExactOrderedPage ExactNextToken RestartPersistsRows\nCHECK_DEADLOCK FALSE\n'
BASE_MODELS.add('CognitoUserPoolMetadata')
BASE_INVARIANTS['CognitoUserPoolMetadata'] = {
    'TypeOK', 'AdmissionBeforeStorage', 'ScopedCRUD', 'ExactOrderedPage', 'ExactNextToken',
    'RestartPersistsRows',
}
REQUIRED_MUTANTS['CognitoUserPoolMetadata'] = {
    'AcceptUnsignedLocal', 'AcceptDummySigning', 'AcceptBrowserCredential', 'AcceptOtherSigningService',
    'IgnoreCredentialPrincipal', 'AcceptUnboundHeaders', 'AdmitUnsupportedOperation',
    'AllowUnsupportedInput', 'ExpandPageSizeRange', 'ApproveWildcardItem', 'IgnoreRequestedAction',
    'TrustCursorForAuthorization', 'DecodeBeforeAuthorization', 'IgnoreCursorIntegrity',
    'IgnoreCursorMarkerShape', 'AcceptRetiredStoreCursor', 'IgnoreCursorAccount', 'IgnoreCursorRegion',
    'IgnoreCursorCredential', 'IgnoreCursorExercise', 'IgnoreCursorPageSize', 'ExpireDeletedMarker',
    'IgnorePoolCapacity', 'AcceptCreateCollision', 'ReadOrDeletePeerAccount', 'ReadOrDeletePeerRegion',
    'ListPeerAccount', 'ListPeerRegion', 'ReplayCursorMarker', 'DropRemainingPage',
    'InventTerminalCursor', 'SkipAfterCursor', 'LosePersistedRows',
}
REQUIRED_SOURCE_SCOPES['CognitoUserPoolMetadata'] = {
    'internal/aws/cognito_user_pools.go', 'internal/aws/cognitoidp/http.go',
    'internal/aws/cognitoidp/pagination.go', 'internal/aws/cognitoidp/pools.go', 'internal/aws/s3.go',
    'internal/aws/ssm.go', 'internal/aws/sts_signature.go', 'internal/server/access_sdk.go',
    'internal/server/cognito_user_pools_capabilities.go', 'internal/server/server.go',
}


BASE_MODELS.add('CognitoFederationPoolOwnership')
BASE_INVARIANTS['CognitoFederationPoolOwnership'] = {'TypeOK', 'PeerFrame', 'CanonicalTransition'}
REQUIRED_SOURCE_SCOPES['CognitoFederationPoolOwnership'] = {
    'internal/aws/cognitoidp/federation_store.go', 'internal/aws/cognitoidp/pools.go',
}
REQUIRED_MUTANTS['CognitoFederationPoolOwnership'] = {
    'WritePeerAccount', 'WritePeerRegion', 'WriteSiblingPool', 'IgnoreRevision', 'WriteUnopenedPool',
    'RetainOnDelete', 'WriteAbsentPool', 'ReuseRetiredRevision', 'IgnoreFailedCommit',
}

BASE_MODELS.add('CognitoFederationConfiguration')
BASE_INVARIANTS['CognitoFederationConfiguration'] = {'TypeOK', 'NoDanglingClient', 'AuthorityFrame', 'ExactBinding', 'MutationFrame'}
REQUIRED_SOURCE_SCOPES['CognitoFederationConfiguration'] = {
    'internal/aws/cognitoidp/federation_config.go', 'internal/aws/cognitoidp/federation_metadata.go',
    'internal/aws/cognitoidp/federation_publisher.go', 'internal/aws/cognitoidp/federation_store.go',
    'internal/aws/cognitoidp/pools.go',
}
REQUIRED_MUTANTS['CognitoFederationConfiguration'] = {
    'DefaultAdmission', 'AllowOtherPoolGrant', 'AllowWrongAction', 'OneDescribeSuffices',
    'UseExpiredSource', 'AcceptPlain', 'UseUnregisteredCallback', 'IgnoreStaleRevision',
    'IgnoreClientProvider', 'UseExpiredBinding', 'AllowUnsupportedScope', 'DeleteReferencedProvider',
    'ClientWithoutProvider', 'PeerWrite', 'KeepRevision', 'DropAuthorizedWrite',
}

BASE_MODELS.add('CognitoFederationNativeAdmission')
BASE_INVARIANTS['CognitoFederationNativeAdmission'] = {'TypeOK', 'ExactNativeAdmission', 'NativeProjection', 'CanonicalFrame'}
REQUIRED_SOURCE_SCOPES['CognitoFederationNativeAdmission'] = {
    'cmd/simulator/main.go', 'internal/aws/cognitoidp/federation_http.go',
    'internal/aws/cognitoidp/federation_publisher.go', 'internal/aws/cognitoidp/http.go',
    'internal/aws/cognitoidp/pools.go', 'internal/server/cognito_user_pools_capabilities.go',
    'internal/server/sakura_provider.go', 'internal/server/server.go',
}
REQUIRED_MUTANTS['CognitoFederationNativeAdmission'] = {
    'ForceOn', 'UseUnverified', 'AcceptOtherPeer', 'AcceptBrowserHeaders', 'IgnoreScope',
    'IgnoreShape', 'AllowWrongGrant', 'EnableLoginDispatch', 'ReturnRawGraph', 'SkipPoolLookup',
    'IgnoreProviderReference', 'WritePeerRegion', 'WrongProjection',
}

BASE_MODELS.add('SimpleDBSelectPage')
BASE_INVARIANTS['SimpleDBSelectPage'] = {
    'TypeOK', 'AdmissionBeforeRead',
    'CursorScopeBound', 'ExactCurrentPage',
    'ExactConsumedCursor',
}
REQUIRED_MUTANTS['SimpleDBSelectPage'] = {
    'SkipSyntax', 'SkipPageAuthorization',
    'InspectDeniedRows', 'InspectInvalidCursorRows',
    'AcceptForeignAccount', 'AcceptForeignRegion',
    'IgnoreExactExpression', 'IgnoreConsistency',
    'AcceptRetiredStore', 'AdmitSiblingOperation',
    'AdmitTamperedCursor', 'ReplayIssuanceImage',
    'RepeatCursorItem', 'ResumeAfterLastMatch',
    'SkipFirstDeferred', 'DropMatches',
}
REQUIRED_SOURCE_SCOPES['SimpleDBSelectPage'] = {
    'internal/aws/simpledb/http.go', 'internal/aws/simpledb/pagination.go',
    'internal/aws/simpledb/select.go', 'internal/aws/simpledb/select_grammar.go',
    'internal/server/server.go',
}

BASE_MODELS.add('SimpleDBSelectConsoleEpoch')
BASE_INVARIANTS['SimpleDBSelectConsoleEpoch'] = {
    'TypeOK', 'ReadAdmission', 'BoundedReadRequest', 'SingleCurrentRead', 'CursorOrigin',
    'OnlyCurrentOwner', 'OnlyAcceptedPages', 'CursorMirrorsReply', 'NextOfferedExactly',
    'FailureClearsPage', 'EffectIsolation', 'ChoiceDoesNotObserve',
}
REQUIRED_MUTANTS['SimpleDBSelectConsoleEpoch'] = {
    'AcceptUnadvertised', 'AllowInvalidCriteria', 'BypassItemBusy', 'WidenPageSize',
    'AllowConcurrentQuery', 'CarryCursorOnFreshRun', 'ContinueWithChangedQuery', 'SkipContinuationPage',
    'OfferNextAtEnd', 'HideNextAfterEmpty', 'KeepOwnerOnPrincipal', 'KeepOwnerOnDomain',
    'KeepOwnerOnCriteria', 'KeepOwnerOnNavigation', 'KeepOwnerOnCapability', 'KeepOwnerOnWrite',
    'AcceptObsoleteResult', 'AcceptObsoleteFailure', 'AcceptRepeatedCursor', 'ExposeDenialAsEmpty',
    'DropEmptyContinuation', 'KeepCursorAfterFailure', 'UseAttributeReadAsSearch', 'ReadOnSelection',
    'KeepObservationOnSelection',
}
REQUIRED_SOURCE_SCOPES['SimpleDBSelectConsoleEpoch'] = {
    'internal/server/web/app.js', 'tools/console/src/main.jsx', 'tools/console/src/simpledb-client.mjs',
    'tools/console/src/simpledb.jsx',
}

BASE_MODELS.add('SimpleDBAttributeDelete')
BASE_INVARIANTS['SimpleDBAttributeDelete'] = {
    'NoPhantomItem', 'NoUnacknowledgedChange', 'OnlyEligibleMayAck', 'ExactSurvivors',
}
REQUIRED_MUTANTS['SimpleDBAttributeDelete'] = {'LoseUnselectedValues', 'DropCondition'}
REQUIRED_SOURCE_SCOPES['SimpleDBAttributeDelete'] = {
    'internal/aws/simpledb/delete.go', 'internal/aws/simpledb/http.go', 'internal/aws/simpledb/item.go',
}


BASE_MODELS.add('SimpleDBDomainMetadata')
BASE_INVARIANTS['SimpleDBDomainMetadata'] = {
    'TypeOK', 'ReadRequiresDomainAuthority', 'ExactCommittedTotals',
}
REQUIRED_MUTANTS['SimpleDBDomainMetadata'] = {
    'CountMetadataKeysPerItem', 'LoseMetadataValueBytes', 'AdmitDeniedMetadataRead',
}
REQUIRED_SOURCE_SCOPES['SimpleDBDomainMetadata'] = {
    'internal/aws/simpledb/metadata.go',
    'internal/aws/simpledb/http.go',
}


BASE_MODELS.add('S3MultipartConsolePaging')
BASE_INVARIANTS['S3MultipartConsolePaging'] = {
    'TypeOK',
    'OnlyCanonicalRows',
    'OnlyProgressingCursor',
    'OnlyCompleteListing',
    'NoReadAfterStop',
}
S3_LISTPARTS_MUTATIONS = [{'id': 'BypassRowOrder',
  'edits': [{'old': 'RowsOK(rows, prior) == CanonicalRows(rows, prior)',
             'new': 'RowsOK(rows, prior) == TRUE',
             'count': 1}],
  'expect_invariant': 'OnlyCanonicalRows'},
 {'id': 'BypassCursor',
  'edits': [{'old': 'CursorOK(rows, prior, cursor) == CanonicalCursor(rows, prior, cursor)',
             'new': 'CursorOK(rows, prior, cursor) == TRUE',
             'count': 1}],
  'expect_invariant': 'OnlyProgressingCursor'},
 {'id': 'AcceptUnknownFlag',
  'edits': [{'old': 'FinalFlag(flag) == flag = "final"',
             'new': 'FinalFlag(flag) == flag /= "more"',
             'count': 1}],
  'expect_invariant': 'OnlyCompleteListing'},
 {'id': 'ReturnPartial',
  'edits': [{'old': 'ExposeOnContinue == FALSE', 'new': 'ExposeOnContinue == TRUE', 'count': 1}],
  'expect_invariant': 'OnlyCompleteListing'},
 {'id': 'ContinueAfterStop',
  'edits': [{'old': 'ActiveOK == ~stopped', 'new': 'ActiveOK == TRUE', 'count': 1}],
  'expect_invariant': 'NoReadAfterStop'}]
REQUIRED_MUTANTS['S3MultipartConsolePaging'] = {control['id'] for control in S3_LISTPARTS_MUTATIONS}
REQUIRED_SOURCE_SCOPES['S3MultipartConsolePaging'] = {
    'internal/server/web/app.js',
    'tools/console/src/s3-multipart-parts.mjs',
    'tools/console/src/s3-multipart.jsx',
}


BASE_MODELS.add('S3MultipartPartReceipt')
BASE_INVARIANTS['S3MultipartPartReceipt'] = {'TypeOK', 'ConfirmedReadback', 'CurrentViewNotice', 'NoMutationReplay'}
S3_PART_RECEIPT_MUTATIONS = [
    {'id': 'RequireSuccessfulWrite', 'expect_invariant': 'ConfirmedReadback',
     'edits': [{'old': '           /\\ ack.status = 200 /\\ ack.strong\n',
                'new': '           /\\ ack.strong\n', 'count': 1}]},
    {'id': 'RequireStrongETag', 'expect_invariant': 'ConfirmedReadback',
     'edits': [{'old': '           /\\ ack.status = 200 /\\ ack.strong\n',
                'new': '           /\\ ack.status = 200\n', 'count': 1}]},
    {'id': 'RequireCurrentUpload', 'expect_invariant': 'ConfirmedReadback',
     'edits': [{'old': '/\\ observed.bound /\\ observed.full /\\ observed.seen',
                'new': '/\\ observed.full /\\ observed.seen', 'count': 1}]},
    {'id': 'RequireCompleteListing', 'expect_invariant': 'ConfirmedReadback',
     'edits': [{'old': '/\\ observed.bound /\\ observed.full /\\ observed.seen',
                'new': '/\\ observed.bound /\\ observed.seen', 'count': 1}]},
    {'id': 'RequirePartNumber', 'expect_invariant': 'ConfirmedReadback',
     'edits': [{'old': '           /\\ observed.v.num = ack.part.num\n',
                'new': '', 'count': 1}]},
    {'id': 'RequirePartSize', 'expect_invariant': 'ConfirmedReadback',
     'edits': [{'old': '           /\\ observed.v.size = ack.part.size\n',
                'new': '', 'count': 1}]},
    {'id': 'RequirePartETag', 'expect_invariant': 'ConfirmedReadback',
     'edits': [{'old': '           /\\ observed.v.tag = ack.part.tag\n',
                'new': '', 'count': 1}]},
    {'id': 'FenceRetiredView', 'expect_invariant': 'CurrentViewNotice',
     'edits': [{'old': '           /\\ observed.generation = scope\n',
                'new': '', 'count': 1}]},
    {'id': 'DoNotPublishFromPUT', 'expect_invariant': 'ConfirmedReadback',
     'edits': [{'old': '  /\\ Allowed\n  /\\ phase\' = "done"',
                'new': '  /\\ (phase = "await" \\/ Allowed)\n  /\\ phase\' = "done"', 'count': 1}]},
    {'id': 'DoNotReplayOnRefresh', 'expect_invariant': 'NoMutationReplay',
     'edits': [{'old': '  /\\ phase\' = "refresh" /\\ notice\' = FALSE\n  /\\ UNCHANGED <<stored, scope, writes, ack, observed>>',
                'new': '  /\\ phase\' = "refresh" /\\ notice\' = FALSE\n  /\\ writes\' = writes+1\n  /\\ UNCHANGED <<stored, scope, ack, observed>>', 'count': 1}]},
]
REQUIRED_MUTANTS['S3MultipartPartReceipt'] = {control['id'] for control in S3_PART_RECEIPT_MUTATIONS}
REQUIRED_SOURCE_SCOPES['S3MultipartPartReceipt'] = {
    'internal/server/web/app.js',
    'tools/console/src/s3-multipart-parts.mjs',
    'tools/console/src/s3-multipart.jsx',
}


BASE_MODELS.add('SARPolicyAuthorizationTarget')
BASE_INVARIANTS['SARPolicyAuthorizationTarget'] = {
    'TypeOK', 'ResolvedParentTarget', 'NoUnpermittedEffects',
    'PermittedBehavior', 'ReadPreservesStoredPolicy',
}
SAR_POLICY_TARGET_MUTATIONS = [{'id': 'UseStorageTarget',
  'edits': [{'old': 'IF hasParent THEN "ParentArn" ELSE "Unresolved"',
             'new': 'IF hasParent THEN "StorageIdentity" ELSE "Unresolved"',
             'count': 1}],
  'expect_invariant': 'ResolvedParentTarget'},
 {'id': 'IgnoreApplicationGrant',
  'edits': [{'old': 'target = "ParentArn" /\\ target \\in grants', 'new': 'target = "ParentArn"', 'count': 1}],
  'expect_invariant': 'NoUnpermittedEffects'},
 {'id': 'RejectPermitted',
  'edits': [{'old': 'target = "ParentArn" /\\ target \\in grants', 'new': 'FALSE', 'count': 1}],
  'expect_invariant': 'PermittedBehavior'},
 {'id': 'MutateOnRead',
  'edits': [{'old': 'IF state = "Written" THEN before + 1 ELSE before',
             'new': 'IF state \\in {"Written", "Read"} THEN before + 1 ELSE before',
             'count': 1}],
  'expect_invariant': 'ReadPreservesStoredPolicy'}]
REQUIRED_MUTANTS['SARPolicyAuthorizationTarget'] = {
    control['id'] for control in SAR_POLICY_TARGET_MUTATIONS
}
REQUIRED_SOURCE_SCOPES['SARPolicyAuthorizationTarget'] = {
    'internal/aws/catalog/engine.go',
    'internal/aws/catalog/semantics.go',
    'internal/aws/catalog/services_compute3.go',
    'internal/aws/catalog/spec.go',
}


BASE_MODELS.add('AzureQueueLifecycle')
BASE_INVARIANTS['AzureQueueLifecycle'] = {'TypeOK', 'Isolation', 'ReadRollbackDurable', 'Rotation', 'ReceiptRequired', 'ExpiryGuard', 'ExpiryRemoval', 'VisibilityAdvance', 'EarlyReceiveBlocked', 'UpdateReceipt', 'InvalidUpdate'}
REQUIRED_MUTANTS['AzureQueueLifecycle'] = {'NoReceiveVisibilityAdvance', 'EarlyReceiveAllowed', 'StaleUpdateAllowed', 'NoUpdateRotation', 'StaleDeleteAllowed', 'RestartLosesState', 'CrossOwnerWrite', 'ExpiredPeekAllowed'}
REQUIRED_SOURCE_SCOPES['AzureQueueLifecycle'] = {'internal/azure/queue/handler.go', 'internal/azure/queue/protocol.go', 'internal/azure/queue/store.go', 'internal/azure/queue/handler_test.go', 'internal/azure/queue/store_test.go', 'internal/azure/queue/expiry_quota_test.go', 'internal/server/cloud_storage.go', 'internal/server/azure_queue_test.go'}


BASE_MODELS.add('AzureQueueOwnerAccess')
BASE_INVARIANTS['AzureQueueOwnerAccess'] = {
    'TypeOK',
    'FixedParticipant',
    'OnlyCurrentDiscovery',
    'NewestObservationRetained',
    'DiscoveryAuthority',
    'PresentationOwnerOnly',
    'ReferenceGetAuthorized',
    'ReferenceAccountIsConfigured',
    'NativePermissionBoundary',
}
REQUIRED_MUTANTS['AzureQueueOwnerAccess'] = {
    'SwitchFixedIdentity',
    'IgnoreDiscoveryEpoch',
    'PermitLateObservationOverwrite',
    'DiscoverWithoutOwner',
    'DiscoverForHandoff',
    'ReferenceAllowsNonGet',
    'ReferenceAllowsHandoff',
    'ReferenceUsesRequestHint',
    'ReferenceSubstitutesPrincipal',
    'NativeAllowsOtherAccount',
    'NativeRequiresVisibleConsole',
    'NativeAllowsAwsHandoff',
}
REQUIRED_SOURCE_SCOPES['AzureQueueOwnerAccess'] = {
    'internal/server/azure_queue_reference.go',
    'internal/azure/queue/openapi.go',
    'tools/console/src/console-navigation.mjs',
    'tools/console/src/main.jsx',
    'internal/server/server.go',
    'internal/server/cloud_storage.go',
    'internal/azure/queue/handler.go',
    'internal/server/web/app.js',
}


BASE_MODELS.add('SNSSubscriptionReadback')
BASE_INVARIANTS['SNSSubscriptionReadback'] = {
    'TypeOK',
    'ReadDoesNotChangeStore',
    'AuthorizationTargetsParent',
    'LookupRequiresAuthorization',
    'NoAuditResurrection',
    'RetainedJoinIsExact',
    'TypedShortCircuit',
    'ReplyMatchesSnapshot',
    'OnlySuccessReturnsAttributes',
}
REQUIRED_MUTANTS['SNSSubscriptionReadback'] = {
    'SkipRequestRegion',
    'AuthorizePeerTopic',
    'LookupBeforeAuthorization',
    'JoinIgnoresSubscriptionArn',
    'JoinIgnoresStoredParent',
    'JoinIgnoresStoredRegion',
    'RecoverFromDeliveryHistory',
    'InvalidArnAsNotFound',
    'DenialAsNotFound',
    'MissingRowAsParameterError',
    'ReadFailureAsNotFound',
    'ReadDeletesHistory',
    'ReplyUsesLaterStore',
    'HardcodeReturnedDescriptor',
    'ReturnAttributesOnError',
    'EmitRawDeliveryTrue',
}
REQUIRED_SOURCE_SCOPES['SNSSubscriptionReadback'] = {
    'internal/aws/sns.go',
    'internal/aws/sns_store.go',
    'internal/aws/sns_sqs_fanout.go',
    'internal/aws/sns_subscription_attributes_test.go',
    'test/e2e/sns_subscription_readback.py',
}


BASE_MODELS.add('SNSConsoleResponseAdmission')
BASE_INVARIANTS['SNSConsoleResponseAdmission'] = {
    'TypeOK',
    'MountRequiresCapabilityAndNoHandoff',
    'KeyedOwnerMatchesScope',
    'OnlyOneCurrentFlight',
    'NoLateVisibleEffect',
    'AbortInhibitsCallback',
    'OnlyCurrentTopicUpdates',
    'OnlyCurrentTopicChoices',
}
REQUIRED_MUTANTS['SNSConsoleResponseAdmission'] = {
    'AdmitBoundHandoff',
    'IgnoreMissingUnsubscribe',
    'RequireExactlyFourActions',
    'AdmitBeforeBootstrap',
    'IgnoreOperationsArrayShape',
    'OmitRegionFromKey',
    'OmitPrincipalFromKey',
    'OmitNavigationFromKey',
    'AllowOverlappingTopicRequests',
    'RouteLateResponseToMatchingKey',
    'IgnoreAbortedSignal',
    'LateFinallyClearsCurrentFlight',
}
REQUIRED_SOURCE_SCOPES['SNSConsoleResponseAdmission'] = {
    'tools/console/src/main.jsx',
    'tools/console/src/console-navigation.mjs',
    'tools/console/src/sns.jsx',
    'tools/console/src/sns-client.mjs',
    'internal/server/web/app.js',
}


# Provider-owned EC2 -> RDS observation only, never permission for later I/O.
BASE_MODELS.add('RDSGuestPeerPreflight')
BASE_INVARIANTS['RDSGuestPeerPreflight'] = {
    'TypeOK',
    'CanonicalGuest',
    'SameProviderOwner',
    'CurrentPrivateGeneration',
    'DestinationIngress',
    'SnapshotClosedBeforeHost',
    'HostAuthenticated',
    'LiveAtReturn',
    'ObservationOnly',
    'DenialDoesNotRetireOwner',
    'HealthyObservation',
    'DestinationAtFinalSnapshot',
    'FinalSnapshotCommitted',
    'OriginalGroupBeforeProbe',
    'OriginalGroupAtFinalSnapshot',
    'MatchingGroupSnapshotRevisions',
}
REQUIRED_MUTANTS['RDSGuestPeerPreflight'] = {
    'AcceptBorrowedGuest',
    'AcceptForeignProvider',
    'SkipOwnedGeneration',
    'SkipNativePrivateBindings',
    'UseSourceSG',
    'IgnoreProtocol',
    'IgnorePort',
    'IgnoreNICAddressRange',
    'IgnoreFailedCommit',
    'AcceptFailedHostSCRAM',
    'ProbeWithSnapshotOpen',
    'SkipNICRecheck',
    'SkipCommandRecheck',
    'IssueReusablePermit',
    'RetireOwnerOnPeerDenial',
    'DenyAllHealthyGuests',
    'AcceptLostDestinationAfterInitialRead',
    'IgnoreFailedFinalCommit',
    'SkipCommittedPostProbeSnapshot',
    'AcceptReplacementBeforeProbe',
    'AcceptReplacementAfterProbe',
    'AcceptChangedGroupRevision',
    'RefuseInPlaceRevision',
}
REQUIRED_SOURCE_SCOPES['RDSGuestPeerPreflight'] = {
    'internal/aws/catalog/rds_guest_peer_linux_amd64.go',
    'internal/aws/catalog/rds_owned_runtime_linux_amd64.go',
    'internal/aws/catalog/rds_private_postgres_topology.go',
    'internal/aws/ec2_completed_guest_unix.go',
    'internal/aws/rds_guest_peer_linux_amd64.go'
}


# Detached internet gateways: persistence, ownership and confirmed UI readback.
BASE_MODELS.add('EC2InternetGatewayLifecycle')
BASE_INVARIANTS['EC2InternetGatewayLifecycle'] = {
    'TypeOK',
    'Authorized',
    'TaggedCreation',
    'NativeOwnership',
    'DependentRowsPreserved',
    'DurableMutation',
    'FaithfulRead',
    'ExactConfirmation',
    'ConfirmedNotice',
}
REQUIRED_MUTANTS['EC2InternetGatewayLifecycle'] = {
    'IgnoreRegionalAuthorization',
    'IgnoreScopedHandoff',
    'IgnoreActionGrant',
    'IgnoreTagGrant',
    'AdoptStackGateway',
    'DeleteReferencedGateway',
    'AcknowledgeFailedPersistence',
    'ProjectCorruptGraph',
    'ReadMissingExactID',
    'InventAttachment',
    'NoticeBeforeReadback',
    'AcceptStaleReadback',
    'AcceptWrongRegionReadback',
    'AcceptUnmatchedReadback',
}
REQUIRED_SOURCE_SCOPES['EC2InternetGatewayLifecycle'] = {
    'internal/aws/ec2_internet_gateways.go',
    'internal/aws/ec2_internet_gateway_store.go',
    'internal/aws/ec2_internet_gateway_attachment_store.go',
    'internal/aws/ec2_read.go',
    'internal/aws/ec2_vpc.go',
    'internal/server/server.go',
    'tools/console/src/ec2-internet-gateways.mjs',
    'tools/console/src/ec2-vpc-internet-gateways.jsx',
    'tools/console/src/ec2-readonly.jsx',
    'tools/console/src/main.jsx',
    'internal/server/web/app.js',
}


# Detached internet gateway pagination and complete publication.
BASE_MODELS.add('EC2InternetGatewayInventoryPages')
BASE_INVARIANTS['EC2InternetGatewayInventoryPages'] = {
    'TypeOK',
    'PageAuthorized',
    'PageBounded',
    'PageFenced',
    'ContinuationBound',
    'CompletePublication',
}
REQUIRED_MUTANTS['EC2InternetGatewayInventoryPages'] = {
    'SkipNewPageAuthorization',
    'AcceptAnonymousCursor',
    'AcceptExactIDCombination',
    'IgnoreRequestedLimit',
    'OverfullPage',
    'IgnoreWireBound',
    'AcceptRepeatedOrSkippedRows',
    'AcceptStaleConsolePage',
    'SkipMAC',
    'CrossRegionCursor',
    'CrossCallerCursor',
    'SwitchPageSizeCursor',
    'ContinueOnNewSnapshot',
    'RenderPartialInventory',
    'AcceptEmptyContinuation',
}
REQUIRED_SOURCE_SCOPES['EC2InternetGatewayInventoryPages'] = {
    'internal/aws/cloudformation.go',
    'internal/aws/ec2_internet_gateway_page.go',
    'internal/aws/ec2_internet_gateways.go',
    'internal/aws/ec2_read.go',
    'internal/server/server.go',
    'tools/console/src/ec2-internet-gateways.mjs',
    'internal/server/web/app.js',
}


# Bounded native Internet Gateway-to-VPC relation and confirmed readback.
BASE_MODELS.add('EC2InternetGatewayAttachment')
BASE_INVARIANTS['EC2InternetGatewayAttachment'] = {
    'TypeOK',
    'OneGatewayPerVPC',
    'ExactMutation',
    'Authority',
    'Committed',
    'ReadbackFence',
    'ConfirmedNotice',
}
REQUIRED_MUTANTS['EC2InternetGatewayAttachment'] = {
    'AdmitUnscopedMutation',
    'SkipGatewayGrant',
    'SkipVPCGrant',
    'AdoptStackResource',
    'IgnoreStackDeclaration',
    'IgnoreStackClaim',
    'ApplyDryRun',
    'AcceptUnconfirmedPersistence',
    'MoveAttachedGateway',
    'AttachToOccupiedVPC',
    'DetachWrongPair',
    'DetachReferencedGateway',
    'AcceptStaleExact',
    'IgnoreExactReadback',
    'AcceptPartialOrStaleList',
    'PublishBeforeVerifiedList',
}
REQUIRED_SOURCE_SCOPES['EC2InternetGatewayAttachment'] = {
    'internal/aws/ec2_internet_gateway_attachment_store.go',
    'internal/aws/ec2_internet_gateway_store.go',
    'internal/aws/ec2_internet_gateways.go',
    'internal/aws/ec2_read.go',
    'internal/aws/ec2_vpc.go',
    'tools/console/src/ec2-internet-gateways.mjs',
    'tools/console/src/ec2-vpc-internet-gateways.jsx',
    'internal/server/web/app.js',
}

# Native IPv4 default route write and exact current-scope readback.
BASE_MODELS.add('EC2NativeDefaultRouteLifecycle')
BASE_INVARIANTS['EC2NativeDefaultRouteLifecycle'] = {
    'TypeOK',
    'OwnedGraph',
    'GrantAndScope',
    'AtomicEffect',
    'ExactReadback',
    'VerifiedNotice',
}
REQUIRED_MUTANTS['EC2NativeDefaultRouteLifecycle'] = {
    'AdmitStaleOrForeignActor',
    'IgnoreRouteTableGrant',
    'AdoptCFNRouteTable',
    'AdoptCFNGateway',
    'IgnoreOwnershipClaim',
    'IgnoreStackDeclaration',
    'ApplyDryRun',
    'ApplyAtQuota',
    'AcknowledgeFailedReadback',
    'AllowForeignGatewayTarget',
    'AllowDetachedGatewayTarget',
    'OverwriteExistingRoute',
    'DeleteAbsentRoute',
    'OrphanSavedRoute',
    'AcceptOldScopeRead',
    'AcceptPartialRead',
    'AcceptDifferentTable',
    'AcceptWrongSavedTarget',
    'PublishBeforeRead',
    'KeepNoticeOnScopeChange',
}
REQUIRED_SOURCE_SCOPES['EC2NativeDefaultRouteLifecycle'] = {
    'internal/aws/ec2_internet_gateway_attachment_store.go',
    'internal/aws/ec2_internet_gateway_store.go',
    'internal/aws/ec2_native_default_route_query.go',
    'internal/aws/ec2_native_default_route_store.go',
    'internal/aws/ec2_read.go',
    'internal/aws/ec2_route_tables_native.go',
    'internal/aws/ec2_route_tables_read.go',
    'internal/aws/ec2_vpc.go',
    'internal/server/server.go',
    'internal/server/web/app.js',
    'tools/console/src/ec2-native-route-tables.mjs',
    'tools/console/src/ec2-readonly.jsx',
    'tools/console/src/ec2-vpc-native-route-tables.jsx',
    'tools/console/src/main.jsx',
}

BASE_MODELS.add('EC2NativeIPv4RouteSet')
BASE_INVARIANTS['EC2NativeIPv4RouteSet'] = {
    'TypeOK',
    'OwnedCanonical',
    'PerTableLimit',
    'AtomicClass',
    'ExactRouteEffect',
    'CompleteReadback',
    'ConfirmedNotice',
}
REQUIRED_MUTANTS['EC2NativeIPv4RouteSet'] = {
    'AdmitReservedDestination',
    'SkipAliasCanonicalization',
    'AdmitNonExactDelete',
    'ReplaceCanonicalDuplicate',
    'SkipTableCapacity',
    'AdoptForeignOwner',
    'AcceptWrongGateway',
    'ApplyDeniedWrite',
    'ApplyDryRun',
    'CommitWithoutStorageReadback',
    'EraseSiblingRoute',
    'IgnoreSiblingReadback',
    'ConfirmWrongTable',
    'ConfirmIncompleteReply',
    'ReuseEarlierScope',
    'PublishWithoutReadback',
}
REQUIRED_SOURCE_SCOPES['EC2NativeIPv4RouteSet'] = {
    'internal/aws/ec2_native_default_route_query.go',
    'internal/aws/ec2_native_default_route_store.go',
    'internal/aws/ec2_internet_gateway_store.go',
    'internal/aws/ec2_vpc.go',
    'internal/aws/ec2_route_tables_native.go',
    'internal/aws/ec2_route_tables_read.go',
    'internal/server/server.go',
    'internal/server/web/app.js',
    'tools/console/src/ec2-ipv4-route-prefix.mjs',
    'tools/console/src/ec2-native-route-tables.mjs',
    'tools/console/src/ec2-readonly.jsx',
    'tools/console/src/ec2-response.mjs',
    'tools/console/src/ec2-route-tables.mjs',
    'tools/console/src/ec2-vpc-native-route-tables.jsx',
    'tools/console/src/main.jsx',
}

BASE_MODELS.add('EC2RouteTableInventoryPages')
BASE_INVARIANTS['EC2RouteTableInventoryPages'] = {
    'TypeOK',
    'PageAuthorized',
    'PageBounds',
    'ScopedContinuation',
    'CurrentAssembly',
    'CompletePublication',
    'ExactWriteReadback',
}
REQUIRED_MUTANTS['EC2RouteTableInventoryPages'] = {
    'RequirePageIAM',
    'RequireBoundActor',
    'AdmitExactIDWithPaging',
    'AdmitInvalidPageSize',
    'SkipVerifiedRegionalGraph',
    'IgnoreEncodedByteBound',
    'OverfullTerminalPage',
    'AdmitEmptyContinuation',
    'AdmitShortNonterminalPage',
    'SkipCursorSeal',
    'ReuseAcrossRegion',
    'ReuseOtherActor',
    'ChangeSubnetsOnResume',
    'ChangeLimitOnResume',
    'ContinueAcrossSavedSnapshots',
    'AdmitRepeatedOrDisorderedRows',
    'UseRetiredScopeReply',
    'PublishBeforeTerminal',
    'ConfirmWriteWithWrongSavedState',
}
REQUIRED_SOURCE_SCOPES['EC2RouteTableInventoryPages'] = {
    'internal/aws/cloudformation.go',
    'internal/aws/ec2_read.go',
    'internal/aws/ec2_vpc.go',
    'internal/aws/ec2_route_tables_read.go',
    'internal/aws/ec2_route_table_page.go',
    'internal/server/web/app.js',
    'tools/console/src/ec2-response.mjs',
    'tools/console/src/ec2-route-table-pages.mjs',
    'tools/console/src/ec2-route-tables.mjs',
    'tools/console/src/ec2-native-route-tables.mjs',
    'tools/console/src/ec2-vpc-route-tables.jsx',
    'tools/console/src/ec2-vpc-native-route-tables.jsx',
}

BASE_MODELS.add('EC2NativeRouteAssociationReplacement')
BASE_INVARIANTS['EC2NativeRouteAssociationReplacement'] = {
    'TypeOK',
    'ExactOutcome',
    'NoEffectOnRefusal',
    'OneExplicit',
    'ExactMove',
    'OtherStateUnchanged',
    'ConfirmedNotice',
    'RetiredRetryNeverApplies',
}
REQUIRED_MUTANTS['EC2NativeRouteAssociationReplacement'] = {
    'RequireScopedSession',
    'RequireResolvedRegion',
    'RetainCFNIncumbent',
    'RequireDestinationGrant',
    'RequireIncumbentGrant',
    'RequireSubnetGrant',
    'RequireDistinctDestination',
    'RequireSameVPC',
    'RetainCFNDestination',
    'RequireNoCFNClaim',
    'RequireNoCFNDeclaration',
    'RejectStorageFault',
    'RejectDurableDrift',
    'DryRunNeverWrites',
    'RemoveRetiredID',
    'GenerateDistinctID',
    'PreserveSibling',
    'NoRouteChanges',
    'RequireReadback',
    'RequireCompleteReadback',
    'RequireSourceReadback',
    'RequireExactNewReadback',
    'RequireRetiredAbsence',
    'RequireSiblingReadback',
    'RequireCurrentView',
    'RejectRetiredReplay',
}
REQUIRED_SOURCE_SCOPES['EC2NativeRouteAssociationReplacement'] = {
    'internal/aws/ec2_read.go',
    'internal/aws/ec2_network.go',
    'internal/aws/ec2_internet_gateway_store.go',
    'internal/aws/ec2_route_association_native_query.go',
    'internal/aws/ec2_route_association_native_store.go',
    'internal/aws/ec2_route_association_replace.go',
    'internal/aws/ec2_route_tables_native.go',
    'internal/aws/ec2_route_tables_read.go',
    'internal/server/server.go',
    'internal/server/web/app.js',
    'tools/console/src/ec2-native-route-tables.mjs',
    'tools/console/src/ec2-route-table-pages.mjs',
    'tools/console/src/ec2-readonly.jsx',
    'tools/console/src/ec2-vpc-native-route-tables.jsx',
    'tools/console/src/main.jsx',
}


BASE_MODELS.add('EC2NativeSubnetPublicIPv4')
BASE_INVARIANTS['EC2NativeSubnetPublicIPv4'] = {
    'TypeOK',
    'ExactAdmission',
    'RefusalNoEffect',
    'ExactPreference',
    'SameFrame',
    'CurrentConfirmedNotice',
    'NoReplay',
}
REQUIRED_MUTANTS['EC2NativeSubnetPublicIPv4'] = {
    'RequireSubnetGrant',
    'RejectScopedHandoff',
    'RequireSameRegion',
    'RequireExistingSubnet',
    'KeepStackOwnership',
    'RejectRetainedClaim',
    'RejectRetainedDeclaration',
    'RequireBooleanInput',
    'RejectStoreFailure',
    'RejectStorageDrift',
    'NeverPersistRefusal',
    'PersistTheRequestedBoolean',
    'DoNotAdvanceIdempotentRevision',
    'PreserveSiblingAndTopology',
    'RequirePostWriteRead',
    'RequireCompleteRead',
    'RequireSameTarget',
    'RequireRequestedValue',
    'RequireCurrentView',
    'NoRetryOnRefresh',
}
REQUIRED_SOURCE_SCOPES['EC2NativeSubnetPublicIPv4'] = {
    'internal/aws/ec2_internet_gateway_store.go',
    'internal/aws/ec2_network.go',
    'internal/aws/ec2_read.go',
    'internal/aws/ec2_subnet_native.go',
    'internal/aws/ec2_vpc.go',
    'internal/server/server.go',
    'internal/server/web/app.js',
    'tools/console/src/ec2-native-subnets.mjs',
    'tools/console/src/ec2-readonly.jsx',
    'tools/console/src/ec2-vpc-native-subnets.jsx',
    'tools/console/src/main.jsx',
}


BASE_MODELS.add('EC2NativeVPCMainRoute')
BASE_INVARIANTS['EC2NativeVPCMainRoute'] = {
    'TypeOK',
    'AtomicPairedSlot',
    'OwnedLocalOnly',
    'NoImplicitWire',
    'CountersExcludeLocal',
    'RetainedFencesDelete',
    'ReadInvalidRefuses',
    'UnrelatedOwnershipUnchanged',
    'EffectiveNeedsFreshInventories',
    'EffectiveOnlyUnassociated',
}
REQUIRED_MUTANTS['EC2NativeVPCMainRoute'] = {
    'CreateUnpaired',
    'OmitAutomaticLocal',
    'DeleteMainDirect',
    'DropReferenceFence',
    'DropFinalFence',
    'InventExplicitBinding',
    'ChargeAutomaticRoute',
    'IgnoreIndependentDeclaration',
    'RewriteLegacy',
    'AdmitEffectiveDeniedSubnets',
    'AdmitEffectivePartialTables',
    'AdmitEffectiveStaleScope',
    'IgnoreUnconfirmedEffectiveOwnership',
    'DoubleCountExplicitSubnet',
}
REQUIRED_SOURCE_SCOPES['EC2NativeVPCMainRoute'] = {
    'internal/aws/ec2_native_default_route_store.go',
    'internal/aws/ec2_native_vpc_main_route.go',
    'internal/aws/ec2_network.go',
    'internal/aws/ec2_route_tables_native.go',
    'internal/aws/ec2_route_tables_native_query.go',
    'internal/aws/ec2_route_tables_read.go',
    'internal/aws/ec2_vpc.go',
    'internal/server/web/app.js',
    'tools/console/src/ec2-native-route-tables.mjs',
    'tools/console/src/ec2-readonly.jsx',
    'tools/console/src/ec2-response.mjs',
    'tools/console/src/ec2-route-table-pages.mjs',
    'tools/console/src/ec2-route-tables.mjs',
    'tools/console/src/ec2-vpc-native-route-tables.jsx',
}


BASE_MODELS.add('EC2InstanceStackConsole')
BASE_INVARIANTS['EC2InstanceStackConsole'] = {'NoGuestLivenessClaim',
 'QualifiedMutation',
 'ReceiptBelongsToView',
 'SingleMutation',
 'TypeOK',
 'VerifiedMeansMatch'}
REQUIRED_MUTANTS['EC2InstanceStackConsole'] = {'AcceptRetiredReceipt',
 'ApproveMismatchedRead',
 'InventGuestLiveness',
 'MutateOnCheckAgain',
 'OfferParticipant',
 'OmitCurrentCapability',
 'ReceiptAsCompletion'}
REQUIRED_SOURCE_SCOPES['EC2InstanceStackConsole'] = {'internal/aws/ec2_cloudformation_runtime_unix.go',
 'internal/server/battle_runtime_unix.go',
 'internal/server/ec2_instance_stack_launch.go',
 'internal/server/sakura_provider.go',
 'internal/server/server.go',
 'internal/server/web/app.js',
 'tools/console/src/ec2-instance-stack-launch.jsx',
 'tools/console/src/ec2-instance-stack-launch.mjs',
 'tools/console/src/ec2-readonly.jsx',
 'tools/console/src/main.jsx'}
EC2INSTANCESTACKCONSOLE_MUTATIONS = [{'id': 'OmitCurrentCapability',
  'edits': [{'old': '    /\\ view.allow\n    /\\ view.writes = 0',
             'new': '    /\\ TRUE\n    /\\ view.writes = 0',
             'count': 1}],
  'expect_invariant': 'QualifiedMutation'},
 {'id': 'OfferParticipant',
  'edits': [{'old': '!.allow = Eligible, !.qualified = Eligible',
             'new': '!.allow = (actor = "participant" /\\ liveConfig) \\/ Eligible, !.qualified = Eligible',
             'count': 1}],
  'expect_invariant': 'QualifiedMutation'},
 {'id': 'AcceptRetiredReceipt',
  'edits': [{'old': 'AcceptReceipt ==\n    /\\ flight.kind = "write"\n    /\\ flight.epoch = epoch',
             'new': 'AcceptReceipt ==\n    /\\ flight.kind = "write"\n    /\\ TRUE',
             'count': 1}],
  'expect_invariant': 'ReceiptBelongsToView'},
 {'id': 'ReceiptAsCompletion',
  'edits': [{'old': '!.phase = "accepted", !.receiptFrom = flight.epoch',
             'new': '!.phase = "verified", !.receiptFrom = flight.epoch',
             'count': 1}],
  'expect_invariant': 'VerifiedMeansMatch'},
 {'id': 'ApproveMismatchedRead',
  'edits': [{'old': 'IF completeMatch THEN "verified" ELSE "unconfirmed"', 'new': '"verified"', 'count': 1}],
  'expect_invariant': 'VerifiedMeansMatch'},
 {'id': 'MutateOnCheckAgain',
  'edits': [{'old': 'view\' = [view EXCEPT !.phase = "checking"]',
             'new': 'view\' = [view EXCEPT !.phase = "checking", !.writes = @ + 1]',
             'count': 1}],
  'expect_invariant': 'SingleMutation'},
 {'id': 'InventGuestLiveness',
  'edits': [{'old': '!.evidence = completeMatch, !.guestClaim = FALSE',
             'new': '!.evidence = completeMatch, !.guestClaim = TRUE',
             'count': 1}],
  'expect_invariant': 'NoGuestLivenessClaim'}]


# Selected-VPC DNS and Resource Map are separate formal obligations.
BASE_MODELS.add('EC2VPCDNSAttributes')
BASE_INVARIANTS['EC2VPCDNSAttributes'] = {
    'TypeOK',
    'ExactReceipt',
    'Frame',
    'RefusalOrReadUnchanged',
    'ExactSetting',
    'NoSpuriousRevision',
}
REQUIRED_MUTANTS['EC2VPCDNSAttributes'] = {
    'IgnoreExactVpcGrant',
    'WriteStackVpc',
    'IgnoreDNSDependency',
    'AlwaysAdvanceGeneration',
}
REQUIRED_SOURCE_SCOPES['EC2VPCDNSAttributes'] = {
    'internal/aws/ec2_native_vpc_main_repair.go',
    'internal/aws/ec2_native_vpc_main_route.go',
    'internal/aws/ec2_network.go',
    'internal/aws/ec2_read.go',
    'internal/aws/ec2_vpc.go',
    'internal/aws/ec2_vpc_dns.go',
}


BASE_MODELS.add('EC2VpcResourceMap')
BASE_INVARIANTS['EC2VpcResourceMap'] = {
    'TypeOK',
    'AssociationQualityExact',
    'ExplicitNeedsEvidence',
    'MainNeedsWitness',
    'ExactAttachedGatewayEdges',
    'GatewayFailureNotEmpty',
    'GatewayHoleReported',
}
REQUIRED_MUTANTS['EC2VpcResourceMap'] = {
    'IgnoreAssociationConflict',
    'InventExplicitForAbsent',
    'InventUnseenMain',
    'AcceptStackNativeMarkers',
    'AllowDetachedOrPeerRoute',
    'IgnoreAttachmentWitness',
    'TreatDeniedAsEmpty',
    'HidePartialMissingTarget',
    'DropOtherConfirmedRoute',
}
REQUIRED_SOURCE_SCOPES['EC2VpcResourceMap'] = {
    'internal/server/web/app.js',
    'tools/console/src/ec2-vpc-resource-map.jsx',
    'tools/console/src/ec2-vpc-topology-read.mjs',
}


BASE_MODELS.add('EC2LegacyNativeMainRepair')
BASE_INVARIANTS['EC2LegacyNativeMainRepair'] = {
    'TypeOK',
    'AtomicRepair',
    'OneStoredRow',
    'ExactAdmission',
    'NoPartialRefusal',
    'PlanAndRepeatAreReadOnly',
    'RetainedFrame',
}
REQUIRED_MUTANTS['EC2LegacyNativeMainRepair'] = {
    'PartialNativeMain',
    'CountSyntheticAssociation',
    'SkipOperatorOwnership',
    'AllowAmbiguousOwner',
    'UseOriginalDirectly',
    'IgnoreStalePlan',
    'IgnoreFullQuota',
    'AllowDependentCascade',
    'AcknowledgeLateRefusal',
    'CommitRefusedPartial',
    'PlanWrites',
    'ReapplyWrites',
    'AlterRetainedAssociation',
}
REQUIRED_SOURCE_SCOPES['EC2LegacyNativeMainRepair'] = {
    'cmd/simulator/ec2_native_main_route_repair.go',
    'cmd/simulator/ec2_native_main_route_repair_other.go',
    'cmd/simulator/ec2_native_main_route_repair_posix.go',
    'cmd/simulator/main.go',
    'internal/aws/ec2_native_vpc_main_repair.go',
    'internal/aws/ec2_native_vpc_main_route.go',
    'internal/aws/ec2_network.go',
    'internal/aws/ec2_route_tables_native.go',
    'internal/aws/ec2_route_tables_read.go',
    'internal/aws/ec2_vpc.go',
}


# These signed-base models were already registered but omitted from the required guards.
# Pin the existing rows exactly; this does not change an invariant, mutant, or implementation.
BASE_MODELS.update({
    'BattleDisruptionRoleCleanup',
    'EC2RegionalSubnetConsole',
    'EC2VPCCreateForm',
    'EC2VpcConsoleNavigation',
})
BASE_INVARIANTS.update({
    'BattleDisruptionRoleCleanup': {
        'ForeignStatePreserved',
        'KnownRoleCleanedAfterFailedWork',
        'NoDeleteWithInlinePolicy',
        'NoFailedGradeReport',
        'NoUnconfirmedCleanupReport',
        'TypeOK',
    },
    'EC2RegionalSubnetConsole': {
        'ConfirmedInventoryBeforeWrite',
        'CurrentScopeOnly',
        'NoUnreconciledRetry',
        'OnlyNativeParentWrites',
        'ReadbackBeforeDetail',
        'TypeOK',
    },
    'EC2VPCCreateForm': {
        'ConfirmedBeforeSuccess',
        'DiscardRetiredRead',
        'InvalidDraftNeverWrites',
        'NativeAndStackRemainDistinct',
        'PairedNeedsOperator',
        'TypeOK',
    },
    'EC2VpcConsoleNavigation': {
        'AdvertisedOnly',
        'CurrentRegionAndView',
        'CurrentWriteOwnsLease',
        'NoLocalDepartureDuringWrite',
        'TypeOK',
        'VerifiedDisplay',
    },
})
REQUIRED_MUTANTS.update({
    'BattleDisruptionRoleCleanup': {
        'DeleteDifferentIdentity',
        'IgnoreMissingRoleReadback',
        'ReportEvenAfterFailedGrading',
        'SkipCleanupOnGradeException',
        'SkipInlinePolicyRemoval',
    },
    'EC2RegionalSubnetConsole': {
        'AcceptRetiredView',
        'AllowStackParent',
        'RetryUnknownWrite',
        'ShowUnverifiedDetail',
        'WriteWithoutRegionalInventory',
    },
    'EC2VPCCreateForm': {
        'OfferPairedToNativeOnly',
        'ShowSuccessWithoutConfirmation',
        'SubmitInvalidDraft',
        'TreatStackAsNative',
        'UseRetiredRead',
    },
    'EC2VpcConsoleNavigation': {
        'AcceptRetiredView',
        'AllowLocalDeparture',
        'DispatchUnadvertised',
        'DisplayUnverified',
        'ReleaseNewerLease',
    },
})
REQUIRED_SOURCE_SCOPES.update({
    'BattleDisruptionRoleCleanup': {
        'test/battle/image/test_rehearsal_operator_role.py',
        'test/battle/rehearsal.py',
    },
    'EC2RegionalSubnetConsole': {
        'internal/server/web/app.js',
        'test/console/ec2_subnets_console_browser.cjs',
        'tools/console/src/ec2-native-subnets.mjs',
        'tools/console/src/ec2-subnet-console-route.mjs',
        'tools/console/src/ec2-subnets-console.jsx',
        'tools/console/src/main.jsx',
        'tools/console/test/ec2-subnet-console.test.mjs',
    },
    'EC2VPCCreateForm': {
        'internal/server/web/app.js',
        'test/console/ec2_vpc_create_browser.cjs',
        'tools/console/src/ec2-readonly.jsx',
        'tools/console/src/ec2-vpc-create.jsx',
        'tools/console/src/ec2-vpc-subnet-fields.jsx',
        'tools/console/src/ec2-vpc-subnet.mjs',
        'tools/console/src/main.jsx',
        'tools/console/test/ec2-vpc-subnet.test.mjs',
    },
    'EC2VpcConsoleNavigation': {
        'internal/server/web/app.js',
        'test/console/ec2_vpc_network_navigation_browser.cjs',
        'tools/console/src/ec2-internet-gateway-console.mjs',
        'tools/console/src/ec2-internet-gateway-page.jsx',
        'tools/console/src/ec2-network-acl-inventory.jsx',
        'tools/console/src/ec2-network-acls.mjs',
        'tools/console/src/ec2-readonly.jsx',
        'tools/console/src/ec2-route-table-console.jsx',
        'tools/console/src/ec2-route-table-console.mjs',
        'tools/console/src/ec2-security-group-inventory.mjs',
        'tools/console/src/ec2-security-groups-console.jsx',
        'tools/console/src/ec2-subnets-console.jsx',
        'tools/console/src/ec2-vpc-native-route-tables.jsx',
        'tools/console/src/ec2-vpc-navigation.mjs',
        'tools/console/src/ec2-vpc-resource-view.jsx',
        'tools/console/src/ec2-vpc-security-groups.jsx',
        'tools/console/src/main.jsx',
        'tools/console/test/ec2-vpc-navigation.test.mjs',
    },
})


# Exact detached ACL entry metadata rules, not the ACL lifecycle or packet path.
BASE_MODELS.add('EC2DetachedNetworkACLRules')
BASE_INVARIANTS['EC2DetachedNetworkACLRules'] = {
    'TypeOK',
    'ScopeFrame',
    'OnlyEditablePersisted',
    'AtomicGeneration',
    'ExactNativeOperation',
    'ExactReadProjection',
    'VerifiedSelectedConsole',
}
REQUIRED_MUTANTS['EC2DetachedNetworkACLRules'] = {
    'AllowForeignRegion',
    'AllowForeignOwnedRow',
    'AllowWrongParent',
    'AllowPersistentCFNClaim',
    'AllowAttachedRow',
    'AllowCorruptGeneration',
    'AllowDifferentIAMAction',
    'AllowDifferentIAMResource',
    'IgnoreIndependentCFNDeclaration',
    'AllowExtraneousQueryShape',
    'ModifyPeerFrame',
    'TreatTerminalAsEditable',
    'LosePerDirectionLimit',
    'KeepUnknownIPv4HostBits',
    'AllProtocolKeepsPorts',
    'AllowTCPWithoutPortPair',
    'AllowReversedUDPPorts',
    'AllowNonIPv4Prefix',
    'AllowUnsupportedProtocol',
    'CountsEqualReplaceAsUpdate',
    'OnlyIncrementSQLGeneration',
    'ForgetUpdatedEmptyRecord',
    'KeyDropsDirection',
    'CreateOverwritesExistingNumber',
    'ReplaceCreatesMissingSlot',
    'IgnoreFailedSQLWrite',
    'IgnoreExactRowReread',
    'IgnorePostWriteClaim',
    'IgnoreGenerationOverflow',
    'AcknowledgeCommitRefusal',
    'ReadStaleSlots',
    'ReturnWrongTerminalVerdict',
    'ProjectUnorderedEditableRows',
    'InventSubnetAssociation',
    'ClaimPacketEnforcement',
    'AllowUnadvertisedConsole',
    'SkipExactPreWriteRule',
    'IgnoreConsoleDirectionCapacity',
    'AcceptFalseWriteReturn',
    'AcceptOtherACLReadback',
    'AcceptDifferentRuleReadback',
    'PublishRetiredConsoleResult',
}
REQUIRED_SOURCE_SCOPES['EC2DetachedNetworkACLRules'] = {
    'internal/aws/ec2_network.go',
    'internal/aws/ec2_network_acl.go',
    'internal/aws/ec2_network_acl_entry.go',
    'internal/aws/ec2_network_acl_store.go',
    'internal/aws/ec2_read.go',
    'internal/aws/ec2_vpc.go',
    'internal/server/server.go',
    'internal/server/web/app.js',
    'tools/console/src/ec2-network-acl-inventory.jsx',
    'tools/console/src/ec2-network-acls.mjs',
}

# Exact one-tag native VPC metadata and the selected Console receipt.
BASE_MODELS.add('EC2NativeVPCTags')
BASE_INVARIANTS['EC2NativeVPCTags'] = {
    'TypeOK',
    'ExactNativeAdmission',
    'ExactTagEffect',
    'AtomicTagCommit',
    'NonTagFrame',
    'ExactConsoleRequest',
    'VerifiedConsoleOutcome',
    'NoConfirmationRollback',
}
REQUIRED_MUTANTS['EC2NativeVPCTags'] = {
    'IgnoreRegionalTarget',
    'IgnoreVPCIdentity',
    'IgnoreGrantedAction',
    'IgnoreGrantedRegion',
    'IgnoreGrantedVPC',
    'AdmitSecondResource',
    'AdmitSecondTag',
    'AdmitMissingTagKey',
    'AdmitInvalidTagText',
    'AdmitCFNOwner',
    'IgnorePersistentCFNClaim',
    'IgnoreIndependentCFNDeclaration',
    'IgnoreMainSlotFence',
    'RewriteNoncanonicalDocument',
    'IgnoreGenerationWitness',
    'AllowReservedPrefix',
    'AllowOmittedCreate',
    'AllowFiftyFirstTag',
    'RejectReplacementAtQuota',
    'TreatEmptyDeleteAsOmitted',
    'RefuseKeyOnlyDeletion',
    'DeleteDifferentValue',
    'PhysicallyRewriteIdenticalTag',
    'PhysicallyRewriteMissingDeletion',
    'IgnoreExactCASDocument',
    'IgnoreExactlyOneAffectedRow',
    'IgnoreDocumentReread',
    'IgnoreOwnershipReread',
    'AcknowledgeWithoutCommit',
    'KeepUncommittedDocument',
    'AssumeConsolePreReadIsCAS',
    'AdvanceTopologyRevisionForTag',
    'AdvanceDNSGenerationForTag',
    'DropNeighborTagFromDocument',
    'RetagPeerVPC',
    'OperateUnadvertisedConsole',
    'OperateScopedParticipantConsole',
    'IgnoreOtherActorCapture',
    'IgnoreOtherPaneLease',
    'SkipFreshObservedValue',
    'IgnoreScopeOfPreRead',
    'UseKeyOnlyConsoleRemoval',
    'PublishOnRefusedMutation',
    'AcceptFalseReturn',
    'PublishRetiredResult',
    'ReadbackFromWrongRegion',
    'ReadbackFromOtherVPC',
    'ReadbackFromOtherKey',
    'ReadbackFromWrongOwner',
    'SkipExpectedTagReadback',
    'DisplayDraftAsVerifiedName',
    'UndoCommittedTagOnUnconfirmedUI',
}
REQUIRED_SOURCE_SCOPES['EC2NativeVPCTags'] = {
    'internal/aws/ec2_read.go',
    'internal/aws/ec2_vpc.go',
    'internal/aws/ec2_vpc_tag.go',
    'internal/server/server.go',
    'internal/server/web/app.js',
    'tools/console/src/ec2-readonly.jsx',
    'tools/console/src/ec2-vpc-tags-panel.jsx',
    'tools/console/src/ec2-vpc-tags.mjs',
}

# Native subnet tags: selected parent/row, changed-row fence and exact Console receipt.
BASE_MODELS.add('EC2NativeSubnetTags')
BASE_INVARIANTS['EC2NativeSubnetTags'] = {
    'TypeOK',
    'SubnetAndParentFences',
    'SingleDocumentCommit',
    'UnchangedNetworkFrame',
    'ExactNativeTagProjection',
    'ExactSelectedSubnetRequest',
    'ExactSelectedSubnetConfirmation',
    'NoUnconfirmedUndo',
}
REQUIRED_MUTANTS['EC2NativeSubnetTags'] = {
    'IgnoreRegionalSubnetTarget',
    'IgnoreExactSubnetTarget',
    'AllowMismatchedTagAction',
    'AllowVPCGrantForSubnetTag',
    'AllowOtherRegionSubnetGrant',
    'AllowCloudFormationSubnet',
    'IgnoreSubnetOwnershipClaim',
    'IgnoreChildCanonicalCIDR',
    'IgnoreChildCIDRContainment',
    'IgnoreRegionalAZ',
    'AllowMissingNativeParent',
    'AllowForeignOwnedParent',
    'IgnoreParentOwnershipClaim',
    'IgnoreParentCanonicalCIDR',
    'IgnoreDeclaredSubnet',
    'IgnoreDeclaredParent',
    'IgnoreVPCMainSlot',
    'IgnoreParentDNSGeneration',
    'RewriteNoncanonicalSubnetDocument',
    'IgnoreExactPriorSubnetRow',
    'IgnoreCollateralWriteCount',
    'IgnoreExactSubnetReload',
    'AcknowledgeFailedSubnetCommit',
    'RewriteNoOpSubnetTag',
    'AdvanceSubnetRevisionForTag',
    'ChangePublicIPv4Preference',
    'AlterExistingRouteAssociation',
    'InventoryDropsStoredTag',
    'InventWriteOnlyRequirementOnInventory',
    'ShowUnprovedSubnetInventory',
    'PreReadIgnoresParent',
    'PreReadIgnoresCIDR',
    'PreReadIgnoresAZ',
    'ReuseStaleCaptureTag',
    'RunScopedHandoffTagPane',
    'IgnoreSubnetTagCapability',
    'AllowBusyTagPane',
    'SendKeyOnlySubnetConsoleDelete',
    'ReadbackIgnoresRegion',
    'ReadbackIgnoresSubnet',
    'ReadbackIgnoresParent',
    'ReadbackIgnoresCIDR',
    'ReadbackIgnoresAZ',
    'ReadbackIgnoresDuplicateSubnet',
    'ReadbackIgnoresSavedTagValue',
    'AcceptFalseSubnetTagAck',
    'ShowWithoutCommittedSubnetTag',
    'PublishRetiredSubnetSelection',
    'UndoTagAfterFailedConsoleReadback',
}
REQUIRED_SOURCE_SCOPES['EC2NativeSubnetTags'] = {
    'internal/aws/ec2_read.go',
    'internal/aws/ec2_subnet_native.go',
    'internal/aws/ec2_subnet_tag.go',
    'internal/aws/ec2_vpc.go',
    'internal/aws/ec2_vpc_tag.go',
    'internal/server/server.go',
    'internal/server/web/app.js',
    'tools/console/src/ec2-native-subnets.mjs',
    'tools/console/src/ec2-subnets-console.jsx',
    'tools/console/src/ec2-vpc-resource-view.jsx',
    'tools/console/src/ec2-vpc-tags-panel.jsx',
}


# Local SSO is deliberately separate from management-only Cognito and verifier-only Entra.
BASE_MODELS.add('LocalSSOSyntheticLifecycle')
BASE_INVARIANTS['LocalSSOSyntheticLifecycle'] = {
    'TypeOK',
    'ExplicitOnly',
    'ReadyAfterPrivateBootstrap',
    'NoPublicCredentialValues',
    'PinnedPromptOnly',
    'OneShotPerFixture',
    'IssuanceBoundToBrowserAndCode',
    'ExactACSWitness',
    'ACSConsumesPendingAndMarksReplay',
    'BoundSingleExchange',
    'AppRequiresVerifiedCallback',
    'RetiredHasNoLiveState',
}
REQUIRED_MUTANTS['LocalSSOSyntheticLifecycle'] = {
    'PublishByDefault',
    'PublishDuringBootstrap',
    'PublishUnverifiedBootstrap',
    'PublishOperatorListener',
    'HideReadyEndpoints',
    'PublishCodeValue',
    'PromptForUnexpectedRequest',
    'ReuseOneShotCode',
    'AcceptDifferentCode',
    'AcceptDifferentBrowser',
    'OmitReplayReceipt',
    'RetainCognitoPending',
    'KeepAuthorizationCode',
    'SkipTokenExchange',
    'RetainPrivateRunFiles',
    'LeaveListenerOnRetirement',
    'AcceptDifferentSAMLOrigin',
    'AcceptDifferentSigningPin',
    'AcceptOtherRequest',
    'AcceptOtherACSBrowser',
    'GrantWithStaleRegistration',
    'GrantWithUnusablePrincipal',
    'IgnorePKCE',
    'ExchangeForOtherClient',
    'ExchangeForOtherCallback',
    'ExchangeWithoutAppBrowserState',
    'ExchangeExpiredCode',
    'AcceptWrongOIDCNonce',
    'AcceptWrongOIDCIssuerOrAudience',
    'AcceptInvalidAppTokenSignature',
}
REQUIRED_SOURCE_SCOPES['LocalSSOSyntheticLifecycle'] = {
    'cmd/simulator/local_sso.go',
    'cmd/simulator/local_sso_relying_party.go',
    'cmd/simulator/main.go',
    'go.mod',
    'go.sum',
    'internal/aws/cognitoidp/federation_config.go',
    'internal/aws/cognitoidp/federation_metadata.go',
    'internal/aws/cognitoidp/federation_publisher.go',
    'internal/aws/cognitoidp/hosted_browser.go',
    'internal/aws/cognitoidp/hosted_completion.go',
    'internal/aws/cognitoidp/hosted_profile.go',
    'internal/aws/cognitoidp/hosted_saml.go',
    'internal/aws/cognitoidp/hosted_state.go',
    'internal/azure/entraidp/directory.go',
    'internal/azure/entraidp/fixture.go',
    'internal/azure/entraidp/saml.go',
    'internal/server/cognito_hosted.go',
    'internal/server/server.go',
}

BASE_MODELS.add('StoreConnectionLifetime')
BASE_INVARIANTS['StoreConnectionLifetime'] = {
    'TypeOK', 'PeerBackingUnchanged', 'ConnectionTurnoverPreserves',
    'CancelDiscardsStaged', 'DisposedBackingReleased', 'ClosedStoresUnavailable',
    'ConnectionPragmasApplied', 'LiveCommittedData',
}
REQUIRED_MUTANTS['StoreConnectionLifetime'] = {
    'ConnectionOwnsBacking', 'CanceledWriteCommitted', 'CanceledStageRetained',
    'SharedStoreBacking', 'DisposedBackingRetained', 'ReconnectClosedStore',
    'MissingReconnectPragmas',
}
REQUIRED_SOURCE_SCOPES['StoreConnectionLifetime'] = {
    'internal/core/store.go', 'internal/core/store_connection_test.go',
}

BASE_MODELS.add('CloudTraceObservations')
BASE_INVARIANTS['CloudTraceObservations'] = {
    'TypeOK', 'FirstPageOrdered', 'PendingInvisible', 'AdmitEligible', 'CallerBound',
    'ValidBatchOnly', 'ProjectBound', 'PeerUnchanged', 'AllObservationsCommitted',
    'RollbackUnchanged', 'GetCurrent', 'NextBound', 'NextStableKey', 'NextStableBody',
}
REQUIRED_MUTANTS['CloudTraceObservations'] = {
    'ArrivalOrderedPage', 'ExposePendingPrefix', 'RejectValidRepeat', 'AdmitWrongOwner',
    'AdmitWrongProject', 'SkipFinalValidation', 'CountOnlyFirstObservation',
    'ShareCommitWithPeer', 'CommitPrefix', 'CoalesceIdenticalObservations',
    'CancelPublishesPrefix', 'RefusalPublishesPrefix', 'GetUsesCursorHighWater',
    'GetDropsRepeatedSpans', 'AllowPeerContinuation', 'AcceptForeignCursor',
    'ReuseCursorForOtherView', 'IncludeNewTraceOnNextPage', 'RepeatLastTraceOnNextPage',
    'ProjectCurrentSpanBodies', 'ExposeMinimalSpanBodies',
}
REQUIRED_SOURCE_SCOPES['CloudTraceObservations'] = {
    'internal/gcp/trace/store.go', 'internal/gcp/trace/model.go', 'internal/gcp/trace/http.go',
    'internal/server/cloud_storage.go', 'internal/gcp/internal/jsonwire/unicode.go',
}


# Fixed checks for locally stored customer cases and staged attachments.
BASE_MODELS.add('SupportCaseLifecycle')
BASE_INVARIANTS['SupportCaseLifecycle'] = {
    'TypeOK',
    'PeerFrame',
    'StageContract',
    'SubmissionContract',
    'ResolutionContract',
    'ReadProjection',
    'RestartAndClock',
}
REQUIRED_SOURCE_SCOPES['SupportCaseLifecycle'] = {
    'internal/aws/s3.go',
    'internal/aws/ssm.go',
    'internal/aws/support_gateway.go',
    'internal/aws/supportcases/mutate.go',
    'internal/aws/supportcases/read.go',
    'internal/aws/supportcases/store.go',
    'internal/server/server.go',
}
SUPPORT_CASE_CONTROLS = [
    ('ExtendExpiryOnAppend', '!.expires = IF new THEN minute + 60 ELSE @', '!.expires = minute + 60', 'StageContract'),
    ('ShortenOriginalHour', '!.expires = IF new THEN minute + 60 ELSE @', '!.expires = IF new THEN minute + 59 ELSE @', 'StageContract'),
    ('AppendAtExpiry', 'eligible == IF new THEN db.expires = 0 ELSE Live', 'eligible == IF new THEN db.expires = 0 ELSE db.expires > 0 /\\ minute <= db.expires', 'StageContract'),
    ('AllowFourthFile', 'valid == eligible /\\ db.staged + batch <= 3', 'valid == eligible /\\ db.staged + batch <= 4', 'StageContract'),
    ('AppendLostFiles', '!.staged = @ + batch, !.expires', '!.staged = batch, !.expires', 'StageContract'),
    ('StageAfterCancellation', 'success == Authorized(g) /\\ valid /\\ Commit(g)', 'success == Authorized(g) /\\ valid /\\ (Commit(g) \\/ g = "cancel")', 'StageContract'),
    ('PersistFirstFileOfInvalidBatch', 'IF success THEN written ELSE db', 'IF success THEN written ELSE IF valid /\\ batch = 2 /\\ g = "late invalid" THEN [db EXCEPT !.staged = @ + 1] ELSE db', 'StageContract'),
    ('CreateWithoutInitialBody', '!.messages = Append(@,Body(text,count))', '!.messages = IF creating THEN @ ELSE Append(@,Body(text,count))', 'SubmissionContract'),
    ('DuplicateInitialMessage', '!.messages = Append(@,Body(text,count))', '!.messages = IF creating THEN <<Body(text,count),Body(text,count)>> ELSE Append(@,Body(text,count))', 'SubmissionContract'),
    ('ReplaceInitialWithReply', '!.messages = Append(@,Body(text,count))', '!.messages = IF creating THEN Append(@,Body(text,count)) ELSE <<Body(text,count)>>', 'SubmissionContract'),
    ('DropCommittedFile', 'count == IF setID = "none" THEN 0 ELSE db.staged', 'count == 0', 'SubmissionContract'),
    ('AcceptExpiredAttachment', 'validSet == setID = "none" \\/ (setID = "local" /\\ Live)', 'validSet == setID = "none" \\/ (setID = "local" /\\ db.expires > 0 /\\ minute <= db.expires)', 'SubmissionContract'),
    ('AcceptMissingAttachmentSet', 'validSet == setID = "none" \\/', 'validSet == setID \\in {"none","missing"} \\/', 'SubmissionContract'),
    ('AcceptPeerAttachmentSet', 'validSet == setID = "none" \\/', 'validSet == setID \\in {"none","foreign"} \\/', 'SubmissionContract'),
    ('CreateCaseAfterDenial', 'success == Authorized(g) /\\ validCase /\\ validSet /\\ Commit(g)', 'success == (Authorized(g) \\/ (creating /\\ g = "denied")) /\\ validCase /\\ validSet /\\ Commit(g)', 'SubmissionContract'),
    ('ReplyAfterResolve', 'validCase == IF creating THEN db.status = "absent" ELSE db.status = "opened"', 'validCase == IF creating THEN db.status = "absent" ELSE db.status # "absent"', 'SubmissionContract'),
    ('ReplyThroughForeignScope', 'success == Authorized(g) /\\ validCase /\\ validSet /\\ Commit(g)', 'success == (Authorized(g) \\/ (~creating /\\ g = "foreign")) /\\ validCase /\\ validSet /\\ Commit(g)', 'SubmissionContract'),
    ('CreateSurvivesLateAttachmentFailure', 'IF success THEN changed ELSE db', 'IF success THEN changed ELSE IF creating /\\ validCase /\\ setID = "local" /\\ Live /\\ g = "late invalid" THEN [db EXCEPT !.status = "opened"] ELSE db', 'SubmissionContract'),
    ('ReplySurvivesLateAttachmentFailure', 'IF success THEN changed ELSE db', 'IF success THEN changed ELSE IF ~creating /\\ validCase /\\ setID = "local" /\\ Live /\\ g = "late invalid" THEN [db EXCEPT !.messages = Append(@,Body("reply",0))] ELSE db', 'SubmissionContract'),
    ('CommitCancelledSubmission', 'IF success THEN changed ELSE db', 'IF success \\/ (validCase /\\ validSet /\\ g = "cancel") THEN changed ELSE db', 'SubmissionContract'),
    ('CommitFailedSubmission', 'IF success THEN changed ELSE db', 'IF success \\/ (validCase /\\ validSet /\\ g = "fault") THEN changed ELSE db', 'SubmissionContract'),
    ('ResolveMissingCase', 'success == Authorized(g) /\\ db.status # "absent" /\\ Commit(g)', 'success == Authorized(g) /\\ Commit(g)', 'ResolutionContract'),
    ('ResolveAfterDenial', 'success == Authorized(g) /\\ db.status # "absent" /\\ Commit(g)', 'success == (Authorized(g) \\/ g = "denied") /\\ db.status # "absent" /\\ Commit(g)', 'ResolutionContract'),
    ('ResolveDropsCommunications', 'updated == [Clean EXCEPT !.status = "resolved"]', 'updated == [Clean EXCEPT !.status = "resolved", !.messages = <<>>]', 'ResolutionContract'),
    ('ResolveForgesAgentReply', 'updated == [Clean EXCEPT !.status = "resolved"]', 'updated == [Clean EXCEPT !.status = "resolved", !.messages = Append(@,Body("agent",0))]', 'ResolutionContract'),
    ('ResolveMutatesPeer', 'updated == [Clean EXCEPT !.status = "resolved"]', 'updated == [Clean EXCEPT !.status = "resolved", !.peer.status = "resolved"]', 'PeerFrame'),
    ('FailedUploadMutatesPeer', 'IF success THEN written ELSE db', 'IF success THEN written ELSE IF g = "fault" /\\ valid THEN [db EXCEPT !.peer.staging = 0] ELSE db', 'PeerFrame'),
    ('ExposeResolvedByDefault', '(flag /\\ db.status = "resolved")', 'db.status = "resolved"', 'ReadProjection'),
    ('HideExplicitResolved', '(flag /\\ db.status = "resolved")', 'FALSE', 'ReadProjection'),
    ('IncludeSuppressedCommunications', 'IF shown /\\ op \\in {"cases","messages"} THEN history', 'IF shown /\\ op \\in {"cases","summary","messages"} THEN history', 'ReadProjection'),
    ('SuppressDefaultCommunications', 'IF shown /\\ op \\in {"cases","messages"} THEN history', 'IF shown /\\ op = "messages" THEN history', 'ReadProjection'),
    ('DiscloseAttachmentWithoutPermission', 'canRead == Authorized(g)', 'canRead == Authorized(g) \\/ (op = "file" /\\ g = "denied")', 'ReadProjection'),
    ('DisclosePeerCase', 'canRead == Authorized(g)', 'canRead == Authorized(g) \\/ (op \\in {"cases","summary"} /\\ present /\\ g = "foreign")', 'ReadProjection'),
    ('LoadFileFromExpiredSet', 'file |-> IF op = "file" THEN db.messages[slot].files[ownFile] ELSE 0', 'file |-> IF op = "file" /\\ Live THEN db.messages[slot].files[ownFile] ELSE 0', 'ReadProjection'),
    ('ReadMutatesStoredMessages', "/\\ db' = db\n    /\\ last' = Mark(op,g,db", '/\\ db\' = IF op = "messages" /\\ present THEN [db EXCEPT !.messages = <<>>] ELSE db\n    /\\ last\' = Mark(op,g,db', 'ReadProjection'),
    ('RestartDropsAttachedMessage', "Restart ==\n /\\ db' = db", "Restart ==\n /\\ db' = (IF \\E i \\in 1..Len(db.messages): Len(db.messages[i].files) > 0 THEN [db EXCEPT !.messages = <<>>] ELSE db)", 'RestartAndClock'),
    ('RestartExtendsDeadline', "Restart ==\n /\\ db' = db", "Restart ==\n /\\ db' = (IF Live THEN [db EXCEPT !.expires = minute + 60] ELSE db)", 'RestartAndClock'),
]
REQUIRED_MUTANTS['SupportCaseLifecycle'] = {entry[0] for entry in SUPPORT_CASE_CONTROLS}


# Independent local CloudFront delivery and invalidation controls. Keep these
# literal sets separate from the mutable model registry they protect.
BASE_MODELS.add('CloudFrontLocalCache')
BASE_INVARIANTS['CloudFrontLocalCache'] = {
    'TypeOK', 'FreshJointAuthority', 'ExactHitIdentity', 'MissReadsOrigin',
    'FailureRollsBack', 'RefusalUnchanged', 'StaleETagUnchanged',
    'UpdateClearsCache', 'InvalidationClearsCache', 'RestartPreserves', 'PeerUnchanged',
}
REQUIRED_MUTANTS['CloudFrontLocalCache'] = {
    'MissingObjectGrant', 'MissingDistributionGrant', 'WrongOriginLifetime',
    'CrossActorHit', 'ExpiredHit', 'IgnoreETag', 'UpdateRetainsCache',
    'InvalidationRetainsCache', 'FailedInvalidationEvicts',
}
REQUIRED_SOURCE_SCOPES['CloudFrontLocalCache'] = {
    'internal/aws/cloudfront_distribution.go', 'internal/aws/cloudfront_distribution_store.go',
    'internal/aws/cloudfront_distribution_http.go', 'internal/aws/cloudfront_distribution_list.go',
    'internal/aws/cloudfront_vpc_origin.go', 'internal/aws/ssm.go', 'internal/server/server.go',
    'internal/aws/cloudfront_distribution_interrupt_test.go',
}

BASE_MODELS.add('CloudFrontConsoleEpoch')
BASE_INVARIANTS['CloudFrontConsoleEpoch'] = {'StableUnknownCaller', 'ProtectedDownload', 'CurrentReceipt', 'TypeOK', 'OneCurrentWrite', 'ExplicitWriteConfirmation', 'CurrentContent', 'AcknowledgedReceipt'}
REQUIRED_MUTANTS['CloudFrontConsoleEpoch'] = {'UnconfirmedWrite', 'PublishOldRead', 'RetainOldContent', 'ReplayDifferentCaller', 'OldReadUnlocksWrite', 'FreshCallerWhileUnknown', 'UnknownAcknowledged', 'UnsafeContent', 'PublishOldReceipt'}
REQUIRED_SOURCE_SCOPES['CloudFrontConsoleEpoch'] = {'internal/aws/cloudfront_distribution_http.go', 'tools/console/src/cloudfront-local.jsx', 'tools/console/src/cloudfront-local.mjs', 'internal/server/web/app.js', 'tools/console/src/console-navigation.mjs', 'tools/console/src/main.jsx'}
REQUIRED_SOURCE_SCOPES['CloudFrontConsoleEpoch'].add('tools/console/src/console-home.jsx')


def load_registry(root, relative='formal/models.json'):
    root = Path(root)
    registry_path = asset(root, relative)
    try:
        registry = load_json(registry_path.read_text())
    except (ValueError, UnicodeError) as error:
        raise GateError('invalid model registry') from error
    require(set(registry) == {'version', 'tool', 'models'} and type(registry['version']) is int and registry['version'] == 1, 'unsupported model registry schema')
    tool = registry['tool']
    require(set(tool) == {'version', 'checksum_file', 'release_url'} and tool['version'] == '1.7.4', 'unsupported TLC tool declaration')
    checksum = asset(root, tool['checksum_file']).read_text().strip().split()
    require(len(checksum) == 2 and re.fullmatch(r'[a-f0-9]{64}', checksum[0]) and checksum[1] == 'tla2tools.jar', 'invalid TLC checksum declaration')
    require(tool['release_url'] == 'https://github.com/tlaplus/tlaplus/releases/download/v1.7.4/tla2tools.jar', 'unexpected TLC download origin')
    models = registry['models']
    require(isinstance(models, list) and models, 'empty model registry')
    ids, files = set(), set()
    for model in models:
        require(set(model) == {'id', 'model', 'config', 'bounds', 'properties', 'limits', 'source_scope', 'mutants'}, 'invalid model fields')
        name = model['id']
        require(isinstance(name, str) and IDENTIFIER.fullmatch(name) and name not in ids, 'invalid or duplicate model id')
        ids.add(name)
        require(model['model'] == f'formal/{name}.tla' and model['config'] == f'formal/{name}.cfg', 'model/config path mismatch')
        source, config = asset(root, model['model']).read_text(), asset(root, model['config']).read_text()
        files.update((model['model'], model['config']))
        require(len(re.findall(r'\bMODULE\s+' + re.escape(name) + r'\b', source)) == 1, 'model module declaration mismatch')
        if name == 'CognitoUserPoolMetadata':
            require(model['bounds'] == COGNITO_METADATA_BOUNDS, 'Cognito metadata model bounds changed')
            require(config == COGNITO_METADATA_CONFIG, 'Cognito metadata model configuration changed')
        if name in BATTLE_INTEGRATION_EXPECTED:
            require(model['bounds'] == BATTLE_INTEGRATION_EXPECTED[name]['bounds'], 'Battle integration bounds changed')
            require(config == BATTLE_INTEGRATION_CONFIGS[name], 'Battle integration configuration changed')
        if name == 'EC2GuestAttachmentLifecycle':
            require(model['bounds'] == EC2_ATTACHMENT_BOUNDS, 'EC2 attachment bounds changed')
            require(config == EC2_ATTACHMENT_CONFIG, 'EC2 attachment configuration changed')
        if name in OWNED_COMPOSITION_BOUNDS:
            require(model['bounds'] == OWNED_COMPOSITION_BOUNDS[name], 'owned composition bounds changed')
            require(config == OWNED_COMPOSITION_CONFIG[name], 'owned composition configuration changed')
        if name == 'CommandDispatchLifecycle':
            require(model['bounds'] == COMMAND_DISPATCH_BOUNDS, 'command dispatch bounds changed')
            require(config == COMMAND_DISPATCH_CONFIG, 'command dispatch configuration changed')
        require(isinstance(model['bounds'], str) and model['bounds'].strip() and isinstance(model['limits'], str) and model['limits'].strip(), 'model bounds/limits must be explicit')
        require(isinstance(model['properties'], list) and len(model['properties']) == len(set(model['properties'])) and set(model['properties']) == set(invariants(config, name)), 'registry/config invariants mismatch')
        require(BASE_INVARIANTS.get(name, set()) <= set(model['properties']), 'an existing required invariant was removed')
        require(isinstance(model['source_scope'], list) and all(isinstance(p, str) and p and not Path(p).is_absolute() and '..' not in Path(p).parts and not any(c in p for c in '*?[') for p in model['source_scope']), 'source scope must name exact reviewed paths')
        require(len(set(model['source_scope'])) == len(model['source_scope']), 'duplicate model source scope')
        if name in REQUIRED_SOURCE_SCOPES:
            require(set(model['source_scope']) == REQUIRED_SOURCE_SCOPES[name], 'required exact source scope changed')
        for path in model['source_scope']:
            asset(root, path)
        require(isinstance(model['mutants'], list) and model['mutants'], 'model needs a sensitivity control')
        mutant_ids = set()
        for mutant in model['mutants']:
            require(set(mutant) == {'id', 'edits', 'expect_invariant'}, 'invalid mutant fields')
            require(isinstance(mutant['id'], str) and IDENTIFIER.fullmatch(mutant['id']) and mutant['id'] not in mutant_ids, 'invalid mutant id')
            mutant_ids.add(mutant['id'])
            require(mutant['expect_invariant'] in model['properties'], 'mutant expects unchecked invariant')
            apply_mutant(source, name, mutant)
        require(REQUIRED_MUTANTS.get(name, set()) <= mutant_ids, 'a required mutation control was removed')
        if name == 'SupportCaseLifecycle':
            expected = [{'id': mid, 'edits': [{'old': old, 'new': new, 'count': 1}], 'expect_invariant': inv}
                        for mid, old, new, inv in SUPPORT_CASE_CONTROLS]
            require(model['mutants'] == expected, 'Support case controls changed')
        if name == 'SARPolicyAuthorizationTarget':
            require(model['mutants'] == SAR_POLICY_TARGET_MUTATIONS, 'SAR policy target controls changed')
        if name == 'SakuraServerReadEpoch':
            require(model['mutants'] == SAKURA_SERVER_READ_EPOCH_MUTATIONS, 'Sakura Server read-epoch mutation controls changed')
        if name == 'AzureStructuredReadAdmission':
            require(model['mutants'] == AZURE_STRUCTURED_READ_MUTATIONS, 'Azure structured read mutation controls changed')
        if name in BATTLE_INTEGRATION_MUTATIONS:
            require(model['mutants'] == BATTLE_INTEGRATION_MUTATIONS[name], 'Battle integration mutation controls changed')
        if name == 'AzureBlobRowDownload':
            require(model['mutants'] == AZURE_BLOB_ROW_DOWNLOAD_CONTROLS,
                    'Azure Blob row download mutation controls changed')
        if name == 'S3MultipartConsolePaging':
            require(model['mutants'] == S3_LISTPARTS_MUTATIONS, 'ListParts paging mutation controls changed')
        if name == 'EC2InstanceStackConsole':
            require(model['mutants'] == EC2INSTANCESTACKCONSOLE_MUTATIONS, 'EC2 instance stack console controls changed')
        if name == 'S3MultipartPartReceipt':
            require(model['mutants'] == S3_PART_RECEIPT_MUTATIONS, 'S3 part receipt mutation controls changed')
    require(BASE_MODELS <= ids, 'a required existing model was removed')
    actual = {p.relative_to(root).as_posix() for suffix in ('*.tla', '*.cfg') for p in (root / 'formal').glob(suffix)}
    require(actual == files, 'unregistered or missing model/config artifact')
    registry['_checksum'] = checksum[0]
    registry['_digest'] = digest(registry_path.read_bytes())
    return registry


_MUTANTS = {}


def apply_mutant(source, name, mutant):
    # load_registry re-applies every mutant on each call; the result depends only on these inputs.
    key = (source, name, repr(mutant))
    if key not in _MUTANTS:
        if len(_MUTANTS) > 20000:
            _MUTANTS.clear()
        _MUTANTS[key] = _apply_mutant(source, name, mutant)
    return _MUTANTS[key]


def _apply_mutant(source, name, mutant):
    require(isinstance(mutant['edits'], list) and mutant['edits'], 'empty mutation')
    changed = source
    for edit in mutant['edits']:
        require(set(edit) == {'old', 'new', 'count'}, 'invalid mutation edit')
        old, new, count = edit['old'], edit['new'], edit['count']
        require(isinstance(old, str) and isinstance(new, str) and old and old != new and type(count) is int and count > 0, 'mutation must change a nonempty exact anchor')
        require(changed.count(old) == count, f'mutation anchor count mismatch: {name}/{mutant["id"]}')
        changed = changed.replace(old, new)
    require(changed != source, 'mutation did not change model behavior')
    module = name + '_' + mutant['id']
    changed, count = re.subn(r'\bMODULE\s+' + re.escape(name) + r'\b', 'MODULE ' + module, changed)
    require(count == 1, 'mutant module rename failed')
    return module, changed


def validate_result(returncode, text, expected=None):
    if expected is None:
        require(returncode == 0 and 'No error has been found' in text, 'TLC baseline did not complete successfully')
    else:
        require(returncode == 12 and f'Invariant {expected} is violated' in text, 'mutation did not produce its required invariant counterexample')


def exact_head_inputs(root):
    """Reject dirty index/tree and undeclared executable/model inputs for merge evidence."""
    root = Path(root)
    def git(*args):
        result = subprocess.run(['git', '-C', str(root), *args], capture_output=True, check=False)
        require(result.returncode == 0, 'immutable HEAD input check failed')
        return result.stdout
    head = git('rev-parse', '--verify', 'HEAD^{commit}').decode().strip()
    git('diff', '--quiet', 'HEAD', '--')
    git('diff', '--cached', '--quiet', 'HEAD', '--')
    require(not git('ls-files', '--others', '--exclude-standard', '-z'), 'untracked files cannot be merge evidence')
    ignored = git('ls-files', '--others', '--ignored', '--exclude-standard', '-z').decode().split('\0')
    require(not any(p and (p.startswith('formal/') or p.startswith('scripts/') and not p.endswith('.md')) for p in ignored), 'ignored model/policy input cannot be merge evidence; use an external Python cache')
    tracked = git('ls-tree', '-r', '--name-only', '-z', head).decode().split('\0')
    registry = load_json(git('show', head + ':formal/models.json'))
    scope = {p for model in registry['models'] for p in model['source_scope']}
    module_inputs = {'go.mod', 'go.sum'}
    require(module_inputs <= set(tracked), 'module/checksum inputs must exist in immutable HEAD')
    paths = {p for p in tracked if p and (p.startswith(('formal/', 'scripts/', '.github/')) or p == 'Makefile' or p in scope or p in module_inputs)}
    content, hashes = {}, {}
    for path in sorted(paths):
        expected = git('show', head + ':' + path)
        actual_path = asset(root, path)
        if path in module_inputs:
            entry = git('ls-tree', head, '--', path).decode().split()
            require(entry and entry[0] == '100644', 'module input HEAD mode must be regular nonexecutable: ' + path)
            current_mode = actual_path.lstat().st_mode
            require(stat.S_ISREG(current_mode) and not current_mode & 0o111, 'module input working mode must be regular nonexecutable: ' + path)
        actual = actual_path.read_bytes()
        require(actual == expected, 'working input differs from HEAD: ' + path)
        content[path], hashes[path] = expected, digest(expected)
    return {'head': head, 'content': content, 'hashes': hashes}


def run_suite(root, jar, reports, registry_path='formal/models.json', timeout=60, merge_gate=False, shard=None):
    root, reports = Path(root), Path(reports)
    reports.mkdir(parents=True, exist_ok=True)
    evidence = {'kind': 'bounded-tlc-not-go-refinement', 'success': False, 'runs': []}
    try:
        if shard is not None:
            # A shard runs every count-th case in registry order, so heavy models spread out.
            require(type(shard[0]) is int and type(shard[1]) is int and 0 <= shard[0] < shard[1], 'invalid formal shard')
            evidence['shard'] = {'index': shard[0], 'count': shard[1]}
        snapshot = exact_head_inputs(root) if merge_gate else None
        registry = load_registry(root, registry_path)
        validate_guesttcp_module_pins(root, snapshot['content'] if snapshot else None)
        if snapshot:
            require(registry['_digest'] == digest(snapshot['content'][registry_path]), 'registry changed during input verification')
            require(registry['_checksum'] == snapshot['content'][registry['tool']['checksum_file']].decode().split()[0], 'checksum changed during input verification')
            evidence.update(input_mode='immutable-head', head_sha=snapshot['head'], input_sha256=snapshot['hashes'])
        else:
            evidence['input_mode'] = 'working-tree-specification-only-not-merge-evidence'
        print('Formal input mode: ' + evidence['input_mode'])
        jar = Path(jar)
        require(jar.is_file() and jar.stat().st_size > 0, 'missing TLC jar')
        jar_bytes = jar.read_bytes()
        require(digest(jar_bytes) == registry['_checksum'], 'TLC jar checksum mismatch')
        java = shutil.which('java')
        require(java is not None, 'Java tool is unavailable')
        evidence.update(tool_version=registry['tool']['version'], tool_sha256=registry['_checksum'], registry_sha256=registry['_digest'])
        with tempfile.TemporaryDirectory(prefix='tcsim-tlc-') as directory:
            temp = Path(directory)
            pinned_jar = temp / 'tla2tools.jar'
            pinned_jar.write_bytes(jar_bytes)
            position = -1
            for model in registry['models']:
                source = snapshot['content'][model['model']].decode() if snapshot else asset(root, model['model']).read_text()
                config = snapshot['content'][model['config']].decode() if snapshot else asset(root, model['config']).read_text()
                cases = [(model['id'], source, None)] + [(*apply_mutant(source, model['id'], m), m['expect_invariant']) for m in model['mutants']]
                for name, candidate, expected in cases:
                    position += 1
                    if shard is not None and position % shard[1] != shard[0]:
                        continue
                    tla, cfg = temp / (name + '.tla'), temp / (name + '.cfg')
                    tla.write_text(candidate)
                    cfg.write_text(config)
                    # This allocation-heavy family needs two bounded TLC workers;
                    # exhaustive states, exact controls and the 60s cap are unchanged.
                    # Other families remain serial until separately measured/reviewed.
                    workers = '2' if model['id'] == 'DynamoDBAdmission' else '1'
                    command = [java, '-cp', str(pinned_jar), 'tlc2.TLC', '-workers', workers, '-metadir', str(temp / (name + '-states')), '-config', str(cfg), str(tla)]
                    started = time.monotonic()
                    try:
                        result = subprocess.run(command, capture_output=True, text=True, timeout=timeout, check=False, cwd=temp)
                    except (OSError, subprocess.TimeoutExpired) as error:
                        # TimeoutExpired can retain bytes even with text=True.
                        # Keep exact partial streams separately; never log the
                        # exception message/command/environment as diagnostics.
                        failure = {'model': model['id'], 'case': name, 'success': False,
                                   'exception_type': type(error).__name__,
                                   'exit_code': None,
                                   'exit_code_status': 'unavailable_from_process_exception',
                                   'errno': getattr(error, 'errno', None),
                                   'timeout_seconds': getattr(error, 'timeout', None),
                                   'deadline_seconds': timeout,
                                   'elapsed_seconds': time.monotonic() - started,
                                   'model_sha256': digest(candidate.encode()),
                                   'config_sha256': digest(config.encode())}
                        evidence['failure'] = failure
                        for channel in ('stdout', 'stderr'):
                            output = getattr(error, channel, None)
                            raw = output if isinstance(output, bytes) else output.encode('utf-8', errors='surrogatepass') if isinstance(output, str) else b''
                            filename = name + '.failure.' + channel + '.bin'
                            stream = {'path': filename, 'sha256': digest(raw),
                                      'bytes': len(raw), 'available': output is not None,
                                      'source_type': type(output).__name__, 'retained': False}
                            failure[channel] = stream
                            try:
                                (reports / filename).write_bytes(raw)
                                stream['retained'] = True
                            except OSError as diagnostic_error:
                                stream['write_error_type'] = type(diagnostic_error).__name__
                                stream['write_errno'] = diagnostic_error.errno
                        try:
                            (reports / (name + '.failure.json')).write_text(json.dumps(failure, indent=2) + '\n')
                        except OSError as diagnostic_error:
                            failure['metadata_write_error_type'] = type(diagnostic_error).__name__
                            failure['metadata_write_errno'] = diagnostic_error.errno
                        raise GateError('TLC could not finish: ' + name) from error
                    text = result.stdout + result.stderr
                    (reports / (name + '.txt')).write_text(text)
                    run = {'model': model['id'], 'case': name, 'expected_violation': expected, 'exit_code': result.returncode, 'model_sha256': digest(candidate.encode()), 'config_sha256': digest(config.encode()), 'bounds': model['bounds'], 'limits': model['limits'], 'success': False}
                    evidence['runs'].append(run)
                    validate_result(result.returncode, text, expected)
                    run['success'] = True
                    print(name + ': ' + ('bounded invariants hold' if expected is None else 'required counterexample observed'))
            if shard is None or shard[0] == 0:
                from formal_seed_replay import run_seed_replays
                run_seed_replays(root, registry, snapshot, temp, pinned_jar, java,
                                 reports, timeout, evidence, require, validate_result)
        if snapshot:
            after = exact_head_inputs(root)
            require(after['head'] == snapshot['head'] and after['hashes'] == snapshot['hashes'], 'HEAD/policy/model inputs changed while TLC was running')
        evidence['success'] = True
        return evidence
    except (GateError, OSError, KeyError, TypeError) as error:
        evidence['error'] = str(error)
        raise GateError(str(error)) from error
    finally:
        (reports / 'evidence.json').write_text(json.dumps(evidence, indent=2) + '\n')


def combine_shards(root, evidence_paths, reports, registry_path='formal/models.json'):
    """Accept shard evidence only if together it is one full merge-gate run of this HEAD."""
    from formal_seed_replay import SPECS
    root, reports = Path(root), Path(reports)
    reports.mkdir(parents=True, exist_ok=True)
    combined = {'kind': 'bounded-tlc-not-go-refinement', 'success': False, 'runs': []}
    try:
        snapshot = exact_head_inputs(root)
        registry = load_registry(root, registry_path)
        require(registry['_digest'] == digest(snapshot['content'][registry_path]), 'registry changed during input verification')
        try:
            shards = [json.loads(Path(path).read_text()) for path in evidence_paths]
        except (OSError, ValueError) as error:
            raise GateError('unreadable formal shard evidence') from error
        count = len(shards)
        require(count > 0 and all(isinstance(s, dict) and isinstance(s.get('shard'), dict) for s in shards), 'missing formal shard evidence')
        require(sorted(s['shard'].get('index') for s in shards) == list(range(count)) and all(s['shard'].get('count') == count for s in shards),
                'formal shards are missing, repeated or from another layout')
        order = []
        for model in registry['models']:
            source = snapshot['content'][model['model']].decode()
            order += [model['id']] + [apply_mutant(source, model['id'], m)[0] for m in model['mutants']]
        for evidence in sorted(shards, key=lambda s: s['shard']['index']):
            index = evidence['shard']['index']
            require(evidence.get('success') is True and evidence.get('input_mode') == 'immutable-head', 'formal shard did not pass in merge mode')
            require(evidence.get('head_sha') == snapshot['head'] and evidence.get('input_sha256') == snapshot['hashes']
                    and evidence.get('registry_sha256') == registry['_digest'] and evidence.get('tool_sha256') == registry['_checksum'],
                    'formal shard checked other inputs')
            runs = evidence.get('runs')
            require(isinstance(runs, list) and all(isinstance(r, dict) and r.get('success') is True for r in runs), 'formal shard recorded a failed run')
            require([r.get('case') for r in runs if r.get('kind') != 'seed-prefix-replay'] == order[index::count],
                    'formal shard runs do not match its share of cases')
            seeds = [r for r in runs if r.get('kind') == 'seed-prefix-replay']
            require(len(seeds) == (2 * len(SPECS) if index == 0 else 0), 'seed replays must run exactly once, in shard 0')
            combined['runs'] += runs
        after = exact_head_inputs(root)
        require(after['head'] == snapshot['head'] and after['hashes'] == snapshot['hashes'], 'HEAD/policy/model inputs changed while combining')
        combined.update(input_mode='immutable-head', head_sha=snapshot['head'], input_sha256=snapshot['hashes'], shards=count,
                        tool_version=registry['tool']['version'], tool_sha256=registry['_checksum'], registry_sha256=registry['_digest'])
        combined['success'] = True
        return combined
    except (GateError, OSError, KeyError, TypeError) as error:
        combined['error'] = str(error)
        raise GateError(str(error)) from error
    finally:
        (reports / 'evidence.json').write_text(json.dumps(combined, indent=2) + '\n')


def parse_shard(value):
    match = re.fullmatch(r'(\d+)/(\d+)', value)
    require(match is not None, 'shard must be INDEX/COUNT')
    index, count = int(match.group(1)), int(match.group(2))
    require(0 <= index < count, 'invalid formal shard')
    return index, count


def main():
    import argparse
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--registry', default='formal/models.json')
    parser.add_argument('--reports', default='reports/formal')
    parser.add_argument('--merge-gate', action='store_true', help='require immutable HEAD inputs before/after TLC; required in CI')
    parser.add_argument('--shard', help='INDEX/COUNT: run only this deterministic share of the registered models')
    parser.add_argument('--combine', nargs='+', metavar='EVIDENCE', help='accept shard evidence files only as one full merge-gate run of HEAD')
    options = parser.parse_args()
    try:
        if options.combine:
            require(options.shard is None and not options.merge_gate, '--combine takes no other run options')
            combine_shards(Path.cwd(), options.combine, options.reports, options.registry)
            return
        require(bool(os.environ.get('TLA_JAR')), 'TLA_JAR must identify the pinned TLC jar')
        shard = parse_shard(options.shard) if options.shard is not None else None
        run_suite(Path.cwd(), os.environ['TLA_JAR'], options.reports, options.registry, merge_gate=options.merge_gate, shard=shard)
    except GateError as error:
        parser.exit(1, 'Formal validation failed: ' + str(error) + '\n')

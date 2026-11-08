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

BASE_MODELS = {'S3MultipartState', 'AzureBlobState',
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
BASE_INVARIANTS = {'S3MultipartState': {'TypeOK', 'Budget', 'NoOrphans', 'Cleanup', 'RefusalAtomic'}, 'CatalogConsoleEpoch': {'TypeOK', 'NoUnmountedAcceptance', 'ReloadOnlyAfterAck', 'OnlyCurrentReplies', 'FailureVisible', 'SingleWriter'},
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
REQUIRED_MUTANTS = {'S3MultipartState': {'OmitPartBudget', 'OmitUploadParent', 'RetainCompletedParts', 'PartialFailure', 'OmitUploadBinding'}, 'CatalogConsoleEpoch': {'OmitReplyEpochGuard', 'DropFailureError', 'ReloadAfterFailedWrite', 'AcceptAfterUnmount', 'AllowConcurrentWrites'},
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
                            'PermitUnsafeEnabled',
                            'RegisterDisabled',
                            'RejectValidEnabled',
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
REQUIRED_SOURCE_SCOPES = {'CFNConditionAdmission': {'internal/aws/cloudformation.go',
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
BASE_INVARIANTS['ConsoleHandoff'] = {'LogoutRejectsReplay', 'TypeOK', 'RedeemRequiresFreshParent', 'OperatorRefused', 'ExactDestination', 'ParentLifetimeBound', 'AttemptConsumesCode', 'UseRequiresFreshAuthority', 'SDKAuthorityPreserved', 'UnrelatedSessionsPreserved', 'OneTimeRedemption'}
REQUIRED_MUTANTS['ConsoleHandoff'] = {'SkipParameter', 'RevokeSDK', 'SkipRedeemExpiry', 'SkipUseGeneration', 'CachePolicy', 'ExtendHandoff', 'SkipRedeemGeneration', 'SkipCookie', 'SkipRegion', 'SkipBrowserExpiry', 'ReuseCode', 'SkipAction', 'SkipRedeemParent', 'RevokePeer', 'KeepLoggedOutCookie', 'ExtendBrowser', 'IgnoreConsumedCode', 'SkipUseParent', 'GrantBrowserSDK', 'AllowOperator'}
REQUIRED_SOURCE_SCOPES['ConsoleHandoff'] = {'internal/server/console_handoff.go', 'internal/aws/sts_credentials_test.go', 'internal/server/console_handoff_test.go', 'internal/server/access.go', 'internal/aws/sts_roles.go', 'internal/aws/sts_credentials.go', 'internal/server/server.go', 'internal/aws/ssm.go'}

IDENTIFIER = re.compile(r'^[A-Za-z][A-Za-z0-9_]*$')

# TLC1.7.4 ModelConfig.ALL_KEYWORDS. A directive cannot masquerade as a
# user invariant even when placed on the same physical CFG line.
CFG_KEYWORDS = {'CONSTANT', 'CONSTANTS', 'CONSTRAINT', 'CONSTRAINTS',
                'ACTION_CONSTRAINT', 'ACTION_CONSTRAINTS', 'INVARIANT', 'INVARIANTS',
                'INIT', 'NEXT', 'VIEW', 'SYMMETRY', 'SPECIFICATION', 'PROPERTY',
                'PROPERTIES', 'TYPE', 'TYPE_CONSTRAINT', 'CHECK_DEADLOCK'}


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
    require(BASE_MODELS <= ids, 'a required existing model was removed')
    actual = {p.relative_to(root).as_posix() for suffix in ('*.tla', '*.cfg') for p in (root / 'formal').glob(suffix)}
    require(actual == files, 'unregistered or missing model/config artifact')
    registry['_checksum'] = checksum[0]
    registry['_digest'] = digest(registry_path.read_bytes())
    return registry


def apply_mutant(source, name, mutant):
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


def run_suite(root, jar, reports, registry_path='formal/models.json', timeout=60, merge_gate=False):
    root, reports = Path(root), Path(reports)
    reports.mkdir(parents=True, exist_ok=True)
    evidence = {'kind': 'bounded-tlc-not-go-refinement', 'success': False, 'runs': []}
    try:
        snapshot = exact_head_inputs(root) if merge_gate else None
        registry = load_registry(root, registry_path)
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
            for model in registry['models']:
                source = snapshot['content'][model['model']].decode() if snapshot else asset(root, model['model']).read_text()
                config = snapshot['content'][model['config']].decode() if snapshot else asset(root, model['config']).read_text()
                cases = [(model['id'], source, None)] + [(*apply_mutant(source, model['id'], m), m['expect_invariant']) for m in model['mutants']]
                for name, candidate, expected in cases:
                    tla, cfg = temp / (name + '.tla'), temp / (name + '.cfg')
                    tla.write_text(candidate)
                    cfg.write_text(config)
                    command = [java, '-cp', str(pinned_jar), 'tlc2.TLC', '-workers', '1', '-metadir', str(temp / (name + '-states')), '-config', str(cfg), str(tla)]
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


def main():
    import argparse
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--registry', default='formal/models.json')
    parser.add_argument('--reports', default='reports/formal')
    parser.add_argument('--merge-gate', action='store_true', help='require immutable HEAD inputs before/after TLC; required in CI')
    options = parser.parse_args()
    try:
        require(bool(os.environ.get('TLA_JAR')), 'TLA_JAR must identify the pinned TLC jar')
        run_suite(Path.cwd(), os.environ['TLA_JAR'], options.reports, options.registry, merge_gate=options.merge_gate)
    except GateError as error:
        parser.exit(1, 'Formal validation failed: ' + str(error) + '\n')

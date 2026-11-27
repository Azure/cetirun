// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Resource-group-scoped orchestrator: deploys AKS cluster into an existing resource group.
// The resource group is created by deploy.sh before this template runs.

// --- Resource group and location ---

param resourceGroupName string
param location string

// --- AKS cluster parameters ---

param clusterName string
@description('Stable Kubernetes version, at least 1.37 for Sandbox and CetiAdmin admission. The selected version must be available in the target AKS region.')
param kubernetesVersion string = '1.37'
param dnsPrefix string
param nodeResourceGroup string

@allowed(['Free', 'Standard', 'Premium'])
param skuTier string = 'Premium'

param tags object = {
  project: 'cetirun'
  milestone: 'm0'
}

// --- System node pool parameters ---

@allowed([
  'Standard_D4s_v5'
  'Standard_D4as_v5'
  'Standard_D4ds_v6'
  'Standard_D8s_v5'
  'Standard_D8as_v5'
  'Standard_D8ds_v6'
  'Standard_D8s_v6'
])
@description('VM size for system node pool')
param systemNodePoolVmSize string = 'Standard_D8s_v6'

param systemNodePoolMinCount int = 2
param systemNodePoolMaxCount int = 10
param systemNodePoolOsDiskSizeGB int = 128

@allowed(['Managed', 'Ephemeral'])
param systemNodePoolOsDiskType string = 'Managed'

param systemNodePoolOsSku string = 'Ubuntu'
param systemNodePoolAvailabilityZones array = ['1', '2', '3']

// --- Agent node pool parameters ---

@allowed([
  'Standard_D4s_v5'
  'Standard_D4as_v5'
  'Standard_D4ds_v6'
  'Standard_D8s_v5'
  'Standard_D8as_v5'
  'Standard_D8ds_v6'
  'Standard_D16s_v5'
  'Standard_D16as_v5'
  'Standard_D16s_v6'
])
@description('VM size for agent node pool')
param agentNodePoolVmSize string = 'Standard_D16s_v6'

param agentNodePoolMinCount int = 15
param agentNodePoolMaxCount int = 150
param agentNodePoolOsDiskSizeGB int = 128
param agentNodePoolAvailabilityZones array = ['1', '2', '3']

// --- Agent2 node pool parameters ---

@allowed([
  'Standard_D4s_v5'
  'Standard_D4as_v5'
  'Standard_D4ds_v6'
  'Standard_D8s_v5'
  'Standard_D8as_v5'
  'Standard_D8ds_v6'
  'Standard_D32s_v5'
  'Standard_D32as_v5'
  'Standard_D32s_v6'
])
@description('VM size for agent2 node pool')
param agent2NodePoolVmSize string = 'Standard_D32s_v6'

param agent2NodePoolMinCount int = 3
param agent2NodePoolMaxCount int = 10
param agent2NodePoolOsDiskSizeGB int = 128
param agent2NodePoolAvailabilityZones array = ['1', '2', '3']

// --- Networking parameters ---

param podCidr string = '10.244.0.0/16'
param serviceCidr string = '172.16.0.0/16'
param dnsServiceIP string = '172.16.0.10'

// --- Istio parameters ---

param istioIngressGatewayEnabled bool = true

@allowed(['External', 'Internal'])
param istioIngressGatewayType string = 'External'

@description('Optional managed Istio revision. Leave empty to let AKS select its default revision.')
param istioRevision string = ''

// --- Monitoring: ADX-Mon into Azure Data Explorer (cetirun-observability chart) ---
// The cluster runs neither Container Insights nor managed Prometheus: the AKS
// module turns both off.

@allowed(['none', 'create', 'existing'])
@description('create, the default: a new Kusto cluster in adxResourceGroupName. existing: the databases and roles in the existing cluster adxClusterName. none: no ADX resources; make deploy then needs a Kusto cluster, its databases and an ingestor identity that exist.')
param adxMode string = 'create'

@description('Kusto cluster name: 4 to 22 lowercase letters and digits, starting with a letter. Kusto cluster names are unique in Azure.')
param adxClusterName string = 'adx${uniqueString(subscription().subscriptionId, resourceGroupName, clusterName)}'

@description('Resource group of the Kusto cluster. It must exist; deploy.sh creates only the cluster resource group.')
param adxResourceGroupName string = resourceGroupName

param adxSubscriptionId string = subscription().subscriptionId

@description('Region of a new Kusto cluster. Not every region offers every Kusto SKU, for example the Dev SKUs.')
param adxLocation string = location

param adxSkuName string = 'Dev(No SLA)_Standard_E2a_v4'

@allowed(['Basic', 'Standard'])
param adxSkuTier string = 'Basic'

param adxSkuCapacity int = 1
param adxLogsRetentionDays int = 90
param adxMetricsRetentionDays int = 30
param adxAgentLogsRetentionDays int = 14

@description('Namespace of the cetirun-observability release, for the ingestor federated credential.')
param observabilityNamespace string = 'cetirun-system'

@description('Readers of the logs and metrics databases: objects with principalId and principalType (Group, User or App).')
param adxViewers array = []

@description('Readers of the Agent logs database, which can hold conversation content.')
param adxAgentLogsViewers array = []

@description('Create the database of AI Gateway prompts and responses, CetiRunContent, and the Gateway identity that writes it. Needs adxMode create or existing.')
param adxContent bool = false

@description('Namespace of the platform release, for the AI Gateway\'s federated credential.')
param platformNamespace string = 'cetirun-system'

@description('Readers of the content database, which holds every AI Gateway prompt and response.')
param adxContentViewers array = []

@minValue(1)
@description('Days of the daily AI Gateway usage view GatewayDailyUsageV2 and of the coverage view GatewayCoverage in the logs database.')
param adxGatewayUsageRetentionDays int = 400

@description('Create the managed identity of the ADX-Mon alerter, with the Viewer role on the logs and metrics databases. Needs adxMode create or existing.')
param adxAlerter bool = false

@description('Create the managed identity with which the Portal reads the AI Gateway usage, with the Viewer role on the logs database. Only with adxMode create, and only while the logs database holds the data of this CetiRun cluster alone: Kusto grants a role on a whole database. Before another installation uses the database, remove the role; setting this to false does not remove it.')
param adxPortalUsage bool = false

@description('Send the Kubernetes audit events of the AKS API server, except get and list, to the database CetiRunAudit through an Event Hub. Needs adxMode create, or an existing Kusto cluster with a system-assigned identity.')
param adxAuditLogs bool = false

@minValue(1)
param adxAuditRetentionDays int = 90

@description('Readers of the audit database, which names every user and source address that changed the cluster.')
param adxAuditViewers array = []

@description('Name of the Event Hub namespace of the audit events, in the cluster resource group.')
param auditEventHubNamespaceName string = 'ehaudit${uniqueString(subscription().subscriptionId, resourceGroupName, clusterName)}'

// --- Modules ---

module cluster 'modules/aks-cluster.bicep' = {
  name: 'deploy-aks-cluster'
  params: {
    clusterName: clusterName
    location: location
    kubernetesVersion: kubernetesVersion
    dnsPrefix: dnsPrefix
    skuTier: skuTier
    tags: tags
    resourceGroupName: resourceGroupName
    nodeResourceGroup: nodeResourceGroup
    systemNodePoolVmSize: systemNodePoolVmSize
    systemNodePoolMinCount: systemNodePoolMinCount
    systemNodePoolMaxCount: systemNodePoolMaxCount
    systemNodePoolOsDiskSizeGB: systemNodePoolOsDiskSizeGB
    systemNodePoolOsDiskType: systemNodePoolOsDiskType
    systemNodePoolOsSku: systemNodePoolOsSku
    systemNodePoolAvailabilityZones: systemNodePoolAvailabilityZones
    agentNodePoolVmSize: agentNodePoolVmSize
    agentNodePoolMinCount: agentNodePoolMinCount
    agentNodePoolMaxCount: agentNodePoolMaxCount
    agentNodePoolOsDiskSizeGB: agentNodePoolOsDiskSizeGB
    agentNodePoolAvailabilityZones: agentNodePoolAvailabilityZones
    agent2NodePoolVmSize: agent2NodePoolVmSize
    agent2NodePoolMinCount: agent2NodePoolMinCount
    agent2NodePoolMaxCount: agent2NodePoolMaxCount
    agent2NodePoolOsDiskSizeGB: agent2NodePoolOsDiskSizeGB
    agent2NodePoolAvailabilityZones: agent2NodePoolAvailabilityZones
    podCidr: podCidr
    serviceCidr: serviceCidr
    dnsServiceIP: dnsServiceIP
    istioIngressGatewayEnabled: istioIngressGatewayEnabled
    istioIngressGatewayType: istioIngressGatewayType
    istioRevision: istioRevision
  }
}

module adxIngestorIdentity 'modules/adx-ingestor-identity.bicep' = if (adxMode != 'none') {
  name: 'deploy-adx-ingestor-identity'
  params: {
    name: '${clusterName}-adx-ingestor'
    location: location
    tags: tags
    oidcIssuerUrl: cluster.outputs.oidcIssuerUrl
    namespace: observabilityNamespace
  }
}

// The AI Gateway's identity for its content: the platform chart names the
// Gateway's ServiceAccount cetirun-ai-gateway.
module gatewayContentIdentity 'modules/adx-ingestor-identity.bicep' = if (adxMode != 'none' && adxContent) {
  name: 'deploy-gateway-content-identity'
  params: {
    name: '${clusterName}-gateway-content'
    location: location
    tags: tags
    oidcIssuerUrl: cluster.outputs.oidcIssuerUrl
    namespace: platformNamespace
    serviceAccountName: 'cetirun-ai-gateway'
    credentialName: 'cetirun-ai-gateway'
  }
}

module adx 'modules/adx.bicep' = if (adxMode != 'none') {
  name: 'deploy-adx'
  scope: resourceGroup(adxSubscriptionId, adxResourceGroupName)
  params: {
    clusterName: adxClusterName
    createCluster: adxMode == 'create'
    location: adxLocation
    skuName: adxSkuName
    skuTier: adxSkuTier
    skuCapacity: adxSkuCapacity
    tags: tags
    logsRetentionDays: adxLogsRetentionDays
    metricsRetentionDays: adxMetricsRetentionDays
    agentLogsRetentionDays: adxAgentLogsRetentionDays
    ingestorClientId: adxIngestorIdentity.?outputs.clientId ?? ''
    viewers: adxViewers
    agentLogsViewers: adxAgentLogsViewers
    createContentDatabase: adxContent
    contentWriterClientId: gatewayContentIdentity.?outputs.clientId ?? ''
    contentViewers: adxContentViewers
    gatewayUsageRetentionDays: adxGatewayUsageRetentionDays
    alerterClientId: adxAlerterIdentity.?outputs.clientId ?? ''
    portalUsageClientId: portalUsageIdentity.?outputs.clientId ?? ''
    auditLogs: adxAuditLogs
    auditRetentionDays: adxAuditRetentionDays
    auditViewers: adxAuditViewers
  }
}

// The ADX-Mon alerter's identity: the observability chart names its
// ServiceAccount cetirun-adx-mon-alerter.
module adxAlerterIdentity 'modules/adx-ingestor-identity.bicep' = if (adxMode != 'none' && adxAlerter) {
  name: 'deploy-adx-alerter-identity'
  params: {
    name: '${clusterName}-adx-alerter'
    location: location
    tags: tags
    oidcIssuerUrl: cluster.outputs.oidcIssuerUrl
    namespace: observabilityNamespace
    serviceAccountName: 'cetirun-adx-mon-alerter'
    credentialName: 'cetirun-adx-mon-alerter'
  }
}

// The Portal's identity for the usage report: the Portal chart names its
// ServiceAccount cetirun-portal.
module portalUsageIdentity 'modules/adx-ingestor-identity.bicep' = if (adxMode == 'create' && adxPortalUsage) {
  name: 'deploy-portal-usage-identity'
  params: {
    name: '${clusterName}-portal-usage'
    location: location
    tags: tags
    oidcIssuerUrl: cluster.outputs.oidcIssuerUrl
    namespace: platformNamespace
    serviceAccountName: 'cetirun-portal'
    credentialName: 'cetirun-portal'
  }
}

// Kubernetes audit events: the Event Hub and the diagnostic setting in the
// cluster resource group, then the data connection of the Kusto cluster.
module aksAudit 'modules/aks-audit.bicep' = if (adxMode != 'none' && adxAuditLogs) {
  name: 'deploy-aks-audit'
  params: {
    aksClusterName: cluster.outputs.clusterName
    location: location
    tags: tags
    namespaceName: auditEventHubNamespaceName
    receiverPrincipalId: adx.?outputs.principalId ?? ''
  }
}

module adxAuditConnection 'modules/adx-audit-connection.bicep' = if (adxMode != 'none' && adxAuditLogs) {
  name: 'deploy-adx-audit-connection'
  scope: resourceGroup(adxSubscriptionId, adxResourceGroupName)
  params: {
    clusterName: adxClusterName
    location: adxLocation
    databaseName: adx.?outputs.auditDatabaseName ?? ''
    eventHubId: aksAudit.?outputs.eventHubId ?? ''
    consumerGroupName: aksAudit.?outputs.consumerGroupName ?? ''
  }
}

// --- Outputs ---

output clusterName string = cluster.outputs.clusterName
output clusterId string = cluster.outputs.clusterId
output oidcIssuerUrl string = cluster.outputs.oidcIssuerUrl
output nodeResourceGroup string = cluster.outputs.nodeResourceGroup
output kubeletIdentityObjectId string = cluster.outputs.kubeletIdentityObjectId
output keyvaultSecretsProviderClientId string = cluster.outputs.keyvaultSecretsProviderClientId
output keyvaultSecretsProviderObjectId string = cluster.outputs.keyvaultSecretsProviderObjectId

// Inputs of the observability block of the make deploy environment file.
output adxClusterUri string = adx.?outputs.clusterUri ?? ''
output adxIngestorClientId string = adxIngestorIdentity.?outputs.clientId ?? ''
@description('observability.alerter.clientId of the make deploy environment file.')
output adxAlerterClientId string = adxAlerterIdentity.?outputs.clientId ?? ''
@description('observability.portal.clientId of the make deploy environment file.')
output adxPortalUsageClientId string = portalUsageIdentity.?outputs.clientId ?? ''
output adxAuditDatabase string = adx.?outputs.auditDatabaseName ?? ''

// Inputs of the AI Gateway's content values.
output adxContentDatabase string = adx.?outputs.contentDatabaseName ?? ''
output adxContentClientId string = gatewayContentIdentity.?outputs.clientId ?? ''

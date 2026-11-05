// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Resource-group-scoped orchestrator: deploys AKS cluster into an existing resource group.
// The resource group is created by deploy.sh before this template runs.

// --- Resource group and location ---

param resourceGroupName string
param location string

// --- AKS cluster parameters ---

param clusterName string
param kubernetesVersion string = '1.34'
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

// --- Monitoring parameters ---

param enableContainerInsights bool = true
param enablePrometheusMetrics bool = true
param logAnalyticsWorkspaceId string = ''

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
    enableContainerInsights: enableContainerInsights
    enablePrometheusMetrics: enablePrometheusMetrics
    logAnalyticsWorkspaceId: logAnalyticsWorkspaceId
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

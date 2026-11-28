// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Resource-group-scoped orchestrator for skill artifact storage.
// The resource group is created by deploy.sh before this template runs.

param location string
param storageAccountName string
param containerName string = 'aks-skills'

@description('Storage backend. azure-files-nfs is the encrypted-in-transit production backend.')
@allowed([
  'blob'
  'azure-files-nfs'
])
param storageBackend string = 'blob'

@description('Azure Files NFS share name. Used only by the azure-files-nfs backend.')
param shareName string = 'aks-skills'

@description('Azure Files NFS share quota in GiB.')
param shareQuotaGiB int = 100

param tags object = {
  project: 'cetirun'
  milestone: 'm0'
}

@description('AKS node subnet IDs allowed to access the storage account via NFS.')
param allowedSubnetIds array = []

@description('Subnet of the private endpoint. Required by azure-files-nfs.')
param privateEndpointSubnetId string = ''

@description('Private DNS zone where the private endpoint registers its address. Required by azure-files-nfs.')
param privateDnsZoneId string = ''

@description('Public network access of the account. Disabled leaves only private endpoints.')
@allowed([
  'Enabled'
  'Disabled'
])
param publicNetworkAccess string = 'Enabled'

// --- Modules ---

module storage 'modules/storage-account.bicep' = {
  name: 'deploy-storage-account'
  params: {
    storageAccountName: storageAccountName
    location: location
    containerName: containerName
    storageBackend: storageBackend
    shareName: shareName
    shareQuotaGiB: shareQuotaGiB
    tags: tags
    allowedSubnetIds: allowedSubnetIds
    privateEndpointSubnetId: privateEndpointSubnetId
    privateDnsZoneId: privateDnsZoneId
    publicNetworkAccess: publicNetworkAccess
  }
}

// --- Outputs ---

output storageAccountName string = storage.outputs.storageAccountName
output storageAccountId string = storage.outputs.storageAccountId
output containerName string = storage.outputs.containerName
output shareName string = storage.outputs.shareName
output primaryBlobEndpoint string = storage.outputs.primaryBlobEndpoint
output primaryFileEndpoint string = storage.outputs.primaryFileEndpoint

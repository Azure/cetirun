// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Premium Blob or Azure Files NFS storage for AKS skill artifacts.

param storageAccountName string
param location string
param containerName string
@allowed([
  'blob'
  'azure-files-nfs'
])
param storageBackend string = 'blob'
param shareName string = 'aks-skills'
@minValue(1)
param shareQuotaGiB int = 100
param tags object = {}

@description('AKS node subnet IDs allowed to access the storage account via NFS.')
param allowedSubnetIds array

@description('Subnet of an optional private endpoint for the blob service. Empty creates no private endpoint.')
param privateEndpointSubnetId string = ''

@description('Private DNS zone (privatelink.blob) where the private endpoint registers its address. Empty registers none.')
param privateDnsZoneId string = ''

var virtualNetworkRules = [for subnetId in allowedSubnetIds: {
  id: subnetId
  action: 'Allow'
}]
var networkAcls = {
  defaultAction: 'Deny'
  bypass: 'AzureServices'
  virtualNetworkRules: virtualNetworkRules
}

@description('Public network access of the account. Disabled leaves only private endpoints.')
@allowed([
  'Enabled'
  'Disabled'
])
param publicNetworkAccess string = 'Enabled'

resource storageAccount 'Microsoft.Storage/storageAccounts@2023-05-01' = {
  name: storageAccountName
  location: location
  tags: tags
  kind: storageBackend == 'azure-files-nfs' ? 'FileStorage' : 'BlockBlobStorage'
  sku: {
    name: 'Premium_LRS'
  }
  properties: union({
    allowBlobPublicAccess: false
    supportsHttpsTrafficOnly: storageBackend == 'azure-files-nfs'
    minimumTlsVersion: 'TLS1_2'
    isHnsEnabled: storageBackend == 'blob'
    isNfsV3Enabled: storageBackend == 'blob'
    // Azure Files NFS is always private-only, including direct Bicep deployments
    // that do not use deploy.sh's input validation.
    publicNetworkAccess: storageBackend == 'azure-files-nfs' ? 'Disabled' : publicNetworkAccess
    networkAcls: networkAcls
  }, storageBackend == 'azure-files-nfs' ? {
    allowSharedKeyAccess: false
  } : {})
}

resource blobService 'Microsoft.Storage/storageAccounts/blobServices@2023-05-01' = if (storageBackend == 'blob') {
  parent: storageAccount
  name: 'default'
}

resource container 'Microsoft.Storage/storageAccounts/blobServices/containers@2023-05-01' = if (storageBackend == 'blob') {
  parent: blobService
  name: containerName
  properties: {
    publicAccess: 'None'
  }
}

// Encryption-in-transit for NFS was added to the stable 2025-01-01 API.
resource fileService 'Microsoft.Storage/storageAccounts/fileServices@2025-01-01' = if (storageBackend == 'azure-files-nfs') {
  parent: storageAccount
  name: 'default'
  properties: {
    protocolSettings: {
      nfs: {
        encryptionInTransit: {
          required: true
        }
      }
    }
  }
}

resource share 'Microsoft.Storage/storageAccounts/fileServices/shares@2025-01-01' = if (storageBackend == 'azure-files-nfs') {
  parent: fileService
  name: shareName
  properties: {
    shareQuota: shareQuotaGiB
    enabledProtocols: 'NFS'
    rootSquash: 'RootSquash'
  }
}

// NFS 3.0 authorizes clients only by network. A private endpoint gives the
// blob service a private address in the selected subnet.
resource privateEndpoint 'Microsoft.Network/privateEndpoints@2023-11-01' = if (!empty(privateEndpointSubnetId)) {
  name: '${storageAccountName}-${storageBackend == 'blob' ? 'blob' : 'file'}'
  location: location
  tags: tags
  properties: {
    subnet: {
      id: privateEndpointSubnetId
    }
    privateLinkServiceConnections: [
      {
        name: '${storageAccountName}-${storageBackend == 'blob' ? 'blob' : 'file'}'
        properties: {
          privateLinkServiceId: storageAccount.id
          groupIds: [
            storageBackend == 'blob' ? 'blob' : 'file'
          ]
        }
      }
    ]
  }
}

// Clients resolve the account's blob host name to the private address through
// this zone; link the zone to the cluster's virtual network.
resource privateDnsZoneGroup 'Microsoft.Network/privateEndpoints/privateDnsZoneGroups@2023-11-01' = if (!empty(privateEndpointSubnetId) && !empty(privateDnsZoneId)) {
  parent: privateEndpoint
  name: 'default'
  properties: {
    privateDnsZoneConfigs: [
      {
        name: storageBackend == 'blob' ? 'blob' : 'file'
        properties: {
          privateDnsZoneId: privateDnsZoneId
        }
      }
    ]
  }
}

output storageAccountName string = storageAccount.name
output storageAccountId string = storageAccount.id
output containerName string = storageBackend == 'blob' ? container.name : ''
output shareName string = storageBackend == 'azure-files-nfs' ? share.name : ''
output primaryBlobEndpoint string = storageBackend == 'blob' ? storageAccount.properties.primaryEndpoints.blob : ''
output primaryFileEndpoint string = storageBackend == 'azure-files-nfs' ? storageAccount.properties.primaryEndpoints.file : ''

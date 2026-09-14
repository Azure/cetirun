// Premium Blob Storage account and container for AKS skill artifacts.

param storageAccountName string
param location string
param containerName string
param tags object = {}

@description('AKS node subnet IDs allowed to access the storage account via NFS.')
param allowedSubnetIds array

resource storageAccount 'Microsoft.Storage/storageAccounts@2023-05-01' = {
  name: storageAccountName
  location: location
  tags: tags
  kind: 'BlockBlobStorage'
  sku: {
    name: 'Premium_LRS'
  }
  properties: {
    allowBlobPublicAccess: false
    supportsHttpsTrafficOnly: false // NFS v3 requires non-encrypted TCP
    minimumTlsVersion: 'TLS1_2'
    isHnsEnabled: true // Required by NFS v3
    isNfsV3Enabled: true
    networkAcls: {
      defaultAction: 'Deny' // NFS v3 requires Deny with explicit subnet allow-rules
      bypass: 'AzureServices'
      virtualNetworkRules: [for subnetId in allowedSubnetIds: {
        id: subnetId
        action: 'Allow'
      }]
    }
  }
}

resource blobService 'Microsoft.Storage/storageAccounts/blobServices@2023-05-01' = {
  parent: storageAccount
  name: 'default'
}

resource container 'Microsoft.Storage/storageAccounts/blobServices/containers@2023-05-01' = {
  parent: blobService
  name: containerName
  properties: {
    publicAccess: 'None'
  }
}

output storageAccountName string = storageAccount.name
output storageAccountId string = storageAccount.id
output containerName string = container.name
output primaryBlobEndpoint string = storageAccount.properties.primaryEndpoints.blob

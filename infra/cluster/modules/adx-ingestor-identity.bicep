// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// An Azure identity of a CetiRun workload that writes to Azure Data Explorer:
// a user-assigned managed identity that the workload's Kubernetes service
// account uses through workload identity. The ADX-Mon ingestor has one, and
// the AI Gateway another for its content. Their only grants are the database
// roles that the ADX module assigns.

param name string
param location string = resourceGroup().location
param tags object = {
  project: 'cetirun'
}

@description('OIDC issuer URL of the AKS cluster, exactly as AKS returns it.')
param oidcIssuerUrl string

@description('Namespace of the workload: the cetirun-observability or platform release.')
param namespace string = 'cetirun-system'

@description('Service account of the workload; the observability chart names the ingestor\'s cetirun-adx-mon-ingestor.')
param serviceAccountName string = 'cetirun-adx-mon-ingestor'

@description('Name of the federated credential on the identity.')
param credentialName string = 'cetirun-adx-mon-ingestor'

resource identity 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' = {
  name: name
  location: location
  tags: tags
}

resource federation 'Microsoft.ManagedIdentity/userAssignedIdentities/federatedIdentityCredentials@2023-01-31' = {
  parent: identity
  name: credentialName
  properties: {
    issuer: oidcIssuerUrl
    subject: 'system:serviceaccount:${namespace}:${serviceAccountName}'
    audiences: [
      'api://AzureADTokenExchange'
    ]
  }
}

output clientId string = identity.properties.clientId
output principalId string = identity.properties.principalId

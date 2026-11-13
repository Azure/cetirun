// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// The Kubernetes audit events of an AKS cluster on their way to Azure Data
// Explorer: an Event Hub namespace in the AKS region with the hub kube-audit,
// the AKS diagnostic setting that sends the category kube-audit-admin there,
// and the role that lets the Kusto cluster's identity read it. Deploy it to
// the resource group of the AKS cluster; the audit connection module then
// creates the data connection of the Kusto cluster.

@description('Name of the existing AKS cluster in this resource group.')
param aksClusterName string

@description('Region of the AKS cluster: diagnostic settings send only to an Event Hub in the same region.')
param location string = resourceGroup().location

param tags object = {
  project: 'cetirun'
}

@description('Name of the Event Hub namespace: 6 to 50 letters, digits and hyphens, starting with a letter. Namespace names are unique in Azure.')
@minLength(6)
@maxLength(50)
param namespaceName string

@description('Principal ID of the Kusto cluster\'s system-assigned identity, which reads the hub.')
param receiverPrincipalId string

var hubName = 'kube-audit'
var consumerGroupName = 'adx'
// Azure Event Hubs Data Receiver
var receiverRoleId = 'a638d3c7-ab3a-418d-83e6-5f17a39d4fde'

resource aks 'Microsoft.ContainerService/managedClusters@2026-01-01' existing = {
  name: aksClusterName
}

resource namespace 'Microsoft.EventHub/namespaces@2024-01-01' = {
  name: namespaceName
  location: location
  tags: tags
  sku: {
    name: 'Standard'
    tier: 'Standard'
    capacity: 1
  }
  properties: {
    minimumTlsVersion: '1.2'
    // The diagnostic setting sends with a shared access key of the namespace.
    disableLocalAuth: false
    isAutoInflateEnabled: false
  }
}

resource hub 'Microsoft.EventHub/namespaces/eventhubs@2024-01-01' = {
  parent: namespace
  name: hubName
  properties: {
    partitionCount: 4
    messageRetentionInDays: 1
  }
}

resource consumerGroup 'Microsoft.EventHub/namespaces/eventhubs/consumergroups@2024-01-01' = {
  parent: hub
  name: consumerGroupName
}

// Diagnostic settings stream to an Event Hub with a rule that can manage,
// send and listen; RootManageSharedAccessKey is that rule of every namespace.
resource auditSetting 'Microsoft.Insights/diagnosticSettings@2021-05-01-preview' = {
  scope: aks
  name: 'cetirun-kube-audit'
  properties: {
    eventHubAuthorizationRuleId: '${namespace.id}/authorizationRules/RootManageSharedAccessKey'
    eventHubName: hub.name
    logs: [
      {
        category: 'kube-audit-admin'
        enabled: true
      }
    ]
  }
}

// On the namespace: the data connection reads the hub and its consumer group.
resource receiver 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  scope: namespace
  name: guid(namespace.id, receiverPrincipalId, receiverRoleId)
  properties: {
    roleDefinitionId: subscriptionResourceId('Microsoft.Authorization/roleDefinitions', receiverRoleId)
    principalId: receiverPrincipalId
    principalType: 'ServicePrincipal'
  }
}

output eventHubId string = hub.id
output consumerGroupName string = consumerGroup.name

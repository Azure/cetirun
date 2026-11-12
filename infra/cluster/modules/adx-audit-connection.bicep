// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// The Event Hub data connection of the audit database: the Kusto cluster
// reads the hub of the AKS audit module with its system-assigned identity and
// ingests each message as one row of KubeAuditLogs. Deploy it to the resource
// group of the Kusto cluster, after the AKS audit module has granted that
// identity the receiver role on the hub's namespace.

param clusterName string
param location string = resourceGroup().location
param databaseName string = 'CetiRunAudit'

@description('Resource ID of the Event Hub that the AKS diagnostic setting sends to.')
param eventHubId string

param consumerGroupName string = 'adx'

resource cluster 'Microsoft.Kusto/clusters@2024-04-13' existing = {
  name: clusterName
}

resource database 'Microsoft.Kusto/clusters/databases@2024-04-13' existing = {
  parent: cluster
  name: databaseName
}

resource connection 'Microsoft.Kusto/clusters/databases/dataConnections@2024-04-13' = {
  parent: database
  name: 'kube-audit'
  location: location
  kind: 'EventHub'
  properties: {
    eventHubResourceId: eventHubId
    consumerGroup: consumerGroupName
    // The message is {"records": [...]}: MULTIJSON takes it as one record,
    // and the mapping keeps its array.
    dataFormat: 'MULTIJSON'
    tableName: 'KubeAuditLogs'
    mappingRuleName: 'KubeAuditLogsMapping'
    compression: 'None'
    managedIdentityResourceId: cluster.id
    databaseRouting: 'Single'
  }
}

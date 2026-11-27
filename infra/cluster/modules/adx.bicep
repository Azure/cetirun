// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Azure Data Explorer for ADX-Mon: an optional Kusto cluster, the three
// CetiRun databases, their retention, caching and ingestion batching, and the
// database roles of the ADX-Mon ingestor and of the people who read the data.
// Deploy it to the resource group of the Kusto cluster. Several CetiRun
// clusters can share one Kusto cluster: each one's rows carry its name in the
// Cluster column.
//
// The ingestor creates the tables and its query functions on first use, so it
// is a database User (create tables and functions) and Ingestor (write data),
// not an Admin. ADX-Mon also alters three functions at every start; this
// module creates them and makes the ingestor their admin, so that several
// clusters' ingestors can share the metrics database. Retention, caching and
// batching are database properties that this module owns. The tables that the
// query functions read are created here too, so that the functions work
// before the first rows arrive.
//
// Optionally, the module also makes the ADX-Mon alerter a Viewer of the logs
// and metrics databases, and creates the database of Kubernetes audit events.

@description('Name of the Kusto cluster: 4 to 22 lowercase letters and digits, starting with a letter.')
@minLength(4)
@maxLength(22)
param clusterName string

@description('Create the Kusto cluster. False uses an existing cluster of this name in this resource group.')
param createCluster bool = true

param location string = resourceGroup().location

@description('Kusto cluster SKU. The default Dev SKU has one engine node and no SLA; use a production SKU for production data.')
param skuName string = 'Dev(No SLA)_Standard_E2a_v4'

@allowed(['Basic', 'Standard'])
param skuTier string = 'Basic'

@minValue(1)
param skuCapacity int = 1

param tags object = {
  project: 'cetirun'
}

@description('Database of platform logs, AI Gateway request logs, Kubernetes events and the Agent inventory.')
param logsDatabaseName string = 'CetiRunLogs'

@description('Database of metrics.')
param metricsDatabaseName string = 'CetiRunMetrics'

@description('Database of Agent workload logs, which can hold conversation content. Grant it separately.')
param agentLogsDatabaseName string = 'CetiRunAgents'

@minValue(1)
param logsRetentionDays int = 90

@minValue(1)
param metricsRetentionDays int = 30

@minValue(1)
param agentLogsRetentionDays int = 14

@minValue(1)
@description('Days of each database kept in the hot cache, at most its retention.')
param hotCacheDays int = 7

@minValue(10)
@maxValue(300)
@description('Longest time ADX waits before it ingests a batch, in seconds. Shorter makes data visible sooner and creates more extents.')
param ingestionBatchingSeconds int = 30

@description('Client (application) ID of the ADX-Mon ingestor identity. Empty skips its role assignments.')
param ingestorClientId string = ''

param tenantId string = tenant().tenantId

@description('Principals that read the logs and metrics databases: objects with principalId (Entra group or user object ID, or application ID) and principalType (Group, User or App).')
param viewers array = []

@description('Principals that read the Agent logs database, in the same form as viewers.')
param agentLogsViewers array = []

@description('Create the database of AI Gateway prompts and responses. The ADX-Mon ingestor gets no role on it.')
param createContentDatabase bool = false

@description('Database of AI Gateway prompts and responses.')
param contentDatabaseName string = 'CetiRunContent'

@minValue(1)
param contentRetentionDays int = 7

@description('Client (application) ID of the AI Gateway\'s identity, which writes content records and reads them back for the LiteLLM console. Empty skips its roles.')
param contentWriterClientId string = ''

@description('Principals that read the content database, in the same form as viewers. Keep it small: it holds conversations.')
param contentViewers array = []

@minValue(1)
@description('Days of the daily AI Gateway usage view GatewayDailyUsageV2 and of the coverage view GatewayCoverage in the logs database. It can be longer than the logs retention.')
param gatewayUsageRetentionDays int = 400

@description('Client (application) ID of the ADX-Mon alerter identity, which runs alert queries. Empty skips its Viewer roles on the logs and metrics databases.')
param alerterClientId string = ''

@description('Create the database of Kubernetes audit events. A new Kusto cluster then gets a system-assigned identity, which the Event Hub data connection uses; an existing cluster must have one.')
param auditLogs bool = false

@description('Database of Kubernetes audit events.')
param auditDatabaseName string = 'CetiRunAudit'

@minValue(1)
param auditRetentionDays int = 90

@description('Principals that read the audit database, in the same form as viewers.')
param auditViewers array = []

// The query functions of the databases. Their files are scripts of management
// commands that can also run on a Kusto cluster made without this module;
// their comment lines are left out here. The daily usage view and the coverage
// view follow the functions, after the logs script has created their table.
var logsFunctions = join(filter(split('${loadTextContent('adx/cetirun-logs.kql')}\n${replace(loadTextContent('adx/cetirun-gateway-usage.kql'), '__RETENTION_DAYS__', string(gatewayUsageRetentionDays))}\n${replace(loadTextContent('adx/cetirun-gateway-coverage.kql'), '__RETENTION_DAYS__', string(gatewayUsageRetentionDays))}', '\n'), line => !startsWith(line, '//')), '\n')
var metricsFunctions = join(filter(split('${loadTextContent('adx/cetirun-metrics.kql')}\n${replace(loadTextContent('adx/cetirun-health.kql'), '__LOGS_DATABASE__', logsDatabaseName)}\n${loadTextContent('adx/adxmon-functions.kql')}', '\n'), line => !startsWith(line, '//')), '\n')
// Each CetiRun cluster's ingestor alters ADX-Mon's functions at start, which
// needs it to be their admin; a second cluster's deployment adds its own.
var adxmonFunctionAdmins = empty(ingestorClientId) ? '' : join(map(['prom_increase', 'prom_rate', 'prom_delta'], name => format('.add function {0} admins (\'aadapp={1};{2}\') \'ADX-Mon ingestor\'', name, ingestorClientId, tenantId)), '\n\n')
var agentLogsFunctions = replace(join(filter(split(loadTextContent('adx/cetirun-agents.kql'), '\n'), line => !startsWith(line, '//')), '\n'), '__LOGS_DATABASE__', logsDatabaseName)

var databases = [
  {
    name: logsDatabaseName
    retentionDays: logsRetentionDays
    agentLogs: false
    functions: logsFunctions
  }
  {
    name: metricsDatabaseName
    retentionDays: metricsRetentionDays
    agentLogs: false
    functions: join(filter([metricsFunctions, adxmonFunctionAdmins], part => !empty(part)), '\n\n')
  }
  {
    name: agentLogsDatabaseName
    retentionDays: agentLogsRetentionDays
    agentLogs: true
    functions: agentLogsFunctions
  }
]

var ingestorRoles = empty(ingestorClientId) ? [] : ['User', 'Ingestor']

var viewerAssignments = flatten(map(range(0, length(databases)), i => map(databases[i].agentLogs ? agentLogsViewers : viewers, viewer => {
  database: i
  principalId: viewer.principalId
  principalType: viewer.principalType
})))

var ingestorAssignments = flatten(map(range(0, length(databases)), i => map(ingestorRoles, role => {
  database: i
  role: role
})))

// The alerter queries the logs and the metrics database, not the Agent logs.
var alerterDatabases = empty(alerterClientId) ? [] : [0, 1]

resource newCluster 'Microsoft.Kusto/clusters@2024-04-13' = if (createCluster) {
  name: clusterName
  location: location
  tags: tags
  sku: {
    name: skuName
    tier: skuTier
    capacity: skuCapacity
  }
  // The Event Hub data connection of the audit database reads as this identity.
  identity: auditLogs ? {
    type: 'SystemAssigned'
  } : null
  properties: {
    // A cluster without ingestion or queries for five days stops; a stopped
    // cluster bills storage only and must be started by hand.
    enableAutoStop: true
    enableStreamingIngest: false
    publicNetworkAccess: 'Enabled'
    // Only the cluster's own tenant.
    trustedExternalTenants: []
  }
}

resource cluster 'Microsoft.Kusto/clusters@2024-04-13' existing = {
  name: clusterName
}

resource database 'Microsoft.Kusto/clusters/databases@2024-04-13' = [for item in databases: {
  parent: cluster
  name: item.name
  location: location
  kind: 'ReadWrite'
  properties: {
    softDeletePeriod: 'P${item.retentionDays}D'
    hotCachePeriod: 'P${min(hotCacheDays, item.retentionDays)}D'
  }
  dependsOn: [
    newCluster
  ]
}]

// Ingestion batching, then the query functions. The deploying principal, an
// Admin of the databases it creates, runs the script.
resource schema 'Microsoft.Kusto/clusters/databases/scripts@2024-04-13' = [for (item, i) in databases: {
  parent: database[i]
  name: 'cetirun-schema'
  properties: {
    // The script holds no secret: database names, a policy and functions.
    #disable-next-line use-secure-value-for-secure-inputs
    scriptContent: join(filter([
      format('.alter database [\'{0}\'] policy ingestionbatching \'{{"MaximumBatchingTimeSpan":"{1}","MaximumNumberOfItems":500,"MaximumRawDataSizeMB":1024}}\'', item.name, format('00:{0:00}:{1:00}', ingestionBatchingSeconds / 60, ingestionBatchingSeconds % 60))
      item.functions
    ], part => !empty(part)), '\n\n')
    continueOnErrors: false
  }
}]

resource ingestorAssignment 'Microsoft.Kusto/clusters/databases/principalAssignments@2024-04-13' = [for item in ingestorAssignments: {
  parent: database[item.database]
  name: guid(cluster.id, databases[item.database].name, ingestorClientId, item.role)
  properties: {
    principalId: ingestorClientId
    principalType: 'App'
    role: item.role
    tenantId: tenantId
  }
}]

resource viewerAssignment 'Microsoft.Kusto/clusters/databases/principalAssignments@2024-04-13' = [for item in viewerAssignments: {
  parent: database[item.database]
  name: guid(cluster.id, databases[item.database].name, item.principalId, 'Viewer')
  properties: {
    principalId: item.principalId
    principalType: item.principalType
    role: 'Viewer'
    tenantId: tenantId
  }
}]

resource alerterAssignment 'Microsoft.Kusto/clusters/databases/principalAssignments@2024-04-13' = [for index in alerterDatabases: {
  parent: database[index]
  name: guid(cluster.id, databases[index].name, alerterClientId, 'Viewer')
  properties: {
    principalId: alerterClientId
    principalType: 'App'
    role: 'Viewer'
    tenantId: tenantId
  }
}]

// Kubernetes audit events, which an Event Hub data connection writes: the
// ADX-Mon ingestor gets no role on this database, and only auditViewers read
// it. The audit connection module creates the data connection once the
// cluster's identity can read the Event Hub.
resource auditDatabase 'Microsoft.Kusto/clusters/databases@2024-04-13' = if (auditLogs) {
  parent: cluster
  name: auditDatabaseName
  location: location
  kind: 'ReadWrite'
  properties: {
    softDeletePeriod: 'P${auditRetentionDays}D'
    hotCachePeriod: 'P${min(hotCacheDays, auditRetentionDays)}D'
  }
  dependsOn: [
    newCluster
  ]
}

resource auditSchema 'Microsoft.Kusto/clusters/databases/scripts@2024-04-13' = if (auditLogs) {
  parent: auditDatabase
  name: 'cetirun-schema'
  properties: {
    #disable-next-line use-secure-value-for-secure-inputs
    scriptContent: join([
      format('.alter database [\'{0}\'] policy ingestionbatching \'{{"MaximumBatchingTimeSpan":"{1}","MaximumNumberOfItems":500,"MaximumRawDataSizeMB":1024}}\'', auditDatabaseName, format('00:{0:00}:{1:00}', ingestionBatchingSeconds / 60, ingestionBatchingSeconds % 60))
      join(filter(split(loadTextContent('adx/cetirun-audit.kql'), '\n'), line => !startsWith(line, '//')), '\n')
    ], '\n\n')
    continueOnErrors: false
  }
}

resource auditViewer 'Microsoft.Kusto/clusters/databases/principalAssignments@2024-04-13' = [for viewer in (auditLogs ? auditViewers : []): {
  parent: auditDatabase
  name: guid(cluster.id, auditDatabaseName, viewer.principalId, 'Viewer')
  properties: {
    principalId: viewer.principalId
    principalType: viewer.principalType
    role: 'Viewer'
    tenantId: tenantId
  }
}]

// Prompts and responses, apart from every other database: the ADX-Mon
// ingestor cannot write here, and only contentViewers read it. The table is
// created here because a writer with the Ingestor role cannot create tables.
resource contentDatabase 'Microsoft.Kusto/clusters/databases@2024-04-13' = if (createContentDatabase) {
  parent: cluster
  name: contentDatabaseName
  location: location
  kind: 'ReadWrite'
  properties: {
    softDeletePeriod: 'P${contentRetentionDays}D'
    hotCachePeriod: 'P${min(hotCacheDays, contentRetentionDays)}D'
  }
  dependsOn: [
    newCluster
  ]
}

resource contentSchema 'Microsoft.Kusto/clusters/databases/scripts@2024-04-13' = if (createContentDatabase) {
  parent: contentDatabase
  name: 'cetirun-schema'
  properties: {
    #disable-next-line use-secure-value-for-secure-inputs
    scriptContent: join([
      format('.alter database [\'{0}\'] policy ingestionbatching \'{{"MaximumBatchingTimeSpan":"{1}","MaximumNumberOfItems":500,"MaximumRawDataSizeMB":1024}}\'', contentDatabaseName, format('00:{0:00}:{1:00}', ingestionBatchingSeconds / 60, ingestionBatchingSeconds % 60))
      join(filter(split(loadTextContent('adx/cetirun-content.kql'), '\n'), line => !startsWith(line, '//')), '\n')
    ], '\n\n')
    continueOnErrors: false
  }
}

// The AI Gateway writes content and reads one call's content back for the
// LiteLLM console, so its identity is Ingestor and Viewer of this database.
resource contentWriter 'Microsoft.Kusto/clusters/databases/principalAssignments@2024-04-13' = [for role in ((createContentDatabase && !empty(contentWriterClientId)) ? ['Ingestor', 'Viewer'] : []): {
  parent: contentDatabase
  name: guid(cluster.id, contentDatabaseName, contentWriterClientId, role)
  properties: {
    principalId: contentWriterClientId
    principalType: 'App'
    role: role
    tenantId: tenantId
  }
}]

resource contentViewer 'Microsoft.Kusto/clusters/databases/principalAssignments@2024-04-13' = [for viewer in (createContentDatabase ? contentViewers : []): {
  parent: contentDatabase
  name: guid(cluster.id, contentDatabaseName, viewer.principalId, 'Viewer')
  properties: {
    principalId: viewer.principalId
    principalType: viewer.principalType
    role: 'Viewer'
    tenantId: tenantId
  }
}]

@description('Query URI of the Kusto cluster; the ingestor derives the ingestion URI from it.')
output clusterUri string = cluster.properties.uri
output dataIngestionUri string = cluster.properties.dataIngestionUri
output logsDatabaseName string = logsDatabaseName
output metricsDatabaseName string = metricsDatabaseName
output agentLogsDatabaseName string = agentLogsDatabaseName
output contentDatabaseName string = createContentDatabase ? contentDatabaseName : ''
output auditDatabaseName string = auditLogs ? auditDatabaseName : ''
@description('Resource ID of the Kusto cluster, the managed identity of the audit data connection.')
output clusterId string = cluster.id
@description('Principal ID of the Kusto cluster\'s system-assigned identity, with audit logs; empty otherwise.')
output principalId string = auditLogs ? (createCluster ? newCluster!.identity.principalId : cluster.identity.principalId) : ''

param tenkacloudNamePrefix string
param tenkacloudProblemId string
param tenkacloudTeam string

resource environment 'Microsoft.App/managedEnvironments@2024-03-01' = {
  name: 'tc-${uniqueString(tenkacloudNamePrefix, tenkacloudProblemId, tenkacloudTeam)}-env'
  location: 'japaneast'
  properties: {}
}

resource app 'Microsoft.App/containerApps@2024-03-01' = {
  name: 'tc-${uniqueString(tenkacloudNamePrefix, tenkacloudProblemId, tenkacloudTeam)}-app'
  location: 'japaneast'
  properties: {
    environmentId: environment.id
    configuration: {
      ingress: {
        external: true
        targetPort: 8080
      }
    }
    template: {
      containers: [
        {
          name: 'web'
          image: 'mcr.microsoft.com/k8se/quickstart:latest'
        }
      ]
      scale: {
        minReplicas: 0
        maxReplicas: 2
      }
    }
  }
}

resource participantRole 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: 'scanner-container-app-reader'
  scope: app
  dependsOn: [
    app
  ]
  properties: {
    roleDefinitionId: 'ContainerAppReader'
    principalId: 'participant@example.test'
  }
}

output AzureHelloUrl string = 'https://${app.properties.configuration.ingress.fqdn}'

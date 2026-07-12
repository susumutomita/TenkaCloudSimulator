resource helloApp 'Microsoft.App/containerApps@2024-03-01' = {
  name: 'hello-container-app'
  location: 'japaneast'
  properties: {
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
        maxReplicas: 3
      }
    }
  }
}

resource participantRole 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: 'participant-container-app-reader'
  scope: helloApp
  dependsOn: [
    helloApp
  ]
  properties: {
    roleDefinitionId: 'ContainerAppReader'
    principalId: 'participant@example.test'
  }
}

output containerAppId string = helloApp.id
output containerAppFqdn string = helloApp.properties.configuration.ingress.fqdn
output roleAssignmentId string = participantRole.id

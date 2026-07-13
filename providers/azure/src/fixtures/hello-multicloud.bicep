// Hello Multicloud (azure-hello target).
//
// The Azure adapter injects these three values for every target deployment.
// uniqueString keeps the resource names bounded and collision-resistant even
// when the full TenkaCloud deployment prefix exceeds Azure's name limits.

param tenkacloudNamePrefix string
param tenkacloudProblemId string
param tenkacloudTeam string

resource helloEnvironment 'Microsoft.App/managedEnvironments@2024-03-01' = {
  name: 'tc-${uniqueString(tenkacloudNamePrefix, tenkacloudProblemId, tenkacloudTeam)}-env'
  location: 'japaneast'
  properties: {}
}

resource helloApp 'Microsoft.App/containerApps@2024-03-01' = {
  name: 'tc-${uniqueString(tenkacloudNamePrefix, tenkacloudProblemId, tenkacloudTeam)}-app'
  location: 'japaneast'
  properties: {
    environmentId: helloEnvironment.id
    configuration: {
      ingress: {
        external: true
        targetPort: 8080
      }
    }
    template: {
      containers: [
        {
          name: 'hello'
          image: 'ghcr.io/susumutomita/tenkacloud-challenge-microservice-migration@sha256:96c7ca29de82b7d0c041e98f9cd9494de283102509134e5fb524d6e89da27cf2'
          resources: {
            cpu: json('0.5')
            memory: '1Gi'
          }
        }
      ]
      scale: {
        minReplicas: 0
        maxReplicas: 1
      }
    }
  }
}

output AzureHelloUrl string = 'https://${helloApp.properties.configuration.ingress.fqdn}'

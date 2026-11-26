package server

import "github.com/susumutomita/TenkaCloudSimulator/internal/aws/cognitoidp"

func cognitoUserPoolCapabilities(localFederation bool) map[string]any {
	c := map[string]any{
		"operations":              []string{"CreateUserPool", "DescribeUserPool", "ListUserPools", "DeleteUserPool"},
		"protocol":                "JSON 1.1 (X-Amz-Target AWSCognitoIdentityProviderService.*)",
		"nativeSDKAuthentication": true, "poolLimitPerAccountRegion": cognitoidp.PoolLimit,
		"metadataOnly": true, "console": false, "scalar": false, "signIn": false,
		"unsupported": []string{"app clients", "users", "groups", "OAuth", "SAML", "sessions", "token issuance", "triggers", "active deletion protection"},
	}
	if localFederation {
		c["operations"] = append(c["operations"].([]string), cognitoidp.LocalFederationConfigOperations()...)
		c["localFederationConfiguration"] = map[string]any{
			"mode": "explicit signed numeric-loopback configuration only", "providerType": "SAML",
			"providers": []string{"Keycloak", "Entra"}, "metadata": "synthetic inline MetadataFile only",
			"publicClientProfile": "authorization_code, S256 configuration only",
			"requestBytes":        cognitoidp.RequestLimit, "console": false, "scalar": false, "signIn": false,
		}
		c["unsupported"] = []string{"client or provider update, list and individual delete", "users", "groups",
			"SAML and OAuth sign-in", "sessions", "token issuance", "triggers", "active deletion protection"}
	}
	return c
}

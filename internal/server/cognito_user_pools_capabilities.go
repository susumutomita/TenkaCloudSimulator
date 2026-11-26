package server

import "github.com/susumutomita/TenkaCloudSimulator/internal/aws/cognitoidp"

func cognitoUserPoolCapabilities() map[string]any {
	return map[string]any{
		"operations":              []string{"CreateUserPool", "DescribeUserPool", "ListUserPools", "DeleteUserPool"},
		"protocol":                "JSON 1.1 (X-Amz-Target AWSCognitoIdentityProviderService.*)",
		"nativeSDKAuthentication": true, "poolLimitPerAccountRegion": cognitoidp.PoolLimit,
		"metadataOnly": true, "console": false, "scalar": false, "signIn": false,
		"unsupported": []string{"app clients", "users", "groups", "OAuth", "SAML", "sessions", "token issuance", "triggers", "active deletion protection"},
	}
}

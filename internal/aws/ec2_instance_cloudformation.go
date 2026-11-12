package aws

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

func compileStackInstance(stackID, logical string, p map[string]any, attrs map[string]string) (string, error) {
	for key := range p {
		switch key {
		case "ImageId", "InstanceType", "IamInstanceProfile", "SubnetId", "SecurityGroupIds", "Tags", "UserData":
		default:
			return "", fmt.Errorf("unsupported EC2 instance property %s", key)
		}
	}
	for _, key := range []string{"ImageId", "InstanceType", "IamInstanceProfile", "SubnetId", "UserData"} {
		v, ok := p[key].(string)
		if !ok || v == "" {
			return "", fmt.Errorf("instance requires %s", key)
		}
	}
	if p["InstanceType"] != "t3.micro" && p["InstanceType"] != "t3.small" {
		return "", fmt.Errorf("unsupported bounded instance type")
	}
	if _, err := networkTags(p["Tags"]); err != nil {
		return "", err
	}
	if value, err := base64.StdEncoding.Strict().DecodeString(p["UserData"].(string)); err != nil || len(value) > 16384 {
		return "", fmt.Errorf("invalid bounded instance UserData")
	}
	groups, ok := p["SecurityGroupIds"].([]any)
	if !ok || len(groups) == 0 || len(groups) > 5 {
		return "", fmt.Errorf("invalid instance security groups")
	}
	seen := map[string]bool{}
	for _, group := range groups {
		s, ok := group.(string)
		if !ok || s == "" || seen[s] {
			return "", fmt.Errorf("invalid instance security group")
		}
		seen[s] = true
	}
	sum := sha256.Sum256([]byte(stackID + "\x00" + logical))
	id := "i-" + hex.EncodeToString(sum[:])[:17]
	attrs[logical+".PublicDnsName"] = EC2InstancePublicDNSName(id)
	return id, nil
}

// The initial local runtime supports a native same-stack profile only. Its
// existing native IAM admission/readback checks bind the role incarnation; this
// helper never assumes a profile name itself confers PassRole authority.
func stackInstanceRole(record StackRecord, r StackResource) (string, error) {
	name := stringValue(r.Properties["IamInstanceProfile"])
	for _, profile := range record.Resources {
		if profile.Type == "AWS::IAM::InstanceProfile" && profile.PhysicalID == name && profile.NativeIAM != nil && profile.NativeIAM.RoleName != "" {
			return profile.NativeIAM.RoleName, nil
		}
	}
	return "", fmt.Errorf("local instance requires a native same-stack instance profile")
}
func stackInstancePermissions(record StackRecord, r StackResource, deleting bool, authorize func(string, string) error) error {
	instanceARN := networkARN(record.Region, "instance", r.PhysicalID)
	if deleting {
		return authorize("ec2:TerminateInstances", instanceARN)
	}
	role, err := stackInstanceRole(record, r)
	if err != nil {
		return err
	}
	if err = authorize("iam:PassRole", "arn:aws:iam::000000000000:role/"+role); err != nil {
		return err
	}
	// Implicit local NIC/root disk share the instance identity. These are not
	// independent EC2 CRUD resources and never provision AWS infrastructure.
	suffix := strings.TrimPrefix(r.PhysicalID, "i-")
	resources := []string{instanceARN, "arn:aws:ec2:" + record.Region + "::image/" + stringValue(r.Properties["ImageId"]), networkARN(record.Region, "subnet", stringValue(r.Properties["SubnetId"])), networkARN(record.Region, "network-interface", "eni-"+suffix), networkARN(record.Region, "volume", "vol-"+suffix)}
	groups, _ := r.Properties["SecurityGroupIds"].([]any)
	for _, group := range groups {
		resources = append(resources, networkARN(record.Region, "security-group", stringValue(group)))
	}
	for _, resource := range resources {
		if err = authorize("ec2:RunInstances", resource); err != nil {
			return err
		}
	}
	if tags, _ := r.Properties["Tags"].([]any); len(tags) > 0 {
		return authorize("ec2:CreateTags", instanceARN)
	}
	return nil
}

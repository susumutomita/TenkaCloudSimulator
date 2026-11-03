package aws

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func cfnS3Settings(properties map[string]any) (string, *publicAccessBlock, []S3Tag, error) {
	invalid := func(message string) (string, *publicAccessBlock, []S3Tag, error) {
		return "", nil, nil, ssmFailure{"ValidationError", message}
	}
	for k := range properties {
		switch k {
		case "BucketName", "VersioningConfiguration", "PublicAccessBlockConfiguration", "Tags":
		default:
			return invalid("Unsupported S3 bucket property: " + k)
		}
	}
	status := ""
	if raw, ok := properties["VersioningConfiguration"]; ok {
		conf, valid := raw.(map[string]any)
		if !valid || len(conf) != 1 {
			return invalid("VersioningConfiguration requires only Status.")
		}
		status = stringValue(conf["Status"])
		if status != "Enabled" && status != "Suspended" {
			return invalid("S3 versioning Status must be Enabled or Suspended.")
		}
	}
	var access *publicAccessBlock
	if raw, ok := properties["PublicAccessBlockConfiguration"]; ok {
		conf, valid := raw.(map[string]any)
		if !valid {
			return invalid("PublicAccessBlockConfiguration must be an object.")
		}
		access = &publicAccessBlock{}
		fields := map[string]*bool{"BlockPublicAcls": &access.BlockPublicAcls, "IgnorePublicAcls": &access.IgnorePublicAcls, "BlockPublicPolicy": &access.BlockPublicPolicy, "RestrictPublicBuckets": &access.RestrictPublicBuckets}
		for key, value := range conf {
			target, known := fields[key]
			b, valid := value.(bool)
			if !known || !valid {
				return invalid("Public access block requires supported boolean fields.")
			}
			*target = b
		}
	}
	tags := []S3Tag{}
	if raw, ok := properties["Tags"]; ok {
		list, valid := raw.([]any)
		if !valid {
			return invalid("S3 Tags must be an array.")
		}
		for _, item := range list {
			tag, valid := item.(map[string]any)
			if !valid || len(tag) != 2 {
				return invalid("Each S3 tag requires Key and Value.")
			}
			key, k := tag["Key"].(string)
			value, v := tag["Value"].(string)
			if !k || !v {
				return invalid("S3 tag Key and Value must be strings.")
			}
			tags = append(tags, S3Tag{key, value})
		}
		if err := validateS3Tags(tags); err != nil {
			return invalid(err.Error())
		}
	}
	return status, access, tags, nil
}
func compileS3Bucket(stack, stackID, logical string, properties map[string]any) (string, error) {
	if _, _, _, err := cfnS3Settings(properties); err != nil {
		return "", err
	}
	name := ""
	if raw, exists := properties["BucketName"]; exists {
		var valid bool
		name, valid = raw.(string)
		if !valid || !ValidBucket(name) {
			return "", ssmFailure{"ValidationError", "Invalid explicit S3 BucketName."}
		}
	} else {
		base := strings.ToLower(stack + "-" + logical)
		if len(base) > 46 {
			base = base[:46]
		}
		base = strings.Trim(base, "-")
		hash := sha256.Sum256([]byte(stackID + "\x00" + logical))
		name = base + "-" + hex.EncodeToString(hash[:8])
		if !ValidBucket(name) {
			return "", ssmFailure{"ValidationError", "Cannot generate a DNS-compatible S3 bucket name."}
		}
	}
	return name, nil
}
func createStackBucket(tx *sql.Tx, region string, r StackResource) error {
	status, access, tags, err := cfnS3Settings(r.Properties)
	if err != nil {
		return err
	}
	if err = s3CreateBucket(tx, r.PhysicalID, region); err != nil {
		switch core.Code(err) {
		case "BucketAlreadyExists", "BucketAlreadyOwnedByYou":
			return ssmFailure{"AlreadyExistsException", "S3 bucket already exists; stack adoption is unsupported."}
		case "ServiceUnavailable":
			return ssmFailure{"LimitExceededException", err.Error()}
		default:
			return err
		}
	}
	if status != "" {
		if _, err = tx.Exec("UPDATE aws_s3_config SET status=? WHERE bucket=?", status, r.PhysicalID); err != nil {
			return err
		}
	}
	if access != nil {
		raw, _ := json.Marshal(access)
		if _, err = tx.Exec("UPDATE aws_s3_config SET public_access=? WHERE bucket=?", string(raw), r.PhysicalID); err != nil {
			return err
		}
	}
	return s3WriteTags(tx, r.PhysicalID, tags)
}
func stackResourceNamespace(region, kind string) string {
	if kind == "AWS::IAM::Role" || kind == "AWS::S3::Bucket" {
		return ""
	}
	return region
}

// CFN without a service RoleArn uses the caller's permissions for underlying
// resource actions too. CAPABILITY_IAM acknowledges risk; it grants no rights.
func stackResourcePermissions(record StackRecord, deleting bool, authorize func(string, string) error) error {
	for _, r := range record.Resources {
		actions := []string{}
		resource := ""
		switch r.Type {
		case "AWS::SSM::Parameter":
			resource = "arn:aws:ssm:" + record.Region + ":000000000000:parameter/" + trimParameterSlash(r.PhysicalID)
			if deleting {
				actions = []string{"ssm:DeleteParameter"}
			} else {
				actions = []string{"ssm:PutParameter"}
			}
		case "AWS::IAM::Role":
			resource = "arn:aws:iam::000000000000:role/" + r.PhysicalID
			if deleting {
				actions = []string{"iam:DeleteRolePolicy", "iam:DeleteRole"}
			} else {
				actions = []string{"iam:CreateRole", "iam:PutRolePolicy"}
			}
			if managed, ok := r.Properties["ManagedPolicyArns"].([]any); ok && len(managed) > 0 {
				if deleting {
					actions = append(actions, "iam:DetachRolePolicy")
				} else {
					actions = append(actions, "iam:AttachRolePolicy")
				}
			}
			if tags, ok := r.Properties["Tags"].([]any); !deleting && ok && len(tags) > 0 {
				actions = append(actions, "iam:TagRole")
			}
		case "AWS::S3::Bucket":
			resource = "arn:aws:s3:::" + r.PhysicalID
			if deleting {
				actions = []string{"s3:DeleteBucket"}
			} else {
				actions = []string{"s3:CreateBucket"}
				for _, pair := range [][2]string{{"VersioningConfiguration", "s3:PutBucketVersioning"}, {"PublicAccessBlockConfiguration", "s3:PutBucketPublicAccessBlock"}, {"Tags", "s3:PutBucketTagging"}} {
					if _, ok := r.Properties[pair[0]]; ok {
						actions = append(actions, pair[1])
					}
				}
			}
		default:
			return ssmFailure{"ValidationError", "Unsupported stored stack resource type."}
		}
		for _, action := range actions {
			if err := authorize(action, resource); err != nil {
				return err
			}
		}
	}
	return nil
}

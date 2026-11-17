package aws

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

const stackVpcOrigin = "AWS::CloudFront::VpcOrigin"

func stackVpcOriginConfig(properties map[string]any) (CFOriginConfig, []CFOriginTag, error) {
	for k := range properties {
		if k != "VpcOriginEndpointConfig" && k != "Tags" {
			return CFOriginConfig{}, nil, fmt.Errorf("unsupported VPC origin property %s", k)
		}
	}
	raw := object(properties["VpcOriginEndpointConfig"])
	c := CFOriginConfig{Name: stringValue(raw["Name"]), ARN: stringValue(raw["Arn"]), OriginProtocolPolicy: stringValue(raw["OriginProtocolPolicy"]), HTTPPort: 80, HTTPSPort: 443}
	for k, v := range raw {
		switch k {
		case "Name", "Arn", "OriginProtocolPolicy":
		case "HTTPPort", "HTTPSPort":
			port, err := strconv.Atoi(stringValue(v))
			if err != nil {
				return CFOriginConfig{}, nil, fmt.Errorf("%s must be a TCP port", k)
			}
			if k == "HTTPPort" {
				c.HTTPPort = port
			} else {
				c.HTTPSPort = port
			}
		case "OriginSSLProtocols":
			items, ok := v.([]any)
			if !ok {
				return CFOriginConfig{}, nil, fmt.Errorf("%s must be an array", k)
			}
			protocols := &CFOriginSSLProtocols{Quantity: len(items)}
			for _, value := range items {
				protocols.Items = append(protocols.Items, stringValue(value))
			}
			c.OriginSslProtocols = protocols
		default:
			return CFOriginConfig{}, nil, fmt.Errorf("unsupported VPC origin endpoint property %s", k)
		}
	}
	if err := validateCFOriginConfig(c); err != nil {
		return CFOriginConfig{}, nil, err
	}
	var tags []CFOriginTag
	if value, ok := properties["Tags"]; ok {
		entries, ok := value.([]any)
		if !ok {
			return CFOriginConfig{}, nil, fmt.Errorf("VPC origin Tags must be an array")
		}
		for _, entry := range entries {
			tag := object(entry)
			key, keyOK := tag["Key"].(string)
			value, valueOK := tag["Value"].(string)
			if len(tag) != 2 || !keyOK || !valueOK {
				return CFOriginConfig{}, nil, fmt.Errorf("each VPC origin tag requires Key and Value strings")
			}
			tags = append(tags, CFOriginTag{Key: key, Value: value})
		}
	}
	return c, tags, validateCFOriginTags(tags)
}

func createStackVpcOrigin(ctx context.Context, tx *sql.Tx, record StackRecord, r *StackResource) error {
	c, tags, err := stackVpcOriginConfig(r.Properties)
	if err != nil {
		return ssmFailure{"ValidationError", err.Error()}
	}
	ns := stackResourceNamespace(record.Region, r.Type)
	if _, err = tx.ExecContext(ctx, "INSERT INTO aws_cfn_ownership(namespace,kind,physical_id,stack_id) VALUES(?,?,?,?)", ns, r.Type, r.PhysicalID, record.ID); err != nil {
		return err
	}
	o, err := saveCFOriginTx(ctx, tx, r.PhysicalID, "", true, c, tags)
	if err != nil {
		return stackVpcOriginError(err)
	}
	r.StateParts = map[string]string{"ETag": o.ETag}
	return nil
}

func stackVpcOriginError(err error) error {
	var f cfFailure
	if errors.As(err, &f) {
		return ssmFailure{"ValidationError", f.message}
	}
	return err
}

func deleteStackVpcOrigin(ctx context.Context, tx *sql.Tx, r StackResource) error {
	_, err := deleteCFOriginTx(ctx, tx, r.PhysicalID, r.StateParts["ETag"])
	var f cfFailure
	if errors.As(err, &f) && f.code == "EntityNotFound" {
		return nil
	}
	return stackVpcOriginError(err)
}

package aws

import (
	"encoding/xml"
	"maps"
	"regexp"
	"slices"
)

const ec2ReplaceNetworkACLAssociation = "ReplaceNetworkAclAssociation"

var ec2NetworkACLAssociationIDPattern = regexp.MustCompile(`^aclassoc-(?:[0-9a-f]{8}|[0-9a-f]{17})$`)

type ec2NetworkACLAssociationQuery struct {
	association, acl string
}

func parseEC2NetworkACLAssociation(fields map[string][]string) (ec2NetworkACLAssociationQuery, *ec2Failure) {
	var q ec2NetworkACLAssociationQuery
	for _, key := range slices.Sorted(maps.Keys(fields)) {
		values := fields[key]
		if len(values) != 1 {
			return q, ec2Invalid("InvalidParameterValue", "Each network ACL association parameter must be sent once.")
		}
		switch key {
		case "Action", "Version":
		case "DryRun":
			if values[0] != "false" {
				return q, ec2Invalid("UnsupportedOperation", "Only DryRun=false is supported for local network ACL association changes.")
			}
		case "AssociationId":
			q.association = values[0]
		case "NetworkAclId":
			q.acl = values[0]
		default:
			return q, ec2Invalid("UnsupportedOperation", "Unsupported network ACL association parameter.")
		}
	}
	return q, q.validate()
}

func (q ec2NetworkACLAssociationQuery) validate() *ec2Failure {
	if q.association == "" || q.acl == "" {
		return ec2Invalid("MissingParameter", "AssociationId and NetworkAclId are required.")
	}
	if !ec2NetworkACLAssociationIDPattern.MatchString(q.association) {
		return ec2Invalid("InvalidParameterValue", "AssociationId must be an aclassoc- ID.")
	}
	if !ec2NetworkACLIDPattern.MatchString(q.acl) {
		return ec2Invalid("InvalidNetworkAclId.Malformed", "NetworkAclId must be an acl- ID.")
	}
	return nil
}

// Only the existing EC2 gateway supplies this decision, bound to its
// effective principal. A missing decision never implies administrative access.
type ec2NetworkACLAssociationAuthority struct {
	check           func(string, string) bool
	revealMissingID bool
}

func (a ec2NetworkACLAssociationAuthority) allows(region, resourceType, id string) bool {
	return a.check != nil && a.check("ec2:"+ec2ReplaceNetworkACLAssociation, networkARN(region, resourceType, id))
}

func deniedNetworkACLAssociation() *ec2Failure {
	return &ec2Failure{"UnauthorizedOperation", "The selected identity cannot change this network ACL association.", 403}
}

func (a ec2NetworkACLAssociationAuthority) missingAssociation() *ec2Failure {
	if a.check != nil && a.revealMissingID {
		return ec2Invalid("InvalidAssociationID.NotFound", "The association is not current in this region. Refresh its saved state.")
	}
	return deniedNetworkACLAssociation()
}

type ec2NetworkACLAssociationRead struct {
	ID       string `xml:"networkAclAssociationId"`
	ACLID    string `xml:"networkAclId"`
	SubnetID string `xml:"subnetId"`
}

type ec2ReplaceNetworkACLAssociationResponse struct {
	XMLName          xml.Name `xml:"ReplaceNetworkAclAssociationResponse"`
	XMLNS            string   `xml:"xmlns,attr"`
	RequestID        string   `xml:"requestId"`
	NewAssociationID string   `xml:"newAssociationId"`
}

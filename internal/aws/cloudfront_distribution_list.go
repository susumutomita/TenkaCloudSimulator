package aws

import (
	"database/sql"
	"encoding/xml"
	"net/http"
)

type cfEmptyList struct{ Quantity int }
type cfDistributionSummary struct {
	Id                   string
	ARN                  string
	Status               string
	LastModifiedTime     string
	DomainName           string
	Aliases              cfEmptyList
	Origins              cfOrigins
	DefaultCacheBehavior cfBehavior
	CacheBehaviors       cfEmptyList
	CustomErrorResponses cfEmptyList
	Comment              string
	PriceClass           string
	Enabled              bool
	ViewerCertificate    struct{ CloudFrontDefaultCertificate bool }
	Restrictions         struct {
		GeoRestriction struct {
			RestrictionType string
			Quantity        int
		}
	}
	WebACLId      string
	HttpVersion   string
	IsIPV6Enabled bool
	Staging       bool
}
type cfDistributionList struct {
	XMLName     xml.Name `xml:"DistributionList"`
	XMLNS       string   `xml:"xmlns,attr"`
	Marker      string
	NextMarker  string `xml:",omitempty"`
	MaxItems    int
	IsTruncated bool
	Quantity    int
	Items       []cfDistributionSummary `xml:"Items>DistributionSummary"`
}

func cfListDistribution(r *http.Request, tx *sql.Tx, marker string, limit int) (cfDistributionList, error) {
	list := cfDistributionList{XMLNS: cloudFrontNS, Marker: marker, MaxItems: limit}
	rows, err := tx.QueryContext(r.Context(), "SELECT id FROM aws_cloudfront_distributions WHERE id>? ORDER BY id LIMIT ?", marker, limit+1)
	if err != nil {
		return list, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return list, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return list, err
	}
	if len(ids) > limit {
		list.IsTruncated = true
		ids = ids[:limit]
		list.NextMarker = ids[len(ids)-1]
	}
	for _, id := range ids {
		row, e := cfReadDistribution(r.Context(), tx, id)
		if e != nil {
			return list, e
		}
		v, c := row.value, row.value.DistributionConfig
		summary := cfDistributionSummary{Id: v.Id, ARN: v.ARN, Status: v.Status, LastModifiedTime: v.LastModifiedTime, DomainName: v.DomainName, Origins: c.Origins, DefaultCacheBehavior: c.DefaultCacheBehavior, Comment: c.Comment, PriceClass: "PriceClass_All", Enabled: c.Enabled, HttpVersion: "http1.1"}
		summary.ViewerCertificate.CloudFrontDefaultCertificate = true
		summary.Restrictions.GeoRestriction.RestrictionType = "none"
		list.Items = append(list.Items, summary)
	}
	list.Quantity = len(list.Items)
	return list, nil
}

package aws

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const cfDistributionPath = "/2020-05-31/distribution"
const cfDeliveryPath = "/_simulator/cloudfront/distributions/"
const cfDistributionARN = "arn:aws:cloudfront::000000000000:distribution/"

var cfDistributionID = regexp.MustCompile(`^D[a-f0-9]{24}$`)
var cfInvalidationID = regexp.MustCompile(`^I[a-f0-9]{24}$`)
var cfLocalKey = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]*$`)

const cfDistributionSchema = `CREATE TABLE IF NOT EXISTS aws_cloudfront_distributions (
 id TEXT PRIMARY KEY, caller TEXT NOT NULL UNIQUE, revision TEXT NOT NULL,
 bucket TEXT NOT NULL, bucket_created TEXT NOT NULL, bucket_region TEXT NOT NULL, document TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS aws_cloudfront_invalidations (
 distribution TEXT NOT NULL REFERENCES aws_cloudfront_distributions(id) ON DELETE CASCADE,
 id TEXT NOT NULL, caller TEXT NOT NULL, document TEXT NOT NULL,
 PRIMARY KEY(distribution,id), UNIQUE(distribution,caller));
 CREATE TABLE IF NOT EXISTS aws_cloudfront_cache (
 distribution TEXT NOT NULL REFERENCES aws_cloudfront_distributions(id) ON DELETE CASCADE,
 revision TEXT NOT NULL, principal TEXT NOT NULL, object_key TEXT NOT NULL,
 expires REAL NOT NULL, inserted REAL NOT NULL, content_type TEXT NOT NULL, etag TEXT NOT NULL, data BLOB NOT NULL,
 PRIMARY KEY(distribution,revision,principal,object_key));`

type cfMethods struct {
	Quantity      int
	Items         []string   `xml:"Items>Method"`
	CachedMethods *cfMethods `xml:",omitempty"`
}
type cfCookies struct{ Forward string }
type cfForwarded struct {
	QueryString bool
	Cookies     cfCookies
}
type cfBehavior struct {
	TargetOriginId       string
	ViewerProtocolPolicy string
	AllowedMethods       cfMethods
	ForwardedValues      cfForwarded
	MinTTL               int64
	DefaultTTL           int64
	MaxTTL               int64
}
type cfS3Origin struct{ OriginAccessIdentity string }
type cfDistributionOrigin struct {
	Id             string
	DomainName     string
	S3OriginConfig cfS3Origin
}
type cfOrigins struct {
	Quantity int
	Items    []cfDistributionOrigin `xml:"Items>Origin"`
}
type cfDistributionConfig struct {
	XMLName              xml.Name `xml:"DistributionConfig" json:"-"`
	XMLNS                string   `xml:"xmlns,attr" json:"-"`
	CallerReference      string
	Origins              cfOrigins
	DefaultCacheBehavior cfBehavior
	Comment              string
	Enabled              bool
}
type cfDistribution struct {
	XMLName                       xml.Name `xml:"Distribution" json:"-"`
	XMLNS                         string   `xml:"xmlns,attr" json:"-"`
	Id                            string
	ARN                           string
	Status                        string
	LastModifiedTime              string
	InProgressInvalidationBatches int
	DomainName                    string
	DistributionConfig            cfDistributionConfig
}
type cfDistributionRow struct {
	value                             cfDistribution
	revision, bucket, created, region string
}
type cfPaths struct {
	Quantity int
	Items    []string `xml:"Items>Path"`
}
type cfInvalidationBatch struct {
	XMLName         xml.Name `xml:"InvalidationBatch" json:"-"`
	XMLNS           string   `xml:"xmlns,attr" json:"-"`
	Paths           cfPaths
	CallerReference string
}
type cfInvalidation struct {
	XMLName           xml.Name `xml:"Invalidation" json:"-"`
	XMLNS             string   `xml:"xmlns,attr" json:"-"`
	Id                string
	Status            string
	CreateTime        string
	InvalidationBatch cfInvalidationBatch
}

// These are the complete accepted XML trees. Required members are checked
// before typed decoding can collapse absence into a zero value.
var cfDistributionFields = map[string][]string{
	"DistributionConfig":                                                         {"CallerReference", "Origins", "DefaultCacheBehavior", "Comment", "Enabled"},
	"DistributionConfig/Origins":                                                 {"Quantity", "Items"},
	"DistributionConfig/Origins/Items":                                           {"Origin"},
	"DistributionConfig/Origins/Items/Origin":                                    {"Id", "DomainName", "S3OriginConfig"},
	"DistributionConfig/Origins/Items/Origin/S3OriginConfig":                     {"OriginAccessIdentity"},
	"DistributionConfig/DefaultCacheBehavior":                                    {"TargetOriginId", "ViewerProtocolPolicy", "AllowedMethods", "ForwardedValues", "MinTTL", "DefaultTTL", "MaxTTL"},
	"DistributionConfig/DefaultCacheBehavior/AllowedMethods":                     {"Quantity", "Items", "CachedMethods"},
	"DistributionConfig/DefaultCacheBehavior/AllowedMethods/Items":               {"Method"},
	"DistributionConfig/DefaultCacheBehavior/AllowedMethods/CachedMethods":       {"Quantity", "Items"},
	"DistributionConfig/DefaultCacheBehavior/AllowedMethods/CachedMethods/Items": {"Method"},
	"DistributionConfig/DefaultCacheBehavior/ForwardedValues":                    {"QueryString", "Cookies"},
	"DistributionConfig/DefaultCacheBehavior/ForwardedValues/Cookies":            {"Forward"},
	"InvalidationBatch":                                                          {"CallerReference", "Paths"},
	"InvalidationBatch/Paths":                                                    {"Quantity", "Items"},
	"InvalidationBatch/Paths/Items":                                              {"Path"},
}

func cfCheckDistributionXML(data []byte, root string) error {
	type frame struct {
		path string
		seen map[string]int
		text bool
	}
	var stack []frame
	closed := false
	decoder := xml.NewDecoder(bytes.NewReader(data))
	bad := cfInvalid("Malformed, missing or unsupported CloudFront XML field.")
	for {
		token, err := decoder.Token()
		if err == io.EOF && closed {
			return nil
		}
		if err != nil {
			return bad
		}
		switch node := token.(type) {
		case xml.StartElement:
			if node.Name.Space != "" && node.Name.Space != cloudFrontNS {
				return bad
			}
			path := node.Name.Local
			if len(stack) == 0 {
				if closed || path != root {
					return bad
				}
			} else {
				parent := &stack[len(stack)-1]
				if parent.text || !slices.Contains(cfDistributionFields[parent.path], node.Name.Local) {
					return bad
				}
				parent.seen[node.Name.Local]++
				if parent.seen[node.Name.Local] > 1 && node.Name.Local != "Method" && node.Name.Local != "Path" {
					return bad
				}
				path = parent.path + "/" + node.Name.Local
			}
			for _, attr := range node.Attr {
				if len(stack) != 0 || attr.Name.Space != "" || attr.Name.Local != "xmlns" || attr.Value != cloudFrontNS {
					return bad
				}
			}
			stack = append(stack, frame{path: path, seen: map[string]int{}})
		case xml.EndElement:
			if len(stack) == 0 {
				return bad
			}
			last := stack[len(stack)-1]
			for _, required := range cfDistributionFields[last.path] {
				if last.seen[required] == 0 {
					return bad
				}
			}
			stack = stack[:len(stack)-1]
			closed = len(stack) == 0
		case xml.CharData:
			if strings.TrimSpace(string(node)) != "" {
				if len(stack) == 0 || cfDistributionFields[stack[len(stack)-1].path] != nil {
					return bad
				}
				stack[len(stack)-1].text = true
			}
		case xml.Comment:
		case xml.ProcInst:
			if node.Target != "xml" || len(stack) != 0 || closed {
				return bad
			}
		default:
			return bad
		}
	}
}

func cfValidLocalKey(key string) bool {
	if len(key) > 1024 || !cfLocalKey.MatchString(key) {
		return false
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}
func cfParseDistribution(data []byte) (cfDistributionConfig, string, error) {
	var c cfDistributionConfig
	if err := cfCheckDistributionXML(data, "DistributionConfig"); err != nil {
		return c, "", err
	}
	if xml.Unmarshal(data, &c) != nil {
		return c, "", cfInvalid("Invalid distribution values.")
	}
	if !utf8.ValidString(c.Comment) || len(c.Comment) > 128 || len(c.CallerReference) < 1 || len(c.CallerReference) > 128 || strings.TrimSpace(c.CallerReference) != c.CallerReference || c.Origins.Quantity != 1 || len(c.Origins.Items) != 1 {
		return c, "", cfInvalid("Use one local S3 origin and bounded caller reference/comment.")
	}
	o := c.Origins.Items[0]
	bucket, ok := strings.CutSuffix(o.DomainName, ".s3.amazonaws.com")
	if !ok || len(bucket) < 3 || len(bucket) > 63 || !regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*[a-z0-9]$`).MatchString(bucket) || o.Id == "" || len(o.Id) > 128 || o.S3OriginConfig.OriginAccessIdentity != "" {
		return c, "", cfInvalid("Only an existing simulator S3 bucket with an empty origin access identity is supported.")
	}
	b := c.DefaultCacheBehavior
	methods := func(m cfMethods) bool {
		return m.Quantity == 2 && len(m.Items) == 2 && slices.Contains(m.Items, "GET") && slices.Contains(m.Items, "HEAD")
	}
	if b.TargetOriginId != o.Id || b.ViewerProtocolPolicy != "allow-all" || !methods(b.AllowedMethods) || b.AllowedMethods.CachedMethods == nil || !methods(*b.AllowedMethods.CachedMethods) || b.AllowedMethods.CachedMethods.CachedMethods != nil || b.ForwardedValues.QueryString || b.ForwardedValues.Cookies.Forward != "none" || b.MinTTL < 0 || b.MinTTL > b.DefaultTTL || b.DefaultTTL > b.MaxTTL || b.MaxTTL > 86400 {
		return c, "", cfInvalid("Use GET/HEAD, no forwarded query/cookies and ordered TTLs between zero and 86400.")
	}
	c.XMLNS = cloudFrontNS
	return c, bucket, nil
}

func cfReadDistribution(ctx context.Context, tx *sql.Tx, id string) (cfDistributionRow, error) {
	var row cfDistributionRow
	var data, caller string
	err := tx.QueryRowContext(ctx, "SELECT caller,revision,bucket,bucket_created,bucket_region,document FROM aws_cloudfront_distributions WHERE id=?", id).Scan(&caller, &row.revision, &row.bucket, &row.created, &row.region, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return row, cfFailure{404, "NoSuchDistribution", "The distribution does not exist."}
	}
	if err != nil {
		return row, err
	}
	if json.Unmarshal([]byte(data), &row.value) != nil || row.value.Id != id || row.value.ARN != cfDistributionARN+id || row.value.DistributionConfig.CallerReference != caller || row.revision == "" {
		return row, errors.New("invalid saved CloudFront distribution")
	}
	row.value.XMLNS = cloudFrontNS
	row.value.DistributionConfig.XMLNS = cloudFrontNS
	return row, nil
}
func cfOriginIdentity(ctx context.Context, tx *sql.Tx, bucket string) (created, region string, err error) {
	err = tx.QueryRowContext(ctx, "SELECT created,region FROM buckets WHERE name=?", bucket).Scan(&created, &region)
	if errors.Is(err, sql.ErrNoRows) {
		err = cfFailure{404, "NoSuchOrigin", "The local origin bucket does not exist."}
	}
	return
}
func cfDistributionMatch(revision, match string) error {
	if match == "" {
		return cfFailure{400, "InvalidIfMatchVersion", "Supply the current distribution ETag."}
	}
	if match != revision {
		return cfFailure{412, "PreconditionFailed", "The distribution ETag has changed."}
	}
	return nil
}

func cfNewDistributionID(prefix string) (string, error) {
	value, err := randomCredential(12)
	return prefix + value, err
}
func cfNow() string { return time.Now().UTC().Format(time.RFC3339Nano) }

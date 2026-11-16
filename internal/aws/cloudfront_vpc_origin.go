package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	cloudFrontPath      = "/2020-05-31/vpc-origin"
	cloudFrontNS        = "http://cloudfront.amazonaws.com/doc/2020-05-31/"
	cloudFrontARNPrefix = "arn:aws:cloudfront::000000000000:vpcorigin/"
)

var cloudFrontOriginID = regexp.MustCompile("^vo_[a-f0-9]{16}$")

type CFOriginConfig struct {
	Name                 string
	ARN                  string `xml:"Arn"`
	HTTPPort             int
	HTTPSPort            int
	OriginProtocolPolicy string
	OriginSslProtocols   *CFOriginSSLProtocols `xml:",omitempty"`
}

type CFOriginSSLProtocols struct {
	Quantity int
	Items    []string `xml:"Items>SslProtocol"`
}

type CFOriginTag struct{ Key, Value string }

type CFVpcOrigin struct {
	XMLName          xml.Name `xml:"VpcOrigin" json:"-"`
	XMLNS            string   `xml:"xmlns,attr" json:"-"`
	ID               string   `xml:"Id"`
	ARN              string   `xml:"Arn"`
	Status           string
	CreatedTime      string
	LastModifiedTime string
	Config           CFOriginConfig `xml:"VpcOriginEndpointConfig"`
	ETag             string         `xml:"-"`
	Tags             []CFOriginTag  `xml:"-"`
}

type cfFailure struct {
	status        int
	code, message string
}

func (e cfFailure) Error() string { return e.message }

func cfInvalid(msg string) error { return cfFailure{400, "InvalidArgument", msg} }
func cfMissing() error           { return cfFailure{404, "EntityNotFound", "The VPC origin does not exist."} }

// CloudFrontStore registers VPC origins against existing ALBs in the same database.
// It holds no endpoint addresses or credentials for outbound traffic.
type CloudFrontStore struct{ db *sql.DB }

func NewCloudFrontStore(db *sql.DB) (*CloudFrontStore, error) {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_cloudfront_vpc_origins (
		id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, alb TEXT NOT NULL REFERENCES aws_elb_load_balancers(arn) ON DELETE RESTRICT,
		document TEXT NOT NULL)`)
	if err != nil {
		return nil, err
	}
	return &CloudFrontStore{db}, nil
}

func validateCFOriginConfig(c CFOriginConfig) error {
	if strings.TrimSpace(c.Name) == "" || len(c.Name) > 128 || !validText(c.Name) {
		return cfInvalid("Use a VPC origin name of 1–128 bytes.")
	}
	parts := elbLBARNPattern.FindStringSubmatch(c.ARN)
	if parts == nil || !slices.Contains(Regions, parts[1]) {
		return cfInvalid("Use a local Application Load Balancer ARN.")
	}
	if c.OriginProtocolPolicy != "http-only" {
		return cfFailure{400, "UnsupportedOperation", "Only HTTP origins on internal Application Load Balancers are supported."}
	}
	if c.HTTPPort < 1 || c.HTTPPort > 65535 || c.HTTPSPort < 1 || c.HTTPSPort > 65535 {
		return cfInvalid("HTTPPort and HTTPSPort must be between 1 and 65535.")
	}
	if p := c.OriginSslProtocols; p != nil {
		if p.Quantity != len(p.Items) {
			return cfFailure{400, "InconsistentQuantities", "The number of SSL protocols differs from Quantity."}
		}
		if p.Quantity != 1 || p.Items[0] != "TLSv1.2" {
			return cfInvalid("Only TLSv1.2 is accepted in the optional OriginSslProtocols setting.")
		}
	}
	return nil
}

func validateCFOriginTags(tags []CFOriginTag) error {
	if len(tags) > 50 {
		return cfInvalid("A VPC origin supports up to 50 tags.")
	}
	keys := map[string]bool{}
	for _, t := range tags {
		if !validText(t.Key) || !validText(t.Value) || t.Key == "" || utf8.RuneCountInString(t.Key) > 128 || utf8.RuneCountInString(t.Value) > 256 || strings.HasPrefix(strings.ToLower(t.Key), "aws:") || keys[t.Key] {
			return cfInvalid("Tag keys must be unique, nonempty and within tag limits.")
		}
		keys[t.Key] = true
	}
	return nil
}

type cfQueryRow interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getCFOrigin(ctx context.Context, q cfQueryRow, id string) (CFVpcOrigin, error) {
	var raw string
	err := q.QueryRowContext(ctx, "SELECT document FROM aws_cloudfront_vpc_origins WHERE id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return CFVpcOrigin{}, cfMissing()
	}
	if err != nil {
		return CFVpcOrigin{}, err
	}
	var origin CFVpcOrigin
	if err = json.Unmarshal([]byte(raw), &origin); err != nil {
		return CFVpcOrigin{}, err
	}
	origin.XMLNS = cloudFrontNS
	return origin, nil
}
func (s *CloudFrontStore) Get(ctx context.Context, id string) (CFVpcOrigin, error) {
	return getCFOrigin(ctx, s.db, id)
}

func cfMatch(o CFVpcOrigin, match string) error {
	if match == "" {
		return cfFailure{400, "InvalidIfMatchVersion", "Provide the ETag from GetVpcOrigin in If-Match."}
	}
	if match != o.ETag {
		return cfFailure{412, "PreconditionFailed", "The VPC origin has changed."}
	}
	return nil
}

func (s *CloudFrontStore) Create(ctx context.Context, c CFOriginConfig, tags []CFOriginTag) (CFVpcOrigin, error) {
	return s.save(ctx, "", "", c, tags)
}
func (s *CloudFrontStore) Update(ctx context.Context, id, match string, c CFOriginConfig) (CFVpcOrigin, error) {
	if id == "" {
		return CFVpcOrigin{}, cfMissing()
	}
	return s.save(ctx, id, match, c, nil)
}

func (s *CloudFrontStore) save(ctx context.Context, id, match string, c CFOriginConfig, tags []CFOriginTag) (CFVpcOrigin, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CFVpcOrigin{}, err
	}
	defer tx.Rollback()
	o, err := saveCFOriginTx(ctx, tx, id, match, c, tags)
	if err != nil {
		return CFVpcOrigin{}, err
	}
	return o, tx.Commit()
}

// saveCFOriginTx creates when id is empty and otherwise updates exactly the matched revision.
func saveCFOriginTx(ctx context.Context, tx *sql.Tx, id, match string, c CFOriginConfig, tags []CFOriginTag) (CFVpcOrigin, error) {
	if err := validateCFOriginConfig(c); err != nil {
		return CFVpcOrigin{}, err
	}
	if err := validateCFOriginTags(tags); err != nil {
		return CFVpcOrigin{}, err
	}
	var err error
	now := time.Now().UTC().Format(time.RFC3339Nano)
	o := CFVpcOrigin{Status: "Deployed", Tags: tags, CreatedTime: now}
	if id != "" {
		o, err = getCFOrigin(ctx, tx, id)
		if err != nil {
			return CFVpcOrigin{}, err
		}
		if err = cfMatch(o, match); err != nil {
			return CFVpcOrigin{}, err
		}
	} else {
		key, err := elbID()
		if err != nil {
			return CFVpcOrigin{}, err
		}
		o.ID = "vo_" + key
		o.ARN = cloudFrontARNPrefix + o.ID
	}
	lb, err := elbReadLoadBalancer(ctx, tx, "arn=?", c.ARN)
	if errors.Is(err, sql.ErrNoRows) {
		return CFVpcOrigin{}, cfInvalid("The endpoint Application Load Balancer does not exist.")
	}
	if err != nil {
		return CFVpcOrigin{}, err
	}
	if lb.Scheme != "internal" {
		return CFVpcOrigin{}, cfInvalid("Use an internal Application Load Balancer.")
	}
	var duplicate bool
	err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM aws_cloudfront_vpc_origins WHERE name=? AND id<>?)", c.Name, o.ID).Scan(&duplicate)
	if err != nil {
		return CFVpcOrigin{}, err
	}
	if duplicate {
		return CFVpcOrigin{}, cfFailure{409, "EntityAlreadyExists", "The VPC origin name is already in use."}
	}
	if id == "" {
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM aws_cloudfront_vpc_origins").Scan(&count); err != nil {
			return CFVpcOrigin{}, err
		}
		if count >= 50 {
			return CFVpcOrigin{}, cfFailure{400, "EntityLimitExceeded", "The local limit is 50 VPC origins."}
		}
	}
	o.ETag, err = elbID()
	if err != nil {
		return CFVpcOrigin{}, err
	}
	o.XMLNS, o.Config, o.LastModifiedTime = cloudFrontNS, c, now
	data, err := json.Marshal(o)
	if err != nil {
		return CFVpcOrigin{}, err
	}
	if id == "" {
		_, err = tx.ExecContext(ctx, "INSERT INTO aws_cloudfront_vpc_origins(id,name,alb,document) VALUES(?,?,?,?)", o.ID, c.Name, c.ARN, string(data))
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE aws_cloudfront_vpc_origins SET name=?,alb=?,document=? WHERE id=?", c.Name, c.ARN, string(data), id)
	}
	if err != nil {
		return CFVpcOrigin{}, err
	}
	return o, nil
}

func (s *CloudFrontStore) Delete(ctx context.Context, id, match string) (CFVpcOrigin, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CFVpcOrigin{}, err
	}
	defer tx.Rollback()
	o, err := deleteCFOriginTx(ctx, tx, id, match)
	if err != nil {
		return CFVpcOrigin{}, err
	}
	return o, tx.Commit()
}

func deleteCFOriginTx(ctx context.Context, tx *sql.Tx, id, match string) (CFVpcOrigin, error) {
	o, err := getCFOrigin(ctx, tx, id)
	if err != nil {
		return CFVpcOrigin{}, err
	}
	if err = cfMatch(o, match); err != nil {
		return CFVpcOrigin{}, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM aws_cloudfront_vpc_origins WHERE id=?", id); err != nil {
		return CFVpcOrigin{}, err
	}
	o.Status = "Deleting"
	return o, nil
}

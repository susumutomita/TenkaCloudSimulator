package aws

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"hash"
	"io"
	"slices"
	"strconv"
	"sync"

	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

const (
	ec2RouteTableMinPageSize = 5
	ec2RouteTableMaxPageSize = 100
	ec2RouteTablePageBodyLen = 4 // uint16 requested size, uint16 continuation offset
	ec2RouteTablePageWireLen = 48

	// 100 table envelopes (<512 bytes each), 50 tags/table (6 wire
	// bytes per input byte + <48 bytes of markup), and at most 1,000
	// total saved child rows regional (<256 bytes each). The same
	// estimate for one table fits the stricter exact-ID readback limit.
	ec2RouteTablePageBytes  = 16 << 20
	ec2RouteTableExactBytes = 512 << 10
)

type ec2RouteTableQuery struct {
	id         string
	subnets    []string
	maxResults int
	nextToken  string
}

func (q ec2RouteTableQuery) paginated() bool { return q.maxResults != 0 || q.nextToken != "" }

func invalidEC2RouteTablePage() *ec2Failure {
	return ec2Invalid("InvalidNextToken", "The route table page token is invalid or no longer current; start a new list.")
}

func parseEC2RouteTableMaxResults(raw string) (int, *ec2Failure) {
	value := 0
	if len(raw) >= 1 && len(raw) <= 3 {
		value, _ = strconv.Atoi(raw)
	}
	if value < ec2RouteTableMinPageSize || value > ec2RouteTableMaxPageSize || strconv.Itoa(value) != raw {
		return 0, ec2Invalid("InvalidMaxResults", "MaxResults must be an integer from 5 to 100.")
	}
	return value, nil
}

func parseEC2RouteTablePageFields(fields map[string][]string, q *ec2RouteTableQuery) *ec2Failure {
	if values, ok := fields["MaxResults"]; ok {
		if len(values) != 1 {
			return ec2Invalid("InvalidParameterValue", "DescribeRouteTables parameters must be sent once.")
		}
		size, failure := parseEC2RouteTableMaxResults(values[0])
		if failure != nil {
			return failure
		}
		q.maxResults = size
	}
	if values, ok := fields["NextToken"]; ok {
		if len(values) != 1 {
			return ec2Invalid("InvalidParameterValue", "DescribeRouteTables parameters must be sent once.")
		}
		if len(values[0]) != ec2RouteTablePageWireLen {
			return invalidEC2RouteTablePage()
		}
		q.nextToken = values[0]
	}
	return nil
}

var ec2RouteTablePageEncoding = base64.RawURLEncoding.Strict()

// Per store and process; the key never leaves the process or persists another
// snapshot. Both the authenticated caller and effective exercise identity are
// bound. Page credentials never grant permission by themselves.
type ec2RouteTablePager struct {
	once sync.Once
	key  [sha256.Size]byte
}

type ec2RouteTablePageHash struct{ hash.Hash }

func (h ec2RouteTablePageHash) count(n int) {
	var frame [4]byte
	binary.BigEndian.PutUint32(frame[:], uint32(n))
	_, _ = h.Write(frame[:])
}
func (h ec2RouteTablePageHash) field(value string) {
	h.count(len(value))
	_, _ = io.WriteString(h, value)
}

func ec2RouteTableSnapshot(rows []ec2RouteTableRead) (digest [sha256.Size]byte) {
	out := ec2RouteTablePageHash{sha256.New()}
	out.count(len(rows))
	for _, row := range rows {
		out.field(row.ID)
		out.field(row.VPC)
		out.field(row.Owner)
		out.count(len(row.Routes))
		for _, route := range row.Routes {
			out.field(route.CIDR)
			out.field(route.Gateway)
			out.field(route.Origin)
			out.field(route.State)
		}
		out.count(len(row.Associations))
		for _, a := range row.Associations {
			out.field(a.ID)
			out.field(a.Table)
			out.field(a.Subnet)
			out.field(strconv.FormatBool(a.Main))
		}
		out.count(len(row.Tags))
		for _, t := range row.Tags {
			out.field(t.Key)
			out.field(t.Value)
		}
	}
	_ = out.Sum(digest[:0])
	return digest
}

func (p *ec2RouteTablePager) signature(region string, actor provider.Actor, subnets []string, snapshot [sha256.Size]byte, body []byte) []byte {
	p.once.Do(func() { _, _ = rand.Read(p.key[:]) })
	mac := ec2RouteTablePageHash{hmac.New(sha256.New, p.key[:])}
	mac.field("tenkacloud/ec2/DescribeRouteTables/2016-11-15/v1")
	mac.field(region)
	mac.field(actor.CredentialIdentity)
	mac.field(actor.ExerciseIdentity)
	mac.field(string(actor.CredentialRole))
	mac.count(len(subnets))
	for _, id := range subnets {
		mac.field(id)
	}
	_, _ = mac.Write(snapshot[:])
	_, _ = mac.Write(body)
	return mac.Sum(nil)
}

// Validate/project the entire region before selection, including tables
// that don't match the filter. A changed verified regional projection
// invalidates a continuation rather than silently mixing old and new rows.
func (s *StackStore) executeEC2RouteTableRead(ctx context.Context, region string, query ec2RouteTableQuery, caller provider.Actor) (*ec2DescribeRouteTablesResponse, error) {
	tables, err := s.describeEC2RouteTables(ctx, region)
	if err != nil {
		return nil, err
	}
	if query.id != "" {
		i := slices.IndexFunc(tables, func(table ec2RouteTableRead) bool { return table.ID == query.id })
		if i < 0 {
			return nil, ec2Invalid("InvalidRouteTableID.NotFound", "The route table does not exist in this region.")
		}
		tables = tables[i : i+1]
	}
	if !query.paginated() || query.nextToken == "" && len(tables) <= query.maxResults {
		return &ec2DescribeRouteTablesResponse{Tables: filterEC2RouteTablesOnSubnet(tables, query.subnets)}, nil
	}
	snapshot := ec2RouteTableSnapshot(tables) // The filter below may reuse the backing slice.
	tables = filterEC2RouteTablesOnSubnet(tables, query.subnets)
	return s.routeTablePages.page(region, query, caller, snapshot, tables)
}

func (p *ec2RouteTablePager) page(region string, q ec2RouteTableQuery, actor provider.Actor, snapshot [sha256.Size]byte, rows []ec2RouteTableRead) (*ec2DescribeRouteTablesResponse, error) {
	if len(rows) > ec2NativeRouteTableRowLimit {
		return nil, errEC2RouteSnapshot
	}
	size, start := q.maxResults, 0
	if q.nextToken != "" {
		raw, err := ec2RouteTablePageEncoding.DecodeString(q.nextToken)
		if err != nil || len(raw) != ec2RouteTablePageBodyLen+sha256.Size || ec2RouteTablePageEncoding.EncodeToString(raw) != q.nextToken {
			return nil, invalidEC2RouteTablePage()
		}
		body := raw[:ec2RouteTablePageBodyLen]
		if !hmac.Equal(raw[ec2RouteTablePageBodyLen:], p.signature(region, actor, q.subnets, snapshot, body)) {
			return nil, invalidEC2RouteTablePage()
		}
		size, start = int(binary.BigEndian.Uint16(body)), int(binary.BigEndian.Uint16(body[2:]))
		if size < ec2RouteTableMinPageSize || size > ec2RouteTableMaxPageSize ||
			q.maxResults != 0 && size != q.maxResults || start < 1 || start >= len(rows) || start%size != 0 {
			return nil, invalidEC2RouteTablePage()
		}
	}
	end := min(start+size, len(rows))
	out := &ec2DescribeRouteTablesResponse{Tables: rows[start:end]}
	if end < len(rows) {
		var body [ec2RouteTablePageBodyLen]byte
		binary.BigEndian.PutUint16(body[:], uint16(size))
		binary.BigEndian.PutUint16(body[2:], uint16(end))
		payload := append(body[:], p.signature(region, actor, q.subnets, snapshot, body[:])...)
		out.NextToken = ec2RouteTablePageEncoding.EncodeToString(payload)
	}
	return out, nil
}

// Encode page and exact replies before writing a success status. Do not
// accidentally send a partial XML document if future fields break the bound.
type ec2RouteTableBoundedWire struct {
	buffer bytes.Buffer
	limit  int
}

func (out *ec2RouteTableBoundedWire) Write(raw []byte) (int, error) {
	if len(raw) > out.limit-out.buffer.Len() {
		return 0, errEC2RouteSnapshot
	}
	return out.buffer.Write(raw)
}

func encodeEC2RouteTableBounded(out *ec2DescribeRouteTablesResponse, exact bool) ([]byte, error) {
	limit := ec2RouteTablePageBytes
	if exact {
		limit = ec2RouteTableExactBytes
	}
	wire := ec2RouteTableBoundedWire{limit: limit}
	stampEC2Response(out)
	if err := xml.NewEncoder(&wire).Encode(out); err != nil {
		return nil, err
	}
	return wire.buffer.Bytes(), nil
}

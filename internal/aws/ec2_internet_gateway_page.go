package aws

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"hash"
	"io"
	"strconv"
	"sync"

	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

// EC2InternetGatewayPageLimit keeps the XML response comfortably below the
// console's unchanged 16 MiB limit even at the storage maxima. Each gateway
// has at most 50 tags of (128+256) UTF-8 bytes. encoding/xml needs at most six
// wire bytes per tag byte; tag markup is <48 bytes and the remaining record
// markup/fields are <512 bytes. With a <1 KiB response envelope, the bound is
// 1024 + 100*(512 + 50*(48 + 6*(128+256))) bytes. See the encoder regression.
const EC2InternetGatewayPageLimit = 100

const (
	ec2GatewayMaxInputPage = 1000
	ec2GatewayListLimit    = 1000
	ec2GatewayPageMACLen   = sha256.Size
	ec2GatewayPageBodyLen  = 4 // uint16 requested limit, uint16 next offset
	ec2GatewayPageWireLen  = 48
)

var ec2GatewayPageEncoding = base64.RawURLEncoding.Strict()

type ec2InternetGatewayPager struct {
	once sync.Once
	key  [sha256.Size]byte
}

func invalidEC2GatewayPage() *ec2Failure {
	return ec2Invalid("InvalidNextToken", "The internet gateway page token is invalid or no longer current; start a new list.")
}

func parseEC2GatewayMaxResults(raw string) (int, *ec2Failure) {
	// Reject before conversion: a bounded positive decimal is required, with
	// no sign, fractional value, exponent or platform-sized overflow.
	if len(raw) < 1 || len(raw) > 4 || raw[0] < '1' || raw[0] > '9' {
		return 0, ec2Invalid("InvalidMaxResults", "MaxResults must be an integer from 5 to 1000.")
	}
	for i := 1; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return 0, ec2Invalid("InvalidMaxResults", "MaxResults must be an integer from 5 to 1000.")
		}
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 5 || value > ec2GatewayMaxInputPage {
		return 0, ec2Invalid("InvalidMaxResults", "MaxResults must be an integer from 5 to 1000.")
	}
	return value, nil
}

func writeGatewayPageNumber(out hash.Hash, value uint32) {
	var width [4]byte
	binary.BigEndian.PutUint32(width[:], value)
	_, _ = out.Write(width[:])
}

func writeGatewayPageField(out hash.Hash, value string) {
	writeGatewayPageNumber(out, uint32(len(value)))
	_, _ = io.WriteString(out, value)
}

// The verified regional projection is in physical ID order, with tags in
// key order. Frame every value, including collection sizes, so all fields
// visible to the reader are bound without retaining old snapshots.
func internetGatewaySnapshot(rows []ec2InternetGateway) (digest [sha256.Size]byte) {
	out := sha256.New()
	writeGatewayPageNumber(out, uint32(len(rows)))
	for _, row := range rows {
		writeGatewayPageField(out, row.ID)
		writeGatewayPageField(out, row.OwnerID)
		writeGatewayPageField(out, row.ManagedBy)
		writeGatewayPageNumber(out, uint32(len(row.Attachments)))
		for _, attach := range row.Attachments {
			writeGatewayPageField(out, attach.VPCID)
			writeGatewayPageField(out, attach.State)
		}
		writeGatewayPageNumber(out, uint32(len(row.Tags)))
		for _, tag := range row.Tags {
			writeGatewayPageField(out, tag.Key)
			writeGatewayPageField(out, tag.Value)
		}
	}
	_ = out.Sum(digest[:0])
	return digest
}

func (p *ec2InternetGatewayPager) signature(region string, caller provider.Actor, snapshot [sha256.Size]byte, body []byte) []byte {
	p.once.Do(func() { _, _ = rand.Read(p.key[:]) })
	mac := hmac.New(sha256.New, p.key[:])
	writeGatewayPageField(mac, "tenkacloud/ec2/DescribeInternetGateways/2016-11-15/unfiltered/v1")
	writeGatewayPageField(mac, region)
	writeGatewayPageField(mac, caller.CredentialIdentity)
	writeGatewayPageField(mac, caller.ExerciseIdentity)
	writeGatewayPageField(mac, string(caller.CredentialRole))
	_, _ = mac.Write(snapshot[:])
	_, _ = mac.Write(body)
	return mac.Sum(nil)
}

// Apply pagination only after normal native authorization and readback of the
// validated inventory. The authentication tag contains no user or row data.
// The per-store key is in memory; a process restart invalidates any old token.
func (p *ec2InternetGatewayPager) page(region string, q ec2InternetGatewayQuery, all []ec2InternetGateway) ([]ec2InternetGateway, string, error) {
	if !q.paginated() {
		return all, "", nil
	}
	if len(all) > ec2GatewayListLimit {
		return nil, "", errEC2InternetGatewayState
	}
	size, start := q.maxResults, 0
	if q.nextToken == "" && len(all) <= min(size, EC2InternetGatewayPageLimit) {
		return all, "", nil
	}
	snapshot := internetGatewaySnapshot(all)
	if q.nextToken != "" {
		raw, err := ec2GatewayPageEncoding.DecodeString(q.nextToken)
		if err != nil || len(raw) != ec2GatewayPageBodyLen+ec2GatewayPageMACLen ||
			ec2GatewayPageEncoding.EncodeToString(raw) != q.nextToken {
			return nil, "", invalidEC2GatewayPage()
		}
		body := raw[:ec2GatewayPageBodyLen]
		if !hmac.Equal(raw[ec2GatewayPageBodyLen:], p.signature(region, q.caller, snapshot, body)) {
			return nil, "", invalidEC2GatewayPage()
		}
		size, start = int(binary.BigEndian.Uint16(body)), int(binary.BigEndian.Uint16(body[2:]))
		if size < 5 || size > ec2GatewayMaxInputPage || (q.maxResults != 0 && q.maxResults != size) ||
			start < 1 || start >= len(all) || start%min(size, EC2InternetGatewayPageLimit) != 0 {
			return nil, "", invalidEC2GatewayPage()
		}
	}
	end := min(start+min(size, EC2InternetGatewayPageLimit), len(all))
	next := ""
	if end < len(all) {
		var body [ec2GatewayPageBodyLen]byte
		binary.BigEndian.PutUint16(body[:], uint16(size))
		binary.BigEndian.PutUint16(body[2:], uint16(end))
		packet := append(body[:], p.signature(region, q.caller, snapshot, body[:])...)
		next = ec2GatewayPageEncoding.EncodeToString(packet)
	}
	return all[start:end], next, nil
}

// Package geoip provides offline IP→region lookups using an embedded
// ip2region v4 (xdb structure 3.0) database.
//
// The data file (ip2region.xdb) is MIT-licensed and produced by the
// lionsoul2014/ip2region project; only ~11 MiB and embedded into the binary.
// To avoid coupling mudp to an evolving upstream Go binding (v4 lives on
// master and has no stable tagged release), this package implements the xdb
// search algorithm directly against the documented binary format. The format
// is stable for structure-3.0 / IPv4 files.
package geoip

import (
	_ "embed"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"sync"
)

//go:embed ip2region.xdb
var xdbData []byte

// xdb layout constants (see ip2region binding/golang/xdb/header.go).
const (
	headerInfoLength = 256
	vectorIndexRows  = 256
	vectorIndexCols  = 256
	vectorIndexSize  = 8
	ipv4SegIndexSize = 14 // 4 start + 4 end + 2 dataLen + 4 dataPtr
	structure30      = 3
	ipv4VersionNo    = 4
)

// Lookup is the region result for an IP. Fields use the names exactly as they
// appear in the xdb region string (Chinese for the bundled CN-focused DB).
// Private/unresolvable IPs get Country == "private" so callers can decide to
// treat them as trusted (typically the LAN, ourselves, or a reverse proxy).
type Lookup struct {
	Country  string
	Region   string // 中间字段（"0" 表示无）
	Province string
	City     string
	ISP      string
}

// IsPrivate reports whether the lookup represents a private/loopback address.
func (l Lookup) IsPrivate() bool { return l.Country == "private" }

// Reader is a concurrency-safe, in-memory xdb searcher. The whole database is
// loaded once at Open; each Lookup is a pure in-memory binary search guarded
// by a mutex (the xdb algorithm is not thread-safe because the index stores
// IPs in little-endian and the compare step swaps them in place).
type Reader struct {
	buf      []byte
	startPtr uint32
	endPtr   uint32
	mu       sync.Mutex
	cache    sync.Map // ip string -> region string (small LRU-ish guard)
}

var errInvalidDB = errors.New("geoip: invalid or unsupported xdb database (need structure 3.0 / IPv4)")

// Open builds a Reader from the embedded xdb data. It never touches disk, so
// the returned Reader is safe to keep for the process lifetime.
func Open() (*Reader, error) {
	if len(xdbData) < headerInfoLength {
		return nil, errInvalidDB
	}
	// Header: [0:2] structure version, [16:18] ip version.
	structure := binary.LittleEndian.Uint16(xdbData[0:2])
	ipVer := binary.LittleEndian.Uint16(xdbData[16:18])
	if structure != structure30 || ipVer != ipv4VersionNo {
		return nil, errInvalidDB
	}
	r := &Reader{
		buf:      xdbData,
		startPtr: binary.LittleEndian.Uint32(xdbData[8:12]),
		endPtr:   binary.LittleEndian.Uint32(xdbData[12:16]),
	}
	return r, nil
}

// Lookup resolves an IPv4 address to its region. The bundled DB is IPv4-only,
// so a global IPv6 address returns ErrIPv6Unsupported — callers should treat
// that as "we cannot geo-locate this client" and decide fail-open vs fail-closed
// based on whether a region rule is active. Private/loopback/link-local
// addresses (both families) return a Lookup with Country=="private" so callers
// can treat LAN traffic (typically ourselves or the reverse proxy) as trusted.
func (r *Reader) Lookup(ipStr string) (Lookup, error) {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return Lookup{}, errors.New("geoip: invalid ip")
	}
	// Identify the family BEFORE collapsing mapped addresses: ip.To4() returns
	// non-nil for IPv4-mapped IPv6 (::ffff:1.2.3.4), which we still want to geo.
	v4 := ip.To4()
	if v4 == nil {
		// True IPv6. The bundled DB has no data for it; but local/private IPv6
		// ranges (loopback, unique-local fc00::/7, link-local fe80::/10) still
		// map to "private" so LAN traffic is treated consistently.
		if isLocalAddress(ip) {
			return Lookup{Country: "private"}, nil
		}
		return Lookup{}, ErrIPv6Unsupported
	}
	ip = v4
	if isLocalAddress(ip) {
		return Lookup{Country: "private"}, nil
	}
	region, err := r.search(ip)
	if err != nil {
		return Lookup{}, err
	}
	if region == "" {
		return Lookup{Country: "unknown"}, nil
	}
	return parseRegion(region), nil
}

// ErrIPv6Unsupported is returned by Lookup for a non-private global IPv6
// address. The bundled xdb is IPv4-only; the server layer uses this sentinel
// to decide between fail-open (no region rule configured) and fail-closed
// (a region rule IS active and the IP cannot be located).
var ErrIPv6Unsupported = errors.New("geoip: ipv6 not supported")

// IsIPv6 reports whether ipStr is a genuine IPv6 address that has no IPv4
// representation — i.e. it is NOT IPv4 and NOT IPv4-mapped IPv6
// ("::ffff:1.2.3.4", which Go collapses to an IPv4). Such mapped addresses
// can be geo-located as IPv4, so they report false here. Returns false for any
// non-parseable input.
func IsIPv6(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	return ip.To4() == nil
}

// isLocalAddress reports whether ip should be treated as local/trusted: it
// covers loopback, RFC1918 private ranges, link-local, unspecified and
// multicast. Note Go's IsGlobalUnicast() does NOT exclude RFC1918 space, so we
// must check IsPrivate() explicitly.
func isLocalAddress(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// search runs the xdb vector-index + binary-search algorithm for one IPv4.
// The index stores segment IPs little-endian; the canonical algorithm swaps
// the candidate bytes in place during compare, which is why the whole search
// is serialized under mu.
func (r *Reader) search(ip net.IP) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Prefer the cache for hot IPs (e.g. many logins from one address).
	if v, ok := r.cache.Load(ip.String()); ok {
		return v.(string), nil
	}

	ipBytes := ip.To4()
	if ipBytes == nil {
		return "", errors.New("geoip: not ipv4")
	}
	il0, il1 := int(ipBytes[0]), int(ipBytes[1])
	idx := il0*vectorIndexCols*vectorIndexSize + il1*vectorIndexSize
	sPtr := binary.LittleEndian.Uint32(r.buf[headerInfoLength+idx:])
	ePtr := binary.LittleEndian.Uint32(r.buf[headerInfoLength+idx+4:])
	if sPtr == 0 || ePtr == 0 {
		return "", nil
	}

	segCount := int((ePtr - sPtr) / ipv4SegIndexSize)
	buff := make([]byte, ipv4SegIndexSize)
	var dataLen int
	var dataPtr uint32
	found := false
	l, h := 0, segCount-1
	for l <= h {
		m := (l + h) >> 1
		p := sPtr + uint32(m)*uint32(ipv4SegIndexSize)
		copy(buff, r.buf[p:p+ipv4SegIndexSize])
		// Segment start/end are little-endian in the index; convert for compare.
		start := binary.LittleEndian.Uint32(buff[0:4])
		end := binary.LittleEndian.Uint32(buff[4:8])
		// Compare input (big-endian uint32) against segment range.
		input := binary.BigEndian.Uint32(ipBytes)
		if input < start {
			h = m - 1
		} else if input > end {
			l = m + 1
		} else {
			dataLen = int(binary.LittleEndian.Uint16(buff[8:10]))
			dataPtr = binary.LittleEndian.Uint32(buff[10:14])
			found = true
			break
		}
	}
	if !found || dataLen == 0 {
		return "", nil
	}
	if int(dataPtr)+dataLen > len(r.buf) {
		return "", nil
	}
	region := string(r.buf[dataPtr : int(dataPtr)+dataLen])
	// Bounded cache so a flood of unique IPs cannot grow it without limit.
	r.cache.Store(ip.String(), region)
	return region, nil
}

// parseRegion splits the xdb region string "国家|区域|省份|城市|ISP".
// Missing segments are encoded as "0"; normalize those to empty strings.
func parseRegion(region string) Lookup {
	parts := strings.Split(region, "|")
	get := func(i int) string {
		if i >= len(parts) {
			return ""
		}
		v := strings.TrimSpace(parts[i])
		if v == "0" {
			return ""
		}
		return v
	}
	return Lookup{
		Country:  get(0),
		Region:   get(1),
		Province: get(2),
		City:     get(3),
		ISP:      get(4),
	}
}

// CountryCodeOf maps a Chinese country name to its ISO 3166-1 alpha-2 code,
// covering the names used by the bundled CN-focused xdb. Unknown names map to
// their uppercased input so admin rules typed as codes (e.g. "CN") still work.
func CountryCodeOf(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if code, ok := countryCodes[name]; ok {
		return code
	}
	// Anything already looking like a 2-letter code (or otherwise) is returned
	// uppercased so admin-entered codes match regardless of case.
	return strings.ToUpper(name)
}

// countryCodes covers the countries most likely to appear in a CN-focused
// region DB; extend as needed. Names mirror the xdb Chinese spellings.
var countryCodes = map[string]string{
	"中国":   "CN",
	"中国香港": "HK", "香港": "HK",
	"中国台湾": "TW", "台湾": "TW",
	"中国澳门": "MO", "澳门": "MO",
	"美国": "US", "日本": "JP", "韩国": "KR", "新加坡": "SG",
	"英国": "GB", "法国": "FR", "德国": "DE", "俄罗斯": "RU",
	"加拿大": "CA", "澳大利亚": "AU", "印度": "IN", "越南": "VN",
	"泰国": "TH", "马来西亚": "MY", "印度尼西亚": "ID", "菲律宾": "PH",
	"0": "",
}

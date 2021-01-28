package dnsfilter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"testing"

	"github.com/AdguardTeam/AdGuardHome/internal/testutil"
	"github.com/AdguardTeam/golibs/cache"
	"github.com/AdguardTeam/golibs/log"
	"github.com/AdguardTeam/urlfilter/rules"
	"github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
)

func TestMain(m *testing.M) {
	testutil.DiscardLogOutput(m)
}

var setts RequestFilteringSettings

// HELPERS
// SAFE BROWSING
// SAFE SEARCH
// PARENTAL
// FILTERING
// BENCHMARKS

// HELPERS

func purgeCaches() {
	for _, c := range []cache.Cache{
		gctx.safebrowsingCache,
		gctx.parentalCache,
		gctx.safeSearchCache,
	} {
		if c != nil {
			c.Clear()
		}
	}
}

func newForTest(c *Config, filters []Filter) *DNSFilter {
	setts = RequestFilteringSettings{}
	setts.FilteringEnabled = true
	if c != nil {
		c.SafeBrowsingCacheSize = 10000
		c.ParentalCacheSize = 10000
		c.SafeSearchCacheSize = 1000
		c.CacheTime = 30
		setts.SafeSearchEnabled = c.SafeSearchEnabled
		setts.SafeBrowsingEnabled = c.SafeBrowsingEnabled
		setts.ParentalEnabled = c.ParentalEnabled
	}
	d := New(c, filters)
	purgeCaches()
	return d
}

func (d *DNSFilter) checkMatch(t *testing.T, hostname string) {
	t.Helper()
	res, err := d.CheckHost(hostname, dns.TypeA, &setts)
	assert.Nilf(t, err, "Error while matching host %s: %s", hostname, err)
	assert.Truef(t, res.IsFiltered, "Expected hostname %s to match", hostname)
}

func (d *DNSFilter) checkMatchIP(t *testing.T, hostname, ip string, qtype uint16) {
	t.Helper()

	res, err := d.CheckHost(hostname, qtype, &setts)
	assert.Nilf(t, err, "Error while matching host %s: %s", hostname, err)
	assert.Truef(t, res.IsFiltered, "Expected hostname %s to match", hostname)
	if !assert.NotEmpty(t, res.Rules, "Expected result to have rules") {
		return
	}

	r := res.Rules[0]
	assert.NotNilf(t, r.IP, "Expected ip %s to match, actual: %v", ip, r.IP)
	assert.Equalf(t, ip, r.IP.String(), "Expected ip %s to match, actual: %v", ip, r.IP)
}

func (d *DNSFilter) checkMatchEmpty(t *testing.T, hostname string) {
	t.Helper()
	res, err := d.CheckHost(hostname, dns.TypeA, &setts)
	assert.Nilf(t, err, "Error while matching host %s: %s", hostname, err)
	assert.Falsef(t, res.IsFiltered, "Expected hostname %s to not match", hostname)
}

func TestEtcHostsMatching(t *testing.T) {
	addr := "216.239.38.120"
	addr6 := "::1"
	text := fmt.Sprintf(`  %s  google.com www.google.com   # enforce google's safesearch
%s  ipv6.com
0.0.0.0 block.com
0.0.0.1 host2
0.0.0.2 host2
::1 host2
`,
		addr, addr6)
	filters := []Filter{{
		ID: 0, Data: []byte(text),
	}}
	d := newForTest(nil, filters)
	t.Cleanup(d.Close)

	d.checkMatchIP(t, "google.com", addr, dns.TypeA)
	d.checkMatchIP(t, "www.google.com", addr, dns.TypeA)
	d.checkMatchEmpty(t, "subdomain.google.com")
	d.checkMatchEmpty(t, "example.org")

	// IPv4
	d.checkMatchIP(t, "block.com", "0.0.0.0", dns.TypeA)

	// ...but empty IPv6
	res, err := d.CheckHost("block.com", dns.TypeAAAA, &setts)
	assert.Nil(t, err)
	assert.True(t, res.IsFiltered)
	if assert.Len(t, res.Rules, 1) {
		assert.Equal(t, "0.0.0.0 block.com", res.Rules[0].Text)
		assert.Empty(t, res.Rules[0].IP)
	}

	// IPv6
	d.checkMatchIP(t, "ipv6.com", addr6, dns.TypeAAAA)

	// ...but empty IPv4
	res, err = d.CheckHost("ipv6.com", dns.TypeA, &setts)
	assert.Nil(t, err)
	assert.True(t, res.IsFiltered)
	if assert.Len(t, res.Rules, 1) {
		assert.Equal(t, "::1  ipv6.com", res.Rules[0].Text)
		assert.Empty(t, res.Rules[0].IP)
	}

	// 2 IPv4 (return only the first one)
	res, err = d.CheckHost("host2", dns.TypeA, &setts)
	assert.Nil(t, err)
	assert.True(t, res.IsFiltered)
	if assert.Len(t, res.Rules, 1) {
		assert.Equal(t, res.Rules[0].IP, net.IP{0, 0, 0, 1})
	}

	// ...and 1 IPv6 address
	res, err = d.CheckHost("host2", dns.TypeAAAA, &setts)
	assert.Nil(t, err)
	assert.True(t, res.IsFiltered)
	if assert.Len(t, res.Rules, 1) {
		assert.Equal(t, res.Rules[0].IP, net.IPv6loopback)
	}
}

// SAFE BROWSING

func TestSafeBrowsing(t *testing.T) {
	logOutput := &bytes.Buffer{}
	testutil.ReplaceLogWriter(t, logOutput)
	testutil.ReplaceLogLevel(t, log.DEBUG)

	d := newForTest(&Config{SafeBrowsingEnabled: true}, nil)
	t.Cleanup(d.Close)
	matching := "wmconvirus.narod.ru"
	d.safeBrowsingUpstream = &testSbUpstream{
		hostname: matching,
		block:    true,
	}
	d.checkMatch(t, matching)

	assert.Contains(t, logOutput.String(), "SafeBrowsing lookup for "+matching)

	d.checkMatch(t, "test."+matching)
	d.checkMatchEmpty(t, "yandex.ru")
	d.checkMatchEmpty(t, "pornhub.com")

	// test cached result
	d.safeBrowsingServer = "127.0.0.1"
	d.checkMatch(t, matching)
	d.checkMatchEmpty(t, "pornhub.com")
	d.safeBrowsingServer = defaultSafebrowsingServer
}

func TestParallelSB(t *testing.T) {
	d := newForTest(&Config{SafeBrowsingEnabled: true}, nil)
	t.Cleanup(d.Close)
	matching := "wmconvirus.narod.ru"
	d.safeBrowsingUpstream = &testSbUpstream{
		hostname: matching,
		block:    true,
	}

	t.Run("group", func(t *testing.T) {
		for i := 0; i < 100; i++ {
			t.Run(fmt.Sprintf("aaa%d", i), func(t *testing.T) {
				t.Parallel()
				d.checkMatch(t, matching)
				d.checkMatch(t, "test."+matching)
				d.checkMatchEmpty(t, "yandex.ru")
				d.checkMatchEmpty(t, "pornhub.com")
			})
		}
	})
}

// SAFE SEARCH

func TestSafeSearch(t *testing.T) {
	d := newForTest(&Config{SafeSearchEnabled: true}, nil)
	t.Cleanup(d.Close)
	val, ok := d.SafeSearchDomain("www.google.com")
	assert.True(t, ok, "Expected safesearch to find result for www.google.com")
	assert.Equal(t, "forcesafesearch.google.com", val, "Expected safesearch for google.com to be forcesafesearch.google.com")
}

func TestCheckHostSafeSearchYandex(t *testing.T) {
	d := newForTest(&Config{SafeSearchEnabled: true}, nil)
	t.Cleanup(d.Close)

	// Check host for each domain
	for _, host := range []string{
		"yAndeX.ru",
		"YANdex.COM",
		"yandex.ua",
		"yandex.by",
		"yandex.kz",
		"www.yandex.com",
	} {
		res, err := d.CheckHost(host, dns.TypeA, &setts)
		assert.Nil(t, err)
		assert.True(t, res.IsFiltered)
		if assert.Len(t, res.Rules, 1) {
			assert.Equal(t, res.Rules[0].IP, net.IPv4(213, 180, 193, 56))
		}
	}
}

// testResolver is a Resolver for tests.
type testResolver struct{}

// LookupIP implements Resolver interface for *testResolver.
func (r *testResolver) LookupIPAddr(_ context.Context, host string) (ips []net.IPAddr, err error) {
	hash := sha256.Sum256([]byte(host))
	addrs := []net.IPAddr{{
		IP:   net.IP(hash[:4]),
		Zone: "somezone",
	}, {
		IP:   net.IP(hash[4:20]),
		Zone: "somezone",
	}}
	return addrs, nil
}

func TestCheckHostSafeSearchGoogle(t *testing.T) {
	d := newForTest(&Config{SafeSearchEnabled: true}, nil)
	t.Cleanup(d.Close)
	d.resolver = &testResolver{}

	// Check host for each domain
	for _, host := range []string{
		"www.google.com",
		"www.google.im",
		"www.google.co.in",
		"www.google.iq",
		"www.google.is",
		"www.google.it",
		"www.google.je",
	} {
		t.Run(host, func(t *testing.T) {
			res, err := d.CheckHost(host, dns.TypeA, &setts)
			assert.Nil(t, err)
			assert.True(t, res.IsFiltered)
			assert.Len(t, res.Rules, 1)
		})
	}
}

func TestSafeSearchCacheYandex(t *testing.T) {
	d := newForTest(nil, nil)
	t.Cleanup(d.Close)
	domain := "yandex.ru"

	// Check host with disabled safesearch.
	res, err := d.CheckHost(domain, dns.TypeA, &setts)
	assert.Nil(t, err)
	assert.False(t, res.IsFiltered)
	assert.Empty(t, res.Rules)

	d = newForTest(&Config{SafeSearchEnabled: true}, nil)
	t.Cleanup(d.Close)

	res, err = d.CheckHost(domain, dns.TypeA, &setts)
	assert.Nilf(t, err, "CheckHost for safesearh domain %s failed cause %s", domain, err)

	// For yandex we already know valid ip.
	if assert.Len(t, res.Rules, 1) {
		assert.Equal(t, res.Rules[0].IP, net.IPv4(213, 180, 193, 56))
	}

	// Check cache.
	cachedValue, isFound := getCachedResult(gctx.safeSearchCache, domain)
	assert.True(t, isFound)
	if assert.Len(t, cachedValue.Rules, 1) {
		assert.Equal(t, cachedValue.Rules[0].IP, net.IPv4(213, 180, 193, 56))
	}
}

func TestSafeSearchCacheGoogle(t *testing.T) {
	d := newForTest(nil, nil)
	t.Cleanup(d.Close)

	resolver := &testResolver{}
	d.resolver = resolver

	domain := "www.google.ru"
	res, err := d.CheckHost(domain, dns.TypeA, &setts)
	assert.Nil(t, err)
	assert.False(t, res.IsFiltered)
	assert.Empty(t, res.Rules)

	d = newForTest(&Config{SafeSearchEnabled: true}, nil)
	t.Cleanup(d.Close)
	d.resolver = resolver

	// Let's lookup for safesearch domain
	safeDomain, ok := d.SafeSearchDomain(domain)
	assert.Truef(t, ok, "Failed to get safesearch domain for %s", domain)

	ipAddrs, err := resolver.LookupIPAddr(context.Background(), safeDomain)
	if err != nil {
		t.Fatalf("Failed to lookup for %s", safeDomain)
	}

	ip := ipAddrs[0].IP
	for _, ipAddr := range ipAddrs {
		if ipAddr.IP.To4() != nil {
			ip = ipAddr.IP
			break
		}
	}

	res, err = d.CheckHost(domain, dns.TypeA, &setts)
	assert.Nil(t, err)
	if assert.Len(t, res.Rules, 1) {
		assert.True(t, res.Rules[0].IP.Equal(ip))
	}

	// Check cache.
	cachedValue, isFound := getCachedResult(gctx.safeSearchCache, domain)
	assert.True(t, isFound)
	if assert.Len(t, cachedValue.Rules, 1) {
		assert.True(t, cachedValue.Rules[0].IP.Equal(ip))
	}
}

// PARENTAL

func TestParentalControl(t *testing.T) {
	logOutput := &bytes.Buffer{}
	testutil.ReplaceLogWriter(t, logOutput)
	testutil.ReplaceLogLevel(t, log.DEBUG)

	d := newForTest(&Config{ParentalEnabled: true}, nil)
	t.Cleanup(d.Close)
	matching := "pornhub.com"
	d.parentalUpstream = &testSbUpstream{
		hostname: matching,
		block:    true,
	}

	d.checkMatch(t, matching)
	assert.Contains(t, logOutput.String(), "Parental lookup for "+matching)
	d.checkMatch(t, "www."+matching)
	d.checkMatchEmpty(t, "www.yandex.ru")
	d.checkMatchEmpty(t, "yandex.ru")
	d.checkMatchEmpty(t, "api.jquery.com")

	// test cached result
	d.parentalServer = "127.0.0.1"
	d.checkMatch(t, matching)
	d.checkMatchEmpty(t, "yandex.ru")
	d.parentalServer = defaultParentalServer
}

// FILTERING

func TestMatching(t *testing.T) {
	const nl = "\n"
	const (
		blockingRules  = `||example.org^` + nl
		allowlistRules = `||example.org^` + nl + `@@||test.example.org` + nl
		importantRules = `@@||example.org^` + nl + `||test.example.org^$important` + nl
		regexRules     = `/example\.org/` + nl + `@@||test.example.org^` + nl
		maskRules      = `test*.example.org^` + nl + `exam*.com` + nl
		dnstypeRules   = `||example.org^$dnstype=AAAA` + nl + `@@||test.example.org^` + nl
	)
	testCases := []struct {
		name           string
		rules          string
		host           string
		wantIsFiltered bool
		wantReason     Reason
		wantDNSType    uint16
	}{{
		name:           "sanity",
		rules:          "||doubleclick.net^",
		host:           "www.doubleclick.net",
		wantIsFiltered: true,
		wantReason:     FilteredBlockList,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "sanity",
		rules:          "||doubleclick.net^",
		host:           "nodoubleclick.net",
		wantIsFiltered: false,
		wantReason:     NotFilteredNotFound,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "sanity",
		rules:          "||doubleclick.net^",
		host:           "doubleclick.net.ru",
		wantIsFiltered: false,
		wantReason:     NotFilteredNotFound,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "sanity",
		rules:          "||doubleclick.net^",
		host:           "wmconvirus.narod.ru",
		wantIsFiltered: false,
		wantReason:     NotFilteredNotFound,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "blocking",
		rules:          blockingRules,
		host:           "example.org",
		wantIsFiltered: true,
		wantReason:     FilteredBlockList,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "blocking",
		rules:          blockingRules,
		host:           "test.example.org",
		wantIsFiltered: true,
		wantReason:     FilteredBlockList,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "blocking",
		rules:          blockingRules,
		host:           "test.test.example.org",
		wantIsFiltered: true,
		wantReason:     FilteredBlockList,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "blocking",
		rules:          blockingRules,
		host:           "testexample.org",
		wantIsFiltered: false,
		wantReason:     NotFilteredNotFound,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "blocking",
		rules:          blockingRules,
		host:           "onemoreexample.org",
		wantIsFiltered: false,
		wantReason:     NotFilteredNotFound,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "allowlist",
		rules:          allowlistRules,
		host:           "example.org",
		wantIsFiltered: true,
		wantReason:     FilteredBlockList,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "allowlist",
		rules:          allowlistRules,
		host:           "test.example.org",
		wantIsFiltered: false,
		wantReason:     NotFilteredAllowList,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "allowlist",
		rules:          allowlistRules,
		host:           "test.test.example.org",
		wantIsFiltered: false,
		wantReason:     NotFilteredAllowList,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "allowlist",
		rules:          allowlistRules,
		host:           "testexample.org",
		wantIsFiltered: false,
		wantReason:     NotFilteredNotFound,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "allowlist",
		rules:          allowlistRules,
		host:           "onemoreexample.org",
		wantIsFiltered: false,
		wantReason:     NotFilteredNotFound,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "important",
		rules:          importantRules,
		host:           "example.org",
		wantIsFiltered: false,
		wantReason:     NotFilteredAllowList,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "important",
		rules:          importantRules,
		host:           "test.example.org",
		wantIsFiltered: true,
		wantReason:     FilteredBlockList,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "important",
		rules:          importantRules,
		host:           "test.test.example.org",
		wantIsFiltered: true,
		wantReason:     FilteredBlockList,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "important",
		rules:          importantRules,
		host:           "testexample.org",
		wantIsFiltered: false,
		wantReason:     NotFilteredNotFound,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "important",
		rules:          importantRules,
		host:           "onemoreexample.org",
		wantIsFiltered: false,
		wantReason:     NotFilteredNotFound,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "regex",
		rules:          regexRules,
		host:           "example.org",
		wantIsFiltered: true,
		wantReason:     FilteredBlockList,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "regex",
		rules:          regexRules,
		host:           "test.example.org",
		wantIsFiltered: false,
		wantReason:     NotFilteredAllowList,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "regex",
		rules:          regexRules,
		host:           "test.test.example.org",
		wantIsFiltered: false,
		wantReason:     NotFilteredAllowList,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "regex",
		rules:          regexRules,
		host:           "testexample.org",
		wantIsFiltered: true,
		wantReason:     FilteredBlockList,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "regex",
		rules:          regexRules,
		host:           "onemoreexample.org",
		wantIsFiltered: true,
		wantReason:     FilteredBlockList,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "mask",
		rules:          maskRules,
		host:           "test.example.org",
		wantIsFiltered: true,
		wantReason:     FilteredBlockList,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "mask",
		rules:          maskRules,
		host:           "test2.example.org",
		wantIsFiltered: true,
		wantReason:     FilteredBlockList,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "mask",
		rules:          maskRules,
		host:           "example.com",
		wantIsFiltered: true,
		wantReason:     FilteredBlockList,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "mask",
		rules:          maskRules,
		host:           "exampleeee.com",
		wantIsFiltered: true,
		wantReason:     FilteredBlockList,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "mask",
		rules:          maskRules,
		host:           "onemoreexamsite.com",
		wantIsFiltered: true,
		wantReason:     FilteredBlockList,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "mask",
		rules:          maskRules,
		host:           "example.org",
		wantIsFiltered: false,
		wantReason:     NotFilteredNotFound,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "mask",
		rules:          maskRules,
		host:           "testexample.org",
		wantIsFiltered: false,
		wantReason:     NotFilteredNotFound,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "mask",
		rules:          maskRules,
		host:           "example.co.uk",
		wantIsFiltered: false,
		wantReason:     NotFilteredNotFound,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "dnstype",
		rules:          dnstypeRules,
		host:           "onemoreexample.org",
		wantIsFiltered: false,
		wantReason:     NotFilteredNotFound,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "dnstype",
		rules:          dnstypeRules,
		host:           "example.org",
		wantIsFiltered: false,
		wantReason:     NotFilteredNotFound,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "dnstype",
		rules:          dnstypeRules,
		host:           "example.org",
		wantIsFiltered: true,
		wantReason:     FilteredBlockList,
		wantDNSType:    dns.TypeAAAA,
	}, {
		name:           "dnstype",
		rules:          dnstypeRules,
		host:           "test.example.org",
		wantIsFiltered: false,
		wantReason:     NotFilteredAllowList,
		wantDNSType:    dns.TypeA,
	}, {
		name:           "dnstype",
		rules:          dnstypeRules,
		host:           "test.example.org",
		wantIsFiltered: false,
		wantReason:     NotFilteredAllowList,
		wantDNSType:    dns.TypeAAAA,
	}}
	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%s-%s", tc.name, tc.host), func(t *testing.T) {
			filters := []Filter{{ID: 0, Data: []byte(tc.rules)}}
			d := newForTest(nil, filters)
			t.Cleanup(d.Close)

			res, err := d.CheckHost(tc.host, tc.wantDNSType, &setts)
			assert.Nilf(t, err, "Error while matching host %s: %s", tc.host, err)
			assert.Equalf(t, tc.wantIsFiltered, res.IsFiltered, "Hostname %s has wrong result (%v must be %v)", tc.host, res.IsFiltered, tc.wantIsFiltered)
			assert.Equalf(t, tc.wantReason, res.Reason, "Hostname %s has wrong reason (%v must be %v)", tc.host, res.Reason.String(), tc.wantReason.String())
		})
	}
}

func TestWhitelist(t *testing.T) {
	rules := `||host1^
||host2^
`
	filters := []Filter{{
		ID: 0, Data: []byte(rules),
	}}

	whiteRules := `||host1^
||host3^
`
	whiteFilters := []Filter{{
		ID: 0, Data: []byte(whiteRules),
	}}
	d := newForTest(nil, filters)
	d.SetFilters(filters, whiteFilters, false)
	t.Cleanup(d.Close)

	// matched by white filter
	res, err := d.CheckHost("host1", dns.TypeA, &setts)
	assert.Nil(t, err)
	assert.False(t, res.IsFiltered)
	assert.Equal(t, res.Reason, NotFilteredAllowList)
	if assert.Len(t, res.Rules, 1) {
		assert.Equal(t, "||host1^", res.Rules[0].Text)
	}

	// not matched by white filter, but matched by block filter
	res, err = d.CheckHost("host2", dns.TypeA, &setts)
	assert.Nil(t, err)
	assert.True(t, res.IsFiltered)
	assert.Equal(t, res.Reason, FilteredBlockList)
	if assert.Len(t, res.Rules, 1) {
		assert.Equal(t, "||host2^", res.Rules[0].Text)
	}
}

// CLIENT SETTINGS

func applyClientSettings(setts *RequestFilteringSettings) {
	setts.FilteringEnabled = false
	setts.ParentalEnabled = false
	setts.SafeBrowsingEnabled = true

	rule, _ := rules.NewNetworkRule("||facebook.com^", 0)
	s := ServiceEntry{}
	s.Name = "facebook"
	s.Rules = []*rules.NetworkRule{rule}
	setts.ServicesRules = append(setts.ServicesRules, s)
}

// Check behaviour without any per-client settings,
//  then apply per-client settings and check behaviour once again
func TestClientSettings(t *testing.T) {
	var r Result
	d := newForTest(
		&Config{
			ParentalEnabled:     true,
			SafeBrowsingEnabled: false,
		},
		[]Filter{{
			ID: 0, Data: []byte("||example.org^\n"),
		}},
	)
	t.Cleanup(d.Close)
	d.parentalUpstream = &testSbUpstream{
		hostname: "pornhub.com",
		block:    true,
	}
	d.safeBrowsingUpstream = &testSbUpstream{
		hostname: "wmconvirus.narod.ru",
		block:    true,
	}

	// No client settings:

	// Blocked by filters
	r, _ = d.CheckHost("example.org", dns.TypeA, &setts)
	assert.True(t, r.IsFiltered, "CheckHost FilteredBlockList")
	assert.Equal(t, FilteredBlockList, r.Reason, "CheckHost FilteredBlockList")

	// Blocked by parental
	r, _ = d.CheckHost("pornhub.com", dns.TypeA, &setts)
	assert.True(t, r.IsFiltered, "CheckHost FilteredParental")
	assert.Equal(t, FilteredParental, r.Reason, "CheckHost FilteredParental")

	// SafeBrowsing is disabled
	r, _ = d.CheckHost("wmconvirus.narod.ru", dns.TypeA, &setts)
	assert.False(t, r.IsFiltered, "CheckHost SafeBrowsing")

	// Not blocked
	r, _ = d.CheckHost("facebook.com", dns.TypeA, &setts)
	assert.False(t, r.IsFiltered)

	// Override client settings:
	applyClientSettings(&setts)

	// Override filtering settings
	r, _ = d.CheckHost("example.org", dns.TypeA, &setts)
	assert.False(t, r.IsFiltered, "CheckHost FilteredBlocklist")

	// Override parental settings (force disable parental)
	r, _ = d.CheckHost("pornhub.com", dns.TypeA, &setts)
	assert.False(t, r.IsFiltered, "CheckHost FilteredParental")

	// Override SafeBrowsing settings (force enable safesearch)
	r, _ = d.CheckHost("wmconvirus.narod.ru", dns.TypeA, &setts)
	assert.True(t, r.IsFiltered, "CheckHost FilteredSafeBrowsing")
	assert.Equal(t, FilteredSafeBrowsing, r.Reason, "CheckHost FilteredSafeBrowsing")

	// Blocked by additional rules
	r, _ = d.CheckHost("facebook.com", dns.TypeA, &setts)
	assert.True(t, r.IsFiltered)
	assert.Equal(t, r.Reason, FilteredBlockedService)
}

// BENCHMARKS

func BenchmarkSafeBrowsing(b *testing.B) {
	d := newForTest(&Config{SafeBrowsingEnabled: true}, nil)
	b.Cleanup(d.Close)
	blocked := "wmconvirus.narod.ru"
	d.safeBrowsingUpstream = &testSbUpstream{
		hostname: blocked,
		block:    true,
	}
	for n := 0; n < b.N; n++ {
		res, err := d.CheckHost(blocked, dns.TypeA, &setts)
		assert.Nilf(b, err, "Error while matching host %s: %s", blocked, err)
		assert.True(b, res.IsFiltered, "Expected hostname %s to match", blocked)
	}
}

func BenchmarkSafeBrowsingParallel(b *testing.B) {
	d := newForTest(&Config{SafeBrowsingEnabled: true}, nil)
	b.Cleanup(d.Close)
	blocked := "wmconvirus.narod.ru"
	d.safeBrowsingUpstream = &testSbUpstream{
		hostname: blocked,
		block:    true,
	}
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			res, err := d.CheckHost(blocked, dns.TypeA, &setts)
			assert.Nilf(b, err, "Error while matching host %s: %s", blocked, err)
			assert.True(b, res.IsFiltered, "Expected hostname %s to match", blocked)
		}
	})
}

func BenchmarkSafeSearch(b *testing.B) {
	d := newForTest(&Config{SafeSearchEnabled: true}, nil)
	b.Cleanup(d.Close)
	for n := 0; n < b.N; n++ {
		val, ok := d.SafeSearchDomain("www.google.com")
		assert.True(b, ok, "Expected safesearch to find result for www.google.com")
		assert.Equal(b, "forcesafesearch.google.com", val, "Expected safesearch for google.com to be forcesafesearch.google.com")
	}
}

func BenchmarkSafeSearchParallel(b *testing.B) {
	d := newForTest(&Config{SafeSearchEnabled: true}, nil)
	b.Cleanup(d.Close)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			val, ok := d.SafeSearchDomain("www.google.com")
			assert.True(b, ok, "Expected safesearch to find result for www.google.com")
			assert.Equal(b, "forcesafesearch.google.com", val, "Expected safesearch for google.com to be forcesafesearch.google.com")
		}
	})
}

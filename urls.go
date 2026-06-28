package kobolt

import (
	"net/url"
	"regexp"
	"strings"
)

// IsKoboURL reports whether u parses as an http(s) URL whose host is kobo.com
// or a subdomain of it (e.g. www.kobo.com). Other hosts are unsupported.
func IsKoboURL(u string) bool {
	parsed, err := url.Parse(u)
	if err != nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "kobo.com" || strings.HasSuffix(host, ".kobo.com")
}

// koboURLRegion matches https://(www.)kobo.com/{cc}/... so we can swap or
// extract the region. Only the cc segment is swapped; the language segment
// (usually /en/) is left alone.
var koboURLRegion = regexp.MustCompile(`^(https?://(?:www\.)?kobo\.com/)([a-z]{2})(/.*)$`)

func ExtractRegion(url string) (string, bool) {
	m := koboURLRegion.FindStringSubmatch(url)
	if m == nil {
		return "", false
	}
	return m[2], true
}

func SubstituteRegion(url, cc string) string {
	idx := koboURLRegion.FindStringSubmatchIndex(url)
	if idx == nil {
		return url
	}
	return url[:idx[4]] + cc + url[idx[5]:]
}

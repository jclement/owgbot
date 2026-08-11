package gem

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"
)

const (
	geminiPort   = "1965"
	maxRedirects = 5
	maxBody      = 64 * 1024
	dialTimeout  = 15 * time.Second
)

// fetch retrieves a gemini URL, following redirects. Certificates are not
// verified (gemini convention is TOFU; the bot treats content as untrusted
// text regardless).
func fetch(rawURL string) (finalURL string, body string, err error) {
	u, err := normalizeURL(rawURL)
	if err != nil {
		return "", "", err
	}
	for i := 0; i <= maxRedirects; i++ {
		status, meta, b, err := fetchOne(u)
		if err != nil {
			return "", "", err
		}
		switch {
		case status/10 == 2:
			return u.String(), b, nil
		case status/10 == 3:
			next, err := u.Parse(meta)
			if err != nil {
				return "", "", fmt.Errorf("bad redirect %q", meta)
			}
			u = next
		case status/10 == 1:
			return "", "", fmt.Errorf("page wants input (unsupported)")
		default:
			return "", "", fmt.Errorf("gemini error %d %s", status, meta)
		}
	}
	return "", "", fmt.Errorf("too many redirects")
}

func fetchOne(u *url.URL) (status int, meta, body string, err error) {
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), geminiPort)
	}
	d := &net.Dialer{Timeout: dialTimeout}
	conn, err := tls.DialWithDialer(d, "tcp", host, &tls.Config{
		InsecureSkipVerify: true, // gemini TOFU
		ServerName:         u.Hostname(),
		MinVersion:         tls.VersionTLS12,
	})
	if err != nil {
		return 0, "", "", fmt.Errorf("connect %s: %w", u.Hostname(), err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(dialTimeout))

	if _, err := fmt.Fprintf(conn, "%s\r\n", u.String()); err != nil {
		return 0, "", "", err
	}
	r := bufio.NewReader(io.LimitReader(conn, maxBody))
	header, err := r.ReadString('\n')
	if err != nil {
		return 0, "", "", fmt.Errorf("read header: %w", err)
	}
	header = strings.TrimRight(header, "\r\n")
	if len(header) < 2 || header[0] < '1' || header[0] > '6' {
		return 0, "", "", fmt.Errorf("bad response header")
	}
	status = int(header[0]-'0')*10 + int(header[1]-'0')
	if len(header) > 3 {
		meta = header[3:]
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return 0, "", "", err
	}
	return status, meta, string(b), nil
}

// normalizeURL accepts "owg.fyi", "owg.fyi/page", or a full gemini:// URL.
func normalizeURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("no url")
	}
	if !strings.Contains(raw, "://") {
		raw = "gemini://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "gemini" || u.Hostname() == "" {
		return nil, fmt.Errorf("bad gemini url")
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u, nil
}

// render converts gemtext to terse plain text, numbering links as [n].
// Returns the rendered text and the ordered absolute link URLs.
func render(baseURL, gemtext string) (string, []string) {
	base, _ := url.Parse(baseURL)
	var out []string
	var links []string
	pre := false
	for _, line := range strings.Split(gemtext, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "```") {
			pre = !pre
			continue
		}
		if pre {
			out = append(out, line)
			continue
		}
		if strings.HasPrefix(line, "=>") {
			target, label := parseLink(line)
			if target == "" {
				continue
			}
			abs := target
			if base != nil {
				if r, err := base.Parse(target); err == nil {
					abs = r.String()
				}
			}
			links = append(links, abs)
			if label == "" {
				label = target
			}
			out = append(out, fmt.Sprintf("[%d] %s", len(links), label))
			continue
		}
		// Strip heading/list/quote markers down to plain terse text.
		line = strings.TrimPrefix(line, "### ")
		line = strings.TrimPrefix(line, "## ")
		line = strings.TrimPrefix(line, "# ")
		out = append(out, line)
	}
	// Collapse runs of blank lines.
	var compact []string
	for _, l := range out {
		if strings.TrimSpace(l) == "" && len(compact) > 0 && compact[len(compact)-1] == "" {
			continue
		}
		if strings.TrimSpace(l) == "" {
			compact = append(compact, "")
		} else {
			compact = append(compact, l)
		}
	}
	return strings.TrimSpace(strings.Join(compact, "\n")), links
}

// parseLink splits a "=> target optional label" gemtext line.
func parseLink(line string) (target, label string) {
	rest := strings.TrimSpace(strings.TrimPrefix(line, "=>"))
	if rest == "" {
		return "", ""
	}
	if i := strings.IndexAny(rest, " \t"); i >= 0 {
		return rest[:i], strings.TrimSpace(rest[i:])
	}
	return rest, ""
}

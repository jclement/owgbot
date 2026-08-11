package sms

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// voip.ms REST API: a flat GET endpoint, JSON responses, polling-friendly —
// no webhooks needed, which suits a bot with no inbound HTTP.
const voipmsBase = "https://voip.ms/api/v1/rest.php"

// creds is one user's voip.ms configuration (stored per user in the KV).
type creds struct {
	Provider string `json:"provider"` // "voipms"
	DID      string `json:"did"`      // their voip.ms number, 10 digits
	User     string `json:"user"`     // account email
	Pass     string `json:"pass"`     // API password
}

// inbound is one received SMS.
type inbound struct {
	ID   int64
	From string
	Text string
	Date string
}

type voipms struct {
	base   string
	client *http.Client
}

func newVoipms(base string, client *http.Client) *voipms {
	if base == "" {
		base = voipmsBase
	}
	return &voipms{base: base, client: client}
}

func (v *voipms) call(c creds, method string, extra url.Values) (json.RawMessage, string, error) {
	q := url.Values{
		"api_username": {c.User},
		"api_password": {c.Pass},
		"method":       {method},
		"did":          {c.DID},
	}
	for k, vals := range extra {
		q[k] = vals
	}
	resp, err := v.client.Get(v.base + "?" + q.Encode())
	if err != nil {
		return nil, "", fmt.Errorf("voip.ms unreachable: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		Status string          `json:"status"`
		SMS    json.RawMessage `json:"sms"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, "", fmt.Errorf("voip.ms: bad response: %w", err)
	}
	return out.SMS, out.Status, nil
}

// send delivers one SMS from the user's DID.
func (v *voipms) send(c creds, dst, message string) error {
	_, status, err := v.call(c, "sendSMS", url.Values{
		"dst":     {dst},
		"message": {message},
	})
	if err != nil {
		return err
	}
	if status != "success" {
		return fmt.Errorf("voip.ms: %s", status)
	}
	return nil
}

// fetch lists SMS received on the DID in the last two days.
func (v *voipms) fetch(c creds) ([]inbound, error) {
	now := time.Now()
	raw, status, err := v.call(c, "getSMS", url.Values{
		"type":  {"1"}, // received
		"from":  {now.AddDate(0, 0, -2).Format("2006-01-02")},
		"to":    {now.Format("2006-01-02")},
		"limit": {"50"},
	})
	if err != nil {
		return nil, err
	}
	if status == "no_sms" {
		return nil, nil
	}
	if status != "success" {
		return nil, fmt.Errorf("voip.ms: %s", status)
	}
	var items []struct {
		ID      string `json:"id"`
		Date    string `json:"date"`
		Contact string `json:"contact"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("voip.ms: bad sms list: %w", err)
	}
	var out []inbound
	for _, it := range items {
		id, err := strconv.ParseInt(it.ID, 10, 64)
		if err != nil {
			continue
		}
		out = append(out, inbound{ID: id, From: it.Contact, Text: it.Message, Date: it.Date})
	}
	return out, nil
}

// normalizeNumber reduces a NANP number to voip.ms's 10-digit form.
func normalizeNumber(s string) (string, error) {
	var digits strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	d := digits.String()
	if len(d) == 11 && d[0] == '1' {
		d = d[1:]
	}
	if len(d) != 10 {
		return "", fmt.Errorf("need a 10-digit North American number (got %q)", s)
	}
	return d, nil
}

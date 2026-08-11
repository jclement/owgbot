// Package weather implements /w — a terse forecast via Open-Meteo
// (free, no API key, includes geocoding).
package weather

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jclement/owgbot/internal/plugin"
)

type Plugin struct {
	env    plugin.Env
	client *http.Client
}

func New() *Plugin {
	return &Plugin{client: &http.Client{Timeout: 15 * time.Second}}
}

func (p *Plugin) Name() string { return "weather" }

func (p *Plugin) Commands() []plugin.Command {
	return []plugin.Command{{
		Name: "w", Args: "[location]",
		Help: "forecast (city or lat,lon; remembers last)",
	}}
}

func (p *Plugin) Init(env plugin.Env) error {
	p.env = env
	return nil
}

type place struct {
	Name string  `json:"name"`
	Lat  float64 `json:"latitude"`
	Lon  float64 `json:"longitude"`
}

func (p *Plugin) HandleCommand(ctx *plugin.Ctx, cmd, args string) error {
	loc := strings.TrimSpace(args)
	if loc == "" {
		saved, err := p.env.KV.Get(ctx.User, "loc")
		if err != nil {
			ctx.Reply("where? /w <city> or /w <lat,lon>")
			return nil
		}
		loc = saved
	}

	pl, err := p.resolve(loc)
	if err != nil {
		ctx.Reply(err.Error())
		return nil
	}
	report, err := p.forecast(pl)
	if err != nil {
		return fmt.Errorf("weather lookup failed: %w", err)
	}
	if err := p.env.KV.Set(ctx.User, "loc", loc); err != nil {
		p.env.Log.Warn("saving location failed", "err", err)
	}
	ctx.Reply(report)
	return nil
}

// resolve turns user input into coordinates: "51.05,-114.07" directly, or a
// place name via the Open-Meteo geocoder.
func (p *Plugin) resolve(loc string) (place, error) {
	if lat, lon, ok := parseLatLon(loc); ok {
		return place{Name: fmt.Sprintf("%.2f,%.2f", lat, lon), Lat: lat, Lon: lon}, nil
	}
	u := "https://geocoding-api.open-meteo.com/v1/search?count=1&name=" + url.QueryEscape(loc)
	var out struct {
		Results []place `json:"results"`
	}
	if err := p.getJSON(u, &out); err != nil {
		return place{}, fmt.Errorf("geocoder unavailable, try later")
	}
	if len(out.Results) == 0 {
		return place{}, fmt.Errorf("no such place: %s", loc)
	}
	return out.Results[0], nil
}

func parseLatLon(s string) (lat, lon float64, ok bool) {
	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return 0, 0, false
	}
	lat, err1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	lon, err2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err1 != nil || err2 != nil || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return 0, 0, false
	}
	return lat, lon, true
}

func (p *Plugin) forecast(pl place) (string, error) {
	u := fmt.Sprintf("https://api.open-meteo.com/v1/forecast?latitude=%.4f&longitude=%.4f"+
		"&current=temperature_2m,weather_code,wind_speed_10m"+
		"&daily=temperature_2m_max,temperature_2m_min,precipitation_probability_max,weather_code"+
		"&timezone=auto&forecast_days=2", pl.Lat, pl.Lon)
	var out struct {
		Current struct {
			Temp float64 `json:"temperature_2m"`
			Code int     `json:"weather_code"`
			Wind float64 `json:"wind_speed_10m"`
		} `json:"current"`
		Daily struct {
			Max  []float64 `json:"temperature_2m_max"`
			Min  []float64 `json:"temperature_2m_min"`
			Pop  []int     `json:"precipitation_probability_max"`
			Code []int     `json:"weather_code"`
		} `json:"daily"`
	}
	if err := p.getJSON(u, &out); err != nil {
		return "", err
	}
	if len(out.Daily.Max) < 1 {
		return "", fmt.Errorf("no forecast data")
	}
	s := fmt.Sprintf("%s: %.0f° %s, wind %.0fkm/h. Hi %.0f/Lo %.0f pop %d%%",
		pl.Name, out.Current.Temp, codeText(out.Current.Code), out.Current.Wind,
		out.Daily.Max[0], out.Daily.Min[0], popAt(out.Daily.Pop, 0))
	if len(out.Daily.Max) > 1 {
		s += fmt.Sprintf("\nTmrw: %s hi %.0f/lo %.0f pop %d%%",
			codeText(codeAt(out.Daily.Code, 1)), out.Daily.Max[1], out.Daily.Min[1], popAt(out.Daily.Pop, 1))
	}
	return s, nil
}

func popAt(pop []int, i int) int {
	if i < len(pop) {
		return pop[i]
	}
	return 0
}

func codeAt(codes []int, i int) int {
	if i < len(codes) {
		return codes[i]
	}
	return 0
}

func (p *Plugin) getJSON(u string, v any) error {
	resp, err := p.client.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// codeText maps WMO weather codes to terse descriptions.
func codeText(code int) string {
	switch {
	case code == 0:
		return "clear"
	case code <= 2:
		return "partly cloudy"
	case code == 3:
		return "overcast"
	case code == 45 || code == 48:
		return "fog"
	case code >= 51 && code <= 57:
		return "drizzle"
	case code >= 61 && code <= 67:
		return "rain"
	case code >= 71 && code <= 77:
		return "snow"
	case code >= 80 && code <= 82:
		return "showers"
	case code == 85 || code == 86:
		return "snow showers"
	case code >= 95:
		return "thunderstorm"
	default:
		return "?"
	}
}

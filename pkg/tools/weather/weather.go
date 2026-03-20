package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/vvsynapse/temporal-agent-sdk-go/pkg/interfaces"
	"github.com/vvsynapse/temporal-agent-sdk-go/pkg/tools"
)

var _ interfaces.Tool = (*Weather)(nil)

// Weather fetches current weather using Open-Meteo API (free, no key required).
type Weather struct {
	client *http.Client
}

// New returns a new Weather tool.
func New() *Weather {
	return &Weather{client: &http.Client{}}
}

func (*Weather) Name() string { return "weather" }

func (*Weather) Description() string {
	return "Gets current weather for a location. Use when the user asks about weather, temperature, humidity, or conditions. Accepts city name or coordinates."
}

func (*Weather) Parameters() interfaces.JSONSchema {
	return tools.Params(
		map[string]interfaces.JSONSchema{
			"location": tools.ParamString("City name (e.g. London, New York) or lat,lon (e.g. 52.52,13.41)"),
		},
		"location",
	)
}

type geocodeResult struct {
	Results []struct {
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		Name      string  `json:"name"`
	} `json:"results"`
}

type forecastResult struct {
	Current struct {
		Temperature float64 `json:"temperature_2m"`
		Humidity    int     `json:"relative_humidity_2m"`
		WindSpeed   float64 `json:"wind_speed_10m"`
		WeatherCode int     `json:"weather_code"`
	} `json:"current"`
}

func (w *Weather) Execute(ctx context.Context, args map[string]any) (any, error) {
	loc, _ := args["location"].(string)
	if loc == "" {
		return nil, fmt.Errorf("location is required")
	}

	lat, lon, err := w.resolveLocation(ctx, loc)
	if err != nil {
		return nil, err
	}

	return w.fetchWeather(ctx, lat, lon)
}

func (w *Weather) resolveLocation(ctx context.Context, location string) (lat, lon float64, err error) {
	// Try parsing as "lat,lon"
	var l1, l2 float64
	if _, scanErr := fmt.Sscanf(location, "%f,%f", &l1, &l2); scanErr == nil {
		return l1, l2, nil
	}

	// Geocode city name
	u := "https://geocoding-api.open-meteo.com/v1/search?name=" + url.QueryEscape(location) + "&count=1"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, 0, err
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("geocoding: %w", err)
	}
	defer resp.Body.Close()

	var geo geocodeResult
	if err := json.NewDecoder(resp.Body).Decode(&geo); err != nil {
		return 0, 0, fmt.Errorf("geocoding decode: %w", err)
	}
	if len(geo.Results) == 0 {
		return 0, 0, fmt.Errorf("location not found: %s", location)
	}
	return geo.Results[0].Latitude, geo.Results[0].Longitude, nil
}

func (w *Weather) fetchWeather(ctx context.Context, lat, lon float64) (any, error) {
	u := fmt.Sprintf("https://api.open-meteo.com/v1/forecast?latitude=%.4f&longitude=%.4f&current=temperature_2m,relative_humidity_2m,wind_speed_10m,weather_code", lat, lon)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("weather fetch: %w", err)
	}
	defer resp.Body.Close()

	var f forecastResult
	if err := json.NewDecoder(resp.Body).Decode(&f); err != nil {
		return nil, fmt.Errorf("weather decode: %w", err)
	}

	desc := weatherCodeDesc(f.Current.WeatherCode)
	return map[string]any{
		"temperature_c":  f.Current.Temperature,
		"humidity_pct":   f.Current.Humidity,
		"wind_speed_kmh": f.Current.WindSpeed,
		"conditions":     desc,
	}, nil
}

func weatherCodeDesc(code int) string {
	codes := map[int]string{
		0: "clear", 1: "mainly clear", 2: "partly cloudy", 3: "overcast",
		45: "foggy", 48: "depositing rime fog",
		51: "light drizzle", 53: "moderate drizzle", 55: "dense drizzle",
		61: "slight rain", 63: "moderate rain", 65: "heavy rain",
		71: "slight snow", 73: "moderate snow", 75: "heavy snow",
		80: "slight rain showers", 81: "moderate rain showers", 82: "violent rain showers",
		95: "thunderstorm", 96: "thunderstorm with slight hail", 99: "thunderstorm with heavy hail",
	}
	if d, ok := codes[code]; ok {
		return d
	}
	return "unknown"
}

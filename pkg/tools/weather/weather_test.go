package weather

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func strResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func TestWeather_NameDescriptionParameters(t *testing.T) {
	w := New()
	if w.Name() != "weather" {
		t.Errorf("Name = %q", w.Name())
	}
	if w.Description() == "" {
		t.Error("Description empty")
	}
	p := w.Parameters()
	if p["type"] != "object" {
		t.Fatalf("type = %v", p["type"])
	}
	props, ok := p["properties"].(map[string]interfaces.JSONSchema)
	if !ok || props["location"] == nil {
		t.Fatal("expected location property")
	}
}

func TestWeather_Execute_LatLonSkipsGeocode(t *testing.T) {
	var sawForecast bool
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		u := req.URL.String()
		if strings.Contains(u, "geocoding-api.open-meteo.com") {
			t.Fatalf("unexpected geocode: %s", u)
		}
		if strings.Contains(u, "api.open-meteo.com/v1/forecast") {
			sawForecast = true
			return strResp(200, `{"current":{"temperature_2m":1.5,"relative_humidity_2m":70,"wind_speed_10m":3,"weather_code":0}}`), nil
		}
		return nil, fmt.Errorf("unexpected URL %s", u)
	})
	w := &Weather{client: &http.Client{Transport: rt}}
	got, err := w.Execute(context.Background(), map[string]any{"location": "12.5,10.0"})
	if err != nil {
		t.Fatal(err)
	}
	if !sawForecast {
		t.Fatal("expected forecast request")
	}
	m := got.(map[string]any)
	if m["temperature_c"] != 1.5 || m["conditions"] != "clear" {
		t.Fatalf("got %#v", m)
	}
}

func TestWeather_Execute_CityGeocodeThenForecast(t *testing.T) {
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		u := req.URL.String()
		if strings.Contains(u, "geocoding-api.open-meteo.com") {
			return strResp(200, `{"results":[{"latitude":51.0,"longitude":-0.1,"name":"London"}]}`), nil
		}
		if strings.Contains(u, "api.open-meteo.com/v1/forecast") {
			return strResp(200, `{"current":{"temperature_2m":8,"relative_humidity_2m":60,"wind_speed_10m":4,"weather_code":95}}`), nil
		}
		return nil, fmt.Errorf("unexpected URL %s", u)
	})
	w := &Weather{client: &http.Client{Transport: rt}}
	got, err := w.Execute(context.Background(), map[string]any{"location": "London"})
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	if m["conditions"] != "thunderstorm" {
		t.Fatalf("conditions = %v", m["conditions"])
	}
}

func TestWeather_Execute_RequiresLocation(t *testing.T) {
	w := New()
	_, err := w.Execute(context.Background(), map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "location is required") {
		t.Fatalf("got %v", err)
	}
}

func TestWeather_ResolveLocation_NotFound(t *testing.T) {
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return strResp(200, `{"results":[]}`), nil
	})
	w := &Weather{client: &http.Client{Transport: rt}}
	_, err := w.Execute(context.Background(), map[string]any{"location": "NowhereCityXYZ123"})
	if err == nil || !strings.Contains(err.Error(), "location not found") {
		t.Fatalf("got %v", err)
	}
}

func TestWeather_GeocodeDecodeError(t *testing.T) {
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.String(), "geocoding-api") {
			return strResp(200, `not-json`), nil
		}
		return strResp(200, `{}`), nil
	})
	w := &Weather{client: &http.Client{Transport: rt}}
	_, err := w.Execute(context.Background(), map[string]any{"location": "Paris"})
	if err == nil || !strings.Contains(err.Error(), "geocoding decode") {
		t.Fatalf("got %v", err)
	}
}

func TestWeather_ForecastDecodeError(t *testing.T) {
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		u := req.URL.String()
		if strings.Contains(u, "geocoding-api") {
			return strResp(200, `{"results":[{"latitude":1,"longitude":2,"name":"P"}]}`), nil
		}
		return strResp(200, `not-json`), nil
	})
	w := &Weather{client: &http.Client{Transport: rt}}
	_, err := w.Execute(context.Background(), map[string]any{"location": "Paris"})
	if err == nil || !strings.Contains(err.Error(), "weather decode") {
		t.Fatalf("got %v", err)
	}
}

func TestWeatherCodeDesc(t *testing.T) {
	if weatherCodeDesc(0) != "clear" {
		t.Fatal(weatherCodeDesc(0))
	}
	if weatherCodeDesc(95) != "thunderstorm" {
		t.Fatal(weatherCodeDesc(95))
	}
	if weatherCodeDesc(99999) != "unknown" {
		t.Fatal(weatherCodeDesc(99999))
	}
}

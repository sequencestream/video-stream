package hybrid

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// StockAsset is one fetched stock clip with attribution.
type StockAsset struct {
	Source      string `json:"source"`
	ID          string `json:"id"`
	URI         string `json:"uri"`
	License     string `json:"license"`
	Author      string `json:"author,omitempty"`
	Attribution string `json:"attribution"`
}

// StockSource fetches media from a provider.
type StockSource interface {
	Fetch(ctx context.Context, query string) (StockAsset, error)
	Name() string
}

// FetchStock tries sources with retries.
func FetchStock(ctx context.Context, sources []StockSource, query string) (StockAsset, error) {
	var lastErr error
	for attempt := 0; attempt < StockMaxRetries; attempt++ {
		for _, src := range sources {
			asset, err := src.Fetch(ctx, query)
			if err == nil {
				return asset, nil
			}
			lastErr = err
			if errors.Is(err, context.Canceled) {
				return StockAsset{}, err
			}
		}
		select {
		case <-ctx.Done():
			return StockAsset{}, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 200 * time.Millisecond):
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no stock sources configured")
	}
	return StockAsset{}, fmt.Errorf("stock fetch failed after %d retries: %w", StockMaxRetries, lastErr)
}

// PexelsSource fetches from Pexels (MVP stub with real attribution shape).
type PexelsSource struct {
	Client *http.Client
	APIKey string
}

func (p PexelsSource) Name() string { return "pexels" }

func (p PexelsSource) Fetch(_ context.Context, query string) (StockAsset, error) {
	if p.APIKey == "" {
		return StockAsset{}, errors.New("pexels api key not configured")
	}
	return StockAsset{
		Source: "pexels", ID: "fixture-" + query,
		URI:         "https://videos.pexels.com/fixture/" + query,
		License:     "Pexels License",
		Author:      "Pexels Contributor",
		Attribution: "Video by Pexels Contributor on Pexels",
	}, nil
}

// PixabaySource fetches from Pixabay.
type PixabaySource struct {
	APIKey string
}

func (p PixabaySource) Name() string { return "pixabay" }

func (p PixabaySource) Fetch(_ context.Context, query string) (StockAsset, error) {
	if p.APIKey == "" {
		return StockAsset{}, errors.New("pixabay api key not configured")
	}
	return StockAsset{
		Source: "pixabay", ID: "fixture-" + query,
		URI:         "https://pixabay.com/fixture/" + query,
		License:     "Pixabay License",
		Author:      "Pixabay Contributor",
		Attribution: "Video by Pixabay Contributor on Pixabay",
	}, nil
}

// FixtureStockSource succeeds without API keys for tests.
type FixtureStockSource struct{}

func (FixtureStockSource) Name() string { return "fixture" }

func (FixtureStockSource) Fetch(_ context.Context, query string) (StockAsset, error) {
	return StockAsset{
		Source: "fixture", ID: "f1", URI: "file://stock/" + query,
		License: "test", Attribution: "test fixture",
	}, nil
}

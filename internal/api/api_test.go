package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/jurabek/software-factory/internal/config"
	"github.com/jurabek/software-factory/internal/store"
)

func TestEventsTailReturnsNewestEventsInSequenceOrder(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	campaign := store.Campaign{ID: "campaign-1", Request: "request", RepositoryType: "local", RepositoryValue: t.TempDir(), State: "draft", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := db.CreateCampaign(context.Background(), campaign); err != nil {
		t.Fatal(err)
	}
	campaignDir := t.TempDir()
	for index := 1; index <= 3; index++ {
		_, err := db.AppendEvent(context.Background(), campaignDir, store.Event{
			ID:         fmt.Sprintf("event-%d", index),
			CampaignID: campaign.ID,
			Type:       "log",
			Name:       fmt.Sprintf("event %d", index),
			Payload:    map[string]any{"index": index},
			StartedAt:  time.Now().UTC(),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	server, err := New(db, nil, config.Config{}, nil, nil, func(context.Context) ([]config.Model, error) { return []config.Model{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/campaigns/campaign-1/events?tail=2", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var body struct {
		Events []store.Event `json:"events"`
		Cursor int64         `json:"cursor"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Events) != 2 || body.Events[0].ID != "event-2" || body.Events[1].ID != "event-3" {
		t.Fatalf("events = %#v", body.Events)
	}
	if body.Cursor != body.Events[1].Sequence {
		t.Fatalf("cursor = %d, want %d", body.Cursor, body.Events[1].Sequence)
	}
}

func TestEmptyCollectionsAreJSONArrays(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server, err := New(db, nil, config.Config{}, nil, nil, func(context.Context) ([]config.Model, error) { return []config.Model{}, nil })
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct{ path, want string }{
		{path: "/api/v1/campaigns", want: "[]\n"},
		{path: "/api/v1/health", want: "{\"errors\":[],\"status\":\"ok\"}\n"},
	} {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d", response.Code)
			}
			if response.Body.String() != test.want {
				t.Fatalf("body = %s, want %s", response.Body.String(), test.want)
			}
		})
	}
}

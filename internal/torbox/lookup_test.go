package torbox_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/mrjoiny/torboxarr/internal/torbox"
)

func TestTaskLookupFindsOwnedTaskAfterConflictingStaleID(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(fmt.Sprint("queued=", queued), func(t *testing.T) {
			client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("id") != "" {
					_, _ = w.Write(jsonEnvelope(map[string]any{"id": 12, "hash": "unrelated"}))
				} else {
					_, _ = w.Write(jsonEnvelope([]map[string]any{{"id": 12, "hash": "unrelated"}, {"id": 34, "hash": "owned"}}))
				}
			})
			var status *torbox.TaskStatus
			var err error
			if queued {
				status, err = client.FindQueuedTask(t.Context(), "torrent", "12", "", "owned")
			} else {
				status, err = client.FindActiveTaskByIdentity(t.Context(), "torrent", "12", "", "", "owned")
			}
			if err != nil {
				t.Fatal(err)
			}
			if status == nil || (queued && status.QueuedID != "34") || (!queued && status.RemoteID != "34") {
				t.Fatalf("status = %#v, want owned task 34", status)
			}
		})
	}
}

func TestTaskLookupPaginatesBeforeConfirmingAbsence(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(fmt.Sprint("queued=", queued), func(t *testing.T) {
			pages := 0
			client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
				pages++
				if r.URL.Query().Get("offset") == "0" {
					items := make([]map[string]any, 1000)
					for i := range items {
						items[i] = map[string]any{"id": i, "hash": "unrelated"}
					}
					_, _ = w.Write(jsonEnvelope(items))
				} else if r.URL.Query().Get("offset") == "1000" {
					_, _ = w.Write(jsonEnvelope([]map[string]any{{"id": 1234, "hash": "owned"}}))
				} else {
					t.Errorf("unexpected offset %q", r.URL.Query().Get("offset"))
					w.WriteHeader(500)
				}
			})
			var status *torbox.TaskStatus
			var err error
			if queued {
				status, err = client.FindQueuedTask(t.Context(), "torrent", "", "", "owned")
			} else {
				status, err = client.FindActiveTaskByIdentity(t.Context(), "torrent", "", "", "", "owned")
			}
			if err != nil {
				t.Fatal(err)
			}
			if status == nil || pages != 2 {
				t.Fatalf("status = %#v, pages = %d, want task on second page", status, pages)
			}
		})
	}
}

func TestTaskLookupKeepsInconclusiveFilteredError(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(fmt.Sprint("queued=", queued), func(t *testing.T) {
			client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("id") != "" {
					w.WriteHeader(500)
					_, _ = w.Write([]byte(`{"success":false,"error":"DATABASE_ERROR"}`))
				} else {
					_, _ = w.Write(jsonEnvelope([]map[string]any{}))
				}
			})
			var err error
			if queued {
				_, err = client.FindQueuedTask(t.Context(), "torrent", "12", "", "owned")
			} else {
				_, err = client.FindActiveTaskByIdentity(t.Context(), "torrent", "12", "", "", "owned")
			}
			if err == nil {
				t.Fatal("lookup failure followed by empty listing must not establish absence")
			}
		})
	}
}

func TestTaskLookupRejectsRepeatedFullPages(t *testing.T) {
	client, _ := newTestHTTPClient(t, func(w http.ResponseWriter, r *http.Request) {
		items := make([]map[string]any, 1000)
		for i := range items {
			items[i] = map[string]any{"id": i}
		}
		_, _ = w.Write(jsonEnvelope(items))
	})
	if _, err := client.FindQueuedTask(t.Context(), "torrent", "", "", "owned"); err == nil {
		t.Fatal("provider ignoring pagination must not establish absence")
	}
}

package fakesources_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gernotstarke/zorgscope/internal/fakesources"
)

type todoistTask struct {
	ID        string `json:"id"`
	Content   string `json:"content"`
	ProjectID string `json:"project_id"`
	Priority  int    `json:"priority"`
	URL       string `json:"url"`
	Due       *struct {
		Date     string `json:"date"`
		Datetime string `json:"datetime"`
	} `json:"due"`
}

func fetchTasks(t *testing.T, base string) []todoistTask {
	t.Helper()
	resp, err := http.Get(base + "/rest/v2/tasks?filter=" + "today%20%7C%20overdue")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var tasks []todoistTask
	if err := json.NewDecoder(resp.Body).Decode(&tasks); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return tasks
}

func TestTodoistTasksHaveFourDistinguishableDueDates(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	tasks := fetchTasks(t, srv.URL)
	if len(tasks) != 4 {
		t.Fatalf("len(tasks) = %d, want 4", len(tasks))
	}

	dates := map[string]int{}
	noDue := 0
	for _, task := range tasks {
		if task.ID == "" || task.Content == "" || task.ProjectID == "" || task.URL == "" {
			t.Errorf("incomplete task: %+v", task)
		}
		if task.Due == nil {
			noDue++
			continue
		}
		dates[task.Due.Date]++
	}

	if noDue != 1 {
		t.Errorf("tasks with no due date = %d, want 1", noDue)
	}
	want := []string{"2026-08-16", "2026-08-17", "2026-08-24"}
	for _, d := range want {
		if dates[d] != 1 {
			t.Errorf("date %s appears %d times, want 1", d, dates[d])
		}
	}
}

func TestTodoistFailControl(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	post(t, srv.URL+"/_control/fail?source=todoist&status=401")

	resp, err := http.Get(srv.URL + "/rest/v2/tasks")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

package domain

import (
	"testing"
	"time"
)

func TestTaskTransitions(t *testing.T) {
	if !CanTransition(TaskPending, TaskQueued) || !CanTransition(TaskProcessing, TaskRetryWait) {
		t.Fatal("expected legal transition")
	}
	if CanTransition(TaskSucceeded, TaskQueued) || CanTransition(TaskPending, TaskSucceeded) {
		t.Fatal("terminal or skipped transition accepted")
	}
}

func TestTransitionAcceptsLegalTransition(t *testing.T) {
	task := AITask{Status: TaskPending}
	if err := task.Transition(TaskQueued); err != nil {
		t.Fatalf("Transition failed: %v", err)
	}
	if task.Status != TaskQueued {
		t.Fatalf("status=%s, want=%s", task.Status, TaskQueued)
	}
}

func TestTransitionRejectsIllegalTransitionWithoutMutation(t *testing.T) {
	task := AITask{Status: TaskPending}
	if err := task.Transition(TaskSucceeded); err == nil {
		t.Fatal("illegal transition accepted")
	}
	if task.Status != TaskPending {
		t.Fatalf("status=%s, want=%s", task.Status, TaskPending)
	}
}

func TestTransitionDoesNotRequeueSucceededTask(t *testing.T) {
	task := AITask{Status: TaskSucceeded}
	if err := task.Transition(TaskQueued); err == nil {
		t.Fatal("succeeded task was requeued")
	}
	if task.Status != TaskSucceeded {
		t.Fatalf("status=%s, want=%s", task.Status, TaskSucceeded)
	}
}

func TestTransitionRejectsUnknownStateWithoutMutation(t *testing.T) {
	unknown := TaskStatus("mystery")
	task := AITask{Status: unknown}
	if err := task.Transition(TaskQueued); err == nil {
		t.Fatal("unknown task state was accepted")
	}
	if task.Status != unknown {
		t.Fatalf("status=%s, want=%s", task.Status, unknown)
	}
}

func TestRetryDelay(t *testing.T) {
	cases := []struct {
		attempt int
		delay   time.Duration
		retry   bool
	}{{1, 2 * time.Second, true}, {2, 4 * time.Second, true}, {3, 0, false}}
	for _, tc := range cases {
		got, ok := RetryDelay(tc.attempt)
		if got != tc.delay || ok != tc.retry {
			t.Fatalf("attempt %d: got %v,%v", tc.attempt, got, ok)
		}
	}
}

func TestReviewInterval(t *testing.T) {
	want := []int{1, 2, 4, 7, 14, 30}
	for mastery, days := range want {
		got, err := ReviewInterval(mastery)
		if err != nil || got != time.Duration(days)*24*time.Hour {
			t.Fatalf("mastery %d: %v %v", mastery, got, err)
		}
	}
	if _, err := ReviewInterval(6); err == nil {
		t.Fatal("expected invalid mastery error")
	}
}

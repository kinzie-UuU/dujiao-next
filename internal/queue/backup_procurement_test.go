package queue

import (
	"encoding/json"
	"net"
	"strconv"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/dujiao-next/internal/config"
	"github.com/hibiken/asynq"
)

func TestBackupProcurementTaskContainsOnlyClaimID(t *testing.T) {
	if _, err := NewBackupProcurementSubmitTask(BackupProcurementSubmitPayload{}); err == nil {
		t.Fatal("zero claim accepted")
	}
	task, err := NewBackupProcurementSubmitTask(BackupProcurementSubmitPayload{ClaimID: 42})
	if err != nil || task.Type() != TaskBackupProcurementSubmit || task.Type() == TaskProcurementSubmit {
		t.Fatalf("invalid backup task: %v", err)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(task.Payload(), &body); err != nil || len(body) != 1 || body["claim_id"] != float64(42) {
		t.Fatalf("unexpected backup payload: %s", task.Payload())
	}
}

func TestBackupProcurementQueueRejectsDisabledAndEnqueuesWithoutRetries(t *testing.T) {
	var disabled *Client
	if err := disabled.EnqueueBackupProcurementSubmit(BackupProcurementSubmitPayload{ClaimID: 1}); err == nil {
		t.Fatal("disabled queue accepted backup claim")
	}
	server := miniredis.RunT(t)
	host, portText, err := net.SplitHostPort(server.Addr())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(&config.QueueConfig{Enabled: true, Host: host, Port: port})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.EnqueueBackupProcurementSubmit(BackupProcurementSubmitPayload{ClaimID: 42}); err != nil {
		t.Fatal(err)
	}
	inspector := asynq.NewInspector(asynq.RedisClientOpt{Addr: server.Addr()})
	defer inspector.Close()
	items, err := inspector.ListPendingTasks(DefaultQueue)
	if err != nil || len(items) != 1 {
		t.Fatalf("pending tasks=%d err=%v", len(items), err)
	}
	if items[0].Type != TaskBackupProcurementSubmit || items[0].MaxRetry != 0 || string(items[0].Payload) != `{"claim_id":42}` {
		t.Fatalf("unexpected queued task: %+v", items[0])
	}
}

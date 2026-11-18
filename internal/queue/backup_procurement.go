package queue

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dujiao-next/internal/constants"
	"github.com/hibiken/asynq"
)

const TaskBackupProcurementSubmit = constants.TaskBackupProcurementSubmit

// BackupProcurementSubmitPayload references the durable claim only. It must
// not carry supplier credentials, order data, pricing, or delivery material.
type BackupProcurementSubmitPayload struct {
	ClaimID uint `json:"claim_id"`
}

func NewBackupProcurementSubmitTask(payload BackupProcurementSubmitPayload) (*asynq.Task, error) {
	if payload.ClaimID == 0 {
		return nil, errors.New("backup procurement claim ID is required")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return asynq.NewTask(TaskBackupProcurementSubmit, body), nil
}

// EnqueueBackupProcurementSubmit reports disabled queues as failures so callers
// can recover committed claims. A timeout does not prove the task was not
// queued. The worker fences every POST using the durable claim; zero retries
// alone cannot prevent Redis redelivery or process-crash replay.
func (c *Client) EnqueueBackupProcurementSubmit(payload BackupProcurementSubmitPayload) error {
	task, err := NewBackupProcurementSubmitTask(payload)
	if err != nil {
		return err
	}
	if !c.Enabled() {
		return errors.New("backup procurement queue unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = c.client.EnqueueContext(ctx, task, asynq.Queue(c.defaultQueue), asynq.MaxRetry(0))
	if err != nil {
		return errors.New("backup procurement enqueue failed")
	}
	return nil
}

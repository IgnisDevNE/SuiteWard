package governance

import "errors"

// Validate reports ErrInvalidRequest when the job cannot be enqueued.
func (j Job) Validate() error { return errors.New("not implemented") }

// Validate reports ErrInvalidRequest when the message cannot be written.
func (m OutboxMessage) Validate() error { return errors.New("not implemented") }
